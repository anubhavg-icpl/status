package notify

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/status/storage"
)

// SetPushManager attaches a Web Push manager. Safe to call with nil.
func (n *Notifier) SetPushManager(p *PushManager) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.push = p
}

// SetNtfySender attaches an ntfy publisher. Safe to call with nil.
func (n *Notifier) SetNtfySender(s *NtfySender) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ntfy = s
}

// Push exposes the attached push manager (nil when unset).
func (n *Notifier) Push() *PushManager {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.push
}

// Ntfy exposes the attached ntfy sender (nil when unset).
func (n *Notifier) Ntfy() *NtfySender {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.ntfy
}

// Channels reports which delivery channels are live, for /api/notifications.
func (n *Notifier) Channels() map[string]any {
	n.mu.RLock()
	webhooks := 0
	for _, w := range n.webhooks {
		if w.Enabled {
			webhooks++
		}
	}
	push, ntfy := n.push, n.ntfy
	n.mu.RUnlock()

	out := map[string]any{
		"webhooks_enabled": webhooks,
		"push_enabled":     push.Enabled(),
		"ntfy_enabled":     ntfy.Enabled(),
	}
	if push.Enabled() {
		out["push_subscriptions"] = push.Count()
	}
	return out
}

// dispatch delivers one event to every configured channel: webhooks, Web Push
// and ntfy. Each channel is independent — a failure in one never blocks or
// suppresses the others.
func (n *Notifier) dispatch(event string, data any, baseURL string) {
	n.notify(event, data, baseURL)

	title, body, severity, link := renderAlert(event, data, baseURL)

	n.mu.RLock()
	push, ntfy := n.push, n.ntfy
	n.mu.RUnlock()

	if push.Enabled() {
		go push.Broadcast(context.Background(), PushPayload{
			Title:     title,
			Body:      body,
			Event:     event,
			Severity:  severity,
			URL:       link,
			Tag:       alertTag(event, data),
			Timestamp: time.Now(),
		})
	}
	if ntfy.Enabled() {
		go ntfy.Send(context.Background(), event, title, body, severity, link)
	}
}

// SendTest pushes a synthetic alert through every channel so an operator can
// confirm their phone actually buzzes before an outage proves otherwise.
func (n *Notifier) SendTest(baseURL string) {
	n.dispatch(EventTest, ServiceAlert{
		Service:    "Test notification",
		Status:     "operational",
		Severity:   "minor",
		Message:    "If you can read this, alerting works end to end.",
		OccurredAt: time.Now(),
	}, baseURL)
}

// renderAlert turns any event payload into the title/body/severity/link tuple
// that push and ntfy both need.
func renderAlert(event string, data any, baseURL string) (title, body, severity, link string) {
	switch v := data.(type) {
	case ServiceAlert:
		if event == EventTest {
			return StatusEmoji("operational") + " Test notification",
				v.Message, "minor", baseURL
		}
		return StatusEmoji(v.Status) + " " + v.Title(), v.Body(), SeverityFor(v.Status), baseURL

	case storage.Incident:
		title = fmt.Sprintf("%s %s", StatusEmoji(v.Status), v.Title)
		body = v.Message
		if len(v.AffectedServices) > 0 {
			body = strings.TrimSpace(body + "\nAffected: " + strings.Join(v.AffectedServices, ", "))
		}
		severity = v.Severity
		if severity == "" {
			severity = "minor"
		}
		link = fmt.Sprintf("%s/incidents/%s", strings.TrimRight(baseURL, "/"), v.ID)
		return

	case storage.Maintenance:
		title = fmt.Sprintf("%s Maintenance: %s", StatusEmoji("maintenance"), v.Title)
		body = strings.TrimSpace(fmt.Sprintf("%s\n%s → %s",
			v.Description,
			v.ScheduledStart.Format("Jan 02 15:04 MST"),
			v.ScheduledEnd.Format("Jan 02 15:04 MST"),
		))
		return title, body, "minor", strings.TrimRight(baseURL, "/")
	}

	return event, fmt.Sprintf("%v", data), "minor", strings.TrimRight(baseURL, "/")
}

// alertTag groups notifications so a phone replaces the previous alert for the
// same service or incident instead of stacking a wall of them.
func alertTag(event string, data any) string {
	switch v := data.(type) {
	case ServiceAlert:
		return "service:" + v.Service
	case storage.Incident:
		return "incident:" + v.ID
	case storage.Maintenance:
		return "maintenance:" + v.ID
	}
	return event
}
