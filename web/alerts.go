package web

import (
	"log"
	"sync"
	"time"

	"github.com/status/config"
	"github.com/status/monitor"
	"github.com/status/notify"
)

// alertState is what the tracker remembers about one service between checks.
type alertState struct {
	status    string    // last status actually observed
	failures  int       // consecutive non-operational checks
	lastAlert time.Time // when this service last produced an alert
	firing    bool      // an unresolved alert is outstanding
}

// alertTracker turns the monitor's per-check stream into state *transitions*,
// and only those reach an operator's phone. Without it every 30s check of a
// down service would be a fresh notification.
type alertTracker struct {
	mu     sync.Mutex
	states map[string]*alertState

	cfg      config.AlertsConfig
	allowed  func(group string) bool
	notifier *notify.Notifier
	baseURL  string
}

func newAlertTracker(cfg *config.Config, n *notify.Notifier) *alertTracker {
	return &alertTracker{
		states:   make(map[string]*alertState),
		cfg:      cfg.Alerts,
		allowed:  cfg.AlertsGroupAllowed,
		notifier: n,
		baseURL:  cfg.BaseURL,
	}
}

// observe consumes one check result and fires an alert when — and only when —
// the service's health actually changed in a way an operator cares about.
func (t *alertTracker) observe(st *monitor.ServiceStatus) {
	if t == nil || st == nil || !t.cfg.Enabled || t.notifier == nil {
		return
	}
	// "unknown" is the pre-first-check placeholder, not a failure.
	if st.Status == monitor.StatusUnknown {
		return
	}
	if !t.allowed(st.Group) {
		return
	}

	status := string(st.Status)
	healthy := st.Status == monitor.StatusOperational

	t.mu.Lock()
	prev, ok := t.states[st.Name]
	if !ok {
		prev = &alertState{}
		t.states[st.Name] = prev
	}
	previousStatus := prev.status
	prev.status = status

	var send bool
	var alertStatus string

	switch {
	case healthy:
		prev.failures = 0
		if prev.firing {
			// Recovery: always worth an alert, cooldown does not apply — an
			// operator waiting on an outage needs the all-clear immediately.
			prev.firing = false
			prev.lastAlert = time.Now()
			send, alertStatus = true, status
		}

	default:
		prev.failures++
		threshold := t.cfg.FailureThreshold
		if threshold < 1 {
			threshold = 1
		}
		if prev.failures < threshold {
			break
		}
		switch {
		case !prev.firing:
			// First alert for this outage, subject to the flap cooldown.
			if prev.lastAlert.IsZero() || time.Since(prev.lastAlert) >= t.cfg.Cooldown {
				prev.firing = true
				prev.lastAlert = time.Now()
				send, alertStatus = true, status
			}
		case previousStatus != "" && previousStatus != status:
			// Escalation or de-escalation while still unhealthy
			// (degraded → down is news even mid-outage).
			if time.Since(prev.lastAlert) >= t.cfg.Cooldown {
				prev.lastAlert = time.Now()
				send, alertStatus = true, status
			}
		case t.cfg.RepeatEvery > 0 && time.Since(prev.lastAlert) >= t.cfg.RepeatEvery:
			// Still-down reminder.
			prev.lastAlert = time.Now()
			send, alertStatus = true, status
		}
	}
	t.mu.Unlock()

	if !send {
		return
	}

	alert := notify.ServiceAlert{
		Service:        st.Name,
		Group:          st.Group,
		Status:         alertStatus,
		Previous:       previousStatus,
		Severity:       notify.SeverityFor(alertStatus),
		Message:        st.ErrorMessage,
		ResponseTimeMs: st.ResponseTimeMs,
		Uptime:         st.Uptime,
		OccurredAt:     st.LastCheck,
	}
	log.Printf("alert: %s %s → %s", st.Name, previousStatus, alertStatus)
	t.notifier.NotifyServiceAlert(alert, t.baseURL)
}
