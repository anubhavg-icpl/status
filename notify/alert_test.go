package notify

import (
	"testing"
	"time"

	"github.com/AnthonyHewins/gotfy"
	"github.com/status/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmojifyExpandsShortcodes(t *testing.T) {
	assert.Equal(t, "🔥 disk 90% full", Emojify(":fire: disk 90% full"),
		"percent signs must survive — Sprint, not Sprintf")
	assert.Equal(t, "no shortcodes here", Emojify("no shortcodes here"))
	assert.Equal(t, "", Emojify(""))
}

func TestEmojifyLeavesUnknownShortcodesAlone(t *testing.T) {
	assert.Equal(t, ":not_a_real_emoji: stays", Emojify(":not_a_real_emoji: stays"))
}

func TestStatusEmojiPerStatus(t *testing.T) {
	assert.Equal(t, "✅", StatusEmoji("operational"))
	assert.Equal(t, "⚠️", StatusEmoji("degraded"))
	assert.Equal(t, "🚨", StatusEmoji("down"))
	assert.NotEmpty(t, StatusEmoji("something-else"))
}

func TestServiceAlertRendering(t *testing.T) {
	a := ServiceAlert{
		Service:        "Invinsense API",
		Group:          "Core",
		Status:         "down",
		Previous:       "operational",
		Message:        "connection refused",
		ResponseTimeMs: 1200,
		Uptime:         98.5,
		OccurredAt:     time.Now(),
	}

	assert.Equal(t, "Invinsense API is DOWN", a.Title())
	assert.Equal(t, EventServiceDown, a.Event())
	assert.Equal(t, "critical", SeverityFor(a.Status))

	body := a.Body()
	assert.Contains(t, body, "operational → down")
	assert.Contains(t, body, "Core")
	assert.Contains(t, body, "1200ms")
	assert.Contains(t, body, "98.50% uptime")
	assert.Contains(t, body, "connection refused")
}

func TestServiceAlertEventMapping(t *testing.T) {
	assert.Equal(t, EventServiceRecovered, ServiceAlert{Status: "operational"}.Event())
	assert.Equal(t, EventServiceDegraded, ServiceAlert{Status: "degraded"}.Event())
	assert.Equal(t, EventServiceDown, ServiceAlert{Status: "down"}.Event())
}

func TestEmojifyIncidentDoesNotMutateInput(t *testing.T) {
	orig := storage.Incident{
		Title:   ":fire: outage",
		Message: ":warning: investigating",
		Updates: []storage.IncidentUpdate{{Message: ":rocket: deploying fix"}},
	}
	out := emojifyIncident(orig)

	assert.Equal(t, ":fire: outage", orig.Title, "caller's copy is untouched")
	assert.Equal(t, ":rocket: deploying fix", orig.Updates[0].Message)
	assert.Equal(t, "🔥 outage", out.Title)
	assert.Equal(t, "🚀 deploying fix", out.Updates[0].Message)
}

func TestTopicMatches(t *testing.T) {
	assert.True(t, topicMatches(nil, "service.down"), "empty filter means everything")
	assert.True(t, topicMatches([]string{"*"}, "incident.created"))
	assert.True(t, topicMatches([]string{"service"}, "service.down"), "prefix match")
	assert.True(t, topicMatches([]string{"service.down"}, "service.down"))
	assert.False(t, topicMatches([]string{"service"}, "incident.created"))
	assert.False(t, topicMatches([]string{"servic"}, "service.down"),
		"a partial prefix must not match without the dot boundary")
}

func TestNtfyPriorityMapping(t *testing.T) {
	assert.Equal(t, gotfy.Max, ntfyPriority("max", gotfy.Low))
	assert.Equal(t, gotfy.Max, ntfyPriority("URGENT", gotfy.Low))
	assert.Equal(t, gotfy.Min, ntfyPriority("min", gotfy.High))
	assert.Equal(t, gotfy.Default, ntfyPriority("normal", gotfy.High))
	assert.Equal(t, gotfy.High, ntfyPriority("nonsense", gotfy.High), "falls back")
}

func TestNewNtfySenderRejectsMissingTopic(t *testing.T) {
	s, err := NewNtfySender(NtfyConfig{Enabled: true})
	require.Error(t, err)
	assert.False(t, s.Enabled(), "a misconfigured sender must never claim to be live")
}

func TestNewNtfySenderDisabledIsQuiet(t *testing.T) {
	s, err := NewNtfySender(NtfyConfig{Enabled: false})
	require.NoError(t, err)
	assert.False(t, s.Enabled())
	// Must be a no-op rather than a panic.
	s.Send(t.Context(), EventServiceDown, "t", "b", "critical", "")
}

func TestNilChannelsAreSafe(t *testing.T) {
	var p *PushManager
	var n *NtfySender
	assert.False(t, p.Enabled())
	assert.False(t, n.Enabled())
	assert.Equal(t, "", p.PublicKey())
	assert.Zero(t, p.Count())
	assert.False(t, p.Unsubscribe("https://example.com/x"))
}

func TestRenderAlertForServiceAlert(t *testing.T) {
	title, body, severity, link := renderAlert(EventServiceDown, ServiceAlert{
		Service: "db", Status: "down", Message: "timeout",
	}, "https://status.example.com/")

	assert.Contains(t, title, "db is DOWN")
	assert.Contains(t, body, "timeout")
	assert.Equal(t, "critical", severity)
	assert.Equal(t, "https://status.example.com/", link)
}

func TestRenderAlertForIncidentBuildsPermalink(t *testing.T) {
	title, body, severity, link := renderAlert(EventIncidentCreated, storage.Incident{
		ID: "abc123", Title: "API outage", Message: "5xx spike",
		Severity: "major", Status: "investigating",
		AffectedServices: []string{"api", "web"},
	}, "https://status.example.com/")

	assert.Contains(t, title, "API outage")
	assert.Contains(t, body, "Affected: api, web")
	assert.Equal(t, "major", severity)
	assert.Equal(t, "https://status.example.com/incidents/abc123", link,
		"the trailing slash on base_url must not double up")
}

func TestAlertTagGroupsByEntity(t *testing.T) {
	assert.Equal(t, "service:api", alertTag(EventServiceDown, ServiceAlert{Service: "api"}))
	assert.Equal(t, "incident:x1", alertTag(EventIncidentCreated, storage.Incident{ID: "x1"}))
	assert.Equal(t, "maintenance:m1", alertTag(EventMaintenanceSchedule, storage.Maintenance{ID: "m1"}))
}

func TestDashIfEmpty(t *testing.T) {
	assert.Equal(t, "—", dashIfEmpty(""))
	assert.Equal(t, "—", dashIfEmpty("   "))
	assert.Equal(t, "Core", dashIfEmpty("Core"))
}

func TestWebhookFormattersHandleServiceAlerts(t *testing.T) {
	n := NewNotifier(nil)
	alert := ServiceAlert{Service: "api", Status: "down", OccurredAt: time.Now()}

	for name, fn := range map[string]func() ([]byte, error){
		"slack":    func() ([]byte, error) { return n.formatSlackPayload(EventServiceDown, alert, "http://x") },
		"discord":  func() ([]byte, error) { return n.formatDiscordPayload(EventServiceDown, alert, "http://x") },
		"teams":    func() ([]byte, error) { return n.formatMSTeamsPayload(EventServiceDown, alert, "http://x") },
		"opsgenie": func() ([]byte, error) { return n.formatOpsgeniePayload(EventServiceDown, alert) },
	} {
		t.Run(name, func(t *testing.T) {
			b, err := fn()
			require.NoError(t, err)
			assert.Contains(t, string(b), "api", "the service name must survive into the payload")
		})
	}
}

func TestPagerDutyResolvesOnRecovery(t *testing.T) {
	n := NewNotifier(nil)
	wh := WebhookConfig{Headers: map[string]string{"routing_key": "rk"}}

	down, err := n.formatPagerDutyPayload(EventServiceDown,
		ServiceAlert{Service: "api", Status: "down"}, wh)
	require.NoError(t, err)
	assert.Contains(t, string(down), `"event_action":"trigger"`)
	assert.Contains(t, string(down), `"dedup_key":"service:api"`)

	up, err := n.formatPagerDutyPayload(EventServiceRecovered,
		ServiceAlert{Service: "api", Status: "operational"}, wh)
	require.NoError(t, err)
	assert.Contains(t, string(up), `"event_action":"resolve"`)
	assert.Contains(t, string(up), `"dedup_key":"service:api"`,
		"the resolve must carry the same dedup key or PagerDuty leaves the alert open")
}
