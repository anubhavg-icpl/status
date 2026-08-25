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
- **Kubernetes Cluster View** — Nodes, workloads, namespaces, problem pods, events and storage
- **Cluster Error Detection** — Every namespace watched on a timer; failures that persist raise alerts
- **Phone Alerts** — Web Push (VAPID) to browser and phone, plus ntfy with voice-call escalation
- **Webhook Notifications** — Slack, Discord, MS Teams, PagerDuty, Opsgenie
- **Multiple Auth Methods** — API Key, Bearer Token, Basic Auth, IP Whitelist
- **90-Day History** — Track uptime and response times
- **BoltDB Storage** — Persistent data with no external dependencies
- **Single Binary** — No dependencies, just download and run

---

## Kubernetes

Running in-cluster unlocks three things beyond the protocol probes.

### 1. Cluster view

`/api/cluster` returns a whole-cluster snapshot read from client-go informer
caches — no API round-trips per request — and the status page renders it as
Issues / Nodes / Workloads / Namespaces / Events tabs.

Covers: node Ready + pressure + schedulability, per-node CPU/memory (live usage
via metrics-server when installed, requests otherwise), pod phases and problem
reasons, Deployment/StatefulSet/DaemonSet readiness, namespace rollups, PVC
binding, and Warning events from the last 15 minutes.

The section hides itself automatically outside a cluster. `/api/cluster`
requires the API key unless you set `cluster.public: true` — node names, images
and namespaces are internal topology.

### 2. Error detection across every namespace

A watcher reconciles the cluster every `alerts.cluster.interval` and diffs the
failure set against the previous pass. Each failure gets a stable key, so it is
alerted once rather than every tick, and a start time, so the page can show
"firing for 22m".

| Failure | Severity |
|---------|----------|
| Node `NotReady` | critical |
| Workload with 0 ready replicas | critical |
| PVC `Lost` | critical |
| Node pressure / cordoned | major |
| Workload with some replicas unavailable | major |
| `CrashLoopBackOff`, `ImagePullBackOff`, `Unschedulable`, `OOMKilled` | major |
| PVC `Pending` | major |
| Container not ready > 5m | minor |

`min_duration` (default `2m`) is what separates a rolling deploy from an
outage: a pod crashing for 20s during a rollout never pages anyone, the same pod
still crashing two minutes later does. `min_severity` (default `major`) controls
what reaches a phone — everything is still shown on the page.

### 3. Service auto-discovery

Annotate a Service and it becomes a probe, with no config file edit and no
restart:

```yaml
metadata:
  annotations:
    status.invinsense.dev/probe: "true"
    status.invinsense.dev/type: "http"
    status.invinsense.dev/path: "/health"
    status.invinsense.dev/group: "Core"
```

### Deploy

```bash
kubectl apply -f k8s/rbac.yaml      # read-only ClusterRole + binding
kubectl apply -f k8s/ntfy.yaml      # self-hosted ntfy (phone alerts)
kubectl apply -f k8s/pvc.yaml
kubectl apply -f k8s/status.yaml    # includes the status-alerts Secret stub
```

`k8s/deployment.yaml` holds the Devtron/Helm values for the same workload.
`ConfigSecrets` there pulls credentials from an external `status-alerts-env`
Secret whose keys are the `STATUS_*` names.

> **One replica, deliberately.** State lives in BoltDB at `/data/status.db` on a
> ReadWriteOnce PVC, and bolt holds an exclusive lock on that file — a second
> replica fails to open it and crashes on boot. That database holds check
> history, incidents, the VAPID key pair and every push subscription, so it is
> not disposable. The Helm values pin `replicaCount: 1`, disable the HPA, and
> set `MaxSurge: 0` so a rolling update releases the volume before the new pod
> claims it. Redis (`k8s/redis.yaml`) covers cross-pod rate limiting and pub/sub
> but does not make the BoltDB file shareable.

ntfy also ships a third-party HelmForge chart. `k8s/ntfy.yaml` is the aligned
option here: same namespace, same manifest style as `k8s/redis.yaml`, pinned
image, `deny-all` auth, and a `status.invinsense.dev/probe` annotation so the
status page monitors its own alert transport — if ntfy is down, the page says
so instead of silently failing to page anyone.

---

## Alerting

Three delivery channels, all fed by the same events.

| Channel | Reaches | Setup |
|---------|---------|-------|
| **Web Push** | Browser + phone, via VAPID | Click "Alert me" on the status page |
| **ntfy** | Phone, with voice-call escalation | Subscribe to the topic in the ntfy app |
| **Webhooks** | Slack, Discord, Teams, PagerDuty, Opsgenie | `webhooks:` in config.yaml |

### Web Push

Enable `alerts.push` and the server mints a VAPID key pair on first boot,
persisting it to BoltDB so existing browser subscriptions survive restarts.
Visitors opt in with the bell button; the service worker at `/sw.js` shows the
notification and focuses the page when tapped. Critical alerts vibrate and stay
on screen until dismissed.

Supply `STATUS_VAPID_PUBLIC_KEY` / `STATUS_VAPID_PRIVATE_KEY` only if you need
the same key across a rebuild — changing the public key invalidates every
existing subscription.

### ntfy

`k8s/ntfy.yaml` deploys ntfy in-cluster with `auth-default-access: deny-all`.
Alert payloads name your namespaces and workloads; on the public ntfy.sh server
the topic name is the only thing protecting them.

```yaml
alerts:
  ntfy:
    enabled: true
    server_url: "http://ntfy.invinsense.svc.cluster.local"
    topic: "invinsense-alerts"
    token: "tk_..."          # or STATUS_NTFY_TOKEN
    priority: high
    critical_priority: max   # bypasses Do Not Disturb on most phones
    call: "+15551234567"     # voice call on critical (ntfy.sh Pro)
```

### Noise control

| Setting | Purpose |
|---------|---------|
| `failure_threshold` | Consecutive failing checks before the first alert |
| `cooldown` | Minimum gap between two alerts for the same service |
| `repeat_every` | Re-alert while still down; `0` = transitions only |
| `only_groups` | Restrict alerting to specific service groups |
| `cluster.min_duration` | How long a cluster failure must persist |
| `cluster.min_severity` | Severity floor for cluster alerts |

Recoveries always alert, cooldown or not — an operator waiting on an outage
needs the all-clear immediately.

Confirm the whole chain before you need it:

```bash
curl -X POST -H "X-API-Key: your-key" https://status.example.com/api/notifications/test
```

### Secrets

Environment always wins over `config.yaml`, so credentials can come from a
Kubernetes Secret:

`STATUS_API_KEY` · `STATUS_BEARER_TOKEN` · `STATUS_BASE_URL` ·
`STATUS_NTFY_ENABLED` · `STATUS_NTFY_SERVER` · `STATUS_NTFY_TOPIC` ·
`STATUS_NTFY_TOKEN` · `STATUS_NTFY_USERNAME` · `STATUS_NTFY_PASSWORD` ·
`STATUS_NTFY_CALL` · `STATUS_NTFY_EMAIL` · `STATUS_PUSH_ENABLED` ·
`STATUS_PUSH_SUBJECT` · `STATUS_VAPID_PUBLIC_KEY` · `STATUS_VAPID_PRIVATE_KEY` ·
`STATUS_ALERTS_ENABLED` · `STATUS_CLUSTER_ENABLED` · `STATUS_CLUSTER_PUBLIC` ·
`STATUS_CLUSTER_ALERTS_ENABLED` · `STATUS_CLUSTER_MIN_SEVERITY` ·
`STATUS_REDIS_PASSWORD`

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
| `GET` | `/api/notifications` | Which alert channels are live |
| `GET` | `/api/push/key` | VAPID public key |
| `POST` | `/api/push/subscribe` | Register a browser for push |
| `POST` | `/api/push/unsubscribe` | Drop a browser endpoint |
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
| `GET` | `/api/cluster` | Kubernetes snapshot + firing issues |
| `POST` | `/api/notifications/test` | Fire a test alert on every channel |

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
- `service.down` — A monitored service went down
- `service.degraded` — A service is degraded
- `service.recovered` — A service came back
- `service` — Prefix match: every `service.*` event
- `*` — All events

Cluster failures arrive as `service.*` events too, with the group set to
`k8s/<namespace>`.

---

## Project Structure

```
├── main.go              # Entry point
├── config/config.go     # Configuration & types
├── monitor/monitor.go   # Multi-protocol health checks
├── storage/storage.go   # BoltDB persistence
├── feeds/feeds.go       # RSS/Atom/JSON feeds
├── notify/
│   ├── notify.go        # Webhook notifications
│   ├── dispatch.go      # Fan-out to every channel
│   ├── push.go          # Web Push (VAPID)
│   ├── ntfy.go          # Phone alerts via ntfy
│   └── alert.go         # Alert shapes + emoji shortcodes
├── k8sclient/
│   ├── client.go        # In-cluster client + informers
│   ├── cluster.go       # Whole-cluster snapshot
│   ├── issues.go        # Failure derivation + severity
│   └── discovery.go     # Annotation-based auto-discovery
├── web/
│   ├── server.go        # HTTP server & API
│   ├── cluster.go       # /api/cluster
│   ├── clusterwatch.go  # Cluster error detection
│   ├── alerts.go        # Service state-change alerts
│   ├── push.go          # Push registration endpoints
│   ├── static/sw.js     # Push service worker
│   └── templates/       # UI templates
├── k8s/                 # RBAC, status, ntfy, redis manifests
├── Containerfile        # Multi-stage Docker build
└── config.yaml          # Configuration
```

---

## License

MIT License — [Anubhav Gain](https://github.com/anubhavg-icpl)

---

<div align="center">

Built with ❤️ and Go by **Anubhav Gain**

</div>
