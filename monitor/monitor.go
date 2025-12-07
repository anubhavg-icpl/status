package monitor

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/status/config"
)

// Status represents the current status of a service
type Status string

const (
	StatusOperational Status = "operational"
	StatusDegraded    Status = "degraded"
	StatusDown        Status = "down"
	StatusUnknown     Status = "unknown"
)

// ServiceStatus holds the current state of a monitored service
type ServiceStatus struct {
	Name           string        `json:"name"`
	Group          string        `json:"group"`
	URL            string        `json:"url"`
	Description    string        `json:"description"`
	Status         Status        `json:"status"`
	ResponseTime   time.Duration `json:"response_time"`
	ResponseTimeMs int64         `json:"response_time_ms"`
	StatusCode     int           `json:"status_code"`
	LastCheck      time.Time     `json:"last_check"`
	Uptime         float64       `json:"uptime"` // percentage
	ErrorMessage   string        `json:"error_message,omitempty"`
	History        []HistoryPoint `json:"history"`
}

// HistoryPoint represents a single check result
type HistoryPoint struct {
	Timestamp      time.Time `json:"timestamp"`
	ResponseTimeMs int64     `json:"response_time_ms"`
	Status         Status    `json:"status"`
	StatusCode     int       `json:"status_code"`
}

// Monitor manages health checks for all services
type Monitor struct {
	services    []config.Service
	statuses    map[string]*ServiceStatus
	mu          sync.RWMutex
	client      *http.Client
	subscribers []chan *ServiceStatus
	subMu       sync.RWMutex
	ctx         context.Context
	cancel      context.CancelFunc
	maxHistory  int
}

// NewMonitor creates a new monitor instance
func NewMonitor(services []config.Service) *Monitor {
	ctx, cancel := context.WithCancel(context.Background())

	// Create HTTP client with custom transport
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
		},
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}

	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil // Follow redirects
		},
	}

	m := &Monitor{
		services:   services,
		statuses:   make(map[string]*ServiceStatus),
		client:     client,
		ctx:        ctx,
		cancel:     cancel,
		maxHistory: 90, // Keep 90 data points (e.g., 90 checks)
	}

	// Initialize statuses
	for _, svc := range services {
		m.statuses[svc.Name] = &ServiceStatus{
			Name:        svc.Name,
			Group:       svc.Group,
			URL:         svc.URL,
			Description: svc.Description,
			Status:      StatusUnknown,
			LastCheck:   time.Time{},
			Uptime:      100.0,
			History:     make([]HistoryPoint, 0, m.maxHistory),
		}
	}

	return m
}

// Start begins monitoring all services
func (m *Monitor) Start() {
	for _, svc := range m.services {
		go m.monitorService(svc)
	}
}

// Stop stops all monitoring goroutines
func (m *Monitor) Stop() {
	m.cancel()
}

// Subscribe returns a channel that receives status updates
func (m *Monitor) Subscribe() chan *ServiceStatus {
	ch := make(chan *ServiceStatus, 100)
	m.subMu.Lock()
	m.subscribers = append(m.subscribers, ch)
	m.subMu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel
func (m *Monitor) Unsubscribe(ch chan *ServiceStatus) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for i, sub := range m.subscribers {
		if sub == ch {
			m.subscribers = append(m.subscribers[:i], m.subscribers[i+1:]...)
			close(ch)
			return
		}
	}
}

// GetAllStatuses returns all current service statuses
func (m *Monitor) GetAllStatuses() []*ServiceStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	statuses := make([]*ServiceStatus, 0, len(m.statuses))
	for _, status := range m.statuses {
		// Create a copy
		s := *status
		s.History = make([]HistoryPoint, len(status.History))
		copy(s.History, status.History)
		statuses = append(statuses, &s)
	}
	return statuses
}

// GetStatus returns the status of a specific service
func (m *Monitor) GetStatus(name string) *ServiceStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if status, ok := m.statuses[name]; ok {
		s := *status
		s.History = make([]HistoryPoint, len(status.History))
		copy(s.History, status.History)
		return &s
	}
	return nil
}

// GetOverallStatus returns the overall system status
func (m *Monitor) GetOverallStatus() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()

	hasDown := false
	hasDegraded := false

	for _, status := range m.statuses {
		switch status.Status {
		case StatusDown:
			hasDown = true
		case StatusDegraded:
			hasDegraded = true
		}
	}

	if hasDown {
		return StatusDown
	}
	if hasDegraded {
		return StatusDegraded
	}
	return StatusOperational
}

// monitorService continuously checks a single service
func (m *Monitor) monitorService(svc config.Service) {
	// Initial check
	m.checkService(svc)

	ticker := time.NewTicker(svc.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.checkService(svc)
		}
	}
}

// checkService performs a single health check based on service type
func (m *Monitor) checkService(svc config.Service) {
	switch svc.Type {
	case config.CheckHTTP, "":
		m.checkHTTP(svc)
	case config.CheckTCP:
		m.checkTCP(svc)
	case config.CheckICMP:
		m.checkICMP(svc)
	case config.CheckDNS:
		m.checkDNS(svc)
	case config.CheckWebSocket:
		m.checkWebSocket(svc)
	default:
		m.checkHTTP(svc) // Default to HTTP
	}
}

// checkHTTP performs an HTTP/HTTPS health check
func (m *Monitor) checkHTTP(svc config.Service) {
	ctx, cancel := context.WithTimeout(m.ctx, svc.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, svc.Method, svc.URL, nil)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, 0, 0, err.Error())
		return
	}

	// Add custom headers
	for key, value := range svc.Headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("User-Agent", "StatusMonitor/1.0")

	// Create client with TLS settings if needed
	client := m.client
	if svc.SkipTLSVerify {
		transport := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
		client = &http.Client{Transport: transport, Timeout: svc.Timeout}
	}

	start := time.Now()
	resp, err := client.Do(req)
	responseTime := time.Since(start)

	if err != nil {
		m.updateStatus(svc.Name, StatusDown, responseTime, 0, err.Error())
		return
	}
	defer resp.Body.Close()

	// Check body if expected
	var bodyMatch bool = true
	if svc.ExpectedBody != "" {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // Limit to 1MB
		if err == nil {
			bodyMatch = strings.Contains(string(body), svc.ExpectedBody)
		}
	}

	// Determine status based on response
	var status Status
	var errMsg string

	if resp.StatusCode == svc.ExpectedStatus && bodyMatch {
		if responseTime < 2*time.Second {
			status = StatusOperational
		} else if responseTime < 5*time.Second {
			status = StatusDegraded
			errMsg = "slow response time"
		} else {
			status = StatusDegraded
			errMsg = "very slow response time"
		}
	} else if !bodyMatch {
		status = StatusDown
		errMsg = "expected body not found"
	} else {
		status = StatusDown
		errMsg = fmt.Sprintf("unexpected status code: %d", resp.StatusCode)
	}

	m.updateStatus(svc.Name, status, responseTime, resp.StatusCode, errMsg)
}

// checkTCP performs a TCP connection check
func (m *Monitor) checkTCP(svc config.Service) {
	address := svc.Host
	if svc.Port > 0 {
		address = fmt.Sprintf("%s:%d", svc.Host, svc.Port)
	}

	start := time.Now()
	conn, err := net.DialTimeout("tcp", address, svc.Timeout)
	responseTime := time.Since(start)

	if err != nil {
		m.updateStatus(svc.Name, StatusDown, responseTime, 0, err.Error())
		return
	}
	defer conn.Close()

	var status Status
	var errMsg string

	if responseTime < 1*time.Second {
		status = StatusOperational
	} else if responseTime < 3*time.Second {
		status = StatusDegraded
		errMsg = "slow connection"
	} else {
		status = StatusDegraded
		errMsg = "very slow connection"
	}

	m.updateStatus(svc.Name, status, responseTime, 0, errMsg)
}

// checkICMP performs an ICMP ping check
func (m *Monitor) checkICMP(svc config.Service) {
	var cmd *exec.Cmd
	host := svc.Host
	if host == "" {
		host = svc.URL
	}

	start := time.Now()

	// Use appropriate ping command based on OS
	if runtime.GOOS == "windows" {
		cmd = exec.Command("ping", "-n", "1", "-w", fmt.Sprintf("%d", svc.Timeout.Milliseconds()), host)
	} else {
		cmd = exec.Command("ping", "-c", "1", "-W", fmt.Sprintf("%d", int(svc.Timeout.Seconds())), host)
	}

	err := cmd.Run()
	responseTime := time.Since(start)

	if err != nil {
		m.updateStatus(svc.Name, StatusDown, responseTime, 0, "ping failed")
		return
	}

	var status Status
	var errMsg string

	if responseTime < 100*time.Millisecond {
		status = StatusOperational
	} else if responseTime < 500*time.Millisecond {
		status = StatusDegraded
		errMsg = "high latency"
	} else {
		status = StatusDegraded
		errMsg = "very high latency"
	}

	m.updateStatus(svc.Name, status, responseTime, 0, errMsg)
}

// checkDNS performs a DNS resolution check
func (m *Monitor) checkDNS(svc config.Service) {
	host := svc.Host
	if host == "" {
		host = svc.URL
	}

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: svc.Timeout}
			return d.DialContext(ctx, "udp", svc.DNSResolver)
		},
	}

	ctx, cancel := context.WithTimeout(m.ctx, svc.Timeout)
	defer cancel()

	start := time.Now()
	var err error

	switch svc.DNSRecordType {
	case "A", "":
		_, err = resolver.LookupIP(ctx, "ip4", host)
	case "AAAA":
		_, err = resolver.LookupIP(ctx, "ip6", host)
	case "CNAME":
		_, err = resolver.LookupCNAME(ctx, host)
	case "MX":
		_, err = resolver.LookupMX(ctx, host)
	case "TXT":
		_, err = resolver.LookupTXT(ctx, host)
	case "NS":
		_, err = resolver.LookupNS(ctx, host)
	default:
		_, err = resolver.LookupHost(ctx, host)
	}

	responseTime := time.Since(start)

	if err != nil {
		m.updateStatus(svc.Name, StatusDown, responseTime, 0, err.Error())
		return
	}

	var status Status
	var errMsg string

	if responseTime < 100*time.Millisecond {
		status = StatusOperational
	} else if responseTime < 500*time.Millisecond {
		status = StatusDegraded
		errMsg = "slow DNS resolution"
	} else {
		status = StatusDegraded
		errMsg = "very slow DNS resolution"
	}

	m.updateStatus(svc.Name, status, responseTime, 0, errMsg)
}

// checkWebSocket performs a WebSocket connection check
func (m *Monitor) checkWebSocket(svc config.Service) {
	// Convert http(s) to ws(s)
	url := svc.URL
	url = strings.Replace(url, "https://", "wss://", 1)
	url = strings.Replace(url, "http://", "ws://", 1)

	dialer := &net.Dialer{Timeout: svc.Timeout}

	start := time.Now()

	// For WebSocket, we just check if we can establish a TCP connection
	// A full WebSocket handshake would require additional libraries
	var conn net.Conn
	var err error

	if strings.HasPrefix(url, "wss://") {
		host := strings.TrimPrefix(url, "wss://")
		if !strings.Contains(host, ":") {
			host = host + ":443"
		}
		conn, err = tls.DialWithDialer(dialer, "tcp", host, &tls.Config{
			InsecureSkipVerify: svc.SkipTLSVerify,
		})
	} else {
		host := strings.TrimPrefix(url, "ws://")
		if !strings.Contains(host, ":") {
			host = host + ":80"
		}
		conn, err = dialer.Dial("tcp", host)
	}

	responseTime := time.Since(start)

	if err != nil {
		m.updateStatus(svc.Name, StatusDown, responseTime, 0, err.Error())
		return
	}
	defer conn.Close()

	var status Status
	var errMsg string

	if responseTime < 1*time.Second {
		status = StatusOperational
	} else if responseTime < 3*time.Second {
		status = StatusDegraded
		errMsg = "slow connection"
	} else {
		status = StatusDegraded
		errMsg = "very slow connection"
	}

	m.updateStatus(svc.Name, status, responseTime, 0, errMsg)
}

// updateStatus updates the status of a service and notifies subscribers
func (m *Monitor) updateStatus(name string, status Status, responseTime time.Duration, statusCode int, errMsg string) {
	m.mu.Lock()

	svcStatus, ok := m.statuses[name]
	if !ok {
		m.mu.Unlock()
		return
	}

	// Update status
	svcStatus.Status = status
	svcStatus.ResponseTime = responseTime
	svcStatus.ResponseTimeMs = responseTime.Milliseconds()
	svcStatus.StatusCode = statusCode
	svcStatus.LastCheck = time.Now()
	svcStatus.ErrorMessage = errMsg

	// Add to history
	point := HistoryPoint{
		Timestamp:      time.Now(),
		ResponseTimeMs: responseTime.Milliseconds(),
		Status:         status,
		StatusCode:     statusCode,
	}
	svcStatus.History = append(svcStatus.History, point)

	// Trim history if needed
	if len(svcStatus.History) > m.maxHistory {
		svcStatus.History = svcStatus.History[len(svcStatus.History)-m.maxHistory:]
	}

	// Calculate uptime from history
	if len(svcStatus.History) > 0 {
		operational := 0
		for _, h := range svcStatus.History {
			if h.Status == StatusOperational || h.Status == StatusDegraded {
				operational++
			}
		}
		svcStatus.Uptime = float64(operational) / float64(len(svcStatus.History)) * 100
	}

	// Create copy for notification
	statusCopy := *svcStatus
	statusCopy.History = make([]HistoryPoint, len(svcStatus.History))
	copy(statusCopy.History, svcStatus.History)

	m.mu.Unlock()

	// Notify subscribers
	m.notifySubscribers(&statusCopy)
}

// notifySubscribers sends status update to all subscribers
func (m *Monitor) notifySubscribers(status *ServiceStatus) {
	m.subMu.RLock()
	defer m.subMu.RUnlock()

	for _, ch := range m.subscribers {
		select {
		case ch <- status:
		default:
			// Channel full, skip
		}
	}
}
