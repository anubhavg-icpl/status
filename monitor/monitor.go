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
	"github.com/status/storage"
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
	storage     *storage.Storage
}

// NewMonitor creates a new monitor instance
func NewMonitor(services []config.Service, store *storage.Storage) *Monitor {
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
		storage:    store,
	}

	// Load persisted check history if available
	var persistedHistory map[string]*storage.ServiceCheckHistory
	if store != nil {
		persistedHistory = store.GetAllServiceCheckHistory()
	}

	// Initialize statuses
	for _, svc := range services {
		status := &ServiceStatus{
			Name:        svc.Name,
			Group:       svc.Group,
			URL:         svc.URL,
			Description: svc.Description,
			Status:      StatusUnknown,
			LastCheck:   time.Time{},
			Uptime:      100.0,
			History:     make([]HistoryPoint, 0, m.maxHistory),
		}

		// Restore persisted history if available
		if persisted, ok := persistedHistory[svc.Name]; ok && persisted != nil {
			for _, cp := range persisted.History {
				status.History = append(status.History, HistoryPoint{
					Timestamp:      cp.Timestamp,
					ResponseTimeMs: cp.ResponseTimeMs,
					Status:         Status(cp.Status),
					StatusCode:     cp.StatusCode,
				})
			}
			status.Uptime = persisted.Uptime
			status.LastCheck = persisted.LastCheck
			status.ErrorMessage = persisted.ErrorMessage
			if len(status.History) > 0 {
				lastPoint := status.History[len(status.History)-1]
				status.Status = lastPoint.Status
				status.ResponseTimeMs = lastPoint.ResponseTimeMs
				status.ResponseTime = time.Duration(lastPoint.ResponseTimeMs) * time.Millisecond
				status.StatusCode = lastPoint.StatusCode
			}
		}

		m.statuses[svc.Name] = status
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
// Uses smart logic: Major outage only if >50% services down
func (m *Monitor) GetOverallStatus() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()

	total := len(m.statuses)
	if total == 0 {
		return StatusOperational
	}

	downCount := 0
	degradedCount := 0

	for _, status := range m.statuses {
		switch status.Status {
		case StatusDown:
			downCount++
		case StatusDegraded:
			degradedCount++
		}
	}

	// Major outage: >50% services are down
	if downCount > total/2 {
		return StatusDown
	}

	// Partial outage: some services down or degraded
	if downCount > 0 || degradedCount > 0 {
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
	case config.CheckUDP:
		m.checkUDP(svc)
	case config.CheckICMP:
		m.checkICMP(svc)
	case config.CheckDNS:
		m.checkDNS(svc)
	case config.CheckWebSocket:
		m.checkWebSocket(svc)
	case config.CheckGRPC:
		m.checkGRPC(svc)
	case config.CheckQUIC:
		m.checkQUIC(svc)
	case config.CheckSMTP:
		m.checkSMTP(svc)
	case config.CheckSSH:
		m.checkSSH(svc)
	case config.CheckTLS:
		m.checkTLS(svc)
	case config.CheckPOP3:
		m.checkPOP3(svc)
	case config.CheckIMAP:
		m.checkIMAP(svc)
	case config.CheckFTP:
		m.checkFTP(svc)
	case config.CheckNTP:
		m.checkNTP(svc)
	case config.CheckLDAP:
		m.checkLDAP(svc)
	case config.CheckRedis:
		m.checkRedis(svc)
	case config.CheckMongoDB:
		m.checkMongoDB(svc)
	case config.CheckMySQL:
		m.checkMySQL(svc)
	case config.CheckPostgres:
		m.checkPostgres(svc)
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

// checkUDP performs a UDP connectivity check
func (m *Monitor) checkUDP(svc config.Service) {
	address := svc.Host
	if svc.Port > 0 {
		address = fmt.Sprintf("%s:%d", svc.Host, svc.Port)
	}

	start := time.Now()
	conn, err := net.DialTimeout("udp", address, svc.Timeout)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, time.Since(start), 0, err.Error())
		return
	}
	defer conn.Close()

	// Set deadline for the entire operation
	conn.SetDeadline(time.Now().Add(svc.Timeout))

	// Send payload if configured, otherwise send a simple probe
	payload := []byte(svc.UDPPayload)
	if len(payload) == 0 {
		payload = []byte{0x00} // Minimal probe packet
	}

	_, err = conn.Write(payload)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, time.Since(start), 0, "write failed: "+err.Error())
		return
	}

	// Try to read response (UDP is connectionless, so this may timeout)
	// For many UDP services, no response is expected
	buf := make([]byte, 1024)
	conn.SetReadDeadline(time.Now().Add(svc.Timeout / 2))
	n, err := conn.Read(buf)
	responseTime := time.Since(start)

	var status Status
	var errMsg string

	if err != nil {
		// For UDP, timeout on read is often OK (service may not send response)
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			// Check if we expect a response
			if svc.UDPExpected != "" {
				status = StatusDown
				errMsg = "no response received"
			} else {
				// No response expected, consider operational if we could send
				status = StatusOperational
			}
		} else {
			status = StatusDown
			errMsg = "read error: " + err.Error()
		}
	} else {
		// Got a response
		if svc.UDPExpected != "" && !strings.Contains(string(buf[:n]), svc.UDPExpected) {
			status = StatusDown
			errMsg = "unexpected response"
		} else if responseTime < 500*time.Millisecond {
			status = StatusOperational
		} else if responseTime < 2*time.Second {
			status = StatusDegraded
			errMsg = "slow response"
		} else {
			status = StatusDegraded
			errMsg = "very slow response"
		}
	}

	m.updateStatus(svc.Name, status, responseTime, 0, errMsg)
}

// checkGRPC performs a gRPC health check (TCP connectivity to gRPC port)
func (m *Monitor) checkGRPC(svc config.Service) {
	// Extract host from URL or use Host field
	host := svc.Host
	if host == "" && svc.URL != "" {
		host = strings.TrimPrefix(svc.URL, "grpc://")
		host = strings.TrimPrefix(host, "grpcs://")
	}

	address := host
	if svc.Port > 0 {
		address = fmt.Sprintf("%s:%d", host, svc.Port)
	} else if !strings.Contains(host, ":") {
		address = host + ":443" // Default gRPC port
	}

	start := time.Now()
	var conn net.Conn
	var err error

	// Check if TLS is needed (grpcs:// prefix or port 443)
	useTLS := strings.HasPrefix(svc.URL, "grpcs://") || strings.HasSuffix(address, ":443")

	if useTLS {
		dialer := &net.Dialer{Timeout: svc.Timeout}
		conn, err = tls.DialWithDialer(dialer, "tcp", address, &tls.Config{
			InsecureSkipVerify: svc.SkipTLSVerify,
		})
	} else {
		conn, err = net.DialTimeout("tcp", address, svc.Timeout)
	}
	responseTime := time.Since(start)

	if err != nil {
		m.updateStatus(svc.Name, StatusDown, responseTime, 0, err.Error())
		return
	}
	defer conn.Close()

	var status Status
	var errMsg string

	if responseTime < 500*time.Millisecond {
		status = StatusOperational
	} else if responseTime < 2*time.Second {
		status = StatusDegraded
		errMsg = "slow connection"
	} else {
		status = StatusDegraded
		errMsg = "very slow connection"
	}

	m.updateStatus(svc.Name, status, responseTime, 0, errMsg)
}

// checkQUIC performs a QUIC/HTTP3 connectivity check
func (m *Monitor) checkQUIC(svc config.Service) {
	// Extract host from URL
	url := svc.URL
	host := strings.TrimPrefix(url, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "quic://")

	// Remove path
	if idx := strings.Index(host, "/"); idx != -1 {
		host = host[:idx]
	}

	// Add port if not present
	if !strings.Contains(host, ":") {
		if svc.Port > 0 {
			host = fmt.Sprintf("%s:%d", host, svc.Port)
		} else {
			host = host + ":443"
		}
	}

	start := time.Now()

	// QUIC uses UDP, so we first check UDP connectivity
	// Then perform a TLS handshake with QUIC ALPN
	udpAddr, err := net.ResolveUDPAddr("udp", host)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, time.Since(start), 0, "DNS resolution failed: "+err.Error())
		return
	}

	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, time.Since(start), 0, err.Error())
		return
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(svc.Timeout))

	// Send QUIC Initial packet header (simplified probe)
	// This is a minimal QUIC version negotiation probe
	// Real QUIC would require full crypto handshake
	quicProbe := []byte{
		0xc0,             // Long header, fixed bit
		0x00, 0x00, 0x00, 0x01, // Version (QUIC v1)
		0x08,             // DCID length
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // DCID (random)
		0x00,             // SCID length
	}

	_, err = conn.Write(quicProbe)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, time.Since(start), 0, "write failed: "+err.Error())
		return
	}

	// Read response (server should respond with version negotiation or retry)
	buf := make([]byte, 1200)
	conn.SetReadDeadline(time.Now().Add(svc.Timeout))
	n, err := conn.Read(buf)
	responseTime := time.Since(start)

	var status Status
	var errMsg string

	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			// Some QUIC servers may not respond to invalid initial packets
			// but if UDP is open, consider it potentially operational
			status = StatusDegraded
			errMsg = "QUIC probe timeout (port may be open)"
		} else {
			status = StatusDown
			errMsg = err.Error()
		}
	} else if n > 0 {
		// Got a response - QUIC is definitely available
		// Check for QUIC version negotiation (first byte should have form bit set)
		if buf[0]&0x80 != 0 {
			status = StatusOperational
		} else {
			status = StatusOperational
			errMsg = "QUIC response received"
		}
	} else {
		status = StatusDown
		errMsg = "empty response"
	}

	if status == StatusOperational && responseTime > 500*time.Millisecond {
		status = StatusDegraded
		errMsg = "slow QUIC handshake"
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

	// Persist to storage
	if m.storage != nil {
		checkPoints := make([]storage.CheckPoint, len(svcStatus.History))
		for i, h := range svcStatus.History {
			checkPoints[i] = storage.CheckPoint{
				Timestamp:      h.Timestamp,
				ResponseTimeMs: h.ResponseTimeMs,
				Status:         string(h.Status),
				StatusCode:     h.StatusCode,
			}
		}
		m.storage.SaveServiceCheckHistory(name, checkPoints, svcStatus.Uptime, svcStatus.LastCheck, svcStatus.ErrorMessage)
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

// checkSMTP performs an SMTP server check
func (m *Monitor) checkSMTP(svc config.Service) {
	host := svc.Host
	port := svc.Port
	if port == 0 {
		port = 25 // Default SMTP port
	}
	address := fmt.Sprintf("%s:%d", host, port)

	start := time.Now()
	conn, err := net.DialTimeout("tcp", address, svc.Timeout)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, time.Since(start), 0, err.Error())
		return
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(svc.Timeout))

	// Read SMTP banner
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	responseTime := time.Since(start)

	if err != nil {
		m.updateStatus(svc.Name, StatusDown, responseTime, 0, "failed to read SMTP banner")
		return
	}

	banner := string(buf[:n])
	var status Status
	var errMsg string
	var statusCode int

	// SMTP banner should start with 220
	if strings.HasPrefix(banner, "220") {
		statusCode = 220
		if responseTime < 1*time.Second {
			status = StatusOperational
		} else {
			status = StatusDegraded
			errMsg = "slow SMTP response"
		}
	} else {
		status = StatusDown
		errMsg = fmt.Sprintf("unexpected SMTP response: %s", strings.TrimSpace(banner))
	}

	m.updateStatus(svc.Name, status, responseTime, statusCode, errMsg)
}

// checkSSH performs an SSH server check
func (m *Monitor) checkSSH(svc config.Service) {
	host := svc.Host
	port := svc.Port
	if port == 0 {
		port = 22 // Default SSH port
	}
	address := fmt.Sprintf("%s:%d", host, port)

	start := time.Now()
	conn, err := net.DialTimeout("tcp", address, svc.Timeout)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, time.Since(start), 0, err.Error())
		return
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(svc.Timeout))

	// Read SSH banner
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	responseTime := time.Since(start)

	if err != nil {
		m.updateStatus(svc.Name, StatusDown, responseTime, 0, "failed to read SSH banner")
		return
	}

	banner := string(buf[:n])
	var status Status
	var errMsg string

	// SSH banner should start with SSH-
	if strings.HasPrefix(banner, "SSH-") {
		if responseTime < 500*time.Millisecond {
			status = StatusOperational
		} else {
			status = StatusDegraded
			errMsg = "slow SSH response"
		}
	} else {
		status = StatusDown
		errMsg = "invalid SSH banner"
	}

	m.updateStatus(svc.Name, status, responseTime, 0, errMsg)
}

// checkTLS performs TLS certificate validation
func (m *Monitor) checkTLS(svc config.Service) {
	host := svc.Host
	if host == "" {
		// Extract host from URL
		host = strings.TrimPrefix(svc.URL, "https://")
		host = strings.TrimPrefix(host, "http://")
		if idx := strings.Index(host, "/"); idx != -1 {
			host = host[:idx]
		}
	}

	port := svc.Port
	if port == 0 {
		port = 443
	}
	address := fmt.Sprintf("%s:%d", host, port)

	start := time.Now()
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: svc.Timeout},
		"tcp",
		address,
		&tls.Config{
			InsecureSkipVerify: false,
			ServerName:         strings.Split(host, ":")[0],
		},
	)
	responseTime := time.Since(start)

	if err != nil {
		m.updateStatus(svc.Name, StatusDown, responseTime, 0, "TLS error: "+err.Error())
		return
	}
	defer conn.Close()

	// Check certificate expiry
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		m.updateStatus(svc.Name, StatusDown, responseTime, 0, "no certificates found")
		return
	}

	cert := certs[0]
	daysUntilExpiry := int(time.Until(cert.NotAfter).Hours() / 24)
	warnDays := svc.TLSWarnDays
	if warnDays == 0 {
		warnDays = 30 // Default 30 days warning
	}

	var status Status
	var errMsg string

	if daysUntilExpiry <= 0 {
		status = StatusDown
		errMsg = "certificate expired"
	} else if daysUntilExpiry <= 7 {
		status = StatusDown
		errMsg = fmt.Sprintf("certificate expires in %d days", daysUntilExpiry)
	} else if daysUntilExpiry <= warnDays {
		status = StatusDegraded
		errMsg = fmt.Sprintf("certificate expires in %d days", daysUntilExpiry)
	} else {
		status = StatusOperational
	}

	m.updateStatus(svc.Name, status, responseTime, daysUntilExpiry, errMsg)
}

// checkPOP3 performs a POP3 server check
func (m *Monitor) checkPOP3(svc config.Service) {
	host := svc.Host
	port := svc.Port
	if port == 0 {
		port = 110 // Default POP3 port (995 for SSL)
	}
	address := fmt.Sprintf("%s:%d", host, port)

	start := time.Now()
	conn, err := net.DialTimeout("tcp", address, svc.Timeout)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, time.Since(start), 0, err.Error())
		return
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(svc.Timeout))

	// Read POP3 banner
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	responseTime := time.Since(start)

	if err != nil {
		m.updateStatus(svc.Name, StatusDown, responseTime, 0, "failed to read POP3 banner")
		return
	}

	banner := string(buf[:n])
	var status Status
	var errMsg string

	// POP3 banner should start with +OK
	if strings.HasPrefix(banner, "+OK") {
		if responseTime < 1*time.Second {
			status = StatusOperational
		} else {
			status = StatusDegraded
			errMsg = "slow POP3 response"
		}
	} else {
		status = StatusDown
		errMsg = "invalid POP3 response"
	}

	m.updateStatus(svc.Name, status, responseTime, 0, errMsg)
}

// checkIMAP performs an IMAP server check
func (m *Monitor) checkIMAP(svc config.Service) {
	host := svc.Host
	port := svc.Port
	if port == 0 {
		port = 143 // Default IMAP port (993 for SSL)
	}
	address := fmt.Sprintf("%s:%d", host, port)

	start := time.Now()
	conn, err := net.DialTimeout("tcp", address, svc.Timeout)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, time.Since(start), 0, err.Error())
		return
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(svc.Timeout))

	// Read IMAP banner
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	responseTime := time.Since(start)

	if err != nil {
		m.updateStatus(svc.Name, StatusDown, responseTime, 0, "failed to read IMAP banner")
		return
	}

	banner := string(buf[:n])
	var status Status
	var errMsg string

	// IMAP banner should contain OK
	if strings.Contains(banner, "OK") || strings.HasPrefix(banner, "* OK") {
		if responseTime < 1*time.Second {
			status = StatusOperational
		} else {
			status = StatusDegraded
			errMsg = "slow IMAP response"
		}
	} else {
		status = StatusDown
		errMsg = "invalid IMAP response"
	}

	m.updateStatus(svc.Name, status, responseTime, 0, errMsg)
}

// checkFTP performs an FTP server check
func (m *Monitor) checkFTP(svc config.Service) {
	host := svc.Host
	port := svc.Port
	if port == 0 {
		port = 21 // Default FTP port
	}
	address := fmt.Sprintf("%s:%d", host, port)

	start := time.Now()
	conn, err := net.DialTimeout("tcp", address, svc.Timeout)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, time.Since(start), 0, err.Error())
		return
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(svc.Timeout))

	// Read FTP banner
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	responseTime := time.Since(start)

	if err != nil {
		m.updateStatus(svc.Name, StatusDown, responseTime, 0, "failed to read FTP banner")
		return
	}

	banner := string(buf[:n])
	var status Status
	var errMsg string
	var statusCode int

	// FTP banner should start with 220
	if strings.HasPrefix(banner, "220") {
		statusCode = 220
		if responseTime < 1*time.Second {
			status = StatusOperational
		} else {
			status = StatusDegraded
			errMsg = "slow FTP response"
		}
	} else {
		status = StatusDown
		errMsg = "invalid FTP response"
	}

	m.updateStatus(svc.Name, status, responseTime, statusCode, errMsg)
}

// checkNTP performs an NTP server check
func (m *Monitor) checkNTP(svc config.Service) {
	host := svc.Host
	port := svc.Port
	if port == 0 {
		port = 123 // Default NTP port
	}
	address := fmt.Sprintf("%s:%d", host, port)

	start := time.Now()
	conn, err := net.DialTimeout("udp", address, svc.Timeout)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, time.Since(start), 0, err.Error())
		return
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(svc.Timeout))

	// NTP request packet (mode 3 = client, version 3)
	ntpPacket := make([]byte, 48)
	ntpPacket[0] = 0x1B // LI=0, VN=3, Mode=3 (client)

	_, err = conn.Write(ntpPacket)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, time.Since(start), 0, "NTP write failed")
		return
	}

	buf := make([]byte, 48)
	_, err = conn.Read(buf)
	responseTime := time.Since(start)

	var status Status
	var errMsg string

	if err != nil {
		status = StatusDown
		errMsg = "NTP read failed"
	} else if buf[0]&0x07 == 4 { // Mode 4 = server
		if responseTime < 200*time.Millisecond {
			status = StatusOperational
		} else {
			status = StatusDegraded
			errMsg = "slow NTP response"
		}
	} else {
		status = StatusDown
		errMsg = "invalid NTP response"
	}

	m.updateStatus(svc.Name, status, responseTime, 0, errMsg)
}

// checkLDAP performs an LDAP server check
func (m *Monitor) checkLDAP(svc config.Service) {
	host := svc.Host
	port := svc.Port
	if port == 0 {
		port = 389 // Default LDAP port (636 for LDAPS)
	}
	address := fmt.Sprintf("%s:%d", host, port)

	start := time.Now()
	conn, err := net.DialTimeout("tcp", address, svc.Timeout)
	responseTime := time.Since(start)

	if err != nil {
		m.updateStatus(svc.Name, StatusDown, responseTime, 0, err.Error())
		return
	}
	defer conn.Close()

	// Just check TCP connectivity for LDAP
	var status Status
	var errMsg string

	if responseTime < 500*time.Millisecond {
		status = StatusOperational
	} else {
		status = StatusDegraded
		errMsg = "slow LDAP connection"
	}

	m.updateStatus(svc.Name, status, responseTime, 0, errMsg)
}

// checkRedis performs a Redis server check
func (m *Monitor) checkRedis(svc config.Service) {
	host := svc.Host
	port := svc.Port
	if port == 0 {
		port = 6379 // Default Redis port
	}
	address := fmt.Sprintf("%s:%d", host, port)

	start := time.Now()
	conn, err := net.DialTimeout("tcp", address, svc.Timeout)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, time.Since(start), 0, err.Error())
		return
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(svc.Timeout))

	// Send PING command
	_, err = conn.Write([]byte("PING\r\n"))
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, time.Since(start), 0, "Redis write failed")
		return
	}

	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	responseTime := time.Since(start)

	var status Status
	var errMsg string

	if err != nil {
		status = StatusDown
		errMsg = "Redis read failed"
	} else if strings.Contains(string(buf[:n]), "PONG") || strings.Contains(string(buf[:n]), "+PONG") {
		if responseTime < 100*time.Millisecond {
			status = StatusOperational
		} else {
			status = StatusDegraded
			errMsg = "slow Redis response"
		}
	} else {
		status = StatusDown
		errMsg = "invalid Redis response"
	}

	m.updateStatus(svc.Name, status, responseTime, 0, errMsg)
}

// checkMongoDB performs a MongoDB server check
func (m *Monitor) checkMongoDB(svc config.Service) {
	host := svc.Host
	port := svc.Port
	if port == 0 {
		port = 27017 // Default MongoDB port
	}
	address := fmt.Sprintf("%s:%d", host, port)

	start := time.Now()
	conn, err := net.DialTimeout("tcp", address, svc.Timeout)
	responseTime := time.Since(start)

	if err != nil {
		m.updateStatus(svc.Name, StatusDown, responseTime, 0, err.Error())
		return
	}
	defer conn.Close()

	// Just check TCP connectivity for MongoDB
	var status Status
	var errMsg string

	if responseTime < 200*time.Millisecond {
		status = StatusOperational
	} else {
		status = StatusDegraded
		errMsg = "slow MongoDB connection"
	}

	m.updateStatus(svc.Name, status, responseTime, 0, errMsg)
}

// checkMySQL performs a MySQL server check
func (m *Monitor) checkMySQL(svc config.Service) {
	host := svc.Host
	port := svc.Port
	if port == 0 {
		port = 3306 // Default MySQL port
	}
	address := fmt.Sprintf("%s:%d", host, port)

	start := time.Now()
	conn, err := net.DialTimeout("tcp", address, svc.Timeout)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, time.Since(start), 0, err.Error())
		return
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(svc.Timeout))

	// Read MySQL handshake
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	responseTime := time.Since(start)

	var status Status
	var errMsg string

	if err != nil {
		status = StatusDown
		errMsg = "MySQL read failed"
	} else if n > 4 && buf[4] == 10 { // Protocol version 10
		if responseTime < 200*time.Millisecond {
			status = StatusOperational
		} else {
			status = StatusDegraded
			errMsg = "slow MySQL response"
		}
	} else {
		status = StatusDown
		errMsg = "invalid MySQL handshake"
	}

	m.updateStatus(svc.Name, status, responseTime, 0, errMsg)
}

// checkPostgres performs a PostgreSQL server check
func (m *Monitor) checkPostgres(svc config.Service) {
	host := svc.Host
	port := svc.Port
	if port == 0 {
		port = 5432 // Default PostgreSQL port
	}
	address := fmt.Sprintf("%s:%d", host, port)

	start := time.Now()
	conn, err := net.DialTimeout("tcp", address, svc.Timeout)
	responseTime := time.Since(start)

	if err != nil {
		m.updateStatus(svc.Name, StatusDown, responseTime, 0, err.Error())
		return
	}
	defer conn.Close()

	// Just check TCP connectivity for PostgreSQL
	var status Status
	var errMsg string

	if responseTime < 200*time.Millisecond {
		status = StatusOperational
	} else {
		status = StatusDegraded
		errMsg = "slow PostgreSQL connection"
	}

	m.updateStatus(svc.Name, status, responseTime, 0, errMsg)
}
