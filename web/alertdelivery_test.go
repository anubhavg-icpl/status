package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/status/config"
	"github.com/status/k8sclient"
	"github.com/status/notify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ntfySpy stands in for an ntfy server and records what was published.
type ntfySpy struct {
	mu   sync.Mutex
	got  []map[string]any
	auth []string
	srv  *httptest.Server
}

func newNtfySpy(t *testing.T) *ntfySpy {
	t.Helper()
	spy := &ntfySpy{}
	spy.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		spy.mu.Lock()
		spy.got = append(spy.got, body)
		spy.auth = append(spy.auth, r.Header.Get("Authorization"))
		spy.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"spy"}`))
	}))
	t.Cleanup(spy.srv.Close)
	return spy
}

// waitFor polls until n messages have arrived or the deadline passes. The
// notifier fans out in goroutines, so the assertion has to be patient.
func (s *ntfySpy) waitFor(t *testing.T, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		if len(s.got) >= n {
			out := append([]map[string]any(nil), s.got...)
			s.mu.Unlock()
			return out
		}
		s.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Fatalf("timed out waiting for %d ntfy message(s), got %d", n, len(s.got))
	return nil
}

func wiredNotifier(t *testing.T, spy *ntfySpy) *notify.Notifier {
	t.Helper()
	n := notify.NewNotifier(nil)
	sender, err := notify.NewNtfySender(notify.NtfyConfig{
		Enabled:          true,
		ServerURL:        spy.srv.URL,
		Topic:            "invinsense-alerts",
		Token:            "tk_test",
		Priority:         "high",
		CriticalPriority: "max",
	})
	require.NoError(t, err)
	require.True(t, sender.Enabled())
	n.SetNtfySender(sender)
	return n
}

func TestClusterIssueReachesNtfy(t *testing.T) {
	spy := newNtfySpy(t)

	cfg := config.DefaultConfig()
	cfg.Alerts.Cooldown = 0
	cfg.Alerts.Cluster = config.ClusterAlertsConfig{
		Enabled: true, Interval: time.Second, MinDuration: 0,
		MinSeverity: k8sclient.SeverityMajor, MaxTracked: 50,
	}
	w := newClusterWatcher(cfg, wiredNotifier(t, spy))

	// A deployment with no ready replicas is the canonical "app is down".
	w.reconcile(&k8sclient.Snapshot{Workloads: []k8sclient.WorkloadInfo{
		{Kind: "Deployment", Namespace: "invinsense", Name: "uec-ingest",
			Status: "down", Desired: 3, Image: "harbor/uec-ingest:2.14.1"},
	}})

	msgs := spy.waitFor(t, 1)
	require.Len(t, msgs, 1)

	assert.Equal(t, "invinsense-alerts", msgs[0]["topic"])
	assert.Contains(t, msgs[0]["title"], "Deployment invinsense/uec-ingest")
	assert.Contains(t, msgs[0]["title"], "NoReadyReplicas")
	assert.Contains(t, msgs[0]["message"], "0/3 ready")
	// Critical failures must use max priority: that is what bypasses a phone's
	// Do Not Disturb.
	assert.EqualValues(t, 5, msgs[0]["priority"])

	spy.mu.Lock()
	assert.Equal(t, "Bearer tk_test", spy.auth[0])
	spy.mu.Unlock()
}

func TestClusterIssueRecoveryReachesNtfy(t *testing.T) {
	spy := newNtfySpy(t)

	cfg := config.DefaultConfig()
	cfg.Alerts.Cooldown = 0
	cfg.Alerts.Cluster = config.ClusterAlertsConfig{
		Enabled: true, Interval: time.Second, MinDuration: 0,
		MinSeverity: k8sclient.SeverityMajor, MaxTracked: 50,
	}
	w := newClusterWatcher(cfg, wiredNotifier(t, spy))

	broken := &k8sclient.Snapshot{Nodes: []k8sclient.NodeInfo{
		{Name: "work-02", Ready: false, Conditions: []string{"NotReady"}},
	}}
	w.reconcile(broken)
	spy.waitFor(t, 1)

	// Node comes back.
	w.reconcile(&k8sclient.Snapshot{Nodes: []k8sclient.NodeInfo{
		{Name: "work-02", Ready: true},
	}})
	msgs := spy.waitFor(t, 2)

	assert.Contains(t, msgs[1]["title"], "recovered",
		"an operator who was paged must also be told it cleared")
	assert.Contains(t, msgs[1]["message"], "cleared after")
}

func TestBelowSeverityFloorNeverReachesNtfy(t *testing.T) {
	spy := newNtfySpy(t)

	cfg := config.DefaultConfig()
	cfg.Alerts.Cooldown = 0
	cfg.Alerts.Cluster = config.ClusterAlertsConfig{
		Enabled: true, Interval: time.Second, MinDuration: 0,
		MinSeverity: k8sclient.SeverityCritical, MaxTracked: 50,
	}
	w := newClusterWatcher(cfg, wiredNotifier(t, spy))

	// Major, not critical.
	w.reconcile(&k8sclient.Snapshot{Workloads: []k8sclient.WorkloadInfo{
		{Kind: "Deployment", Namespace: "prod", Name: "api", Status: "degraded", Ready: 1, Desired: 2},
	}})

	time.Sleep(250 * time.Millisecond)
	spy.mu.Lock()
	defer spy.mu.Unlock()
	assert.Empty(t, spy.got, "the severity floor must actually stop delivery, not just the UI badge")
}

func TestServiceAlertReachesNtfy(t *testing.T) {
	spy := newNtfySpy(t)
	n := wiredNotifier(t, spy)

	n.NotifyServiceAlert(notify.ServiceAlert{
		Service: "Invinsense API", Group: "Core", Status: "down",
		Previous: "operational", Message: "connection refused",
		ResponseTimeMs: 1500, Uptime: 99.1, OccurredAt: time.Now(),
	}, "https://status.invinsense.dev")

	msgs := spy.waitFor(t, 1)
	assert.Contains(t, msgs[0]["title"], "Invinsense API is DOWN")
	assert.Contains(t, msgs[0]["message"], "connection refused")
	assert.Equal(t, "https://status.invinsense.dev", msgs[0]["click"])
	assert.EqualValues(t, 5, msgs[0]["priority"])
}
