package k8sclient

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/status/config"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Annotation keys recognized on a Service to opt into auto-discovery.
//
// Example:
//
//	metadata:
//	  annotations:
//	    status.invinsense.dev/probe:           "true"
//	    status.invinsense.dev/type:            "http"     # default: http
//	    status.invinsense.dev/path:            "/health"  # default: /
//	    status.invinsense.dev/port:            "8080"     # default: first svc port
//	    status.invinsense.dev/group:           "Custom"   # default: namespace name
//	    status.invinsense.dev/interval:        "30s"
//	    status.invinsense.dev/timeout:         "10s"
//	    status.invinsense.dev/expected-status: "200"
//	    status.invinsense.dev/name:            "Pretty Name"  # default: svc name
//	    status.invinsense.dev/description:     "what it does"
//	    status.invinsense.dev/password-ref:    "secret-name/key"  # redis/mongo auth
const (
	annPrefix     = "status.invinsense.dev/"
	annProbe      = annPrefix + "probe"
	annType       = annPrefix + "type"
	annPath       = annPrefix + "path"
	annPort       = annPrefix + "port"
	annGroup      = annPrefix + "group"
	annInterval   = annPrefix + "interval"
	annTimeout    = annPrefix + "timeout"
	annExpected   = annPrefix + "expected-status"
	annName       = annPrefix + "name"
	annDesc       = annPrefix + "description"
	annPasswordRf = annPrefix + "password-ref"
)

// DiscoverServices scans every Service in cache and returns generated probes
// for those carrying `status.invinsense.dev/probe: true`. Headless services
// (clusterIP=None) are skipped. Existing names are NOT deduped here — caller
// merges with config.yaml-defined services.
//
// The Service informer/lister must be primed before calling.
func (c *Client) DiscoverServices() ([]config.Service, error) {
	if c == nil || c.Factory == nil {
		return nil, fmt.Errorf("k8s client not initialized")
	}
	all, err := c.Services.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}

	var out []config.Service
	for _, s := range all {
		ann := s.Annotations
		if ann == nil {
			continue
		}
		if !truthy(ann[annProbe]) {
			continue
		}
		if s.Spec.ClusterIP == corev1.ClusterIPNone {
			continue // headless
		}
		svc := buildProbe(s)
		if svc.Name != "" {
			out = append(out, svc)
		}
	}
	return out, nil
}

func buildProbe(s *corev1.Service) config.Service {
	ann := s.Annotations
	name := ann[annName]
	if name == "" {
		name = fmt.Sprintf("%s/%s", s.Namespace, s.Name)
	}
	group := ann[annGroup]
	if group == "" {
		group = "auto:" + s.Namespace
	}

	port := pickPort(s, ann[annPort])
	host := fmt.Sprintf("%s.%s.svc.cluster.local", s.Name, s.Namespace)

	t := strings.ToLower(strings.TrimSpace(ann[annType]))
	if t == "" {
		t = "http"
	}

	svc := config.Service{
		Name:        name,
		Group:       group,
		Description: ann[annDesc],
		Interval:    parseDur(ann[annInterval], 30*time.Second),
		Timeout:     parseDur(ann[annTimeout], 10*time.Second),
	}

	switch t {
	case "http", "https":
		path := ann[annPath]
		if path == "" {
			path = "/"
		}
		scheme := "http"
		if t == "https" {
			scheme = "https"
		}
		svc.Type = config.CheckHTTP
		svc.URL = fmt.Sprintf("%s://%s:%d%s", scheme, host, port, path)
		svc.Method = "GET"
		svc.ExpectedStatus = atoi(ann[annExpected], 200)
	case "tcp":
		svc.Type = config.CheckTCP
		svc.Host = host
		svc.Port = port
	case "redis":
		svc.Type = config.CheckRedis
		svc.Host = host
		if port == 0 {
			port = 6379
		}
		svc.Port = port
	case "mongodb", "mongo":
		svc.Type = config.CheckMongoDB
		svc.Host = host
		if port == 0 {
			port = 27017
		}
		svc.Port = port
	case "postgres", "postgresql":
		svc.Type = config.CheckPostgres
		svc.Host = host
		if port == 0 {
			port = 5432
		}
		svc.Port = port
	case "mysql":
		svc.Type = config.CheckMySQL
		svc.Host = host
		if port == 0 {
			port = 3306
		}
		svc.Port = port
	case "tls":
		svc.Type = config.CheckTLS
		svc.Host = host
		if port == 0 {
			port = 443
		}
		svc.Port = port
		svc.TLSWarnDays = 30
	default:
		// unknown type: skip
		return config.Service{}
	}
	return svc
}

func pickPort(s *corev1.Service, override string) int {
	if override != "" {
		if p := atoi(override, 0); p > 0 {
			return p
		}
	}
	for _, p := range s.Spec.Ports {
		if p.Protocol == corev1.ProtocolTCP || p.Protocol == "" {
			return int(p.Port)
		}
	}
	return 0
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "t", "true", "yes", "y", "on", "enabled":
		return true
	}
	return false
}

func atoi(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}

func parseDur(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}
