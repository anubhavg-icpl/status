package notify

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AnthonyHewins/gotfy"
)

// NtfyConfig configures phone alerting through an ntfy server (ntfy.sh or a
// self-hosted instance). Subscribing to the topic in the ntfy mobile app is
// the whole setup on the phone side — no app store account, no FCM keys.
type NtfyConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// ServerURL defaults to https://ntfy.sh when empty.
	ServerURL string `yaml:"server_url" json:"server_url"`
	// Topic is the ntfy topic to publish to. Treat it as a secret: anyone who
	// knows a public-server topic name can read and write it.
	Topic string `yaml:"topic" json:"-"`
	// Token is an ntfy access token (tk_…). Takes precedence over user/pass.
	Token string `yaml:"token" json:"-"`
	// Username/Password for servers using basic auth.
	Username string `yaml:"username" json:"-"`
	Password string `yaml:"password" json:"-"`
	// Priority for routine alerts: min | low | default | high | max.
	Priority string `yaml:"priority" json:"priority"`
	// CriticalPriority is used for down/critical events. Defaults to max,
	// which bypasses the phone's Do Not Disturb on most setups.
	CriticalPriority string `yaml:"critical_priority" json:"critical_priority"`
	// Call places a voice call to this number on critical alerts. Requires a
	// paid ntfy.sh plan and a verified number.
	Call string `yaml:"call" json:"-"`
	// Email forwards alerts to this address via the ntfy server.
	Email string `yaml:"email" json:"-"`
	// Events limits which events are pushed. Empty means all of them.
	Events []string `yaml:"events" json:"events"`
}

// NtfySender publishes alerts to an ntfy topic.
type NtfySender struct {
	cfg PushableNtfy
	pub *gotfy.Publisher
}

// PushableNtfy is the resolved, validated form of NtfyConfig.
type PushableNtfy struct {
	NtfyConfig
	server *url.URL
}

// NewNtfySender validates the config and builds a publisher. It returns a
// disabled sender rather than an error when ntfy is simply switched off.
func NewNtfySender(cfg NtfyConfig) (*NtfySender, error) {
	s := &NtfySender{cfg: PushableNtfy{NtfyConfig: cfg}}
	if !cfg.Enabled {
		return s, nil
	}
	if strings.TrimSpace(cfg.Topic) == "" {
		s.cfg.Enabled = false
		return s, fmt.Errorf("ntfy enabled but no topic configured")
	}
	raw := cfg.ServerURL
	if raw == "" {
		raw = "https://ntfy.sh"
	}
	u, err := url.Parse(raw)
	if err != nil {
		s.cfg.Enabled = false
		return s, fmt.Errorf("ntfy server_url %q: %w", raw, err)
	}
	s.cfg.server = u

	opts := []gotfy.Option{
		gotfy.WithHTTPClient(&http.Client{Timeout: 15 * time.Second}),
	}
	if cfg.Token == "" && cfg.Username != "" {
		opts = append(opts, gotfy.WithAuth(cfg.Username, cfg.Password))
	}

	pub, err := gotfy.NewPublisher(u, opts...)
	if err != nil {
		s.cfg.Enabled = false
		return s, fmt.Errorf("ntfy publisher: %w", err)
	}
	if cfg.Token != "" {
		if pub.Headers == nil {
			pub.Headers = http.Header{}
		}
		pub.Headers.Set("Authorization", "Bearer "+cfg.Token)
	}
	s.pub = pub
	return s, nil
}

// Enabled reports whether ntfy publishing is live.
func (s *NtfySender) Enabled() bool {
	return s != nil && s.cfg.Enabled && s.pub != nil
}

// Send publishes one alert. Critical events are escalated to the configured
// critical priority and, when a number is set, trigger a voice call.
func (s *NtfySender) Send(ctx context.Context, event, title, body, severity, link string) {
	if !s.Enabled() {
		return
	}
	if !eventAllowed(s.cfg.Events, event) {
		return
	}

	critical := severity == "critical" || event == EventServiceDown || event == EventIncidentCreated
	prio := ntfyPriority(s.cfg.Priority, gotfy.High)
	if critical {
		prio = ntfyPriority(s.cfg.CriticalPriority, gotfy.Max)
	}

	msg := &gotfy.Message{
		Topic:    s.cfg.Topic,
		Title:    title,
		Message:  body,
		Priority: prio,
		Tags:     ntfyTags(event, severity),
		Email:    s.cfg.Email,
	}
	if critical && s.cfg.Call != "" {
		msg.Call = s.cfg.Call
	}
	if link != "" {
		if u, err := url.Parse(link); err == nil {
			msg.ClickURL = u
		}
	}

	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := s.pub.SendMessage(sctx, msg); err != nil {
		log.Printf("ntfy: publish to %q failed: %v", s.cfg.Topic, err)
	}
}

// ntfyTags maps an event to ntfy tag names, which the app renders as emoji.
func ntfyTags(event, severity string) []string {
	switch event {
	case EventServiceDown:
		return []string{"rotating_light", "red_circle"}
	case EventServiceDegraded:
		return []string{"warning", "yellow_circle"}
	case EventServiceRecovered:
		return []string{"white_check_mark", "green_circle"}
	case EventIncidentCreated:
		return []string{"rotating_light", "fire"}
	case EventIncidentUpdated:
		return []string{"pencil"}
	case EventIncidentResolved:
		return []string{"white_check_mark"}
	case EventMaintenanceSchedule:
		return []string{"construction", "calendar"}
	case EventClusterDegraded:
		return []string{"warning", "wheel_of_dharma"}
	case EventTest:
		return []string{"bell"}
	}
	if severity == "critical" {
		return []string{"rotating_light"}
	}
	return []string{"bell"}
}

func ntfyPriority(name string, def gotfy.Priority) gotfy.Priority {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "min":
		return gotfy.Min
	case "low":
		return gotfy.Low
	case "default", "normal":
		return gotfy.Default
	case "high":
		return gotfy.High
	case "max", "urgent":
		return gotfy.Max
	default:
		return def
	}
}

// eventAllowed reports whether an event passes a filter list.
// An empty list allows everything; "*" and prefix matches ("service") work too.
func eventAllowed(allowed []string, event string) bool {
	return topicMatches(allowed, event)
}
