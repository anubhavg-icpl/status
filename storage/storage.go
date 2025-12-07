package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Storage handles persistent data storage
type Storage struct {
	dataDir   string
	mu        sync.RWMutex
	incidents []Incident
	history   map[string][]DailyStatus
	maintenance []Maintenance
}

// Incident represents a status incident
type Incident struct {
	ID               string           `json:"id"`
	Title            string           `json:"title"`
	Status           string           `json:"status"` // investigating, identified, monitoring, resolved
	Severity         string           `json:"severity"` // minor, major, critical
	Message          string           `json:"message"`
	AffectedServices []string         `json:"affected_services"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
	ResolvedAt       *time.Time       `json:"resolved_at,omitempty"`
	Updates          []IncidentUpdate `json:"updates"`
}

// IncidentUpdate represents an update to an incident
type IncidentUpdate struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// Maintenance represents scheduled maintenance
type Maintenance struct {
	ID               string    `json:"id"`
	Title            string    `json:"title"`
	Description      string    `json:"description"`
	AffectedServices []string  `json:"affected_services"`
	ScheduledStart   time.Time `json:"scheduled_start"`
	ScheduledEnd     time.Time `json:"scheduled_end"`
	Status           string    `json:"status"` // scheduled, in_progress, completed
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// DailyStatus represents daily uptime status
type DailyStatus struct {
	Date           string  `json:"date"`
	UptimePercent  float64 `json:"uptime_percent"`
	AvgResponseMs  int64   `json:"avg_response_ms"`
	TotalChecks    int     `json:"total_checks"`
	SuccessChecks  int     `json:"success_checks"`
	Incidents      int     `json:"incidents"`
}

// NewStorage creates a new storage instance
func NewStorage(dataDir string) (*Storage, error) {
	if dataDir == "" {
		dataDir = "data"
	}

	// Create data directory if it doesn't exist
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}

	s := &Storage{
		dataDir:   dataDir,
		incidents: []Incident{},
		history:   make(map[string][]DailyStatus),
		maintenance: []Maintenance{},
	}

	// Load existing data
	s.load()

	return s, nil
}

// load reads data from disk
func (s *Storage) load() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Load incidents
	incidentsFile := filepath.Join(s.dataDir, "incidents.json")
	if data, err := os.ReadFile(incidentsFile); err == nil {
		json.Unmarshal(data, &s.incidents)
	}

	// Load history
	historyFile := filepath.Join(s.dataDir, "history.json")
	if data, err := os.ReadFile(historyFile); err == nil {
		json.Unmarshal(data, &s.history)
	}

	// Load maintenance
	maintenanceFile := filepath.Join(s.dataDir, "maintenance.json")
	if data, err := os.ReadFile(maintenanceFile); err == nil {
		json.Unmarshal(data, &s.maintenance)
	}
}

// save writes data to disk
func (s *Storage) save() error {
	// Save incidents
	incidentsFile := filepath.Join(s.dataDir, "incidents.json")
	if data, err := json.MarshalIndent(s.incidents, "", "  "); err == nil {
		os.WriteFile(incidentsFile, data, 0644)
	}

	// Save history
	historyFile := filepath.Join(s.dataDir, "history.json")
	if data, err := json.MarshalIndent(s.history, "", "  "); err == nil {
		os.WriteFile(historyFile, data, 0644)
	}

	// Save maintenance
	maintenanceFile := filepath.Join(s.dataDir, "maintenance.json")
	if data, err := json.MarshalIndent(s.maintenance, "", "  "); err == nil {
		os.WriteFile(maintenanceFile, data, 0644)
	}

	return nil
}

// === Incident Management ===

// CreateIncident creates a new incident
func (s *Storage) CreateIncident(incident Incident) (*Incident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	incident.CreatedAt = time.Now()
	incident.UpdatedAt = time.Now()
	if incident.ID == "" {
		incident.ID = generateID()
	}

	// Add initial update
	if incident.Message != "" {
		incident.Updates = append(incident.Updates, IncidentUpdate{
			ID:        generateID(),
			Status:    incident.Status,
			Message:   incident.Message,
			CreatedAt: incident.CreatedAt,
		})
	}

	s.incidents = append([]Incident{incident}, s.incidents...)
	s.save()

	return &incident, nil
}

// UpdateIncident updates an existing incident
func (s *Storage) UpdateIncident(id string, status string, message string) (*Incident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.incidents {
		if s.incidents[i].ID == id {
			s.incidents[i].Status = status
			s.incidents[i].UpdatedAt = time.Now()

			if status == "resolved" {
				now := time.Now()
				s.incidents[i].ResolvedAt = &now
			}

			if message != "" {
				s.incidents[i].Updates = append(s.incidents[i].Updates, IncidentUpdate{
					ID:        generateID(),
					Status:    status,
					Message:   message,
					CreatedAt: time.Now(),
				})
			}

			s.save()
			return &s.incidents[i], nil
		}
	}

	return nil, nil
}

// GetIncidents returns all incidents
func (s *Storage) GetIncidents(limit int, activeOnly bool) []Incident {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []Incident
	for _, inc := range s.incidents {
		if activeOnly && inc.Status == "resolved" {
			continue
		}
		result = append(result, inc)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result
}

// GetIncident returns a specific incident
func (s *Storage) GetIncident(id string) *Incident {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, inc := range s.incidents {
		if inc.ID == id {
			return &inc
		}
	}
	return nil
}

// DeleteIncident deletes an incident
func (s *Storage) DeleteIncident(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.incidents {
		if s.incidents[i].ID == id {
			s.incidents = append(s.incidents[:i], s.incidents[i+1:]...)
			s.save()
			return true
		}
	}
	return false
}

// === Maintenance Management ===

// CreateMaintenance creates a new maintenance window
func (s *Storage) CreateMaintenance(m Maintenance) (*Maintenance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	m.CreatedAt = time.Now()
	m.UpdatedAt = time.Now()
	if m.ID == "" {
		m.ID = generateID()
	}
	if m.Status == "" {
		m.Status = "scheduled"
	}

	s.maintenance = append([]Maintenance{m}, s.maintenance...)
	s.save()

	return &m, nil
}

// GetMaintenance returns all maintenance windows
func (s *Storage) GetMaintenance(upcoming bool) []Maintenance {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !upcoming {
		return s.maintenance
	}

	var result []Maintenance
	now := time.Now()
	for _, m := range s.maintenance {
		if m.ScheduledEnd.After(now) || m.Status == "in_progress" {
			result = append(result, m)
		}
	}
	return result
}

// UpdateMaintenance updates a maintenance window
func (s *Storage) UpdateMaintenance(id string, status string) (*Maintenance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.maintenance {
		if s.maintenance[i].ID == id {
			s.maintenance[i].Status = status
			s.maintenance[i].UpdatedAt = time.Now()
			s.save()
			return &s.maintenance[i], nil
		}
	}
	return nil, nil
}

// === History Management ===

// RecordDailyStatus records daily status for a service
func (s *Storage) RecordDailyStatus(serviceName string, status DailyStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.history[serviceName] == nil {
		s.history[serviceName] = []DailyStatus{}
	}

	// Check if we already have an entry for today
	for i, existing := range s.history[serviceName] {
		if existing.Date == status.Date {
			// Update existing entry
			s.history[serviceName][i] = status
			s.save()
			return
		}
	}

	// Add new entry
	s.history[serviceName] = append(s.history[serviceName], status)

	// Keep only last 90 days
	if len(s.history[serviceName]) > 90 {
		s.history[serviceName] = s.history[serviceName][len(s.history[serviceName])-90:]
	}

	s.save()
}

// GetHistory returns history for a service
func (s *Storage) GetHistory(serviceName string, days int) []DailyStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	history := s.history[serviceName]
	if days > 0 && len(history) > days {
		return history[len(history)-days:]
	}
	return history
}

// GetAllHistory returns history for all services
func (s *Storage) GetAllHistory(days int) map[string][]DailyStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string][]DailyStatus)
	for service, history := range s.history {
		if days > 0 && len(history) > days {
			result[service] = history[len(history)-days:]
		} else {
			result[service] = history
		}
	}
	return result
}

// Helper to generate unique IDs
func generateID() string {
	return time.Now().Format("20060102150405") + randomString(6)
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[time.Now().UnixNano()%int64(len(letters))]
		time.Sleep(time.Nanosecond)
	}
	return string(b)
}
