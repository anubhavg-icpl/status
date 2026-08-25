package k8sclient

import (
	"bufio"
	"context"
	"regexp"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Log scanning turns "CrashLoopBackOff" into "CrashLoopBackOff: cannot open
// /var/lib/clickhouse: permission denied". The state alone tells an operator
// something broke; the log line tells them what to do about it.

// errorLine matches the shapes real applications actually use to say they
// failed. Deliberately anchored near the start of a line: a stack trace or a
// JSON payload that merely contains the word "error" is not itself the error.
var errorLine = regexp.MustCompile(`(?i)(^|[\s\[\|"'])(fatal|panic|exception|err(or)?|failed|failure|cannot|unable to|refused|denied|timeout|timed out|no such|not found|out of memory|oom)([\s:="'\],]|$)`)

// noiseLine drops lines that match errorLine but carry no diagnosis: log
// framework preamble, and counters that merely report zero errors.
var noiseLine = regexp.MustCompile(`(?i)(errors?[=:]\s*0\b|error_count|"level"\s*:\s*"(info|debug)"|\berrorlevel\b)`)

const (
	// logTailLines bounds what is pulled from the API. A crashing container
	// says why near the end; more than this is scrollback nobody reads in
	// a phone notification.
	logTailLines = 80
	// maxExcerptLines is how many distinct error lines survive into an alert.
	maxExcerptLines = 4
	// maxLineRunes truncates a single line. Stack traces and JSON blobs run
	// to thousands of characters and would bury the alert.
	maxLineRunes = 220
	// logFetchTimeout caps a single container's log read.
	logFetchTimeout = 6 * time.Second
)

// LogExcerpt is the error tail pulled from one container.
type LogExcerpt struct {
	Namespace string    `json:"namespace"`
	Pod       string    `json:"pod"`
	Container string    `json:"container"`
	Previous  bool      `json:"previous"` // read from the crashed instance
	Lines     []string  `json:"lines"`
	FetchedAt time.Time `json:"fetched_at"`
}

// Empty reports whether the excerpt found nothing worth showing.
func (e *LogExcerpt) Empty() bool { return e == nil || len(e.Lines) == 0 }

// Text renders the excerpt for an alert body.
func (e *LogExcerpt) Text() string {
	if e.Empty() {
		return ""
	}
	head := e.Container
	if e.Previous {
		head += " (previous)"
	}
	return head + ": " + strings.Join(e.Lines, " / ")
}

// logCache stops the same container's logs being pulled on every reconcile.
// A crashing pod is re-observed every interval; its logs are not new every
// interval, and the apiserver charges real work for each read.
type logCache struct {
	mu   sync.Mutex
	seen map[string]logCacheEntry
}

type logCacheEntry struct {
	excerpt *LogExcerpt
	at      time.Time
}

var logs = logCache{seen: make(map[string]logCacheEntry)}

// logCacheTTL is how long an excerpt is reused. Long enough that a pod
// flapping every 30s is not re-read each time, short enough that a genuinely
// new failure mode shows up in the next alert.
const logCacheTTL = 5 * time.Minute

// PodErrorLog returns the error tail for a failing pod, choosing the container
// that actually failed rather than the first one listed.
//
// Best-effort throughout: a pod whose logs cannot be read (evicted, image
// never pulled, RBAC withheld) yields an empty excerpt, never an error that
// would stop an alert being sent. An alert with no log line is still worth
// far more than no alert.
func (c *Client) PodErrorLog(ctx context.Context, pod *corev1.Pod) *LogExcerpt {
	if c == nil || c.Clientset == nil || pod == nil {
		return nil
	}
	container, previous := failingContainer(pod)
	if container == "" {
		return nil
	}
	key := pod.Namespace + "/" + pod.Name + "/" + container

	logs.mu.Lock()
	if e, ok := logs.seen[key]; ok && time.Since(e.at) < logCacheTTL {
		logs.mu.Unlock()
		return e.excerpt
	}
	logs.mu.Unlock()

	ex := c.fetchExcerpt(ctx, pod.Namespace, pod.Name, container, previous)

	logs.mu.Lock()
	// Bound the cache: a cluster-wide meltdown must not grow it without limit.
	if len(logs.seen) > 500 {
		logs.seen = make(map[string]logCacheEntry)
	}
	logs.seen[key] = logCacheEntry{excerpt: ex, at: time.Now()}
	logs.mu.Unlock()
	return ex
}

func (c *Client) fetchExcerpt(ctx context.Context, ns, pod, container string, previous bool) *LogExcerpt {
	read := func(prev bool) []string {
		lctx, cancel := context.WithTimeout(ctx, logFetchTimeout)
		defer cancel()
		tail := int64(logTailLines)
		req := c.Clientset.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{
			Container:  container,
			Previous:   prev,
			TailLines:  &tail,
			Timestamps: false,
		})
		stream, err := req.Stream(lctx)
		if err != nil {
			return nil
		}
		defer stream.Close()
		return extractErrors(stream)
	}

	// A CrashLoopBackOff container's current instance is usually empty or
	// still starting; the reason it died is in the previous instance.
	if previous {
		if lines := read(true); len(lines) > 0 {
			return &LogExcerpt{Namespace: ns, Pod: pod, Container: container,
				Previous: true, Lines: lines, FetchedAt: time.Now()}
		}
	}
	if lines := read(false); len(lines) > 0 {
		return &LogExcerpt{Namespace: ns, Pod: pod, Container: container,
			Previous: false, Lines: lines, FetchedAt: time.Now()}
	}
	return nil
}

// extractErrors keeps the last few distinct error-shaped lines from a stream.
func extractErrors(r interface{ Read([]byte) (int, error) }) []string {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 256*1024)

	var hits []string
	seen := map[string]bool{}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !errorLine.MatchString(line) || noiseLine.MatchString(line) {
			continue
		}
		line = truncateRunes(line, maxLineRunes)
		if seen[line] {
			continue // a container repeating one error should say it once
		}
		seen[line] = true
		hits = append(hits, line)
		// Keep the most recent: a crash loop's last words matter most.
		if len(hits) > maxExcerptLines {
			hits = hits[1:]
			delete(seen, hits[0])
		}
	}
	return hits
}

// failingContainer picks which container to read, and whether the previous
// instance is the one holding the explanation.
func failingContainer(pod *corev1.Pod) (name string, previous bool) {
	for _, cs := range pod.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil {
			switch w.Reason {
			case "CrashLoopBackOff":
				return cs.Name, true
			case "ImagePullBackOff", "ErrImagePull", "InvalidImageName",
				"CreateContainerConfigError", "CreateContainerError", "RunContainerError":
				// Nothing ever ran, so there are no logs — the kubelet's
				// message on the pod is the whole story.
				return "", false
			}
		}
		if t := cs.State.Terminated; t != nil && t.ExitCode != 0 {
			return cs.Name, true
		}
		if !cs.Ready && cs.State.Running != nil {
			return cs.Name, false
		}
	}
	// Failed pods with no container status at all: fall back to the spec.
	if pod.Status.Phase == corev1.PodFailed && len(pod.Spec.Containers) > 0 {
		return pod.Spec.Containers[0].Name, false
	}
	return "", false
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// EnrichProblemPods attaches log excerpts to the snapshot's problem pods, so
// the alert an operator receives names the error rather than only the state.
//
// Bounded on purpose: reading logs costs an apiserver round-trip per pod, and
// a cluster with 200 broken pods must not turn one reconcile into 200 calls.
func (c *Client) EnrichProblemPods(ctx context.Context, s *Snapshot, max int) {
	if c == nil || s == nil || max <= 0 {
		return
	}
	done := 0
	for i := range s.Problems {
		if done >= max {
			return
		}
		p := &s.Problems[i]
		pod, err := c.Pods.Pods(p.Namespace).Get(p.Name)
		if err != nil || pod == nil {
			continue
		}
		ex := c.PodErrorLog(ctx, pod)
		if ex.Empty() {
			continue
		}
		p.LogExcerpt = ex.Text()
		done++
	}
}
