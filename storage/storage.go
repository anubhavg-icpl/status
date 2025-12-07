package storage

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Bucket names
var (
	bucketIncidents    = []byte("incidents")
	bucketMaintenance  = []byte("maintenance")
	bucketHistory      = []byte("history")
	bucketCheckHistory = []byte("check_history")
)

// Storage handles persistent data storage using BoltDB
type Storage struct {
	dataDir string
	db      *bolt.DB
	mu      sync.RWMutex
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
	Date          string  `json:"date"`
	UptimePercent float64 `json:"uptime_percent"`
	AvgResponseMs int64   `json:"avg_response_ms"`
	TotalChecks   int     `json:"total_checks"`
	SuccessChecks int     `json:"success_checks"`
	Incidents     int     `json:"incidents"`
}

// CheckPoint represents a single health check result (for persistence)
type CheckPoint struct {
	Timestamp      time.Time `json:"timestamp"`
	ResponseTimeMs int64     `json:"response_time_ms"`
	Status         string    `json:"status"`
	StatusCode     int       `json:"status_code"`
}

// ServiceCheckHistory holds persisted check history for a service
type ServiceCheckHistory struct {
	ServiceName  string       `json:"service_name"`
	History      []CheckPoint `json:"history"`
	Uptime       float64      `json:"uptime"`
	LastCheck    time.Time    `json:"last_check"`
	ErrorMessage string       `json:"error_message,omitempty"`
}

// NewStorage creates a new storage instance with BoltDB
func NewStorage(dataDir string) (*Storage, error) {
	if dataDir == "" {
		dataDir = "data"
	}

	// Create data directory if it doesn't exist
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}

	// Open BoltDB database
	dbPath := filepath.Join(dataDir, "status.db")
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Create buckets
	err = db.Update(func(tx *bolt.Tx) error {
		buckets := [][]byte{bucketIncidents, bucketMaintenance, bucketHistory, bucketCheckHistory}
		for _, bucket := range buckets {
			if _, err := tx.CreateBucketIfNotExists(bucket); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create buckets: %w", err)
	}

	s := &Storage{
		dataDir: dataDir,
		db:      db,
	}

	return s, nil
}

// Close closes the database
func (s *Storage) Close() error {
	if s.db != nil {
		return s.db.Close()
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

	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketIncidents)
		data, err := json.Marshal(incident)
		if err != nil {
			return err
		}
		return b.Put([]byte(incident.ID), data)
	})

	if err != nil {
		return nil, err
	}
	return &incident, nil
}

// UpdateIncident updates an existing incident
func (s *Storage) UpdateIncident(id string, status string, message string) (*Incident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var incident *Incident

	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketIncidents)
		data := b.Get([]byte(id))
		if data == nil {
			return nil
		}

		var inc Incident
		if err := json.Unmarshal(data, &inc); err != nil {
			return err
		}

		inc.Status = status
		inc.UpdatedAt = time.Now()

		if status == "resolved" {
			now := time.Now()
			inc.ResolvedAt = &now
		}

		if message != "" {
			inc.Updates = append(inc.Updates, IncidentUpdate{
				ID:        generateID(),
				Status:    status,
				Message:   message,
				CreatedAt: time.Now(),
			})
		}

		newData, err := json.Marshal(inc)
		if err != nil {
			return err
		}

		incident = &inc
		return b.Put([]byte(id), newData)
	})

	if err != nil {
		return nil, err
	}
	return incident, nil
}

// GetIncidents returns all incidents
func (s *Storage) GetIncidents(limit int, activeOnly bool) []Incident {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var incidents []Incident

	s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketIncidents)
		c := b.Cursor()

		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			var inc Incident
			if err := json.Unmarshal(v, &inc); err != nil {
				continue
			}

			if activeOnly && inc.Status == "resolved" {
				continue
			}

			incidents = append(incidents, inc)
			if limit > 0 && len(incidents) >= limit {
				break
			}
		}
		return nil
	})

	return incidents
}

// GetIncident returns a specific incident
func (s *Storage) GetIncident(id string) *Incident {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var incident *Incident

	s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketIncidents)
		data := b.Get([]byte(id))
		if data == nil {
			return nil
		}

		var inc Incident
		if err := json.Unmarshal(data, &inc); err != nil {
			return err
		}
		incident = &inc
		return nil
	})

	return incident
}

// DeleteIncident deletes an incident
func (s *Storage) DeleteIncident(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketIncidents)
		return b.Delete([]byte(id))
	})

	return err == nil
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

	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMaintenance)
		data, err := json.Marshal(m)
		if err != nil {
			return err
		}
		return b.Put([]byte(m.ID), data)
	})

	if err != nil {
		return nil, err
	}
	return &m, nil
}

// GetMaintenance returns all maintenance windows
func (s *Storage) GetMaintenance(upcoming bool) []Maintenance {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var maintenance []Maintenance

	s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMaintenance)
		c := b.Cursor()

		now := time.Now()
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			var m Maintenance
			if err := json.Unmarshal(v, &m); err != nil {
				continue
			}

			if upcoming && m.ScheduledEnd.Before(now) && m.Status != "in_progress" {
				continue
			}

			maintenance = append(maintenance, m)
		}
		return nil
	})

	return maintenance
}

// UpdateMaintenance updates a maintenance window
func (s *Storage) UpdateMaintenance(id string, status string) (*Maintenance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var maintenance *Maintenance

	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMaintenance)
		data := b.Get([]byte(id))
		if data == nil {
			return nil
		}

		var m Maintenance
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}

		m.Status = status
		m.UpdatedAt = time.Now()

		newData, err := json.Marshal(m)
		if err != nil {
			return err
		}

		maintenance = &m
		return b.Put([]byte(id), newData)
	})

	if err != nil {
		return nil, err
	}
	return maintenance, nil
}

// === History Management ===

// RecordDailyStatus records daily status for a service
func (s *Storage) RecordDailyStatus(serviceName string, status DailyStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketHistory)

		// Get existing history for this service
		var history []DailyStatus
		key := []byte(serviceName)
		if data := b.Get(key); data != nil {
			json.Unmarshal(data, &history)
		}

		// Check if we already have an entry for today
		found := false
		for i, existing := range history {
			if existing.Date == status.Date {
				history[i] = status
				found = true
				break
			}
		}

		if !found {
			history = append(history, status)
		}

		// Keep only last 90 days
		if len(history) > 90 {
			history = history[len(history)-90:]
		}

		data, err := json.Marshal(history)
		if err != nil {
			return err
		}
		return b.Put(key, data)
	})
}

// GetHistory returns history for a service
func (s *Storage) GetHistory(serviceName string, days int) []DailyStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var history []DailyStatus

	s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketHistory)
		data := b.Get([]byte(serviceName))
		if data != nil {
			json.Unmarshal(data, &history)
		}
		return nil
	})

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

	s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketHistory)
		c := b.Cursor()

		for k, v := c.First(); k != nil; k, v = c.Next() {
			var history []DailyStatus
			if err := json.Unmarshal(v, &history); err != nil {
				continue
			}

			serviceName := string(k)
			if days > 0 && len(history) > days {
				result[serviceName] = history[len(history)-days:]
			} else {
				result[serviceName] = history
			}
		}
		return nil
	})

	return result
}

// === Service Check History (for uptime bars) ===

// SaveServiceCheckHistory persists the check history for a service
func (s *Storage) SaveServiceCheckHistory(serviceName string, history []CheckPoint, uptime float64, lastCheck time.Time, errorMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketCheckHistory)

		data := ServiceCheckHistory{
			ServiceName:  serviceName,
			History:      history,
			Uptime:       uptime,
			LastCheck:    lastCheck,
			ErrorMessage: errorMsg,
		}

		jsonData, err := json.Marshal(data)
		if err != nil {
			return err
		}
		return b.Put([]byte(serviceName), jsonData)
	})
}

// GetServiceCheckHistory retrieves persisted check history for a service
func (s *Storage) GetServiceCheckHistory(serviceName string) *ServiceCheckHistory {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var history *ServiceCheckHistory

	s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketCheckHistory)
		data := b.Get([]byte(serviceName))
		if data == nil {
			return nil
		}

		var h ServiceCheckHistory
		if err := json.Unmarshal(data, &h); err != nil {
			return err
		}
		history = &h
		return nil
	})

	return history
}

// GetAllServiceCheckHistory retrieves all persisted check histories
func (s *Storage) GetAllServiceCheckHistory() map[string]*ServiceCheckHistory {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]*ServiceCheckHistory)

	s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketCheckHistory)
		c := b.Cursor()

		for k, v := c.First(); k != nil; k, v = c.Next() {
			var h ServiceCheckHistory
			if err := json.Unmarshal(v, &h); err != nil {
				continue
			}
			result[string(k)] = &h
		}
		return nil
	})

	return result
}

// Helper to generate unique IDs using crypto/rand for proper entropy
func generateID() string {
	return time.Now().Format("20060102150405") + randomString(6)
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	randBytes := make([]byte, n)
	if _, err := rand.Read(randBytes); err != nil {
		// Fallback to less secure but functional method
		for i := range b {
			b[i] = letters[time.Now().UnixNano()%int64(len(letters))]
		}
		return string(b)
	}
	for i := range b {
		b[i] = letters[int(randBytes[i])%len(letters)]
	}
	return string(b)
}
