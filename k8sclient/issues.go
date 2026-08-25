package k8sclient

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Severity ranks a cluster issue. The ordering matters: alert filters compare
// against a configured minimum.
const (
	SeverityMinor    = "minor"
	SeverityMajor    = "major"
	SeverityCritical = "critical"
)

// SeverityRank turns a severity name into a comparable number.
// Unknown names rank as minor so a typo cannot silently escalate paging.
func SeverityRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case SeverityCritical:
		return 3
	case SeverityMajor:
		return 2
	default:
		return 1
	}
}

// Issue is one distinct failure in the cluster, derived from a Snapshot.
// Key is stable across reconciles so the same failure is never re-alerted and
// its duration can be measured.
type Issue struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"` // Node | Pod | Deployment | StatefulSet | DaemonSet | PVC
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Reason    string `json:"reason"`
	Message   string `json:"message,omitempty"`
	Severity  string `json:"severity"`
}

// Title renders the issue as a single alert headline.
func (i Issue) Title() string {
	if i.Namespace != "" {
		return fmt.Sprintf("%s %s/%s: %s", i.Kind, i.Namespace, i.Name, i.Reason)
	}
	return fmt.Sprintf("%s %s: %s", i.Kind, i.Name, i.Reason)
}

// Issues derives every current failure from a snapshot.
//
// It reads only the snapshot, never the API, so it is cheap enough to run on
// every reconcile and trivially testable.
func (s *Snapshot) Issues() []Issue {
	if s == nil {
		return nil
	}
	var out []Issue

	// --- Nodes: a NotReady node takes its whole workload with it.
	for _, n := range s.Nodes {
		if !n.Ready {
			out = append(out, Issue{
				Key:      "node/" + n.Name + "/NotReady",
				Kind:     "Node",
				Name:     n.Name,
				Reason:   "NotReady",
				Message:  strings.Join(n.Conditions, ", "),
				Severity: SeverityCritical,
			})
			continue
		}
		// Pressure conditions on a Ready node are a warning, not an outage.
		for _, c := range n.Conditions {
			if c == "Unschedulable" || strings.HasSuffix(c, "Pressure") {
				out = append(out, Issue{
					Key:      "node/" + n.Name + "/" + c,
					Kind:     "Node",
					Name:     n.Name,
					Reason:   c,
					Severity: SeverityMajor,
				})
			}
		}
	}

	// --- Workloads: the signal that an application is actually unavailable.
	for _, w := range s.Workloads {
		switch w.Status {
		case "down":
			out = append(out, Issue{
				Key:       strings.ToLower(w.Kind) + "/" + w.Namespace + "/" + w.Name + "/down",
				Kind:      w.Kind,
				Namespace: w.Namespace,
				Name:      w.Name,
				Reason:    "NoReadyReplicas",
				Message:   fmt.Sprintf("0/%d ready · %s", w.Desired, w.Image),
				Severity:  SeverityCritical,
			})
		case "degraded":
			out = append(out, Issue{
				Key:       strings.ToLower(w.Kind) + "/" + w.Namespace + "/" + w.Name + "/degraded",
				Kind:      w.Kind,
				Namespace: w.Namespace,
				Name:      w.Name,
				Reason:    "ReplicasUnavailable",
				Message:   fmt.Sprintf("%d/%d ready · %s", w.Ready, w.Desired, w.Image),
				Severity:  SeverityMajor,
			})
		}
	}

	// --- Pods: the detail an operator needs to act on the workload alert.
	for _, p := range s.Problems {
		out = append(out, Issue{
			Key:       "pod/" + p.Namespace + "/" + p.Name + "/" + p.Reason,
			Kind:      "Pod",
			Namespace: p.Namespace,
			Name:      p.Name,
			Reason:    p.Reason,
			Message:   podIssueMessage(p),
			Severity:  podSeverity(p.Reason),
		})
	}

	// --- Storage: a Lost volume is unrecoverable, a Pending one blocks a pod.
	if s.Storage.Lost > 0 {
		out = append(out, Issue{
			Key:      "pvc/cluster/lost",
			Kind:     "PVC",
			Name:     "persistent volume claims",
			Reason:   "Lost",
			Message:  fmt.Sprintf("%d claim(s) lost their volume", s.Storage.Lost),
			Severity: SeverityCritical,
		})
	}
	if s.Storage.Pending > 0 {
		out = append(out, Issue{
			Key:      "pvc/cluster/pending",
			Kind:     "PVC",
			Name:     "persistent volume claims",
			Reason:   "Pending",
			Message:  fmt.Sprintf("%d claim(s) unbound", s.Storage.Pending),
			Severity: SeverityMajor,
		})
	}

	// Most severe first, then stable by key so equal-severity ordering does
	// not churn between reconciles.
	sort.Slice(out, func(i, j int) bool {
		ri, rj := SeverityRank(out[i].Severity), SeverityRank(out[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func podIssueMessage(p ProblemPod) string {
	var b strings.Builder
	if p.Restarts > 0 {
		fmt.Fprintf(&b, "%d restarts", p.Restarts)
	}
	if p.Node != "" {
		if b.Len() > 0 {
			b.WriteString(" · ")
		}
		fmt.Fprintf(&b, "node %s", p.Node)
	}
	if p.Message != "" {
		if b.Len() > 0 {
			b.WriteString(" · ")
		}
		b.WriteString(p.Message)
	}
	return b.String()
}

// podSeverity grades a pod failure by how likely it is to be self-healing.
func podSeverity(reason string) string {
	switch reason {
	case "CrashLoopBackOff", "Failed", "Evicted", "OOMKilled":
		return SeverityMajor
	case "ImagePullBackOff", "ErrImagePull", "InvalidImageName",
		"CreateContainerConfigError", "CreateContainerError", "RunContainerError":
		// A bad image or missing secret never fixes itself.
		return SeverityMajor
	case "Unschedulable":
		return SeverityMajor
	default:
		return SeverityMinor
	}
}

// IssueAge is a convenience for callers rendering durations.
func IssueAge(since time.Time) time.Duration {
	if since.IsZero() {
		return 0
	}
	return time.Since(since)
}
