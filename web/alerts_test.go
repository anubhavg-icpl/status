package web

import (
	"testing"
	"time"

	"github.com/status/config"
	"github.com/status/monitor"
	"github.com/status/notify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tracker decides whether a phone rings, so every branch below is tested
// against the notifier's own state rather than a mock: firing/lastAlert are
// what actually gate delivery.
func newTracker(t *testing.T, mutate func(*config.Config)) *alertTracker {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Alerts.Enabled = true
	cfg.Alerts.FailureThreshold = 1
	cfg.Alerts.Cooldown = 0
	cfg.Alerts.RepeatEvery = 0
	if mutate != nil {
		mutate(cfg)
	}
	return newAlertTracker(cfg, notify.NewNotifier(nil))
}

func status(name string, s monitor.Status) *monitor.ServiceStatus {
	return &monitor.ServiceStatus{
		Name:      name,
		Group:     "Core",
		Status:    s,
		LastCheck: time.Now(),
	}
}

// fired reports whether observe() would have produced an alert, read off the
// tracker's own bookkeeping.
func fired(t *alertTracker, name string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.states[name]
	return st != nil && st.firing
}

func TestTrackerIgnoresUnknownStatus(t *testing.T) {
	tr := newTracker(t, nil)
	tr.observe(status("api", monitor.StatusUnknown))
	assert.False(t, fired(tr, "api"), "the pre-first-check placeholder is not an outage")
}

func TestTrackerFiresOnceThenStaysQuiet(t *testing.T) {
	tr := newTracker(t, nil)

	tr.observe(status("api", monitor.StatusDown))
	require.True(t, fired(tr, "api"))

	before := lastAlertOf(tr, "api")
	// Repeat checks of an already-down service must not re-alert.
	for i := 0; i < 5; i++ {
		tr.observe(status("api", monitor.StatusDown))
	}
	assert.Equal(t, before, lastAlertOf(tr, "api"), "no repeat alerts without repeat_every")
}

func TestTrackerClearsOnRecovery(t *testing.T) {
	tr := newTracker(t, nil)
	tr.observe(status("api", monitor.StatusDown))
	require.True(t, fired(tr, "api"))

	tr.observe(status("api", monitor.StatusOperational))
	assert.False(t, fired(tr, "api"), "recovery clears the outstanding alert")

	// A healthy service that was never down must not alert on recovery.
	tr2 := newTracker(t, nil)
	tr2.observe(status("web", monitor.StatusOperational))
	assert.False(t, fired(tr2, "web"))
	assert.True(t, lastAlertOf(tr2, "web").IsZero(), "no alert was ever sent")
}

func TestTrackerHonoursFailureThreshold(t *testing.T) {
	tr := newTracker(t, func(c *config.Config) { c.Alerts.FailureThreshold = 3 })

	tr.observe(status("api", monitor.StatusDown))
	assert.False(t, fired(tr, "api"), "1 of 3")
	tr.observe(status("api", monitor.StatusDown))
	assert.False(t, fired(tr, "api"), "2 of 3")
	tr.observe(status("api", monitor.StatusDown))
	assert.True(t, fired(tr, "api"), "3 of 3 — now it is a real outage")
}

func TestTrackerResetsFailureCountOnRecovery(t *testing.T) {
	tr := newTracker(t, func(c *config.Config) { c.Alerts.FailureThreshold = 3 })

	tr.observe(status("api", monitor.StatusDown))
	tr.observe(status("api", monitor.StatusDown))
	tr.observe(status("api", monitor.StatusOperational))
	tr.observe(status("api", monitor.StatusDown))
	assert.False(t, fired(tr, "api"),
		"a recovery in between must restart the count, not carry it over")
}

func TestTrackerCooldownSuppressesFlapping(t *testing.T) {
	tr := newTracker(t, func(c *config.Config) { c.Alerts.Cooldown = time.Hour })

	tr.observe(status("api", monitor.StatusDown))
	require.True(t, fired(tr, "api"))

	// Recovery always alerts — an operator waiting on an outage needs the
	// all-clear regardless of cooldown.
	tr.observe(status("api", monitor.StatusOperational))
	afterRecovery := lastAlertOf(tr, "api")
	require.False(t, afterRecovery.IsZero())

	// Immediate re-failure: this is the flap the cooldown exists to absorb.
	tr.observe(status("api", monitor.StatusDown))
	assert.False(t, fired(tr, "api"), "the cooldown blocks the re-fire")
	assert.Equal(t, afterRecovery, lastAlertOf(tr, "api"),
		"a suppressed alert must not move the clock, or the cooldown never expires")
}

func TestTrackerEscalatesDegradedToDown(t *testing.T) {
	tr := newTracker(t, nil)

	tr.observe(status("api", monitor.StatusDegraded))
	first := lastAlertOf(tr, "api")
	require.False(t, first.IsZero())

	time.Sleep(2 * time.Millisecond)
	tr.observe(status("api", monitor.StatusDown))
	assert.True(t, lastAlertOf(tr, "api").After(first),
		"degraded → down mid-outage is news worth a second alert")
}

func TestTrackerRespectsGroupFilter(t *testing.T) {
	tr := newTracker(t, func(c *config.Config) { c.Alerts.OnlyGroups = []string{"Payments"} })

	tr.observe(status("api", monitor.StatusDown)) // group "Core"
	assert.False(t, fired(tr, "api"), "out-of-scope groups never alert")

	payments := status("checkout", monitor.StatusDown)
	payments.Group = "payments" // case-insensitive match
	tr.observe(payments)
	assert.True(t, fired(tr, "checkout"))
}

func TestTrackerDisabledDoesNothing(t *testing.T) {
	tr := newTracker(t, func(c *config.Config) { c.Alerts.Enabled = false })
	tr.observe(status("api", monitor.StatusDown))
	assert.False(t, fired(tr, "api"))
}

func TestTrackerNilSafe(t *testing.T) {
	var tr *alertTracker
	assert.NotPanics(t, func() { tr.observe(status("api", monitor.StatusDown)) })

	real := newTracker(t, nil)
	assert.NotPanics(t, func() { real.observe(nil) })
}

func lastAlertOf(t *alertTracker, name string) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	if st := t.states[name]; st != nil {
		return st.lastAlert
	}
	return time.Time{}
}
