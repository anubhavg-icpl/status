package k8sclient

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/status/config"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
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
	annPrefix      = "status.invinsense.dev/"
	annProbe       = annPrefix + "probe"
	annType        = annPrefix + "type"
	annPath        = annPrefix + "path"
	annPort        = annPrefix + "port"
	annGroup       = annPrefix + "group"
	annInterval    = annPrefix + "interval"
	annTimeout     = annPrefix + "timeout"
	annExpected    = annPrefix + "expected-status"
	annName        = annPrefix + "name"
	annDesc        = annPrefix + "description"
	annPasswordRef = annPrefix + "password-secret" // ns/secret-name#key  or  secret-name#key
	annUsername    = annPrefix + "username"
)

// Reconciler is the minimal Monitor surface auto-discovery needs to drive
// hot-reload. It is implemented by *monitor.Monitor in the parent package.
type Reconciler interface {
	AddService(config.Service) bool
	RemoveService(string) bool
}

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
		if svc.Name == "" {
			continue
		}
		// Resolve password-secret annotation (best-effort; logged on failure).
		if ref := ann[annPasswordRef]; ref != "" {
			if pw, err := c.resolveSecretRef(s.Namespace, ref); err != nil {
				log.Printf("autodisc: %s/%s password-secret %q failed: %v", s.Namespace, s.Name, ref, err)
			} else {
				svc.Password = pw
			}
		}
		out = append(out, svc)
	}
	return out, nil
}

// resolveSecretRef parses "ns/name#key" or "name#key" (ns defaults to svcNs)
// and returns the secret value. Uses the cluster clientset directly because
// Secrets are not in the informer factory (keeps RBAC narrower).
func (c *Client) resolveSecretRef(svcNs, ref string) (string, error) {
	ns, rest, ok := strings.Cut(ref, "/")
	if !ok {
		rest = ref
		ns = svcNs
	}
	name, key, ok := strings.Cut(rest, "#")
	if !ok {
		return "", fmt.Errorf("ref must be [ns/]name#key, got %q", ref)
	}
	sec, err := c.Clientset.CoreV1().Secrets(ns).Get(
		context.Background(), name, metav1.GetOptions{},
	)
	if err != nil {
		return "", err
	}
	v, ok := sec.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not in secret %s/%s", key, ns, name)
	}
	return string(v), nil
}

// WatchServices wires Service add/update/delete handlers so probes
// register and deregister at runtime without a pod restart.
// reservedNames is the set of svc names defined in config.yaml — those
// are never auto-added (config wins).
func (c *Client) WatchServices(ctx context.Context, rec Reconciler, reservedNames map[string]bool) error {
	if c == nil || c.Factory == nil {
		return fmt.Errorf("k8s client not initialized")
	}
	inf := c.Factory.Core().V1().Services().Informer()
	_, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			s, ok := obj.(*corev1.Service)
			if !ok {
				return
			}
			c.reconcileOne(s, "", rec, reservedNames)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldS, _ := oldObj.(*corev1.Service)
			newS, _ := newObj.(*corev1.Service)
			if newS == nil {
				return
			}
			prevName := ""
			if oldS != nil {
				prevName = probeName(oldS)
			}
			c.reconcileOne(newS, prevName, rec, reservedNames)
		},
		DeleteFunc: func(obj interface{}) {
			s, ok := obj.(*corev1.Service)
			if !ok {
				// Tombstone case
				if t, isT := obj.(cache.DeletedFinalStateUnknown); isT {
					s, _ = t.Obj.(*corev1.Service)
				}
			}
			if s == nil {
				return
			}
			name := probeName(s)
			if name != "" && !reservedNames[name] {
				if rec.RemoveService(name) {
					log.Printf("autodisc: removed %q (svc %s/%s deleted)", name, s.Namespace, s.Name)
				}
			}
		},
	})
	return err
}

func (c *Client) reconcileOne(s *corev1.Service, prevName string, rec Reconciler, reserved map[string]bool) {
	if s == nil {
		return
	}
	currentName := probeName(s)

	// If name changed (annotation flipped), drop the old probe.
	if prevName != "" && prevName != currentName {
		if !reserved[prevName] {
			rec.RemoveService(prevName)
		}
	}

	enabled := s.Annotations != nil && truthy(s.Annotations[annProbe])
	if !enabled || s.Spec.ClusterIP == corev1.ClusterIPNone {
		// probe annotation removed → drop probe
		if currentName != "" && !reserved[currentName] {
			rec.RemoveService(currentName)
		}
		return
	}
	svc := buildProbe(s)
	if svc.Name == "" {
		return
	}
	if reserved[svc.Name] {
		return // config.yaml wins
	}
	// Replace strategy: remove + add so updates pick up new fields.
	rec.RemoveService(svc.Name)
	if rec.AddService(svc) {
		log.Printf("autodisc: registered %q (svc %s/%s, type=%s)", svc.Name, s.Namespace, s.Name, svc.Type)
	}
}

func probeName(s *corev1.Service) string {
	if s == nil || s.Annotations == nil {
		return ""
	}
	if n := s.Annotations[annName]; n != "" {
		return n
	}
	return fmt.Sprintf("%s/%s", s.Namespace, s.Name)
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
		svc.Username = ann[annUsername]
	case "mongodb", "mongo":
		svc.Type = config.CheckMongoDB
		svc.Host = host
		if port == 0 {
			port = 27017
		}
		svc.Port = port
		svc.Username = ann[annUsername]
	case "postgres", "postgresql":
		svc.Type = config.CheckPostgres
		svc.Host = host
		if port == 0 {
			port = 5432
		}
		svc.Port = port
		svc.Username = ann[annUsername]
	case "mysql":
		svc.Type = config.CheckMySQL
		svc.Host = host
		if port == 0 {
			port = 3306
		}
		svc.Port = port
		svc.Username = ann[annUsername]
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
