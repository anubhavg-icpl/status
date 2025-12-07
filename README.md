# Status Monitor

A beautiful, real-time status page written in Go. Better than Cloudflare's status page.

## Features

- **Real-time Monitoring**: Automatic health checks with configurable intervals
- **Beautiful Dark Mode UI**: Glassmorphism design with smooth animations
- **WebSocket Updates**: Live status updates without page refresh
- **Response Time Charts**: Visual history of response times with Chart.js
- **Uptime Tracking**: 90-point history with visual uptime bars
- **Service Grouping**: Organize services into logical groups
- **Incident Management**: Track and display past/ongoing incidents
- **Single Binary**: All assets embedded, just one file to deploy
- **YAML Configuration**: Easy to configure via YAML file

## Quick Start

```bash
# Build
go build -o status .

# Run with default demo services
./status

# Run with custom config
./status -config config.yaml
```

Open http://localhost:8080 in your browser.

## Configuration

Create a `config.yaml` file:

```yaml
title: "My Status Page"
description: "Real-time system monitoring"

theme:
  primary_color: "#3B82F6"
  accent_color: "#10B981"
  dark_mode: true

server:
  port: 8080
  read_timeout: 15s
  write_timeout: 15s

services:
  - name: "API Server"
    group: "Core Services"
    url: "https://api.example.com/health"
    method: GET
    interval: 30s
    timeout: 10s
    expected_status: 200
    description: "Main API endpoint"

  - name: "Database"
    group: "Infrastructure"
    url: "https://db.example.com:5432"
    interval: 15s
    timeout: 5s

incidents:
  - id: "inc-001"
    title: "API Latency Issues"
    description: "Investigating slow responses"
    status: "investigating"
    severity: "minor"
    created_at: "2024-01-15T10:30:00Z"
```

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | Main status page |
| `/api/status` | GET | All service statuses |
| `/api/status/{name}` | GET | Single service status |
| `/api/incidents` | GET | All incidents |
| `/api/metrics` | GET | System-wide metrics |
| `/ws` | WebSocket | Real-time updates |

## Service Configuration Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `name` | string | required | Service display name |
| `group` | string | "Services" | Group name for organization |
| `url` | string | required | URL to monitor |
| `method` | string | "GET" | HTTP method |
| `interval` | duration | 30s | Check interval |
| `timeout` | duration | 10s | Request timeout |
| `expected_status` | int | 200 | Expected HTTP status code |
| `headers` | map | {} | Custom HTTP headers |
| `description` | string | "" | Service description |

## Status Types

- **Operational**: Service responding with expected status, response time < 2s
- **Degraded**: Service responding but slow (2-5s) or unexpected status
- **Down**: Service not responding or error occurred

## Project Structure

```
.
├── main.go              # Entry point
├── config/
│   └── config.go        # Configuration parsing
├── monitor/
│   └── monitor.go       # Health check monitoring
├── web/
│   ├── server.go        # HTTP server & API
│   ├── templates/
│   │   └── index.html   # Main UI template
│   └── static/
│       └── .gitkeep
├── config.yaml          # Sample configuration
└── go.mod
```

## License

MIT
