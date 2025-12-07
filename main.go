package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/status/config"
	"github.com/status/monitor"
	"github.com/status/notify"
	"github.com/status/storage"
	"github.com/status/web"
)

func main() {
	// Parse command line flags
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	flag.Parse()

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
				Name:           "Database",
				Group:          "Infrastructure",
				Type:           config.CheckTCP,
				Host:           "github.com",
				Port:           443,
				Interval:       30 * time.Second,
				Timeout:        5 * time.Second,
				Description:    "Primary database cluster",
			},
			{
				Name:           "DNS",
				Group:          "Infrastructure",
				Type:           config.CheckDNS,
				Host:           "github.com",
				DNSRecordType:  "A",
				DNSResolver:    "8.8.8.8:53",
				Interval:       60 * time.Second,
				Timeout:        5 * time.Second,
				Description:    "DNS resolution",
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

	// Create monitor with storage for persistence
	mon := monitor.NewMonitor(cfg.Services, store)

	// Start monitoring
	log.Printf("Starting health monitors for %d services...", len(cfg.Services))
	mon.Start()

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
