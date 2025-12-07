package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/status/storage"
)

// Notifier handles sending notifications via webhooks
type Notifier struct {
	webhooks    []WebhookConfig
	subscribers []Subscriber
	mu          sync.RWMutex
	client      *http.Client
}

// WebhookConfig represents a webhook configuration
type WebhookConfig struct {
	ID      string            `json:"id" yaml:"id"`
	Name    string            `json:"name" yaml:"name"`
	URL     string            `json:"url" yaml:"url"`
	Type    string            `json:"type" yaml:"type"` // generic, slack, discord, teams, pagerduty
	Events  []string          `json:"events" yaml:"events"` // incident.created, incident.updated, incident.resolved, maintenance.scheduled
	Headers map[string]string `json:"headers" yaml:"headers"`
	Enabled bool              `json:"enabled" yaml:"enabled"`
}

// Subscriber represents an email subscriber
type Subscriber struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Verified  bool      `json:"verified"`
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
	Services  []string  `json:"services"` // Empty means all services
}

// WebhookPayload is the generic webhook payload
type WebhookPayload struct {
	Event     string      `json:"event"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"`
}

// SlackPayload for Slack webhooks
type SlackPayload struct {
	Text        string            `json:"text,omitempty"`
	Attachments []SlackAttachment `json:"attachments,omitempty"`
}

type SlackAttachment struct {
	Color      string       `json:"color"`
	Title      string       `json:"title"`
	TitleLink  string       `json:"title_link,omitempty"`
	Text       string       `json:"text"`
	Fields     []SlackField `json:"fields,omitempty"`
	Footer     string       `json:"footer,omitempty"`
	Ts         int64        `json:"ts,omitempty"`
}

type SlackField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

// DiscordPayload for Discord webhooks
type DiscordPayload struct {
	Content string         `json:"content,omitempty"`
	Embeds  []DiscordEmbed `json:"embeds,omitempty"`
}

type DiscordEmbed struct {
	Title       string               `json:"title"`
	Description string               `json:"description"`
	URL         string               `json:"url,omitempty"`
	Color       int                  `json:"color"`
	Fields      []DiscordEmbedField  `json:"fields,omitempty"`
	Timestamp   string               `json:"timestamp,omitempty"`
	Footer      *DiscordEmbedFooter  `json:"footer,omitempty"`
}

type DiscordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type DiscordEmbedFooter struct {
	Text string `json:"text"`
}

// NewNotifier creates a new notifier
func NewNotifier(webhooks []WebhookConfig) *Notifier {
	return &Notifier{
		webhooks:    webhooks,
		subscribers: []Subscriber{},
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// AddWebhook adds a webhook
func (n *Notifier) AddWebhook(webhook WebhookConfig) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.webhooks = append(n.webhooks, webhook)
}

// NotifyIncidentCreated notifies about a new incident
func (n *Notifier) NotifyIncidentCreated(incident storage.Incident, baseURL string) {
	n.notify("incident.created", incident, baseURL)
}

// NotifyIncidentUpdated notifies about an incident update
func (n *Notifier) NotifyIncidentUpdated(incident storage.Incident, baseURL string) {
	n.notify("incident.updated", incident, baseURL)
}

// NotifyIncidentResolved notifies about a resolved incident
func (n *Notifier) NotifyIncidentResolved(incident storage.Incident, baseURL string) {
	n.notify("incident.resolved", incident, baseURL)
}

// NotifyMaintenanceScheduled notifies about scheduled maintenance
func (n *Notifier) NotifyMaintenanceScheduled(maintenance storage.Maintenance, baseURL string) {
	n.notify("maintenance.scheduled", maintenance, baseURL)
}

func (n *Notifier) notify(event string, data interface{}, baseURL string) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	for _, webhook := range n.webhooks {
		if !webhook.Enabled {
			continue
		}

		// Check if webhook is subscribed to this event
		if !n.isSubscribedToEvent(webhook, event) {
			continue
		}

		go n.sendWebhook(webhook, event, data, baseURL)
	}
}

func (n *Notifier) isSubscribedToEvent(webhook WebhookConfig, event string) bool {
	if len(webhook.Events) == 0 {
		return true // Subscribe to all events by default
	}
	for _, e := range webhook.Events {
		if e == event || e == "*" {
			return true
		}
	}
	return false
}

func (n *Notifier) sendWebhook(webhook WebhookConfig, event string, data interface{}, baseURL string) {
	var payload []byte
	var err error

	switch webhook.Type {
	case "slack":
		payload, err = n.formatSlackPayload(event, data, baseURL)
	case "discord":
		payload, err = n.formatDiscordPayload(event, data, baseURL)
	default:
		payload, err = json.Marshal(WebhookPayload{
			Event:     event,
			Timestamp: time.Now(),
			Data:      data,
		})
	}

	if err != nil {
		log.Printf("Error formatting webhook payload: %v", err)
		return
	}

	req, err := http.NewRequest("POST", webhook.URL, bytes.NewBuffer(payload))
	if err != nil {
		log.Printf("Error creating webhook request: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	for key, value := range webhook.Headers {
		req.Header.Set(key, value)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		log.Printf("Error sending webhook to %s: %v", webhook.Name, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		log.Printf("Webhook %s returned status %d", webhook.Name, resp.StatusCode)
	}
}

func (n *Notifier) formatSlackPayload(event string, data interface{}, baseURL string) ([]byte, error) {
	var attachment SlackAttachment

	switch v := data.(type) {
	case storage.Incident:
		color := n.severityToColor(v.Severity)
		attachment = SlackAttachment{
			Color:     color,
			Title:     fmt.Sprintf("[%s] %s", v.Status, v.Title),
			TitleLink: fmt.Sprintf("%s/incidents/%s", baseURL, v.ID),
			Text:      v.Message,
			Fields: []SlackField{
				{Title: "Status", Value: v.Status, Short: true},
				{Title: "Severity", Value: v.Severity, Short: true},
			},
			Footer: "Status Monitor",
			Ts:     v.UpdatedAt.Unix(),
		}
		if len(v.AffectedServices) > 0 {
			attachment.Fields = append(attachment.Fields, SlackField{
				Title: "Affected Services",
				Value: fmt.Sprintf("%v", v.AffectedServices),
				Short: false,
			})
		}

	case storage.Maintenance:
		attachment = SlackAttachment{
			Color:     "#3498db",
			Title:     fmt.Sprintf("Scheduled Maintenance: %s", v.Title),
			TitleLink: fmt.Sprintf("%s/maintenance/%s", baseURL, v.ID),
			Text:      v.Description,
			Fields: []SlackField{
				{Title: "Start", Value: v.ScheduledStart.Format("Jan 02, 2006 15:04 MST"), Short: true},
				{Title: "End", Value: v.ScheduledEnd.Format("Jan 02, 2006 15:04 MST"), Short: true},
			},
			Footer: "Status Monitor",
			Ts:     v.CreatedAt.Unix(),
		}
	}

	return json.Marshal(SlackPayload{
		Attachments: []SlackAttachment{attachment},
	})
}

func (n *Notifier) formatDiscordPayload(event string, data interface{}, baseURL string) ([]byte, error) {
	var embed DiscordEmbed

	switch v := data.(type) {
	case storage.Incident:
		color := n.severityToDiscordColor(v.Severity)
		embed = DiscordEmbed{
			Title:       fmt.Sprintf("[%s] %s", v.Status, v.Title),
			Description: v.Message,
			URL:         fmt.Sprintf("%s/incidents/%s", baseURL, v.ID),
			Color:       color,
			Fields: []DiscordEmbedField{
				{Name: "Status", Value: v.Status, Inline: true},
				{Name: "Severity", Value: v.Severity, Inline: true},
			},
			Timestamp: v.UpdatedAt.Format(time.RFC3339),
			Footer:    &DiscordEmbedFooter{Text: "Status Monitor"},
		}
		if len(v.AffectedServices) > 0 {
			embed.Fields = append(embed.Fields, DiscordEmbedField{
				Name:   "Affected Services",
				Value:  fmt.Sprintf("%v", v.AffectedServices),
				Inline: false,
			})
		}

	case storage.Maintenance:
		embed = DiscordEmbed{
			Title:       fmt.Sprintf("Scheduled Maintenance: %s", v.Title),
			Description: v.Description,
			URL:         fmt.Sprintf("%s/maintenance/%s", baseURL, v.ID),
			Color:       3447003, // Blue
			Fields: []DiscordEmbedField{
				{Name: "Start", Value: v.ScheduledStart.Format("Jan 02, 2006 15:04 MST"), Inline: true},
				{Name: "End", Value: v.ScheduledEnd.Format("Jan 02, 2006 15:04 MST"), Inline: true},
			},
			Timestamp: v.CreatedAt.Format(time.RFC3339),
			Footer:    &DiscordEmbedFooter{Text: "Status Monitor"},
		}
	}

	return json.Marshal(DiscordPayload{
		Embeds: []DiscordEmbed{embed},
	})
}

func (n *Notifier) severityToColor(severity string) string {
	switch severity {
	case "critical":
		return "#e74c3c"
	case "major":
		return "#f39c12"
	case "minor":
		return "#3498db"
	default:
		return "#95a5a6"
	}
}

func (n *Notifier) severityToDiscordColor(severity string) int {
	switch severity {
	case "critical":
		return 15158332 // Red
	case "major":
		return 15105570 // Orange
	case "minor":
		return 3447003 // Blue
	default:
		return 9807270 // Gray
	}
}
