package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/status/config"
	"github.com/status/k8sclient"
	"github.com/status/monitor"
	"github.com/status/notify"
	"github.com/status/storage"
	"github.com/status/web"
)

// needsK8s reports whether any service uses a k8s_* check type.
func needsK8s(svcs []config.Service) bool {
	for _, s := range svcs {
		switch s.Type {
		case config.CheckK8sAPIServer, config.CheckK8sAPILatency, config.CheckK8sNodes,
			config.CheckK8sDeployment, config.CheckK8sStatefulSet, config.CheckK8sDaemonSet,
			config.CheckK8sPodsCrash, config.CheckK8sPVC, config.CheckK8sEvents,
			config.CheckK8sHPA, config.CheckK8sCronJob:
			return true
		}
	}
	return false
}

func main() {
	// Parse command line flags
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	hashPW := flag.String("hash-password", "", "Print a bcrypt hash for the given password and exit (for auth.users[].password_hash)")
	genPW := flag.Bool("gen-password", false, "Generate a random password, print it with its bcrypt hash, and exit")
	flag.Parse()

	// Credential helpers: let an operator mint a hash without installing
	// htpasswd or writing a throwaway Go program.
	if *genPW {
		pw, err := randomPassword(24)
		if err != nil {
			log.Fatalf("generate password: %v", err)
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			log.Fatalf("hash password: %v", err)
		}
		fmt.Printf("password: %s\nhash:     %s\n", pw, hash)
		return
	}
	if *hashPW != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(*hashPW), bcrypt.DefaultCost)
		if err != nil {
			log.Fatalf("hash password: %v", err)
		}
		fmt.Printf("%s\n", hash)
		return
	}

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Printf("Warning: Could not load config file: %v", err)
		log.Println("Using default configuration with sample services...")
		cfg = config.DefaultConfig()
		// Add sample services for demo
		cfg.Services = []config.Service{
			{
				Name:           "API Server",
				Group:          "Core Services",
				Type:           config.CheckHTTP,
				URL:            "https://api.github.com",
				Method:         "GET",
				Interval:       30 * time.Second,
				Timeout:        10 * time.Second,
				ExpectedStatus: 200,
				Description:    "Main API endpoint",
			},
			{
				Name:           "Website",
				Group:          "Core Services",
				Type:           config.CheckHTTP,
				URL:            "https://github.com",
				Method:         "GET",
				Interval:       30 * time.Second,
				Timeout:        10 * time.Second,
				ExpectedStatus: 200,
				Description:    "Public website",
			},
			{
				Name:        "Database",
				Group:       "Infrastructure",
				Type:        config.CheckTCP,
				Host:        "github.com",
				Port:        443,
				Interval:    30 * time.Second,
				Timeout:     5 * time.Second,
				Description: "Primary database cluster",
			},
			{
				Name:          "DNS",
				Group:         "Infrastructure",
				Type:          config.CheckDNS,
				Host:          "github.com",
				DNSRecordType: "A",
				DNSResolver:   "8.8.8.8:53",
				Interval:      60 * time.Second,
				Timeout:       5 * time.Second,
				Description:   "DNS resolution",
			},
			{
				Name:           "CDN",
				Group:          "Edge Services",
				Type:           config.CheckHTTP,
				URL:            "https://cdn.jsdelivr.net",
				Method:         "GET",
				Interval:       60 * time.Second,
				Timeout:        10 * time.Second,
				ExpectedStatus: 200,
				Description:    "Content delivery network",
			},
			{
				Name:        "UDP DNS",
				Group:       "Infrastructure",
				Type:        config.CheckUDP,
				Host:        "8.8.8.8",
				Port:        53,
				Interval:    60 * time.Second,
				Timeout:     5 * time.Second,
				Description: "Google DNS (UDP)",
			},
			{
				Name:        "QUIC Server",
				Group:       "Edge Services",
				Type:        config.CheckQUIC,
				URL:         "https://www.google.com",
				Interval:    60 * time.Second,
				Timeout:     5 * time.Second,
				Description: "HTTP/3 QUIC endpoint",
			},
		}
	}

	// Print startup banner
	printBanner()

	// Initialize storage
	store, err := storage.NewStorage(cfg.Storage.DataDir)
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	log.Printf("Storage initialized at: %s", cfg.Storage.DataDir)

	// Initialize notifier with webhooks
	var webhookConfigs []notify.WebhookConfig
	for _, wh := range cfg.Webhooks {
		webhookConfigs = append(webhookConfigs, notify.WebhookConfig{
			ID:      wh.ID,
			Name:    wh.Name,
			URL:     wh.URL,
			Type:    wh.Type,
			Events:  wh.Events,
			Headers: wh.Headers,
			Enabled: wh.Enabled,
		})
	}
	notifier := notify.NewNotifier(webhookConfigs)
	log.Printf("Webhooks configured: %d", len(webhookConfigs))

	// Web Push (browser + phone). Keys are generated and persisted on first
	// boot when none are supplied, so enabling push needs no manual setup.
	pushMgr, err := notify.NewPushManager(store, cfg.Alerts.Push)
	if err != nil {
		log.Printf("web push disabled: %v", err)
	}
	notifier.SetPushManager(pushMgr)
	if pushMgr.Enabled() {
		log.Printf("Web push enabled (%d subscriptions, subject=%s)", pushMgr.Count(), cfg.Alerts.Push.Subject)
	}

	// ntfy: phone alerts without an app store account or FCM credentials.
	ntfySender, err := notify.NewNtfySender(cfg.Alerts.Ntfy)
	if err != nil {
		log.Printf("ntfy disabled: %v", err)
	}
	notifier.SetNtfySender(ntfySender)
	if ntfySender.Enabled() {
		log.Printf("ntfy enabled (server=%s)", cfg.Alerts.Ntfy.ServerURL)
	}
	if cfg.Alerts.Enabled {
		log.Printf("Service alerts on: threshold=%d cooldown=%s repeat=%s",
			cfg.Alerts.FailureThreshold, cfg.Alerts.Cooldown, cfg.Alerts.RepeatEvery)
	}

	// Initialize k8s client + informers when any k8s_* probe is configured OR
	// when auto-discovery is requested. Auto-discovery is enabled by default
	// once the in-cluster client is reachable; disable via env STATUS_DISABLE_AUTODISCOVERY=1.
	var kc *k8sclient.Client
	wantK8s := needsK8s(cfg.Services) || os.Getenv("STATUS_DISABLE_AUTODISCOVERY") != "1"
	if wantK8s {
		log.Println("initializing in-cluster k8s client + informers")
		kctx, kcancel := context.WithCancel(context.Background())
		_ = kcancel // kept alive for the process lifetime
		c, err := k8sclient.New(kctx, 10*time.Minute)
		if err != nil {
			log.Printf("k8s client init failed: %v (k8s_* probes + auto-discovery disabled)", err)
		} else {
			kc = c
			log.Println("k8s informers synced")
		}
	}

	// Reserved names = svcs defined in config.yaml. Auto-discovery never
	// overrides these and the hot-reload watcher ignores annotation edits on
	// services that happen to share a name with a yaml entry.
	reserved := map[string]bool{}
	for _, s := range cfg.Services {
		reserved[s.Name] = true
	}

	// Auto-discover services annotated with status.invinsense.dev/probe.
	if kc != nil && os.Getenv("STATUS_DISABLE_AUTODISCOVERY") != "1" {
		discovered, err := kc.DiscoverServices()
		if err != nil {
			log.Printf("auto-discovery failed: %v", err)
		} else {
			added := 0
			for _, s := range discovered {
				if reserved[s.Name] {
					continue
				}
				cfg.Services = append(cfg.Services, s)
				added++
			}
			log.Printf("auto-discovered %d additional services from annotations", added)
		}
	}

	// Per-application probes: one for every Deployment / StatefulSet /
	// DaemonSet in scope. Generated from the cluster rather than listed by
	// hand, because a hand-written list of 48 workloads is wrong the next day.
	var workloadOpts k8sclient.WorkloadDiscoveryOptions
	if kc != nil && cfg.Cluster.AutoWorkloads.Enabled {
		aw := cfg.Cluster.AutoWorkloads
		workloadOpts = k8sclient.WorkloadDiscoveryOptions{
			Namespaces:        aw.Namespaces,
			ExcludeNamespaces: aw.ExcludeNamespaces,
			Kinds:             aw.Kinds,
			Interval:          aw.Interval,
			GroupPrefix:       aw.GroupPrefix,
			MaxProbes:         aw.MaxProbes,
		}
		workloads, err := kc.DiscoverWorkloads(workloadOpts)
		if err != nil {
			log.Printf("workload discovery failed: %v", err)
		} else {
			added := 0
			for _, w := range workloads {
				if reserved[w.Name] {
					continue
				}
				cfg.Services = append(cfg.Services, w)
				added++
			}
			log.Printf("workload discovery: %d application probes generated", added)
		}
	}

	// Create monitor with storage for persistence
	mon := monitor.NewMonitor(cfg.Services, store)
	if kc != nil {
		mon.SetK8sClient(kc)
	}

	// Start monitoring
	log.Printf("Starting health monitors for %d services...", len(cfg.Services))
	mon.Start()

	// Hot-reload: watch Service add/update/delete so probes appear in seconds.
	if kc != nil && os.Getenv("STATUS_DISABLE_AUTODISCOVERY") != "1" {
		if err := kc.WatchServices(context.Background(), mon, reserved); err != nil {
			log.Printf("autodisc watcher failed to install: %v", err)
		} else {
			log.Println("autodisc watcher installed — annotations take effect at runtime")
		}
	}

	// Keep application probes in step: a new Deployment becomes a probe within
	// seconds, and a deleted one stops being reported as down forever.
	if kc != nil && cfg.Cluster.AutoWorkloads.Enabled {
		if err := kc.WatchWorkloads(context.Background(), mon, reserved, workloadOpts); err != nil {
			log.Printf("workload watcher failed to install: %v", err)
		} else {
			log.Println("workload watcher installed — new applications appear automatically")
		}
	}

	// Create and start web server
	server := web.NewServer(cfg, mon, store, notifier)

	// Handle graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := server.Start(); err != nil {
			log.Printf("Server error: %v", err)
			done <- syscall.SIGTERM
		}
	}()

	log.Printf("Status page is running at %s", cfg.BaseURL)
	log.Println("")
	log.Println("Available endpoints:")
	log.Println("  GET  /                    - Status page")
	log.Println("  GET  /api/summary         - Summary (Cloudflare-style)")
	log.Println("  GET  /api/status          - All service statuses")
	log.Println("  GET  /api/components      - Component list")
	log.Println("  GET  /api/incidents       - Incident list")
	log.Println("  POST /api/incidents       - Create incident (requires API key)")
	log.Println("  GET  /api/maintenance     - Scheduled maintenance")
	log.Println("  GET  /api/history         - 90-day history")
	log.Println("  GET  /api/metrics         - System metrics")
	log.Println("  GET  /api/cluster         - Kubernetes cluster snapshot")
	log.Println("  GET  /api/push/key        - VAPID public key")
	log.Println("  POST /api/push/subscribe  - Register for push alerts")
	log.Println("  GET  /api/notifications   - Notification channel status")
	log.Println("  POST /api/notifications/test - Fire a test alert (requires API key)")
	log.Println("  GET  /feed/rss            - RSS feed")
	log.Println("  GET  /feed/atom           - Atom feed")
	log.Println("  GET  /feed/json           - JSON feed")
	log.Println("  WS   /ws                  - WebSocket updates")
	log.Println("")
	if cfg.API.Key != "" {
		log.Printf("API Key configured for admin endpoints")
	} else {
		log.Printf("WARNING: No API key configured. Admin endpoints are open.")
	}
	log.Println("")
	log.Println("Press Ctrl+C to stop")

	// Wait for shutdown signal
	<-done
	log.Println("Shutting down...")

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mon.Stop()
	if err := server.Stop(ctx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}

	// Close storage
	if err := store.Close(); err != nil {
		log.Printf("Storage close error: %v", err)
	}

	log.Println("Server stopped")
}

func printBanner() {
	banner := `
╔═══════════════════════════════════════════════════════════════════════════════╗
║                                                                               ║
║   ███████╗████████╗ █████╗ ████████╗██╗   ██╗███████╗                         ║
║   ██╔════╝╚══██╔══╝██╔══██╗╚══██╔══╝██║   ██║██╔════╝                         ║
║   ███████╗   ██║   ███████║   ██║   ██║   ██║███████╗                         ║
║   ╚════██║   ██║   ██╔══██║   ██║   ██║   ██║╚════██║                         ║
║   ███████║   ██║   ██║  ██║   ██║   ╚██████╔╝███████║                         ║
║   ╚══════╝   ╚═╝   ╚═╝  ╚═╝   ╚═╝    ╚═════╝ ╚══════╝                         ║
║                                                                               ║
║   Enterprise-Ready Status Page                                                ║
║   Real-time monitoring • RSS/Atom/JSON feeds • Webhooks • Incident Management ║
║                                                                               ║
╚═══════════════════════════════════════════════════════════════════════════════╝
`
	log.Println(banner)
}

// randomPassword returns a URL-safe password drawn from crypto/rand.
// The alphabet omits characters that are easy to confuse when read aloud or
// copied by hand (0/O, 1/l/I) — a credential that gets mistyped gets weakened
// by whoever "simplifies" it.
func randomPassword(n int) (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789-_"
	out := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range out {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = alphabet[idx.Int64()]
	}
	return string(out), nil
}
