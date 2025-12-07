package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds the main configuration
type Config struct {
	Title       string          `yaml:"title"`
	Description string          `yaml:"description"`
	Logo        string          `yaml:"logo"`
	Favicon     string          `yaml:"favicon"`
	BaseURL     string          `yaml:"base_url"`
	Theme       ThemeConfig     `yaml:"theme"`
	Server      ServerConfig    `yaml:"server"`
	Services    []Service       `yaml:"services"`
	Incidents   []Incident      `yaml:"incidents"`
	Webhooks    []WebhookConfig `yaml:"webhooks"`
	Storage     StorageConfig   `yaml:"storage"`
	API         APIConfig       `yaml:"api"`
}

// StorageConfig holds storage settings
type StorageConfig struct {
	DataDir string `yaml:"data_dir"`
}

// APIConfig holds API settings
type APIConfig struct {
	Enabled      bool     `yaml:"enabled"`
	Key          string   `yaml:"key"`           // API key (X-API-Key header)
	BearerToken  string   `yaml:"bearer_token"`  // Bearer token auth
	BasicAuth    BasicAuth `yaml:"basic_auth"`   // Basic auth
	AllowedIPs   []string `yaml:"allowed_ips"`   // IP whitelist
	RateLimit    int      `yaml:"rate_limit"`
}

// BasicAuth holds basic auth credentials
type BasicAuth struct {
	Enabled  bool   `yaml:"enabled"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// WebhookConfig represents a webhook configuration
type WebhookConfig struct {
	ID      string            `yaml:"id"`
	Name    string            `yaml:"name"`
	URL     string            `yaml:"url"`
	Type    string            `yaml:"type"` // generic, slack, discord, teams
	Events  []string          `yaml:"events"`
	Headers map[string]string `yaml:"headers"`
	Enabled bool              `yaml:"enabled"`
}

// ThemeConfig holds theme customization
type ThemeConfig struct {
	PrimaryColor   string `yaml:"primary_color"`
	AccentColor    string `yaml:"accent_color"`
	DarkMode       bool   `yaml:"dark_mode"`
}

// ServerConfig holds HTTP server settings
type ServerConfig struct {
	Port         int           `yaml:"port"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
}

// CheckType represents the type of health check
type CheckType string

const (
	CheckHTTP      CheckType = "http"
	CheckTCP       CheckType = "tcp"
	CheckUDP       CheckType = "udp"
	CheckICMP      CheckType = "icmp"
	CheckDNS       CheckType = "dns"
	CheckWebSocket CheckType = "websocket"
	CheckGRPC      CheckType = "grpc"
	CheckQUIC      CheckType = "quic"
	CheckSMTP      CheckType = "smtp"
	CheckSSH       CheckType = "ssh"
	CheckTLS       CheckType = "tls"  // TLS certificate validation
	CheckPOP3      CheckType = "pop3"
	CheckIMAP      CheckType = "imap"
	CheckFTP       CheckType = "ftp"
	CheckNTP       CheckType = "ntp"
	CheckLDAP      CheckType = "ldap"
	CheckRedis     CheckType = "redis"
	CheckMongoDB   CheckType = "mongodb"
	CheckMySQL     CheckType = "mysql"
	CheckPostgres  CheckType = "postgres"
)

// Service represents a monitored service
type Service struct {
	Name           string            `yaml:"name"`
	Group          string            `yaml:"group"`
	Type           CheckType         `yaml:"type"`           // http, tcp, icmp, dns, websocket, grpc
	URL            string            `yaml:"url"`            // For HTTP/WebSocket/gRPC
	Host           string            `yaml:"host"`           // For TCP/ICMP/DNS
	Port           int               `yaml:"port"`           // For TCP/gRPC
	Method         string            `yaml:"method"`         // HTTP method
	Interval       time.Duration     `yaml:"interval"`
	Timeout        time.Duration     `yaml:"timeout"`
	Headers        map[string]string `yaml:"headers"`
	ExpectedStatus int               `yaml:"expected_status"`
	Description    string            `yaml:"description"`
	// DNS specific
	DNSRecordType  string            `yaml:"dns_record_type"` // A, AAAA, CNAME, MX, TXT
	DNSResolver    string            `yaml:"dns_resolver"`    // Custom DNS resolver
	// TLS options
	SkipTLSVerify  bool              `yaml:"skip_tls_verify"`
	// Body validation
	ExpectedBody   string            `yaml:"expected_body"`   // String to find in response
	// UDP specific
	UDPPayload     string            `yaml:"udp_payload"`     // Payload to send for UDP check
	UDPExpected    string            `yaml:"udp_expected"`    // Expected response pattern
	// QUIC specific (HTTP/3)
	QUICALPN       []string          `yaml:"quic_alpn"`       // ALPN protocols (h3, h3-29, etc.)
	// TLS Certificate check
	TLSWarnDays    int               `yaml:"tls_warn_days"`   // Days before expiry to warn (default 30)
	// Database connection strings
	ConnectionString string          `yaml:"connection_string"` // For database checks
	// SMTP/Email specific
	SMTPStartTLS   bool              `yaml:"smtp_starttls"`   // Use STARTTLS
	SMTPAuth       bool              `yaml:"smtp_auth"`       // Require auth response
}

// Incident represents a past or ongoing incident
type Incident struct {
	ID          string    `yaml:"id"`
	Title       string    `yaml:"title"`
	Description string    `yaml:"description"`
	Status      string    `yaml:"status"` // investigating, identified, monitoring, resolved
	Severity    string    `yaml:"severity"` // minor, major, critical
	CreatedAt   time.Time `yaml:"created_at"`
	UpdatedAt   time.Time `yaml:"updated_at"`
	ResolvedAt  *time.Time `yaml:"resolved_at"`
	AffectedServices []string `yaml:"affected_services"`
	Updates     []IncidentUpdate `yaml:"updates"`
}

// IncidentUpdate represents an update to an incident
type IncidentUpdate struct {
	Status    string    `yaml:"status"`
	Message   string    `yaml:"message"`
	Timestamp time.Time `yaml:"timestamp"`
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *Config {
	return &Config{
		Title:       "System Status",
		Description: "Real-time system status and uptime monitoring",
		BaseURL:     "http://localhost:8080",
		Theme: ThemeConfig{
			PrimaryColor: "#3B82F6",
			AccentColor:  "#10B981",
			DarkMode:     true,
		},
		Server: ServerConfig{
			Port:         8080,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
		},
		Storage: StorageConfig{
			DataDir: "data",
		},
		API: APIConfig{
			Enabled:   true,
			RateLimit: 100,
		},
		Services: []Service{},
		Webhooks: []WebhookConfig{},
	}
}

// Load reads configuration from a YAML file
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	// Apply defaults for services
	for i := range cfg.Services {
		// Default check type is HTTP
		if cfg.Services[i].Type == "" {
			cfg.Services[i].Type = CheckHTTP
		}
		if cfg.Services[i].Method == "" {
			cfg.Services[i].Method = "GET"
		}
		if cfg.Services[i].Interval == 0 {
			cfg.Services[i].Interval = 30 * time.Second
		}
		if cfg.Services[i].Timeout == 0 {
			cfg.Services[i].Timeout = 10 * time.Second
		}
		if cfg.Services[i].ExpectedStatus == 0 {
			cfg.Services[i].ExpectedStatus = 200
		}
		if cfg.Services[i].DNSRecordType == "" {
			cfg.Services[i].DNSRecordType = "A"
		}
		if cfg.Services[i].DNSResolver == "" {
			cfg.Services[i].DNSResolver = "8.8.8.8:53"
		}
	}

	return cfg, nil
}
