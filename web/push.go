package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/status/storage"
)

// maybeAuth wraps a handler behind requireAuth only when `protect` is true.
// Used for endpoints whose exposure is a config decision, not a fixed policy.
func (s *Server) maybeAuth(protect bool, next http.HandlerFunc) http.HandlerFunc {
	if !protect {
		return next
	}
	return s.requireAuth(next)
}

// handlePushKey returns the VAPID application server key a browser needs to
// call pushManager.subscribe(). The public key is not a secret.
func (s *Server) handlePushKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pm := s.notifier.Push()
	if !pm.Enabled() {
		s.jsonResponse(w, map[string]any{"enabled": false})
		return
	}
	s.jsonResponse(w, map[string]any{
		"enabled":    true,
		"public_key": pm.PublicKey(),
	})
}

// pushSubscriptionRequest mirrors the shape of a browser PushSubscription
// serialised with JSON.stringify(sub), so the front-end can post it verbatim.
type pushSubscriptionRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	Topics []string `json:"topics"`
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pm := s.notifier.Push()
	if !pm.Enabled() {
		s.jsonError(w, "Web push is not enabled on this server", http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req pushSubscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !validPushEndpoint(req.Endpoint) {
		s.jsonError(w, "endpoint must be an https:// URL", http.StatusBadRequest)
		return
	}

	sub, err := pm.Subscribe(storage.PushSubscription{
		Endpoint:  req.Endpoint,
		P256dh:    req.Keys.P256dh,
		Auth:      req.Keys.Auth,
		UserAgent: truncateUA(r.UserAgent()),
		Topics:    req.Topics,
	})
	if err != nil {
		s.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.jsonResponse(w, map[string]any{
		"subscribed": true,
		"id":         sub.ID,
		"topics":     sub.Topics,
	})
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req pushSubscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Endpoint == "" {
		s.jsonError(w, "endpoint is required", http.StatusBadRequest)
		return
	}
	removed := s.notifier.Push().Unsubscribe(req.Endpoint)
	s.jsonResponse(w, map[string]any{"unsubscribed": removed})
}

// handleNotificationChannels reports which alert channels are live. Read-only
// and safe to expose: it returns counts and booleans, never topics or tokens.
func (s *Server) handleNotificationChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ch := s.notifier.Channels()
	ch["service_alerts_enabled"] = s.config.Alerts.Enabled
	s.jsonResponse(w, ch)
}

// handleNotificationTest fires a synthetic alert through every channel so an
// operator can prove their phone buzzes before an outage does it for them.
// Authenticated: it costs real push quota and rings real devices.
func (s *Server) handleNotificationTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.notifier.SendTest(s.config.BaseURL)
	s.jsonResponse(w, map[string]any{
		"sent":     true,
		"channels": s.notifier.Channels(),
	})
}

// handleServiceWorker serves the push service worker from the site root.
// A worker can only control pages at or below its own path, so /sw.js is the
// only location that works for the whole site.
func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	data, err := staticFiles.ReadFile("static/sw.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Service-Worker-Allowed", "/")
	_, _ = w.Write(data)
}

// validPushEndpoint rejects anything that is not an https URL. The endpoint is
// fetched by this server, so an unchecked value is an SSRF primitive.
func validPushEndpoint(ep string) bool {
	return strings.HasPrefix(ep, "https://") && len(ep) < 2048
}

func truncateUA(ua string) string {
	if len(ua) > 180 {
		return ua[:180]
	}
	return ua
}
