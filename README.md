<p align="center">
  <img src="web/static/logo.svg" alt="Status Logo" width="280">
</p>

<p align="center">
  <strong>Enterprise-Ready Status Page & Monitoring</strong>
</p>

<p align="center">
  <a href="#features">Features</a> •
  <a href="#quick-start">Quick Start</a> •
  <a href="#configuration">Configuration</a> •
  <a href="#api">API</a> •
  <a href="#webhooks">Webhooks</a>
</p>

---

## Features

- **Multi-Protocol Monitoring** - HTTP/HTTPS, TCP, ICMP (Ping), DNS, WebSocket, gRPC
- **Real-Time Updates** - WebSocket-powered live status updates
- **Beautiful Dark Mode UI** - Glassmorphism design with smooth animations
- **RSS/Atom/JSON Feeds** - Subscribe to status updates via your preferred format
- **Incident Management** - Create, update, and resolve incidents via API
- **Scheduled Maintenance** - Plan and communicate maintenance windows
- **Webhook Notifications** - Slack, Discord, MS Teams, PagerDuty, Opsgenie
- **Multiple Auth Methods** - API Key, Bearer Token, Basic Auth, IP Whitelist
- **90-Day History** - Track uptime and response times over time
- **Single Binary** - No dependencies, just download and run

## Quick Start

### Build from Source

```bash
# Clone the repository
git clone https://github.com/anubhavg-icpl/status.git
cd status

# Build
go build -o status .

# Run (uses config.yaml if present, or demo config)
./status

# Run with custom config
./status -config /path/to/config.yaml
```

Then open http://localhost:8080 in your browser.

## Configuration

Create a `config.yaml` file:

```yaml
title: "My Status Page"
description: "Real-time system status monitoring"
base_url: "https://status.example.com"

theme:
  primary_color: "#3B82F6"
  accent_color: "#10B981"
  dark_mode: true

server:
  port: 8080
  read_timeout: 15s
  write_timeout: 15s

# API Authentication (multiple methods supported)
api:
  enabled: true
  key: "your-secret-api-key"              # X-API-Key header
  # bearer_token: "your-bearer-token"     # Authorization: Bearer
  # basic_auth:
  #   enabled: true
  #   username: "admin"
  #   password: "password"
  # allowed_ips: ["127.0.0.1"]            # IP whitelist

services:
  # HTTP/HTTPS Check
  - name: "API Server"
    type: http
    group: "Core Services"
    url: "https://api.example.com/health"
    interval: 30s
    timeout: 10s
    expected_status: 200

  # TCP Port Check
  - name: "Database"
    type: tcp
    group: "Infrastructure"
    host: "db.example.com"
    port: 5432
    interval: 30s
    timeout: 5s

  # DNS Check
  - name: "DNS"
    type: dns
    group: "Infrastructure"
    host: "example.com"
    dns_record_type: A
    dns_resolver: "8.8.8.8:53"
    interval: 60s

  # ICMP Ping Check
  - name: "Gateway"
    type: icmp
    group: "Network"
    host: "10.0.0.1"
    interval: 30s

webhooks:
  - id: "slack"
    name: "Slack Alerts"
    url: "https://hooks.slack.com/services/..."
    type: "slack"
    events: ["incident.created", "incident.resolved"]
    enabled: true
```

## Check Types

| Type | Description | Required Fields |
|------|-------------|-----------------|
| `http` | HTTP/HTTPS endpoint | `url`, `expected_status` |
| `tcp` | TCP port connectivity | `host`, `port` |
| `icmp` | Ping/ICMP | `host` |
| `dns` | DNS resolution | `host`, `dns_record_type` |
| `websocket` | WebSocket connection | `url` |
| `grpc` | gRPC health check | `url`, `port` |

## API

### Public Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/summary` | Cloudflare-style status summary |
| GET | `/api/status` | All service statuses |
| GET | `/api/components` | Component list |
| GET | `/api/incidents` | Incident list |
| GET | `/api/maintenance` | Scheduled maintenance |
| GET | `/api/history` | 90-day history |
| GET | `/api/metrics` | System metrics |
| GET | `/feed/rss` | RSS 2.0 feed |
| GET | `/feed/atom` | Atom 1.0 feed |
| GET | `/feed/json` | JSON Feed 1.1 |
| WS | `/ws` | Real-time WebSocket updates |

### Authenticated Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/incidents` | Create incident |
| PUT | `/api/incidents/:id` | Update incident |
| DELETE | `/api/incidents/:id` | Delete incident |
| POST | `/api/maintenance` | Schedule maintenance |

### Authentication Methods

```bash
# X-API-Key header
curl -H "X-API-Key: your-key" https://status.example.com/api/incidents

# Bearer token
curl -H "Authorization: Bearer your-token" https://status.example.com/api/incidents

# Basic Auth
curl -u admin:password https://status.example.com/api/incidents

# Query parameter
curl "https://status.example.com/api/incidents?api_key=your-key"
```

### Create Incident

```bash
curl -X POST https://status.example.com/api/incidents \
  -H "X-API-Key: your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "title": "Database Connection Issues",
    "message": "Investigating elevated error rates",
    "status": "investigating",
    "severity": "major",
    "affected_services": ["Database", "API Server"]
  }'
```

## Webhooks

Supported platforms:

| Platform | Type | Features |
|----------|------|----------|
| **Slack** | `slack` | Rich attachments with colors |
| **Discord** | `discord` | Embedded messages |
| **MS Teams** | `teams` | MessageCard format |
| **PagerDuty** | `pagerduty` | Events API v2, auto-resolve |
| **Opsgenie** | `opsgenie` | Priority mapping (P1-P4) |
| **Generic** | `generic` | Custom JSON payload |

### Webhook Events

- `incident.created` - New incident reported
- `incident.updated` - Incident status changed
- `incident.resolved` - Incident resolved
- `maintenance.scheduled` - Maintenance scheduled
- `*` - All events

## Project Structure

```
.
├── main.go              # Entry point
├── config/
│   └── config.go        # Configuration & types
├── monitor/
│   └── monitor.go       # Multi-protocol health checks
├── storage/
│   └── storage.go       # Persistent data storage
├── feeds/
│   └── feeds.go         # RSS/Atom/JSON feed generation
├── notify/
│   └── notify.go        # Webhook notifications
├── web/
│   ├── server.go        # HTTP server & API
│   ├── templates/
│   │   └── index.html   # Main UI template
│   └── static/
│       ├── favicon.svg  # Favicon
│       └── logo.svg     # Logo
├── config.yaml          # Configuration file
└── go.mod
```

## License

MIT License

---

<p align="center">
  Built with Go
</p>
