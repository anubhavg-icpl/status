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
	"github.com/redis/go-redis/v9"
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

// ipEntry tracks per-IP request counts within a sliding window.
type ipEntry struct {
	count     int
	windowEnd time.Time
}

// apiRateLimiter is a per-IP fixed-window rate limiter.
type apiRateLimiter struct {
	mu      sync.Mutex
	clients map[string]*ipEntry
	limit   int
}

func newAPIRateLimiter(limit int) *apiRateLimiter {
	if limit <= 0 {
		limit = 100
	}
	rl := &apiRateLimiter{
		clients: make(map[string]*ipEntry),
		limit:   limit,
	}
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			rl.mu.Lock()
			now := time.Now()
			for ip, e := range rl.clients {
				if now.After(e.windowEnd) {
					delete(rl.clients, ip)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

func (rl *apiRateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	e, ok := rl.clients[ip]
	if !ok || now.After(e.windowEnd) {
		rl.clients[ip] = &ipEntry{count: 1, windowEnd: now.Add(time.Minute)}
		return true
	}
	if e.count >= rl.limit {
		return false
	}
	e.count++
	return true
}

// Server represents the web server
type Server struct {
	config       *config.Config
	monitor      *monitor.Monitor
	storage      *storage.Storage
	notifier     *notify.Notifier
	feedGen      *feeds.FeedGenerator
	upgrader     websocket.Upgrader
	clients      map[*websocket.Conn]bool
	clientMu     sync.RWMutex
	server       *http.Server
	ctx          context.Context
	cancel       context.CancelFunc
	indexTmpl    *template.Template
	apiDocsTmpl  *template.Template
	rl           *apiRateLimiter
	rdb          *redis.Client // nil when Redis disabled
	cluster      clusterCache
	alerts       *alertTracker
	clusterWatch *clusterWatcher
	auth         *authenticator
	loginTmpl    *template.Template
}

// NewServer creates a new web server instance
func NewServer(cfg *config.Config, mon *monitor.Monitor, store *storage.Storage, notif *notify.Notifier) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		config:   cfg,
		monitor:  mon,
		storage:  store,
		notifier: notif,
		feedGen:  feeds.NewFeedGenerator(cfg.Title, cfg.BaseURL),
		upgrader: websocket.Upgrader{
			// TODO: restrict CheckOrigin to allowed origins in production instead of allowing all
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
		clients: make(map[*websocket.Conn]bool),
		ctx:     ctx,
		cancel:  cancel,
		rl:      newAPIRateLimiter(cfg.API.RateLimit),
	}
	s.alerts = newAlertTracker(cfg, notif)
	s.clusterWatch = newClusterWatcher(cfg, notif)
	s.auth = newAuthenticator(cfg.Auth)
	if cfg.Redis.Enabled {
		rdb := redis.NewClient(&redis.Options{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
		})
		s.rdb = rdb
	}
	return s
}

// Start starts the web server
func (s *Server) Start() error {
	// Parse templates once at startup. Sprig gives operators the usual ~100
	// helpers (date, default, upper, ternary, …) inside custom templates
	// without us hand-rolling a FuncMap per need.
	funcs := templateFuncs()

	indexTmpl, err := template.New("index.html").Funcs(funcs).ParseFS(templateFiles, "templates/index.html")
	if err != nil {
		return fmt.Errorf("failed to parse index template: %w", err)
	}
	s.indexTmpl = indexTmpl

	apiDocsTmpl, err := template.New("api.html").Funcs(funcs).ParseFS(templateFiles, "templates/api.html")
	if err != nil {
		return fmt.Errorf("failed to parse api docs template: %w", err)
	}
	s.apiDocsTmpl = apiDocsTmpl

	loginTmpl, err := template.New("login.html").Funcs(funcs).ParseFS(templateFiles, "templates/login.html")
	if err != nil {
		return fmt.Errorf("failed to parse login template: %w", err)
	}
	s.loginTmpl = loginTmpl

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

	// === Login ===
	// /login and /logout must stay reachable without a session, or there is no
	// way in. They are rate limited like any other public endpoint.
	mux.HandleFunc("/login", s.withRateLimit(s.handleLogin))
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/api/auth", s.withRateLimit(s.handleAuthStatus))

	// === Status API ===
	// gate() applies the login requirement unless auth.public_status keeps the
	// service list and incidents readable to anonymous visitors. The Kubernetes
	// cluster view is never covered by public_status — it carries internal
	// topology and is gated separately below.
	gate := func(h http.HandlerFunc) http.HandlerFunc {
		if s.config.Auth.PublicStatus {
			return s.withRateLimit(h)
		}
		return s.withRateLimit(s.requireSession(h))
	}

	mux.HandleFunc("/api/status", gate(s.handleAPIStatus))
	mux.HandleFunc("/api/status/", gate(s.handleAPIServiceStatus))
	mux.HandleFunc("/api/summary", gate(s.handleAPISummary))
	mux.HandleFunc("/api/components", gate(s.handleAPIComponents))

	// History API
	mux.HandleFunc("/api/history", gate(s.handleAPIHistory))
	mux.HandleFunc("/api/history/", gate(s.handleAPIServiceHistory))
	mux.HandleFunc("/api/uptime", gate(s.handleAPIUptime))

	// Incidents API (public read, authenticated write)
	mux.HandleFunc("/api/incidents", gate(s.handleAPIIncidents))
	mux.HandleFunc("/api/incidents/", gate(s.handleAPIIncident))

	// Maintenance API
	mux.HandleFunc("/api/maintenance", gate(s.handleAPIMaintenance))
	mux.HandleFunc("/api/maintenance/", gate(s.handleAPIMaintenanceItem))

	// Metrics API
	mux.HandleFunc("/api/metrics", gate(s.handleAPIMetrics))

	// Kubernetes cluster snapshot (auth-gated unless cluster.public is set)
	// Cluster snapshot: internal topology. When auth is on it always needs a
	// session — cluster.public only relaxes the API-key requirement, it never
	// exposes node names and images to anonymous visitors.
	mux.HandleFunc("/api/cluster", s.withRateLimit(
		s.requireSession(s.maybeAuth(!s.config.Cluster.Public, s.handleAPICluster))))

	// Notification channels: Web Push registration + delivery self-test
	mux.HandleFunc("/api/push/key", s.withRateLimit(s.handlePushKey))
	mux.HandleFunc("/api/push/subscribe", s.withRateLimit(s.handlePushSubscribe))
	mux.HandleFunc("/api/push/unsubscribe", s.withRateLimit(s.handlePushUnsubscribe))
	mux.HandleFunc("/api/notifications", s.withRateLimit(s.handleNotificationChannels))
	mux.HandleFunc("/api/notifications/test", s.withRateLimit(s.requireAuth(s.handleNotificationTest)))

	// Service worker must be served from the site root to control the page.
	mux.HandleFunc("/sw.js", s.handleServiceWorker)

	// Prometheus scrape endpoint (no rate-limit — kube-prom will probe this)
	mux.HandleFunc("/metrics", s.handlePrometheus)

	// === Subscription Routes (rate limited) ===
	mux.HandleFunc("/api/subscribe", s.withRateLimit(s.handleSubscribe))

	// API Documentation (not rate limited — serves HTML docs)
	mux.HandleFunc("/api/", s.handleAPIDocs)

	// === Feed Routes ===
	mux.HandleFunc("/feed/rss", gate(s.handleRSSFeed))
	mux.HandleFunc("/feed/atom", gate(s.handleAtomFeed))
	mux.HandleFunc("/feed/json", gate(s.handleJSONFeed))
	mux.HandleFunc("/feed", gate(s.handleRSSFeed)) // Default to RSS

	// WebSocket endpoint — streams the same data as /api/status, so it gets the
	// same gate. Without this the login could be bypassed by opening the socket.
	mux.HandleFunc("/ws", gate(s.handleWebSocket))

	// Main pages
	mux.HandleFunc("/", gate(s.handleIndex))
	mux.HandleFunc("/history", gate(s.handleHistoryPage))
	mux.HandleFunc("/incidents/", gate(s.handleIncidentPage))

	// Health check endpoints (no rate limiting)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readiness", s.handleReadiness)

	s.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.config.Server.Port),
		Handler:      s.withMiddleware(mux),
		ReadTimeout:  s.config.Server.ReadTimeout,
		WriteTimeout: s.config.Server.WriteTimeout,
	}

	// Start broadcasting updates
	go s.broadcastUpdates(s.ctx)

	if s.rdb != nil {
		go s.subscribeRedisEvents()
	}

	// Start daily history recorder
	go s.recordDailyHistory(s.ctx)

	// Sweep expired sessions; an abandoned browser never calls logout.
	if s.auth.Enabled() {
		go s.purgeSessions(s.ctx.Done())
	}

	// Watch the cluster for application-level failures across every namespace.
	// Runs regardless of page views: an outage at 3am has no audience.
	// Skipped without an in-cluster client, or every tick would just log a
	// snapshot failure on a laptop.
	if s.config.Cluster.Enabled && s.config.Alerts.Cluster.Enabled && s.monitor.K8s() != nil {
		go s.clusterWatch.run(s.ctx, s.clusterSnapshot)
	}

	log.Printf("Starting server on http://localhost:%d", s.config.Server.Port)
	return s.server.ListenAndServe()
}

// Stop gracefully stops the server
func (s *Server) Stop(ctx context.Context) error {
	s.cancel()
	if err := s.server.Shutdown(ctx); err != nil {
		return err
	}
	if s.rdb != nil {
		if err := s.rdb.Close(); err != nil {
			log.Printf("Redis close error: %v", err)
		}
	}
	return nil
}

// Middleware
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")

		// Security headers
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// rateLimitCheck checks rate limit using Redis when available, falling back to in-memory.
func (s *Server) rateLimitCheck(ip string) bool {
	if s.rdb != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		key := "ratelimit:" + ip
		count, err := s.rdb.Incr(ctx, key).Result()
		if err == nil {
			if count == 1 {
				s.rdb.Expire(ctx, key, time.Minute)
			}
			return count <= int64(s.config.API.RateLimit)
		}
		// Redis error — fall through to in-memory
	}
	return s.rl.allow(ip)
}

// withRateLimit wraps a handler with per-IP fixed-window rate limiting.
func (s *Server) withRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		if colonIdx := strings.LastIndex(ip, ":"); colonIdx != -1 {
			ip = ip[:colonIdx]
		}
		if !s.rateLimitCheck(ip) {
			s.jsonError(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
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

		// 1. Check X-API-Key header only (query-param is intentionally omitted
		//    because API keys in URLs leak into server logs and browser history).
		if s.config.API.Key != "" {
			apiKey := r.Header.Get("X-API-Key")
			if apiKey == "" {
				apiKey = r.Header.Get("X-Api-Key") // Case variation
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

// getClientIP extracts client IP from RemoteAddr only.
// X-Forwarded-For and X-Real-IP headers are intentionally ignored to prevent
// spoofed headers from bypassing IP whitelists.
func getClientIP(r *http.Request) string {
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
	if err := s.indexTmpl.Execute(w, data); err != nil {
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

// handleAPIDocs serves the API documentation page
func (s *Server) handleAPIDocs(w http.ResponseWriter, r *http.Request) {
	// Only serve docs at exact /api/ or /api path
	if r.URL.Path != "/api/" && r.URL.Path != "/api" {
		http.NotFound(w, r)
		return
	}

	data := struct {
		Title   string
		BaseURL string
		Theme   config.ThemeConfig
	}{
		Title:   s.config.Title,
		BaseURL: s.config.BaseURL,
		Theme:   s.config.Theme,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.apiDocsTmpl.Execute(w, data); err != nil {
		log.Printf("API docs template execution error: %v", err)
	}
}

// === Status API ===

type APIResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
	Meta    *APIMeta    `json:"meta,omitempty"`
}

type APIMeta struct {
	Page        int    `json:"page,omitempty"`
	PerPage     int    `json:"per_page,omitempty"`
	Total       int    `json:"total,omitempty"`
	GeneratedAt string `json:"generated_at"`
}

// Summary response like Cloudflare/GitHub
type SummaryResponse struct {
	Page        PageInfo          `json:"page"`
	Status      StatusInfo        `json:"status"`
	Components  []ComponentInfo   `json:"components"`
	Incidents   []IncidentInfo    `json:"incidents"`
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
	ID                 string       `json:"id"`
	Name               string       `json:"name"`
	Status             string       `json:"status"`
	Impact             string       `json:"impact"`
	CreatedAt          string       `json:"created_at"`
	UpdatedAt          string       `json:"updated_at"`
	ResolvedAt         string       `json:"resolved_at,omitempty"`
	Shortlink          string       `json:"shortlink"`
	AffectedComponents []string     `json:"affected_components"`
	Updates            []UpdateInfo `json:"incident_updates"`
}

type UpdateInfo struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type MaintenanceInfo struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Status             string   `json:"status"`
	ScheduledFor       string   `json:"scheduled_for"`
	ScheduledUntil     string   `json:"scheduled_until"`
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
	if days < 1 || days > 365 {
		days = 90
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
	if days < 1 || days > 365 {
		days = 90
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
		if limit < 1 || limit > 500 {
			limit = 50
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
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB limit
	var incident storage.Incident
	if err := json.NewDecoder(r.Body).Decode(&incident); err != nil {
		s.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if incident.Title == "" {
		s.jsonError(w, "title is required", http.StatusBadRequest)
		return
	}
	validStatuses := map[string]bool{"investigating": true, "identified": true, "monitoring": true, "resolved": true}
	if !validStatuses[incident.Status] {
		s.jsonError(w, "status must be one of: investigating, identified, monitoring, resolved", http.StatusBadRequest)
		return
	}
	validSeverities := map[string]bool{"minor": true, "major": true, "critical": true}
	if !validSeverities[incident.Severity] {
		s.jsonError(w, "severity must be one of: minor, major, critical", http.StatusBadRequest)
		return
	}

	created, err := s.storage.CreateIncident(incident)
	if err != nil {
		log.Printf("internal error: %v", err)
		s.jsonError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Notify webhooks
	if s.notifier != nil {
		s.notifier.NotifyIncidentCreated(*created, s.config.BaseURL)
	}
	s.publishEvent("incident.created", created)

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
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB limit
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
				log.Printf("internal error: %v", err)
				s.jsonError(w, "internal server error", http.StatusInternalServerError)
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
					s.publishEvent("incident.resolved", updated)
				} else {
					s.notifier.NotifyIncidentUpdated(*updated, s.config.BaseURL)
					s.publishEvent("incident.updated", updated)
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
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB limit
			var m storage.Maintenance
			if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
				s.jsonError(w, "Invalid request body", http.StatusBadRequest)
				return
			}

			created, err := s.storage.CreateMaintenance(m)
			if err != nil {
				log.Printf("internal error: %v", err)
				s.jsonError(w, "internal server error", http.StatusInternalServerError)
				return
			}

			// Notify webhooks
			if s.notifier != nil {
				s.notifier.NotifyMaintenanceScheduled(*created, s.config.BaseURL)
			}
			s.publishEvent("maintenance.created", created)

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
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB limit
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

func (s *Server) getStatusSummary() *feeds.StatusSummary {
	statuses := s.monitor.GetAllStatuses()
	summary := &feeds.StatusSummary{
		Overall: string(s.monitor.GetOverallStatus()),
		Total:   len(statuses),
	}

	for _, status := range statuses {
		switch status.Status {
		case monitor.StatusOperational:
			summary.Operational++
		case monitor.StatusDegraded:
			summary.Degraded++
		case monitor.StatusDown:
			summary.Down++
		default:
			summary.Operational++ // Unknown treated as operational
		}
	}

	return summary
}

func (s *Server) handleRSSFeed(w http.ResponseWriter, r *http.Request) {
	incidents := s.storage.GetIncidents(50, false)
	status := s.getStatusSummary()
	feed, err := s.feedGen.GenerateRSSWithStatus(incidents, status)
	if err != nil {
		http.Error(w, "Failed to generate feed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300") // 5 min cache
	w.Write([]byte(xml.Header))
	w.Write(feed)
}

func (s *Server) handleAtomFeed(w http.ResponseWriter, r *http.Request) {
	incidents := s.storage.GetIncidents(50, false)
	status := s.getStatusSummary()
	feed, err := s.feedGen.GenerateAtomWithStatus(incidents, status)
	if err != nil {
		http.Error(w, "Failed to generate feed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(feed)
}

func (s *Server) handleJSONFeed(w http.ResponseWriter, r *http.Request) {
	incidents := s.storage.GetIncidents(50, false)
	status := s.getStatusSummary()
	feed, err := s.feedGen.GenerateJSONWithStatus(incidents, status)
	if err != nil {
		http.Error(w, "Failed to generate feed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/feed+json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(feed)
}

// === Subscription Handler ===

func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB limit
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
	if err := conn.WriteJSON(initialData); err != nil {
		log.Printf("WebSocket initial write error: %v", err)
		s.clientMu.Lock()
		delete(s.clients, conn)
		s.clientMu.Unlock()
		conn.Close()
		return
	}

	// Set read deadline and pong handler so dead clients are detected
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Handle connection close
	go func() {
		defer func() {
			s.clientMu.Lock()
			delete(s.clients, conn)
			s.clientMu.Unlock()
			conn.Close()
		}()

		// Ping ticker to keep connections alive and detect dead clients
		pingTicker := time.NewTicker(30 * time.Second)
		defer pingTicker.Stop()

		readErr := make(chan error, 1)
		go func() {
			for {
				_, _, err := conn.ReadMessage()
				if err != nil {
					readErr <- err
					return
				}
			}
		}()

		for {
			select {
			case <-pingTicker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
					return
				}
			case <-readErr:
				return
			}
		}
	}()
}

// publishEvent publishes an event to Redis pub/sub for cross-pod broadcasting.
func (s *Server) publishEvent(eventType string, payload interface{}) {
	if s.rdb == nil {
		return
	}
	data, err := json.Marshal(map[string]interface{}{
		"type":    eventType,
		"payload": payload,
	})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := s.rdb.Publish(ctx, "status:events", data).Err(); err != nil {
		log.Printf("Redis publish error: %v", err)
	}
}

// subscribeRedisEvents subscribes to Redis pub/sub and fans out events to WebSocket clients.
func (s *Server) subscribeRedisEvents() {
	pubsub := s.rdb.Subscribe(s.ctx, "status:events")
	defer pubsub.Close()
	for msg := range pubsub.Channel() {
		var dead []*websocket.Conn
		s.clientMu.RLock()
		for client := range s.clients {
			if err := client.WriteJSON(map[string]interface{}{
				"type":    "event",
				"payload": json.RawMessage(msg.Payload),
			}); err != nil {
				dead = append(dead, client)
			}
		}
		s.clientMu.RUnlock()
		if len(dead) > 0 {
			s.clientMu.Lock()
			for _, c := range dead {
				delete(s.clients, c)
				c.Close()
			}
			s.clientMu.Unlock()
		}
	}
}

func (s *Server) broadcastUpdates(ctx context.Context) {
	ch := s.monitor.Subscribe()
	defer s.monitor.Unsubscribe(ch)

	for {
		select {
		case <-ctx.Done():
			return
		case status, ok := <-ch:
			if !ok {
				return
			}
			// Turn the check stream into alerts before fanning out to browsers;
			// the tracker decides whether this result is a real transition.
			s.alerts.observe(status)

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
}

// Record daily history
func (s *Server) recordDailyHistory(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
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
}

// === Health Check Handlers ===

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":    "ok",
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	statuses := s.monitor.GetAllStatuses()
	if statuses == nil {
		http.Error(w, `{"status":"not ready"}`, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ready"}`))
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
