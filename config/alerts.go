package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/status/notify"
)

// AlertsConfig controls outbound alerting for service state changes and the
// channels those alerts travel over.
type AlertsConfig struct {
	// Enabled turns service state-change alerts on. Incident and maintenance
	// notifications are always delivered; this switch is about probe results.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Cooldown is the minimum gap between two alerts for the same service.
	// It absorbs flapping without hiding a real, sustained outage.
	Cooldown time.Duration `yaml:"cooldown" json:"cooldown"`
	// RepeatEvery re-sends an alert while a service stays down. Zero disables
	// reminders — you get one alert on the way down and one on recovery.
	RepeatEvery time.Duration `yaml:"repeat_every" json:"repeat_every"`
	// FailureThreshold is how many consecutive bad checks are required before
	// alerting. 1 alerts on the first failure; 2-3 suits noisy networks.
	FailureThreshold int `yaml:"failure_threshold" json:"failure_threshold"`
	// OnlyGroups limits alerting to these service groups. Empty means all.
	OnlyGroups []string `yaml:"only_groups" json:"only_groups"`

	Push    notify.PushConfig   `yaml:"push" json:"push"`
	Ntfy    notify.NtfyConfig   `yaml:"ntfy" json:"ntfy"`
	Cluster ClusterAlertsConfig `yaml:"cluster" json:"cluster"`
}

// ClusterAlertsConfig governs alerting on Kubernetes-side failures — crashing
// pods, unschedulable workloads, NotReady nodes — across every namespace.
type ClusterAlertsConfig struct {
	// Enabled turns cluster error detection on. Needs an in-cluster client.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Interval is how often the cluster is reconciled for new failures.
	// Detection latency is at most one interval.
	Interval time.Duration `yaml:"interval" json:"interval"`
	// MinDuration is how long a failure must persist before it alerts. This is
	// what separates a rolling deploy from an outage: a pod that is
	// CrashLooping for 20s during a rollout is noise, one still doing it two
	// minutes later is not.
	MinDuration time.Duration `yaml:"min_duration" json:"min_duration"`
	// MinSeverity filters what reaches a phone: minor | major | critical.
	MinSeverity string `yaml:"min_severity" json:"min_severity"`
	// IgnoreNamespaces skips noisy or irrelevant namespaces entirely.
	IgnoreNamespaces []string `yaml:"ignore_namespaces" json:"ignore_namespaces"`
	// OnlyNamespaces restricts detection to these namespaces. Empty = all.
	OnlyNamespaces []string `yaml:"only_namespaces" json:"only_namespaces"`
	// MaxTracked caps the issue registry so a cluster-wide meltdown cannot
	// grow this process's memory without bound.
	MaxTracked int `yaml:"max_tracked" json:"max_tracked"`
}

// ClusterConfig controls the Kubernetes cluster view on the status page.
type ClusterConfig struct {
	// Enabled shows the cluster section. Requires an in-cluster client; the
	// section hides itself automatically when that is unavailable.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Public serves /api/cluster without authentication. Off by default: node
	// names, images and namespaces are internal topology.
	Public bool `yaml:"public" json:"public"`
	// CacheTTL is how long a snapshot is reused between requests.
	CacheTTL time.Duration `yaml:"cache_ttl" json:"cache_ttl"`
}

// defaultAlerts returns the alerting defaults applied before YAML is merged.
func defaultAlerts() AlertsConfig {
	return AlertsConfig{
		Enabled:          true,
		Cooldown:         5 * time.Minute,
		RepeatEvery:      0,
		FailureThreshold: 1,
		Push: notify.PushConfig{
			TTL:     86400,
			Urgency: "high",
		},
		Ntfy: notify.NtfyConfig{
			ServerURL:        "https://ntfy.sh",
			Priority:         "high",
			CriticalPriority: "max",
		},
		Cluster: ClusterAlertsConfig{
			Enabled:     true,
			Interval:    30 * time.Second,
			MinDuration: 2 * time.Minute,
			MinSeverity: "major",
			MaxTracked:  500,
		},
	}
}

func defaultCluster() ClusterConfig {
	return ClusterConfig{
		Enabled:  true,
		Public:   false,
		CacheTTL: 10 * time.Second,
	}
}

// applyAlertDefaults fills zero values left by a partial YAML block.
func (c *Config) applyAlertDefaults() {
	d := defaultAlerts()
	if c.Alerts.Cooldown == 0 {
		c.Alerts.Cooldown = d.Cooldown
	}
	if c.Alerts.FailureThreshold <= 0 {
		c.Alerts.FailureThreshold = d.FailureThreshold
	}
	if c.Alerts.Push.TTL <= 0 {
		c.Alerts.Push.TTL = d.Push.TTL
	}
	if c.Alerts.Push.Urgency == "" {
		c.Alerts.Push.Urgency = d.Push.Urgency
	}
	if c.Alerts.Push.Subject == "" && c.BaseURL != "" {
		c.Alerts.Push.Subject = c.BaseURL
	}
	if c.Alerts.Ntfy.ServerURL == "" {
		c.Alerts.Ntfy.ServerURL = d.Ntfy.ServerURL
	}
	if c.Alerts.Ntfy.Priority == "" {
		c.Alerts.Ntfy.Priority = d.Ntfy.Priority
	}
	if c.Alerts.Ntfy.CriticalPriority == "" {
		c.Alerts.Ntfy.CriticalPriority = d.Ntfy.CriticalPriority
	}
	if c.Cluster.CacheTTL == 0 {
		c.Cluster.CacheTTL = defaultCluster().CacheTTL
	}
	if c.Alerts.Cluster.Interval <= 0 {
		c.Alerts.Cluster.Interval = d.Cluster.Interval
	}
	if c.Alerts.Cluster.MinDuration < 0 {
		c.Alerts.Cluster.MinDuration = d.Cluster.MinDuration
	}
	if c.Alerts.Cluster.MinSeverity == "" {
		c.Alerts.Cluster.MinSeverity = d.Cluster.MinSeverity
	}
	if c.Alerts.Cluster.MaxTracked <= 0 {
		c.Alerts.Cluster.MaxTracked = d.Cluster.MaxTracked
	}
}

// applyEnvOverrides lets secrets stay out of config.yaml and arrive from a
// Kubernetes Secret instead. Env always wins over file values.
func (c *Config) applyEnvOverrides() {
	setStr := func(dst *string, key string) {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			*dst = v
		}
	}
	setBool := func(dst *bool, key string) {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
				*dst = b
			}
		}
	}

	setStr(&c.API.Key, "STATUS_API_KEY")
	setStr(&c.API.BearerToken, "STATUS_BEARER_TOKEN")
	setStr(&c.BaseURL, "STATUS_BASE_URL")

	setBool(&c.Alerts.Enabled, "STATUS_ALERTS_ENABLED")

	setBool(&c.Alerts.Push.Enabled, "STATUS_PUSH_ENABLED")
	setStr(&c.Alerts.Push.Subject, "STATUS_PUSH_SUBJECT")
	setStr(&c.Alerts.Push.PublicKey, "STATUS_VAPID_PUBLIC_KEY")
	setStr(&c.Alerts.Push.PrivateKey, "STATUS_VAPID_PRIVATE_KEY")

	setBool(&c.Alerts.Ntfy.Enabled, "STATUS_NTFY_ENABLED")
	setStr(&c.Alerts.Ntfy.ServerURL, "STATUS_NTFY_SERVER")
	setStr(&c.Alerts.Ntfy.Topic, "STATUS_NTFY_TOPIC")
	setStr(&c.Alerts.Ntfy.Token, "STATUS_NTFY_TOKEN")
	setStr(&c.Alerts.Ntfy.Username, "STATUS_NTFY_USERNAME")
	setStr(&c.Alerts.Ntfy.Password, "STATUS_NTFY_PASSWORD")
	setStr(&c.Alerts.Ntfy.Call, "STATUS_NTFY_CALL")
	setStr(&c.Alerts.Ntfy.Email, "STATUS_NTFY_EMAIL")

	setBool(&c.Alerts.Cluster.Enabled, "STATUS_CLUSTER_ALERTS_ENABLED")
	setStr(&c.Alerts.Cluster.MinSeverity, "STATUS_CLUSTER_MIN_SEVERITY")

	setBool(&c.Cluster.Enabled, "STATUS_CLUSTER_ENABLED")
	setBool(&c.Cluster.Public, "STATUS_CLUSTER_PUBLIC")

	setStr(&c.Redis.Password, "STATUS_REDIS_PASSWORD")
}

// AlertsGroupAllowed reports whether a service group is in scope for alerting.
func (c *Config) AlertsGroupAllowed(group string) bool {
	if len(c.Alerts.OnlyGroups) == 0 {
		return true
	}
	for _, g := range c.Alerts.OnlyGroups {
		if strings.EqualFold(strings.TrimSpace(g), group) {
			return true
		}
	}
	return false
}
