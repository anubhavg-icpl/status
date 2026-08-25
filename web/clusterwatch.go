package web

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/status/config"
	"github.com/status/k8sclient"
	"github.com/status/notify"
)

// ActiveIssue is a cluster failure that is currently happening, with the time
// context that makes it actionable: when it started and how long it has run.
type ActiveIssue struct {
	k8sclient.Issue
	FirstSeen       time.Time `json:"first_seen"`
	LastSeen        time.Time `json:"last_seen"`
	DurationSeconds int64     `json:"duration_seconds"`
	Alerted         bool      `json:"alerted"`
	Observations    int       `json:"observations"`
}

// clusterWatcher turns repeated cluster snapshots into opened/resolved events.
//
// Detection is a set diff between reconciles rather than raw informer
// callbacks: a diff is inherently deduplicated, gives every failure a start
// time for free, and cannot be stampeded by an event storm.
type clusterWatcher struct {
	mu     sync.Mutex
	active map[string]*ActiveIssue

	cfg      config.ClusterAlertsConfig
	notifier *notify.Notifier
	baseURL  string
	minRank  int
	cooldown time.Duration
	lastSent map[string]time.Time
}

func newClusterWatcher(cfg *config.Config, n *notify.Notifier) *clusterWatcher {
	return &clusterWatcher{
		active:   make(map[string]*ActiveIssue),
		lastSent: make(map[string]time.Time),
		cfg:      cfg.Alerts.Cluster,
		notifier: n,
		baseURL:  cfg.BaseURL,
		minRank:  k8sclient.SeverityRank(cfg.Alerts.Cluster.MinSeverity),
		cooldown: cfg.Alerts.Cooldown,
	}
}

// run reconciles on a timer so failures are detected even when nobody has the
// status page open. Detection latency is bounded by cfg.Interval.
func (w *clusterWatcher) run(ctx context.Context, snapshot func(context.Context) (*k8sclient.Snapshot, error)) {
	if w == nil || !w.cfg.Enabled {
		return
	}
	interval := w.cfg.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Printf("cluster watch: every %s, alerting on %s+ after %s",
		interval, w.cfg.MinSeverity, w.cfg.MinDuration)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap, err := snapshot(ctx)
			if err != nil {
				log.Printf("cluster watch: snapshot failed: %v", err)
				continue
			}
			w.reconcile(snap)
		}
	}
}

// reconcile diffs the current failure set against the tracked one, then alerts
// on anything that has persisted past MinDuration. Safe to call concurrently
// with the ticker; the HTTP handler calls it too so a page view refreshes the
// durations immediately.
func (w *clusterWatcher) reconcile(snap *k8sclient.Snapshot) {
	if w == nil || snap == nil || !w.cfg.Enabled {
		return
	}
	now := time.Now()
	current := snap.Issues()

	var toAlert []ActiveIssue
	var resolved []ActiveIssue

	w.mu.Lock()
	seen := make(map[string]bool, len(current))

	for _, iss := range current {
		if !w.namespaceInScope(iss.Namespace) {
			continue
		}
		seen[iss.Key] = true

		tracked, ok := w.active[iss.Key]
		if !ok {
			if len(w.active) >= w.cfg.MaxTracked {
				continue // registry full: a meltdown must not exhaust memory
			}
			tracked = &ActiveIssue{Issue: iss, FirstSeen: now}
			w.active[iss.Key] = tracked
		}
		// Refresh the payload — a pod's restart count and message move.
		tracked.Issue = iss
		tracked.LastSeen = now
		tracked.Observations++
		tracked.DurationSeconds = int64(now.Sub(tracked.FirstSeen).Seconds())

		if w.shouldAlert(tracked, now) {
			tracked.Alerted = true
			w.lastSent[iss.Key] = now
			toAlert = append(toAlert, *tracked)
		}
	}

	for key, tracked := range w.active {
		if seen[key] {
			continue
		}
		delete(w.active, key)
		delete(w.lastSent, key)
		if tracked.Alerted {
			// Only announce a recovery for something we actually paged about.
			tracked.DurationSeconds = int64(now.Sub(tracked.FirstSeen).Seconds())
			resolved = append(resolved, *tracked)
		}
	}
	w.mu.Unlock()

	for _, iss := range toAlert {
		w.send(iss, false)
	}
	for _, iss := range resolved {
		w.send(iss, true)
	}
}

// shouldAlert applies the two gates that keep a rolling deploy from paging
// anyone: severity floor, and "has this been broken long enough to be real".
func (w *clusterWatcher) shouldAlert(t *ActiveIssue, now time.Time) bool {
	if k8sclient.SeverityRank(t.Severity) < w.minRank {
		return false
	}
	if now.Sub(t.FirstSeen) < w.cfg.MinDuration {
		return false
	}
	if t.Alerted {
		return false
	}
	if last, ok := w.lastSent[t.Key]; ok && w.cooldown > 0 && now.Sub(last) < w.cooldown {
		return false
	}
	return true
}

func (w *clusterWatcher) send(iss ActiveIssue, recovered bool) {
	status := "down"
	if iss.Severity == k8sclient.SeverityMajor {
		status = "degraded"
	}
	message := iss.Message
	if recovered {
		status = "operational"
		message = "cleared after " + humanDuration(iss.DurationSeconds)
	} else if iss.DurationSeconds > 0 {
		message = strings.TrimSpace(message + " · firing for " + humanDuration(iss.DurationSeconds))
	}

	group := iss.Namespace
	if group == "" {
		group = "cluster"
	}

	log.Printf("cluster watch: %s %s (%s)",
		map[bool]string{true: "resolved", false: "firing"}[recovered], iss.Key, iss.Severity)

	w.notifier.NotifyServiceAlert(notify.ServiceAlert{
		Service:    iss.Title(),
		Group:      "k8s/" + group,
		Status:     status,
		Severity:   iss.Severity,
		Message:    message,
		OccurredAt: iss.LastSeen,
	}, w.baseURL)
}

// namespaceInScope applies the allow/deny namespace filters.
func (w *clusterWatcher) namespaceInScope(ns string) bool {
	if ns == "" {
		return true // cluster-scoped objects (nodes) are always in scope
	}
	for _, skip := range w.cfg.IgnoreNamespaces {
		if strings.EqualFold(strings.TrimSpace(skip), ns) {
			return false
		}
	}
	if len(w.cfg.OnlyNamespaces) == 0 {
		return true
	}
	for _, only := range w.cfg.OnlyNamespaces {
		if strings.EqualFold(strings.TrimSpace(only), ns) {
			return true
		}
	}
	return false
}

// Issues returns the currently active failures, worst and longest-running
// first, for the status page to render.
func (w *clusterWatcher) Issues() []ActiveIssue {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	out := make([]ActiveIssue, 0, len(w.active))
	now := time.Now()
	for _, t := range w.active {
		c := *t
		c.DurationSeconds = int64(now.Sub(t.FirstSeen).Seconds())
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := k8sclient.SeverityRank(out[i].Severity), k8sclient.SeverityRank(out[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if out[i].DurationSeconds != out[j].DurationSeconds {
			return out[i].DurationSeconds > out[j].DurationSeconds
		}
		return out[i].Key < out[j].Key
	})
	return out
}
