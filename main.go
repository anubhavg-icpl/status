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
				Name:        "API Server",
				Group:       "Core Services",
				URL:         "https://httpstat.us/200",
				Method:      "GET",
				Interval:    30 * time.Second,
				Timeout:     10 * time.Second,
				ExpectedStatus: 200,
				Description: "Main API endpoint",
			},
			{
				Name:        "Website",
				Group:       "Core Services",
				URL:         "https://example.com",
				Method:      "GET",
				Interval:    30 * time.Second,
				Timeout:     10 * time.Second,
				ExpectedStatus: 200,
				Description: "Public website",
			},
			{
				Name:        "Database",
				Group:       "Infrastructure",
				URL:         "https://httpstat.us/200",
				Method:      "GET",
				Interval:    15 * time.Second,
				Timeout:     5 * time.Second,
				ExpectedStatus: 200,
				Description: "Primary database cluster",
			},
			{
				Name:        "Cache Server",
				Group:       "Infrastructure",
				URL:         "https://httpstat.us/200?sleep=100",
				Method:      "GET",
				Interval:    20 * time.Second,
				Timeout:     5 * time.Second,
				ExpectedStatus: 200,
				Description: "Redis cache layer",
			},
			{
				Name:        "CDN",
				Group:       "Edge Services",
				URL:         "https://httpstat.us/200",
				Method:      "GET",
				Interval:    30 * time.Second,
				Timeout:     10 * time.Second,
				ExpectedStatus: 200,
				Description: "Content delivery network",
			},
		}
	}

	// Print startup banner
	printBanner()

	// Create monitor
	mon := monitor.NewMonitor(cfg.Services)

	// Start monitoring
	log.Println("Starting health monitors...")
	mon.Start()

	// Create and start web server
	server := web.NewServer(cfg, mon)

	// Handle graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := server.Start(); err != nil {
			log.Printf("Server error: %v", err)
			done <- syscall.SIGTERM
		}
	}()

	log.Printf("Status page is running at http://localhost:%d", cfg.Server.Port)
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

	log.Println("Server stopped")
}

func printBanner() {
	banner := `
╔═══════════════════════════════════════════════════════════════╗
║                                                               ║
║   ███████╗████████╗ █████╗ ████████╗██╗   ██╗███████╗         ║
║   ██╔════╝╚══██╔══╝██╔══██╗╚══██╔══╝██║   ██║██╔════╝         ║
║   ███████╗   ██║   ███████║   ██║   ██║   ██║███████╗         ║
║   ╚════██║   ██║   ██╔══██║   ██║   ██║   ██║╚════██║         ║
║   ███████║   ██║   ██║  ██║   ██║   ╚██████╔╝███████║         ║
║   ╚══════╝   ╚═╝   ╚═╝  ╚═╝   ╚═╝    ╚═════╝ ╚══════╝         ║
║                                                               ║
║   Real-time System Status & Uptime Monitoring                 ║
║                                                               ║
╚═══════════════════════════════════════════════════════════════╝
`
	log.Println(banner)
}
