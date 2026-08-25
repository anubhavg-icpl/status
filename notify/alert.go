package notify

import (
	"fmt"
	"strings"
	"time"

	"github.com/kyokomi/emoji/v2"
	"github.com/status/storage"
)

// Event names emitted by the notifier. Webhooks filter on these strings.
const (
	EventIncidentCreated     = "incident.created"
	EventIncidentUpdated     = "incident.updated"
	EventIncidentResolved    = "incident.resolved"
	EventMaintenanceSchedule = "maintenance.scheduled"
	EventServiceDown         = "service.down"
	EventServiceDegraded     = "service.degraded"
	EventServiceRecovered    = "service.recovered"
	EventClusterDegraded     = "cluster.degraded"
	EventTest                = "test"
)

// ServiceAlert is emitted when a monitored service changes state. It is the
// payload every channel renders for service.* events.
type ServiceAlert struct {
	Service        string    `json:"service"`
	Group          string    `json:"group,omitempty"`
	Status         string    `json:"status"`             // operational | degraded | down
	Previous       string    `json:"previous,omitempty"` // status before the transition
	Severity       string    `json:"severity"`           // critical | major | minor
	Message        string    `json:"message,omitempty"`
	ResponseTimeMs int64     `json:"response_time_ms"`
	Uptime         float64   `json:"uptime"`
	OccurredAt     time.Time `json:"occurred_at"`
}

// Title is the one-line headline used by push, ntfy, Slack and Discord.
func (a ServiceAlert) Title() string {
	switch a.Status {
	case "operational":
		return fmt.Sprintf("%s recovered", a.Service)
	case "degraded":
		return fmt.Sprintf("%s degraded", a.Service)
	default:
		return fmt.Sprintf("%s is DOWN", a.Service)
	}
}

// Body is the human-readable detail line.
func (a ServiceAlert) Body() string {
	var b strings.Builder
	if a.Previous != "" && a.Previous != a.Status {
		fmt.Fprintf(&b, "%s → %s", a.Previous, a.Status)
	} else {
		b.WriteString(a.Status)
	}
	if a.Group != "" {
		fmt.Fprintf(&b, " · %s", a.Group)
	}
	if a.ResponseTimeMs > 0 {
		fmt.Fprintf(&b, " · %dms", a.ResponseTimeMs)
	}
	if a.Uptime > 0 {
		fmt.Fprintf(&b, " · %.2f%% uptime", a.Uptime)
	}
	if a.Message != "" {
		fmt.Fprintf(&b, "\n%s", a.Message)
	}
	return b.String()
}

// Event maps the alert's status to the event name webhooks subscribe to.
func (a ServiceAlert) Event() string {
	switch a.Status {
	case "operational":
		return EventServiceRecovered
	case "degraded":
		return EventServiceDegraded
	default:
		return EventServiceDown
	}
}

// SeverityFor derives an incident-style severity from a service status.
func SeverityFor(status string) string {
	switch status {
	case "down":
		return "critical"
	case "degraded":
		return "major"
	default:
		return "minor"
	}
}

// The library pads every replacement with a space by default, which doubles up
// against the space already in ":fire: disk full". Turn padding off and let the
// author's own spacing stand.
func init() { emoji.ReplacePadding = "" }

// StatusEmoji returns the glyph used across every channel for a status.
func StatusEmoji(status string) string {
	switch status {
	case "operational", "resolved", "completed":
		return emoji.Sprint(":white_check_mark:")
	case "degraded", "monitoring", "identified", "in_progress":
		return emoji.Sprint(":warning:")
	case "down", "investigating":
		return emoji.Sprint(":rotating_light:")
	case "scheduled":
		return emoji.Sprint(":calendar:")
	case "maintenance":
		return emoji.Sprint(":construction:")
	default:
		return emoji.Sprint(":grey_question:")
	}
}

// Emojify expands GitHub-style :shortcodes: into real glyphs so operators can
// write ":fire: disk full" in an incident and have it render everywhere —
// status page, feeds, Slack, push and ntfy alike.
func Emojify(s string) string {
	if !strings.Contains(s, ":") {
		return s // fast path: nothing to expand
	}
	return emoji.Sprint(s)
}

// emojifyIncident returns a copy with shortcodes expanded in user-authored text.
func emojifyIncident(v storage.Incident) storage.Incident {
	v.Title = Emojify(v.Title)
	v.Message = Emojify(v.Message)
	updates := make([]storage.IncidentUpdate, len(v.Updates))
	copy(updates, v.Updates)
	for i := range updates {
		updates[i].Message = Emojify(updates[i].Message)
	}
	v.Updates = updates
	return v
}

// emojifyMaintenance returns a copy with shortcodes expanded.
func emojifyMaintenance(v storage.Maintenance) storage.Maintenance {
	v.Title = Emojify(v.Title)
	v.Description = Emojify(v.Description)
	return v
}

// dashIfEmpty substitutes a placeholder for blank values. Discord rejects
// embed fields with an empty value outright, so this is correctness, not polish.
func dashIfEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
