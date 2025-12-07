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
	"sync"

	"github.com/gorilla/websocket"
	"github.com/status/config"
	"github.com/status/monitor"
)

//go:embed static/*
var staticFiles embed.FS

//go:embed templates/*
var templateFiles embed.FS

// Server represents the web server
type Server struct {
	config   *config.Config
	monitor  *monitor.Monitor
	upgrader websocket.Upgrader
	clients  map[*websocket.Conn]bool
	clientMu sync.RWMutex
	server   *http.Server
}

// NewServer creates a new web server instance
func NewServer(cfg *config.Config, mon *monitor.Monitor) *Server {
	return &Server{
		config:  cfg,
		monitor: mon,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins for simplicity
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

	// API routes
	mux.HandleFunc("/api/status", s.handleAPIStatus)
	mux.HandleFunc("/api/status/", s.handleAPIServiceStatus)
	mux.HandleFunc("/api/incidents", s.handleAPIIncidents)
	mux.HandleFunc("/api/metrics", s.handleAPIMetrics)

	// WebSocket endpoint
	mux.HandleFunc("/ws", s.handleWebSocket)

	// Main page
	mux.HandleFunc("/", s.handleIndex)

	s.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.config.Server.Port),
		Handler:      mux,
		ReadTimeout:  s.config.Server.ReadTimeout,
		WriteTimeout: s.config.Server.WriteTimeout,
	}

	// Start broadcasting updates
	go s.broadcastUpdates()

	log.Printf("Starting server on http://localhost:%d", s.config.Server.Port)
	return s.server.ListenAndServe()
}

// Stop gracefully stops the server
func (s *Server) Stop(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// handleIndex serves the main status page
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

	data := struct {
		Title       string
		Description string
		Logo        string
		Theme       config.ThemeConfig
		Services    []*monitor.ServiceStatus
		Incidents   []config.Incident
		Overall     monitor.Status
	}{
		Title:       s.config.Title,
		Description: s.config.Description,
		Logo:        s.config.Logo,
		Theme:       s.config.Theme,
		Services:    s.monitor.GetAllStatuses(),
		Incidents:   s.config.Incidents,
		Overall:     s.monitor.GetOverallStatus(),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		log.Printf("Template execution error: %v", err)
	}
}

// APIResponse represents a standard API response
type APIResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// handleAPIStatus returns all service statuses
func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	statuses := s.monitor.GetAllStatuses()
	overall := s.monitor.GetOverallStatus()

	// Group services by group name
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

	s.jsonResponse(w, data)
}

// handleAPIServiceStatus returns status for a specific service
func (s *Server) handleAPIServiceStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := r.URL.Path[len("/api/status/"):]
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

// handleAPIIncidents returns all incidents
func (s *Server) handleAPIIncidents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.jsonResponse(w, s.config.Incidents)
}

// MetricsResponse contains system-wide metrics
type MetricsResponse struct {
	TotalServices     int     `json:"total_services"`
	OperationalCount  int     `json:"operational_count"`
	DegradedCount     int     `json:"degraded_count"`
	DownCount         int     `json:"down_count"`
	OverallUptime     float64 `json:"overall_uptime"`
	AverageResponseMs int64   `json:"average_response_ms"`
}

// handleAPIMetrics returns system-wide metrics
func (s *Server) handleAPIMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	statuses := s.monitor.GetAllStatuses()

	metrics := MetricsResponse{
		TotalServices: len(statuses),
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

// handleWebSocket handles WebSocket connections
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
	initialData := map[string]interface{}{
		"type":     "initial",
		"overall":  overall,
		"services": statuses,
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

// broadcastUpdates sends status updates to all WebSocket clients
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

// jsonResponse sends a JSON response
func (s *Server) jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(APIResponse{
		Success: true,
		Data:    data,
	})
}

// jsonError sends a JSON error response
func (s *Server) jsonError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(APIResponse{
		Success: false,
		Error:   message,
	})
}
