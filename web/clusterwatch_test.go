package web

import (
	"testing"
	"time"

	"github.com/status/config"
	"github.com/status/k8sclient"
	"github.com/status/notify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newWatcher(t *testing.T, mutate func(*config.Config)) *clusterWatcher {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Alerts.Cooldown = 0
	cfg.Alerts.Cluster = config.ClusterAlertsConfig{
		Enabled:     true,
		Interval:    time.Second,
		MinDuration: 0, // alert on first sight unless a test says otherwise
		MinSeverity: k8sclient.SeverityMajor,
		MaxTracked:  100,
	}
	if mutate != nil {
		mutate(cfg)
	}
	w := newClusterWatcher(cfg, notify.NewNotifier(nil))
	return w
}

func crashingPod(ns, name string) *k8sclient.Snapshot {
	return &k8sclient.Snapshot{Problems: []k8sclient.ProblemPod{{
		Namespace: ns, Name: name, Reason: "CrashLoopBackOff", Restarts: 4,
	}}}
}

func alertedKeys(w *clusterWatcher) []string {
	var out []string
	for _, i := range w.Issues() {
		if i.Alerted {
			out = append(out, i.Key)
		}
	}
	return out
}

func TestWatcherTracksIssueAndGivesItAStartTime(t *testing.T) {
	w := newWatcher(t, nil)
	w.reconcile(crashingPod("prod", "api"))

	issues := w.Issues()
	require.Len(t, issues, 1)
	assert.Equal(t, "pod/prod/api/CrashLoopBackOff", issues[0].Key)
	assert.False(t, issues[0].FirstSeen.IsZero(), "every issue needs a start time")
	assert.Equal(t, 1, issues[0].Observations)
}

func TestWatcherAlertsOnceNotEveryReconcile(t *testing.T) {
	w := newWatcher(t, nil)
	for i := 0; i < 5; i++ {
		w.reconcile(crashingPod("prod", "api"))
	}
	issues := w.Issues()
	require.Len(t, issues, 1)
	assert.True(t, issues[0].Alerted)
	assert.Equal(t, 5, issues[0].Observations, "still observed every tick…")
	assert.Len(t, alertedKeys(w), 1, "…but alerted only once")
}

func TestWatcherHoldsFireUntilMinDuration(t *testing.T) {
	w := newWatcher(t, func(c *config.Config) {
		c.Alerts.Cluster.MinDuration = time.Hour
	})
	w.reconcile(crashingPod("prod", "api"))
	w.reconcile(crashingPod("prod", "api"))

	require.Len(t, w.Issues(), 1)
	assert.False(t, w.Issues()[0].Alerted,
		"a pod crashing for seconds is a rolling deploy, not a page")
}

func TestWatcherFiresOnceMinDurationElapses(t *testing.T) {
	w := newWatcher(t, func(c *config.Config) {
		c.Alerts.Cluster.MinDuration = 20 * time.Millisecond
	})
	w.reconcile(crashingPod("prod", "api"))
	require.False(t, w.Issues()[0].Alerted)

	time.Sleep(30 * time.Millisecond)
	w.reconcile(crashingPod("prod", "api"))
	assert.True(t, w.Issues()[0].Alerted, "sustained past MinDuration, now it is real")
}

func TestWatcherDropsResolvedIssues(t *testing.T) {
	w := newWatcher(t, nil)
	w.reconcile(crashingPod("prod", "api"))
	require.Len(t, w.Issues(), 1)

	w.reconcile(&k8sclient.Snapshot{}) // cluster healthy again
	assert.Empty(t, w.Issues(), "a cleared failure must leave the registry")
}

func TestWatcherFiltersBySeverity(t *testing.T) {
	w := newWatcher(t, func(c *config.Config) {
		c.Alerts.Cluster.MinSeverity = k8sclient.SeverityCritical
	})
	// A degraded workload is major, below the critical floor.
	w.reconcile(&k8sclient.Snapshot{Workloads: []k8sclient.WorkloadInfo{
		{Kind: "Deployment", Namespace: "prod", Name: "api", Status: "degraded", Ready: 1, Desired: 2},
	}})
	require.Len(t, w.Issues(), 1, "still tracked and shown on the page")
	assert.False(t, w.Issues()[0].Alerted, "but not paged")

	w.reconcile(&k8sclient.Snapshot{Nodes: []k8sclient.NodeInfo{
		{Name: "n1", Ready: false, Conditions: []string{"NotReady"}},
	}})
	assert.Equal(t, []string{"node/n1/NotReady"}, alertedKeys(w))
}

func TestWatcherRespectsIgnoreNamespaces(t *testing.T) {
	w := newWatcher(t, func(c *config.Config) {
		c.Alerts.Cluster.IgnoreNamespaces = []string{"CI"}
	})
	w.reconcile(&k8sclient.Snapshot{Problems: []k8sclient.ProblemPod{
		{Namespace: "ci", Name: "build-1", Reason: "CrashLoopBackOff"},
		{Namespace: "prod", Name: "api", Reason: "CrashLoopBackOff"},
	}})
	issues := w.Issues()
	require.Len(t, issues, 1)
	assert.Equal(t, "prod", issues[0].Namespace, "ignore matching is case-insensitive")
}

func TestWatcherRespectsOnlyNamespaces(t *testing.T) {
	w := newWatcher(t, func(c *config.Config) {
		c.Alerts.Cluster.OnlyNamespaces = []string{"prod"}
	})
	w.reconcile(&k8sclient.Snapshot{
		Problems: []k8sclient.ProblemPod{
			{Namespace: "staging", Name: "api", Reason: "CrashLoopBackOff"},
			{Namespace: "prod", Name: "api", Reason: "CrashLoopBackOff"},
		},
		Nodes: []k8sclient.NodeInfo{{Name: "n1", Ready: false, Conditions: []string{"NotReady"}}},
	})
	keys := map[string]bool{}
	for _, i := range w.Issues() {
		keys[i.Key] = true
	}
	assert.True(t, keys["pod/prod/api/CrashLoopBackOff"])
	assert.False(t, keys["pod/staging/api/CrashLoopBackOff"])
	assert.True(t, keys["node/n1/NotReady"],
		"cluster-scoped objects have no namespace and stay in scope")
}

func TestWatcherCapsTrackedIssues(t *testing.T) {
	w := newWatcher(t, func(c *config.Config) { c.Alerts.Cluster.MaxTracked = 3 })

	var problems []k8sclient.ProblemPod
	for i := 0; i < 50; i++ {
		problems = append(problems, k8sclient.ProblemPod{
			Namespace: "prod", Name: string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Reason: "CrashLoopBackOff",
		})
	}
	w.reconcile(&k8sclient.Snapshot{Problems: problems})
	assert.LessOrEqual(t, len(w.Issues()), 3,
		"a cluster-wide meltdown must not grow this process without bound")
}

func TestWatcherRefreshesPayloadButKeepsStartTime(t *testing.T) {
	w := newWatcher(t, nil)
	w.reconcile(crashingPod("prod", "api"))
	first := w.Issues()[0].FirstSeen

	time.Sleep(5 * time.Millisecond)
	updated := crashingPod("prod", "api")
	updated.Problems[0].Restarts = 99
	w.reconcile(updated)

	issues := w.Issues()
	require.Len(t, issues, 1)
	assert.Equal(t, first, issues[0].FirstSeen, "duration is measured from the original sighting")
	assert.Contains(t, issues[0].Message, "99 restarts", "but the detail is current")
}

func TestWatcherSortsWorstAndOldestFirst(t *testing.T) {
	w := newWatcher(t, nil)
	w.reconcile(&k8sclient.Snapshot{
		Workloads: []k8sclient.WorkloadInfo{
			{Kind: "Deployment", Namespace: "prod", Name: "b", Status: "degraded", Ready: 1, Desired: 2},
			{Kind: "Deployment", Namespace: "prod", Name: "a", Status: "down", Desired: 2},
		},
	})
	issues := w.Issues()
	require.Len(t, issues, 2)
	assert.Equal(t, k8sclient.SeverityCritical, issues[0].Severity)
	assert.Equal(t, k8sclient.SeverityMajor, issues[1].Severity)
}

func TestWatcherDisabledDoesNothing(t *testing.T) {
	w := newWatcher(t, func(c *config.Config) { c.Alerts.Cluster.Enabled = false })
	w.reconcile(crashingPod("prod", "api"))
	assert.Empty(t, w.Issues())
}

func TestWatcherNilSafe(t *testing.T) {
	var w *clusterWatcher
	assert.NotPanics(t, func() { w.reconcile(crashingPod("prod", "api")) })
	assert.Nil(t, w.Issues())

	real := newWatcher(t, nil)
	assert.NotPanics(t, func() { real.reconcile(nil) })
}
