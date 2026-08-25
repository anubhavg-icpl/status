package web

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/status/k8sclient"
)

// clusterCache memoises the last snapshot. A snapshot is built from informer
// caches, but serialising a few thousand pods on every page load is still
// waste — one build per CacheTTL is plenty for a status page.
type clusterCache struct {
	mu       sync.Mutex
	snap     *k8sclient.Snapshot
	builtAt  time.Time
	building bool
}

// handleAPICluster serves the whole-cluster snapshot.
//
// It returns 200 with {"enabled": false} rather than an error when the process
// is not running in a cluster, so the front-end can hide the section without
// treating a normal non-k8s deployment as a failure.
func (s *Server) handleAPICluster(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	kc := s.monitor.K8s()
	if !s.config.Cluster.Enabled || kc == nil {
		s.jsonResponse(w, map[string]any{
			"enabled": false,
			"reason":  clusterDisabledReason(s.config.Cluster.Enabled, kc != nil),
		})
		return
	}

	snap, err := s.clusterSnapshot(r.Context())
	if err != nil {
		s.jsonError(w, "cluster snapshot failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Refresh durations on the way out so a page view always shows current
	// "firing for 14m" numbers rather than the last tick's.
	s.enrichLogs(r.Context(), snap)
	s.clusterWatch.reconcile(snap)

	s.jsonResponseWithMeta(w, map[string]any{
		"enabled":  true,
		"snapshot": snap,
		"issues":   s.clusterWatch.Issues(),
	})
}

// clusterSnapshot returns a cached snapshot, rebuilding it when stale.
// Concurrent callers that arrive during a rebuild are served the stale copy
// rather than piling up duplicate builds.
func (s *Server) clusterSnapshot(ctx context.Context) (*k8sclient.Snapshot, error) {
	kc := s.monitor.K8s()
	if kc == nil {
		return nil, errClusterUnavailable
	}
	ttl := s.config.Cluster.CacheTTL
	if ttl <= 0 {
		ttl = 10 * time.Second
	}

	s.cluster.mu.Lock()
	fresh := s.cluster.snap != nil && time.Since(s.cluster.builtAt) < ttl
	if fresh {
		snap := s.cluster.snap
		s.cluster.mu.Unlock()
		return snap, nil
	}
	if s.cluster.building && s.cluster.snap != nil {
		snap := s.cluster.snap
		s.cluster.mu.Unlock()
		return snap, nil // stale-while-revalidate
	}
	s.cluster.building = true
	s.cluster.mu.Unlock()

	snap, err := kc.Snapshot(ctx)

	s.cluster.mu.Lock()
	s.cluster.building = false
	if err == nil {
		s.cluster.snap = snap
		s.cluster.builtAt = time.Now()
	}
	stale := s.cluster.snap
	s.cluster.mu.Unlock()

	if err != nil {
		if stale != nil {
			return stale, nil // serve the last good read rather than a blank page
		}
		return nil, err
	}
	return snap, nil
}

// enrichLogs attaches the error tail from failing containers, so both the page
// and the alert name the error rather than only the pod state. Best-effort and
// bounded: a read that fails leaves the state-only message intact.
func (s *Server) enrichLogs(ctx context.Context, snap *k8sclient.Snapshot) {
	cfg := s.config.Alerts.Cluster
	if !cfg.IncludeLogs || snap == nil {
		return
	}
	if kc := s.monitor.K8s(); kc != nil {
		kc.EnrichProblemPods(ctx, snap, cfg.MaxLogReads)
	}
}

func clusterDisabledReason(enabled, hasClient bool) string {
	switch {
	case !enabled:
		return "cluster view disabled in config"
	case !hasClient:
		return "not running in a Kubernetes cluster"
	default:
		return ""
	}
}

// errClusterUnavailable is returned when no in-cluster client exists.
var errClusterUnavailable = clusterError("kubernetes client not available")

type clusterError string

func (e clusterError) Error() string { return string(e) }
