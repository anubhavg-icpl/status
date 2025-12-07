package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/status/config"
	"github.com/status/feeds"
	"github.com/status/monitor"
	"github.com/status/notify"
	"github.com/status/storage"
)

//go:embed static/*
var staticFiles embed.FS

//go:embed templates/*
var templateFiles embed.FS

// Server represents the web server
type Server struct {
	config      *config.Config
	monitor     *monitor.Monitor
	storage     *storage.Storage
	notifier    *notify.Notifier
	feedGen     *feeds.FeedGenerator
	upgrader    websocket.Upgrader
	clients     map[*websocket.Conn]bool
	clientMu    sync.RWMutex
	server      *http.Server
}

// NewServer creates a new web server instance
func NewServer(cfg *config.Config, mon *monitor.Monitor, store *storage.Storage, notif *notify.Notifier) *Server {
	return &Server{
		config:   cfg,
		monitor:  mon,
		storage:  store,
		notifier: notif,
		feedGen:  feeds.NewFeedGenerator(cfg.Title, cfg.BaseURL),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
		clients: make(map[*websocket.Conn]bool),
	}
}

// Start starts the web server
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// Serve static files
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return fmt.Errorf("failed to create static filesystem: %w", err)
	}
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// Favicon
	mux.HandleFunc("/favicon.ico", s.handleFavicon)
	mux.HandleFunc("/favicon.svg", s.handleFavicon)

	// === Public API Routes ===
	mux.HandleFunc("/api/status", s.handleAPIStatus)
	mux.HandleFunc("/api/status/", s.handleAPIServiceStatus)
	mux.HandleFunc("/api/summary", s.handleAPISummary)
	mux.HandleFunc("/api/components", s.handleAPIComponents)

	// History API
	mux.HandleFunc("/api/history", s.handleAPIHistory)
	mux.HandleFunc("/api/history/", s.handleAPIServiceHistory)
	mux.HandleFunc("/api/uptime", s.handleAPIUptime)

	// Incidents API (public read, authenticated write)
	mux.HandleFunc("/api/incidents", s.handleAPIIncidents)
	mux.HandleFunc("/api/incidents/", s.handleAPIIncident)

	// Maintenance API
	mux.HandleFunc("/api/maintenance", s.handleAPIMaintenance)
	mux.HandleFunc("/api/maintenance/", s.handleAPIMaintenanceItem)

	// Metrics API
	mux.HandleFunc("/api/metrics", s.handleAPIMetrics)

	// === Feed Routes ===
	mux.HandleFunc("/feed/rss", s.handleRSSFeed)
	mux.HandleFunc("/feed/atom", s.handleAtomFeed)
	mux.HandleFunc("/feed/json", s.handleJSONFeed)
	mux.HandleFunc("/feed", s.handleRSSFeed) // Default to RSS

	// === Subscription Routes ===
	mux.HandleFunc("/api/subscribe", s.handleSubscribe)

	// WebSocket endpoint
	mux.HandleFunc("/ws", s.handleWebSocket)

	// Main pages
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/history", s.handleHistoryPage)
	mux.HandleFunc("/incidents/", s.handleIncidentPage)

	s.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.config.Server.Port),
		Handler:      s.withMiddleware(mux),
		ReadTimeout:  s.config.Server.ReadTimeout,
		WriteTimeout: s.config.Server.WriteTimeout,
	}

	// Start broadcasting updates
	go s.broadcastUpdates()

	// Start daily history recorder
	go s.recordDailyHistory()

	log.Printf("Starting server on http://localhost:%d", s.config.Server.Port)
	return s.server.ListenAndServe()
}

// Stop gracefully stops the server
func (s *Server) Stop(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// Middleware
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Auth middleware for admin endpoints - supports multiple auth methods
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check if any auth is configured
		hasAuth := s.config.API.Key != "" ||
			s.config.API.BearerToken != "" ||
			s.config.API.BasicAuth.Enabled

		if !hasAuth {
			next(w, r)
			return
		}

		// Check IP whitelist first
		if len(s.config.API.AllowedIPs) > 0 {
			clientIP := getClientIP(r)
			ipAllowed := false
			for _, ip := range s.config.API.AllowedIPs {
				if ip == clientIP || ip == "*" {
					ipAllowed = true
					break
				}
			}
			if ipAllowed {
				next(w, r)
				return
			}
		}

		// 1. Check X-API-Key header
		if s.config.API.Key != "" {
			apiKey := r.Header.Get("X-API-Key")
			if apiKey == "" {
				apiKey = r.Header.Get("X-Api-Key") // Case variation
			}
			if apiKey == "" {
				apiKey = r.URL.Query().Get("api_key")
			}
			if apiKey == s.config.API.Key {
				next(w, r)
				return
			}
		}

		// 2. Check Bearer token
		if s.config.API.BearerToken != "" {
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				token := strings.TrimPrefix(authHeader, "Bearer ")
				if token == s.config.API.BearerToken {
					next(w, r)
					return
				}
			}
		}

		// 3. Check Basic Auth
		if s.config.API.BasicAuth.Enabled {
			username, password, ok := r.BasicAuth()
			if ok && username == s.config.API.BasicAuth.Username &&
				password == s.config.API.BasicAuth.Password {
				next(w, r)
				return
			}
		}

		// No valid auth found
		w.Header().Set("WWW-Authenticate", `Bearer realm="Status API", Basic realm="Status API"`)
		s.jsonError(w, "Unauthorized - provide X-API-Key, Bearer token, or Basic auth", http.StatusUnauthorized)
	}
}

// getClientIP extracts client IP from request
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		ips := strings.Split(xff, ",")
		return strings.TrimSpace(ips[0])
	}

	// Check X-Real-IP header
	xri := r.Header.Get("X-Real-IP")
	if xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	ip := r.RemoteAddr
	if colonIdx := strings.LastIndex(ip, ":"); colonIdx != -1 {
		ip = ip[:colonIdx]
	}
	return ip
}

// handleFavicon serves the favicon
func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	faviconData, err := staticFiles.ReadFile("static/favicon.svg")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(faviconData)
}

// === Page Handlers ===

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	tmpl, err := template.ParseFS(templateFiles, "templates/index.html")
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		log.Printf("Template error: %v", err)
		return
	}

	// Get active incidents
	incidents := s.storage.GetIncidents(5, true)

	// Get upcoming maintenance
	maintenance := s.storage.GetMaintenance(true)

	data := struct {
		Title       string
		Description string
		Logo        string
		BaseURL     string
		Theme       config.ThemeConfig
		Services    []*monitor.ServiceStatus
		Incidents   []storage.Incident
		Maintenance []storage.Maintenance
		Overall     monitor.Status
	}{
		Title:       s.config.Title,
		Description: s.config.Description,
		Logo:        s.config.Logo,
		BaseURL:     s.config.BaseURL,
		Theme:       s.config.Theme,
		Services:    s.monitor.GetAllStatuses(),
		Incidents:   incidents,
		Maintenance: maintenance,
		Overall:     s.monitor.GetOverallStatus(),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		log.Printf("Template execution error: %v", err)
	}
}

func (s *Server) handleHistoryPage(w http.ResponseWriter, r *http.Request) {
	// Serve history page
	s.handleIndex(w, r)
}

func (s *Server) handleIncidentPage(w http.ResponseWriter, r *http.Request) {
	// Serve incident detail page
	s.handleIndex(w, r)
}

// === Status API ===

type APIResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
	Meta    *APIMeta    `json:"meta,omitempty"`
}

type APIMeta struct {
	Page       int    `json:"page,omitempty"`
	PerPage    int    `json:"per_page,omitempty"`
	Total      int    `json:"total,omitempty"`
	GeneratedAt string `json:"generated_at"`
}

// Summary response like Cloudflare/GitHub
type SummaryResponse struct {
	Page       PageInfo       `json:"page"`
	Status     StatusInfo     `json:"status"`
	Components []ComponentInfo `json:"components"`
	Incidents  []IncidentInfo  `json:"incidents"`
	Maintenance []MaintenanceInfo `json:"scheduled_maintenances"`
}

type PageInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	UpdatedAt string `json:"updated_at"`
}

type StatusInfo struct {
	Indicator   string `json:"indicator"` // none, minor, major, critical
	Description string `json:"description"`
}

type ComponentInfo struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Status      string  `json:"status"`
	Group       string  `json:"group,omitempty"`
	Uptime      float64 `json:"uptime_percent"`
	ResponseMs  int64   `json:"response_ms"`
	UpdatedAt   string  `json:"updated_at"`
}

type IncidentInfo struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	Status           string        `json:"status"`
	Impact           string        `json:"impact"`
	CreatedAt        string        `json:"created_at"`
	UpdatedAt        string        `json:"updated_at"`
	ResolvedAt       string        `json:"resolved_at,omitempty"`
	Shortlink        string        `json:"shortlink"`
	AffectedComponents []string    `json:"affected_components"`
	Updates          []UpdateInfo  `json:"incident_updates"`
}

type UpdateInfo struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type MaintenanceInfo struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Status         string   `json:"status"`
	ScheduledFor   string   `json:"scheduled_for"`
	ScheduledUntil string   `json:"scheduled_until"`
	AffectedComponents []string `json:"affected_components"`
}

func (s *Server) handleAPISummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	statuses := s.monitor.GetAllStatuses()
	incidents := s.storage.GetIncidents(10, false)
	maintenance := s.storage.GetMaintenance(true)
	overall := s.monitor.GetOverallStatus()

	// Build components
	components := make([]ComponentInfo, 0, len(statuses))
	for _, status := range statuses {
		components = append(components, ComponentInfo{
			ID:          strings.ReplaceAll(strings.ToLower(status.Name), " ", "-"),
			Name:        status.Name,
			Description: status.Description,
			Status:      string(status.Status),
			Group:       status.Group,
			Uptime:      status.Uptime,
			ResponseMs:  status.ResponseTimeMs,
			UpdatedAt:   status.LastCheck.Format(time.RFC3339),
		})
	}

	// Build incidents
	incidentInfos := make([]IncidentInfo, 0, len(incidents))
	for _, inc := range incidents {
		updates := make([]UpdateInfo, 0, len(inc.Updates))
		for _, u := range inc.Updates {
			updates = append(updates, UpdateInfo{
				ID:        u.ID,
				Status:    u.Status,
				Body:      u.Message,
				CreatedAt: u.CreatedAt.Format(time.RFC3339),
			})
		}

		resolvedAt := ""
		if inc.ResolvedAt != nil {
			resolvedAt = inc.ResolvedAt.Format(time.RFC3339)
		}

		incidentInfos = append(incidentInfos, IncidentInfo{
			ID:                 inc.ID,
			Name:               inc.Title,
			Status:             inc.Status,
			Impact:             inc.Severity,
			CreatedAt:          inc.CreatedAt.Format(time.RFC3339),
			UpdatedAt:          inc.UpdatedAt.Format(time.RFC3339),
			ResolvedAt:         resolvedAt,
			Shortlink:          fmt.Sprintf("%s/incidents/%s", s.config.BaseURL, inc.ID),
			AffectedComponents: inc.AffectedServices,
			Updates:            updates,
		})
	}

	// Build maintenance
	maintenanceInfos := make([]MaintenanceInfo, 0, len(maintenance))
	for _, m := range maintenance {
		maintenanceInfos = append(maintenanceInfos, MaintenanceInfo{
			ID:                 m.ID,
			Name:               m.Title,
			Status:             m.Status,
			ScheduledFor:       m.ScheduledStart.Format(time.RFC3339),
			ScheduledUntil:     m.ScheduledEnd.Format(time.RFC3339),
			AffectedComponents: m.AffectedServices,
		})
	}

	// Determine status indicator
	indicator := "none"
	description := "All Systems Operational"
	switch overall {
	case monitor.StatusDegraded:
		indicator = "minor"
		description = "Partial System Outage"
	case monitor.StatusDown:
		indicator = "major"
		description = "Major System Outage"
	}

	summary := SummaryResponse{
		Page: PageInfo{
			ID:        "status",
			Name:      s.config.Title,
			URL:       s.config.BaseURL,
			UpdatedAt: time.Now().Format(time.RFC3339),
		},
		Status: StatusInfo{
			Indicator:   indicator,
			Description: description,
		},
		Components:  components,
		Incidents:   incidentInfos,
		Maintenance: maintenanceInfos,
	}

	s.jsonResponse(w, summary)
}

func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	statuses := s.monitor.GetAllStatuses()
	overall := s.monitor.GetOverallStatus()

	// Group services
	groups := make(map[string][]*monitor.ServiceStatus)
	for _, status := range statuses {
		group := status.Group
		if group == "" {
			group = "Services"
		}
		groups[group] = append(groups[group], status)
	}

	data := map[string]interface{}{
		"overall":  overall,
		"services": statuses,
		"groups":   groups,
	}

	s.jsonResponseWithMeta(w, data)
}

func (s *Server) handleAPIServiceStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/api/status/")
	if name == "" {
		s.jsonError(w, "Service name required", http.StatusBadRequest)
		return
	}

	status := s.monitor.GetStatus(name)
	if status == nil {
		s.jsonError(w, "Service not found", http.StatusNotFound)
		return
	}

	s.jsonResponse(w, status)
}

func (s *Server) handleAPIComponents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	statuses := s.monitor.GetAllStatuses()
	components := make([]ComponentInfo, 0, len(statuses))

	for _, status := range statuses {
		components = append(components, ComponentInfo{
			ID:          strings.ReplaceAll(strings.ToLower(status.Name), " ", "-"),
			Name:        status.Name,
			Description: status.Description,
			Status:      string(status.Status),
			Group:       status.Group,
			Uptime:      status.Uptime,
			ResponseMs:  status.ResponseTimeMs,
			UpdatedAt:   status.LastCheck.Format(time.RFC3339),
		})
	}

	s.jsonResponse(w, components)
}

// === History API ===

func (s *Server) handleAPIHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	days := 90
	if d := r.URL.Query().Get("days"); d != "" {
		fmt.Sscanf(d, "%d", &days)
	}

	history := s.storage.GetAllHistory(days)
	s.jsonResponse(w, history)
}

func (s *Server) handleAPIServiceHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/api/history/")
	if name == "" {
		s.jsonError(w, "Service name required", http.StatusBadRequest)
		return
	}

	days := 90
	if d := r.URL.Query().Get("days"); d != "" {
		fmt.Sscanf(d, "%d", &days)
	}

	history := s.storage.GetHistory(name, days)
	s.jsonResponse(w, history)
}

func (s *Server) handleAPIUptime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	statuses := s.monitor.GetAllStatuses()
	uptime := make(map[string]float64)

	for _, status := range statuses {
		uptime[status.Name] = status.Uptime
	}

	s.jsonResponse(w, uptime)
}

// === Incidents API ===

func (s *Server) handleAPIIncidents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		activeOnly := r.URL.Query().Get("active") == "true"
		limit := 50
		if l := r.URL.Query().Get("limit"); l != "" {
			fmt.Sscanf(l, "%d", &limit)
		}

		incidents := s.storage.GetIncidents(limit, activeOnly)
		s.jsonResponse(w, incidents)

	case http.MethodPost:
		s.requireAuth(s.createIncident)(w, r)

	default:
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) createIncident(w http.ResponseWriter, r *http.Request) {
	var incident storage.Incident
	if err := json.NewDecoder(r.Body).Decode(&incident); err != nil {
		s.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	created, err := s.storage.CreateIncident(incident)
	if err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Notify webhooks
	if s.notifier != nil {
		s.notifier.NotifyIncidentCreated(*created, s.config.BaseURL)
	}

	w.WriteHeader(http.StatusCreated)
	s.jsonResponse(w, created)
}

func (s *Server) handleAPIIncident(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/incidents/")
	if id == "" {
		s.jsonError(w, "Incident ID required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		incident := s.storage.GetIncident(id)
		if incident == nil {
			s.jsonError(w, "Incident not found", http.StatusNotFound)
			return
		}
		s.jsonResponse(w, incident)

	case http.MethodPut, http.MethodPatch:
		s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
			var update struct {
				Status  string `json:"status"`
				Message string `json:"message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
				s.jsonError(w, "Invalid request body", http.StatusBadRequest)
				return
			}

			updated, err := s.storage.UpdateIncident(id, update.Status, update.Message)
			if err != nil {
				s.jsonError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if updated == nil {
				s.jsonError(w, "Incident not found", http.StatusNotFound)
				return
			}

			// Notify webhooks
			if s.notifier != nil {
				if update.Status == "resolved" {
					s.notifier.NotifyIncidentResolved(*updated, s.config.BaseURL)
				} else {
					s.notifier.NotifyIncidentUpdated(*updated, s.config.BaseURL)
				}
			}

			s.jsonResponse(w, updated)
		})(w, r)

	case http.MethodDelete:
		s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
			if s.storage.DeleteIncident(id) {
				w.WriteHeader(http.StatusNoContent)
			} else {
				s.jsonError(w, "Incident not found", http.StatusNotFound)
			}
		})(w, r)

	default:
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// === Maintenance API ===

func (s *Server) handleAPIMaintenance(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		upcoming := r.URL.Query().Get("upcoming") != "false"
		maintenance := s.storage.GetMaintenance(upcoming)
		s.jsonResponse(w, maintenance)

	case http.MethodPost:
		s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
			var m storage.Maintenance
			if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
				s.jsonError(w, "Invalid request body", http.StatusBadRequest)
				return
			}

			created, err := s.storage.CreateMaintenance(m)
			if err != nil {
				s.jsonError(w, err.Error(), http.StatusInternalServerError)
				return
			}

			// Notify webhooks
			if s.notifier != nil {
				s.notifier.NotifyMaintenanceScheduled(*created, s.config.BaseURL)
			}

			w.WriteHeader(http.StatusCreated)
			s.jsonResponse(w, created)
		})(w, r)

	default:
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAPIMaintenanceItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/maintenance/")
	if id == "" {
		s.jsonError(w, "Maintenance ID required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPut, http.MethodPatch:
		s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
			var update struct {
				Status string `json:"status"`
			}
			if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
				s.jsonError(w, "Invalid request body", http.StatusBadRequest)
				return
			}

			updated, _ := s.storage.UpdateMaintenance(id, update.Status)
			if updated == nil {
				s.jsonError(w, "Maintenance not found", http.StatusNotFound)
				return
			}

			s.jsonResponse(w, updated)
		})(w, r)

	default:
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// === Metrics API ===

type MetricsResponse struct {
	TotalServices     int     `json:"total_services"`
	OperationalCount  int     `json:"operational_count"`
	DegradedCount     int     `json:"degraded_count"`
	DownCount         int     `json:"down_count"`
	OverallUptime     float64 `json:"overall_uptime"`
	AverageResponseMs int64   `json:"average_response_ms"`
	ActiveIncidents   int     `json:"active_incidents"`
	TotalIncidents    int     `json:"total_incidents"`
}

func (s *Server) handleAPIMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	statuses := s.monitor.GetAllStatuses()
	incidents := s.storage.GetIncidents(0, false)
	activeIncidents := s.storage.GetIncidents(0, true)

	metrics := MetricsResponse{
		TotalServices:   len(statuses),
		ActiveIncidents: len(activeIncidents),
		TotalIncidents:  len(incidents),
	}

	var totalUptime float64
	var totalResponseTime int64
	var responseCount int64

	for _, status := range statuses {
		switch status.Status {
		case monitor.StatusOperational:
			metrics.OperationalCount++
		case monitor.StatusDegraded:
			metrics.DegradedCount++
		case monitor.StatusDown:
			metrics.DownCount++
		}
		totalUptime += status.Uptime
		if status.ResponseTimeMs > 0 {
			totalResponseTime += status.ResponseTimeMs
			responseCount++
		}
	}

	if len(statuses) > 0 {
		metrics.OverallUptime = totalUptime / float64(len(statuses))
	}
	if responseCount > 0 {
		metrics.AverageResponseMs = totalResponseTime / responseCount
	}

	s.jsonResponse(w, metrics)
}

// === Feed Handlers ===

func (s *Server) handleRSSFeed(w http.ResponseWriter, r *http.Request) {
	incidents := s.storage.GetIncidents(50, false)
	feed, err := s.feedGen.GenerateRSS(incidents)
	if err != nil {
		http.Error(w, "Failed to generate feed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	w.Write(feed)
}

func (s *Server) handleAtomFeed(w http.ResponseWriter, r *http.Request) {
	incidents := s.storage.GetIncidents(50, false)
	feed, err := s.feedGen.GenerateAtom(incidents)
	if err != nil {
		http.Error(w, "Failed to generate feed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Write(feed)
}

func (s *Server) handleJSONFeed(w http.ResponseWriter, r *http.Request) {
	incidents := s.storage.GetIncidents(50, false)
	feed, err := s.feedGen.GenerateJSON(incidents)
	if err != nil {
		http.Error(w, "Failed to generate feed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/feed+json; charset=utf-8")
	w.Write(feed)
}

// === Subscription Handler ===

func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var sub struct {
		Email    string   `json:"email"`
		Services []string `json:"services"`
	}
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
		s.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// In production, you'd save this and send verification email
	s.jsonResponse(w, map[string]string{
		"message": "Subscription request received. Please check your email for verification.",
		"email":   sub.Email,
	})
}

// === WebSocket Handler ===

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	s.clientMu.Lock()
	s.clients[conn] = true
	s.clientMu.Unlock()

	// Send initial status
	statuses := s.monitor.GetAllStatuses()
	overall := s.monitor.GetOverallStatus()
	incidents := s.storage.GetIncidents(5, true)

	initialData := map[string]interface{}{
		"type":      "initial",
		"overall":   overall,
		"services":  statuses,
		"incidents": incidents,
	}
	conn.WriteJSON(initialData)

	// Handle connection close
	go func() {
		defer func() {
			s.clientMu.Lock()
			delete(s.clients, conn)
			s.clientMu.Unlock()
			conn.Close()
		}()

		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}()
}

func (s *Server) broadcastUpdates() {
	ch := s.monitor.Subscribe()
	defer s.monitor.Unsubscribe(ch)

	for status := range ch {
		s.clientMu.RLock()
		for client := range s.clients {
			data := map[string]interface{}{
				"type":    "update",
				"service": status,
				"overall": s.monitor.GetOverallStatus(),
			}
			err := client.WriteJSON(data)
			if err != nil {
				client.Close()
				go func(c *websocket.Conn) {
					s.clientMu.Lock()
					delete(s.clients, c)
					s.clientMu.Unlock()
				}(client)
			}
		}
		s.clientMu.RUnlock()
	}
}

// Record daily history
func (s *Server) recordDailyHistory() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		statuses := s.monitor.GetAllStatuses()
		today := time.Now().Format("2006-01-02")

		for _, status := range statuses {
			dailyStatus := storage.DailyStatus{
				Date:          today,
				UptimePercent: status.Uptime,
				AvgResponseMs: status.ResponseTimeMs,
				TotalChecks:   len(status.History),
			}

			// Count successful checks
			for _, h := range status.History {
				if h.Status == monitor.StatusOperational || h.Status == monitor.StatusDegraded {
					dailyStatus.SuccessChecks++
				}
			}

			s.storage.RecordDailyStatus(status.Name, dailyStatus)
		}
	}
}

// === JSON Response Helpers ===

func (s *Server) jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(APIResponse{
		Success: true,
		Data:    data,
	})
}

func (s *Server) jsonResponseWithMeta(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(APIResponse{
		Success: true,
		Data:    data,
		Meta: &APIMeta{
			GeneratedAt: time.Now().Format(time.RFC3339),
		},
	})
}

func (s *Server) jsonError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(APIResponse{
		Success: false,
		Error:   message,
	})
}

var xml = struct {
	Header string
}{
	Header: `<?xml version="1.0" encoding="UTF-8"?>` + "\n",
}
