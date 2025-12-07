<div align="center">

<img src="web/static/logo.svg" alt="Status" width="360">

<br>
<br>

[![Go Version](https://img.shields.io/badge/Go-1.23+-00ADD8?style=flat&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat&logo=docker)](Containerfile)

**Enterprise-grade status page with multi-protocol health monitoring**

[Features](#features) • [Quick Start](#quick-start) • [Configuration](#configuration) • [API](#api) • [Docker](#docker)

</div>

---

## Features

### Multi-Protocol Monitoring

| Protocol | Description |
|----------|-------------|
| **HTTP/HTTPS** | Web endpoints with status codes, headers, body validation |
| **TCP** | Port connectivity checks |
| **UDP** | UDP service checks |
| **ICMP** | Ping/latency monitoring |
| **DNS** | Resolution checks (A, AAAA, MX, TXT, CNAME, NS) |
| **TLS** | SSL certificate expiry monitoring |
| **SMTP** | Email server (25/465/587) |
| **SSH** | SSH server banner check |
| **POP3/IMAP** | Mail server checks |
| **FTP** | FTP server availability |
| **NTP** | Time synchronization |
| **LDAP** | Directory server |
| **Redis** | PING/PONG check |
| **MongoDB** | Connectivity check |
| **MySQL** | Server handshake |
| **PostgreSQL** | Connectivity check |
| **gRPC** | gRPC endpoint |
| **QUIC** | HTTP/3 QUIC protocol |
| **WebSocket** | WebSocket connectivity |

### Core Features

- **Real-Time Updates** — WebSocket-powered live status dashboard
- **Beautiful Dark Mode UI** — Glassmorphism design with smooth animations
- **RSS/Atom/JSON Feeds** — Subscribe via your preferred format
- **Incident Management** — Create, update, resolve incidents via API
- **Scheduled Maintenance** — Plan and communicate maintenance windows
- **Webhook Notifications** — Slack, Discord, MS Teams, PagerDuty, Opsgenie
- **Multiple Auth Methods** — API Key, Bearer Token, Basic Auth, IP Whitelist
- **90-Day History** — Track uptime and response times
- **BoltDB Storage** — Persistent data with no external dependencies
- **Single Binary** — No dependencies, just download and run

---

## Quick Start

### Build from Source

```bash
git clone https://github.com/anubhavg-icpl/status.git
cd status
go build -o status .
./status
```

Open http://localhost:8080

### Docker / Podman

```bash
# Build
podman build -t status -f Containerfile .

# Run
podman run -d -p 8080:8080 \
  -v ./config.yaml:/config.yaml:ro \
  -v ./data:/data \
  status
```

---

## Configuration

Create a `config.yaml` file:

```yaml
title: "System Status"
description: "Real-time system status monitoring"
base_url: "https://status.example.com"

theme:
  primary_color: "#3B82F6"
  accent_color: "#10B981"
  dark_mode: true

server:
  port: 8080

api:
  enabled: true
  key: "your-secret-api-key"

services:
  # HTTP Check
  - name: "API Server"
    type: http
    group: "Core Services"
    url: "https://api.example.com/health"
    interval: 30s
    timeout: 10s
    expected_status: 200

  # TLS Certificate Check
  - name: "SSL Certificate"
    type: tls
    group: "Security"
    host: "example.com"
    port: 443
    interval: 1h
    tls_warn_days: 30

  # TCP Port Check
  - name: "Database"
    type: tcp
    group: "Infrastructure"
    host: "db.example.com"
    port: 5432
    interval: 30s

  # DNS Check
  - name: "DNS"
    type: dns
    group: "Infrastructure"
    host: "example.com"
    dns_record_type: A
    interval: 60s

  # ICMP Ping
  - name: "Gateway"
    type: icmp
    group: "Network"
    host: "10.0.0.1"
    interval: 30s

  # Redis Check
  - name: "Redis Cache"
    type: redis
    group: "Databases"
    host: "localhost"
    port: 6379
    interval: 30s

webhooks:
  - id: "slack"
    name: "Slack Alerts"
    url: "https://hooks.slack.com/services/..."
    type: "slack"
    events: ["incident.created", "incident.resolved"]
    enabled: true
```

---

## API

### Public Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/summary` | Cloudflare-style status summary |
| `GET` | `/api/status` | All service statuses |
| `GET` | `/api/components` | Component list |
| `GET` | `/api/incidents` | Incident list |
| `GET` | `/api/history` | 90-day history |
| `GET` | `/feed/rss` | RSS 2.0 feed |
| `GET` | `/feed/atom` | Atom 1.0 feed |
| `GET` | `/feed/json` | JSON Feed 1.1 |
| `WS` | `/ws` | Real-time updates |

### Authenticated Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/api/incidents` | Create incident |
| `PUT` | `/api/incidents/:id` | Update incident |
| `DELETE` | `/api/incidents/:id` | Delete incident |

### Authentication

```bash
# API Key
curl -H "X-API-Key: your-key" https://status.example.com/api/incidents

# Bearer Token
curl -H "Authorization: Bearer token" https://status.example.com/api/incidents

# Basic Auth
curl -u admin:password https://status.example.com/api/incidents
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

---

## Docker

### Multi-Stage Build (Scratch)

The included `Containerfile` produces a minimal ~15MB image:

```dockerfile
# Build stage
FROM golang:1.23-alpine AS builder
# ... builds static binary

# Production stage
FROM scratch
COPY --from=builder /build/status /status
ENTRYPOINT ["/status"]
```

### Build & Run

```bash
# Build image
podman build -t status -f Containerfile .

# Run with config
podman run -d \
  --name status \
  -p 8080:8080 \
  -v ./config.yaml:/config.yaml:ro \
  -v status-data:/data \
  status
```

---

## Webhooks

| Platform | Type | Features |
|----------|------|----------|
| **Slack** | `slack` | Rich attachments |
| **Discord** | `discord` | Embedded messages |
| **MS Teams** | `teams` | MessageCard format |
| **PagerDuty** | `pagerduty` | Events API v2 |
| **Opsgenie** | `opsgenie` | Priority mapping |
| **Generic** | `generic` | Custom JSON |

### Events

- `incident.created` — New incident
- `incident.updated` — Status changed
- `incident.resolved` — Incident resolved
- `maintenance.scheduled` — Maintenance planned
- `*` — All events

---

## Project Structure

```
├── main.go              # Entry point
├── config/config.go     # Configuration & types
├── monitor/monitor.go   # Multi-protocol health checks
├── storage/storage.go   # BoltDB persistence
├── feeds/feeds.go       # RSS/Atom/JSON feeds
├── notify/notify.go     # Webhook notifications
├── web/
│   ├── server.go        # HTTP server & API
│   └── templates/       # UI templates
├── Containerfile        # Multi-stage Docker build
└── config.yaml          # Configuration
```

---

## License

MIT License

---

<div align="center">

Built with Go

</div>
