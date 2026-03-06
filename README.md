# OpenSIEM Backend — Complete Operations Guide

This document covers everything needed to get the backend running, connect agents to it, query events, and keep it healthy in production. Read it top to bottom the first time. Use the section links to jump back later.

---

## Table of Contents

1. [How It All Fits Together](#1-how-it-all-fits-together)
2. [Prerequisites](#2-prerequisites)
3. [Repository Layout](#3-repository-layout)
4. [Step 1 — Clone and Prepare](#4-step-1--clone-and-prepare)
5. [Step 2 — Generate TLS Certificates](#5-step-2--generate-tls-certificates)
6. [Step 3 — Configure the Backend](#6-step-3--configure-the-backend)
7. [Step 4 — Start with Docker Compose](#7-step-4--start-with-docker-compose)
8. [Step 5 — Verify the Backend is Running](#8-step-5--verify-the-backend-is-running)
9. [Step 6 — Connect an Agent](#9-step-6--connect-an-agent)
10. [API Reference](#10-api-reference)
11. [Database Operations](#11-database-operations)
12. [Logs and Monitoring](#12-logs-and-monitoring)
13. [Configuration Reference](#13-configuration-reference)
14. [Switching to mTLS](#14-switching-to-mtls)
15. [Upgrading](#15-upgrading)
16. [Troubleshooting](#16-troubleshooting)

---

## 1. How It All Fits Together

```
Windows Host(s)                    Linux / Cloud Server
┌─────────────────────┐            ┌──────────────────────────────────┐
│                     │            │  Docker Compose                  │
│  agent.exe          │            │                                  │
│  (collects events)  │            │  ┌─────────────────────────┐    │
│                     │  HTTPS     │  │  opensiem-backend       │    │
│  agent.yaml:        │ ─────────► │  │  :8443                  │    │
│    backend_url:     │            │  │                         │    │
│    https://YOUR_IP  │            │  │  POST /api/v1/events    │    │
│    :8443            │            │  │  GET  /api/v1/events    │    │
│    api_key: xxxxx   │            │  │  GET  /api/v1/agents    │    │
│                     │            │  │  GET  /health           │    │
└─────────────────────┘            │  └──────────┬──────────────┘    │
                                   │             │ SQL                │
                                   │  ┌──────────▼──────────────┐    │
                                   │  │  timescaledb (postgres)  │    │
                                   │  │  :5432                   │    │
                                   │  │                          │    │
                                   │  │  events (hypertable)     │    │
                                   │  │  agents                  │    │
                                   │  └─────────────────────────┘    │
                                   └──────────────────────────────────┘
```

**What happens on first start:**

1. Docker Compose pulls TimescaleDB and builds the backend image
2. TimescaleDB starts and becomes healthy
3. The backend starts, connects to the database, and auto-runs SQL migrations
4. Tables `events`, `agents`, and `schema_migrations` are created automatically
5. The backend listens on `:8443` for HTTPS connections from agents

---

## 2. Prerequisites

### On the server (Linux / VPS / cloud VM)

| Requirement | Minimum version | Check |
|---|---|---|
| Docker | 24+ | `docker --version` |
| Docker Compose | v2 (plugin) | `docker compose version` |
| OpenSSL | Any recent | `openssl version` |
| Open port | TCP 8443 outbound from agents | firewall / security group |

**Install Docker on Ubuntu/Debian:**
```bash
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER
newgrp docker
docker compose version   # should print v2.x.x
```

**Install Docker on RHEL/CentOS:**
```bash
sudo dnf install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
sudo systemctl enable --now docker
```

### On the Windows host (for the agent)

- Windows 10 / Server 2016+ (64-bit)
- Administrator access
- Network access to the backend server on port 8443
- The built `agent.exe` binary

---

## 3. Repository Layout

After cloning, the backend lives in the `backend/` directory:

```
backend/
├── cmd/server/main.go              # Entry point
├── internal/
│   ├── api/                        # HTTP handlers
│   │   ├── server.go               # Routes, middleware, TLS setup
│   │   ├── ingest.go               # POST /api/v1/events
│   │   ├── query.go                # GET  /api/v1/events
│   │   ├── agents.go               # GET  /api/v1/agents
│   │   └── health.go               # GET  /health
│   ├── auth/
│   │   ├── apikey.go               # X-API-Key header validation
│   │   └── mtls.go                 # mTLS client cert validation
│   ├── store/
│   │   ├── db.go                   # Database connection pool
│   │   ├── events.go               # Write + query events table
│   │   ├── agents.go               # Upsert + list agents
│   │   └── migrations/
│   │       ├── 001_events.sql      # Creates events hypertable + indexes
│   │       ├── 002_agents.sql      # Creates agents table
│   │       └── migrate.go          # Migration runner (auto on startup)
│   └── config/config.go            # YAML config loader + validation
├── pkg/schema/event.go             # Shared Event and Batch types
├── configs/server.yaml             # Build-time config template
├── docker/
│   ├── Dockerfile                  # Multi-stage build (Go → Alpine)
│   ├── docker-compose.yml          # TimescaleDB + backend together
│   └── server.yaml                 # Runtime config (edit this one)
└── go.mod
```

> **Important:** There are two `server.yaml` files.
> - `configs/server.yaml` — the template baked into the Docker image. Do not edit for runtime use.
> - `docker/server.yaml` — mounted into the container at runtime. **This is the one you edit.**

---

## 4. Step 1 — Clone and Prepare

```bash
git clone https://github.com/honbles/Seim-Agent.git
cd Seim-Agent/backend/docker

# Create the certs directory that will be mounted into the container
mkdir -p certs
```

Your working directory for all remaining steps is `backend/docker/`.

---

## 5. Step 2 — Generate TLS Certificates

The backend requires HTTPS — it will refuse to start without a TLS certificate. You have two options.

---

### Option A — Self-signed certificate (fastest, dev/internal use)

Run all of this from `backend/docker/`:

```bash
# 1. Generate a private key
openssl genrsa -out certs/server.key 2048

# 2. Generate a self-signed certificate
#    Replace the -subj values and the IP/hostname with your actual server details
openssl req -new -x509 -days 825 \
  -key certs/server.key \
  -out certs/server.crt \
  -subj "/C=US/O=OpenSIEM/CN=opensiem-backend" \
  -addext "subjectAltName=IP:YOUR_SERVER_IP,DNS:your-server-hostname"

# Example with a real IP:
openssl req -new -x509 -days 825 \
  -key certs/server.key \
  -out certs/server.crt \
  -subj "/C=US/O=OpenSIEM/CN=opensiem-backend" \
  -addext "subjectAltName=IP:192.168.1.100,DNS:siem.local"
```

Verify the cert was created:
```bash
ls -la certs/
# certs/server.crt
# certs/server.key

openssl x509 -in certs/server.crt -noout -text | grep -E "Subject:|DNS:|IP:"
```

Because this cert is self-signed, the agent needs a copy of it to verify the backend. Copy `certs/server.crt` to your Windows host and set `ca_file` in the agent config — see [Step 6](#9-step-6--connect-an-agent).

---

### Option B — Signed by your own CA (recommended for production)

This lets you issue one CA cert that covers all agents and the backend.

```bash
cd backend/docker

# 1. Create a Certificate Authority (do once, keep ca.key secret)
openssl genrsa -out certs/ca.key 4096
openssl req -new -x509 -days 3650 \
  -key certs/ca.key \
  -out certs/ca.crt \
  -subj "/C=US/O=OpenSIEM/CN=OpenSIEM-CA"

# 2. Generate the backend server key and CSR
openssl genrsa -out certs/server.key 2048
openssl req -new \
  -key certs/server.key \
  -out certs/server.csr \
  -subj "/C=US/O=OpenSIEM/CN=your-server-hostname"

# 3. Sign the server cert with your CA
#    Replace IP and DNS with your actual server address
openssl x509 -req -days 825 \
  -in certs/server.csr \
  -CA certs/ca.crt \
  -CAkey certs/ca.key \
  -CAcreateserial \
  -out certs/server.crt \
  -extfile <(printf "subjectAltName=IP:YOUR_SERVER_IP,DNS:your-server-hostname")
```

With this setup, agents only need `ca.crt` — they verify the backend using the CA, not the server cert directly.

---

## 6. Step 3 — Configure the Backend

Edit `backend/docker/server.yaml`. This file is mounted into the running container — changes take effect after a restart.

**Minimum changes required before first start:**

### 1. Set a strong database password

```yaml
database:
  password: "changeme"    # ← replace this
```

Pick a strong password. Write it down — you also need it in `docker-compose.yml`.

### 2. Set the same password in docker-compose.yml

Open `backend/docker/docker-compose.yml` and update this line to match:

```yaml
environment:
  POSTGRES_PASSWORD: changeme   # ← must match database.password in server.yaml
```

### 3. Generate and set an API key

```bash
# Generate a strong random key
openssl rand -hex 32
# Example output: a3f8b2c1d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1
```

Paste it into `server.yaml`:

```yaml
auth:
  api_keys:
    - "a3f8b2c1d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1"
```

Save this key — you'll paste the same value into `agent.yaml` on every Windows host.

### Full `docker/server.yaml` after editing

```yaml
server:
  listen_addr: ":8443"
  tls_cert_file: "certs/server.crt"
  tls_key_file:  "certs/server.key"
  read_timeout:  "30s"
  write_timeout: "30s"
  max_batch_size: 1000

database:
  host:     "timescaledb"          # do not change — this is the Docker service name
  port:     5432
  name:     "opensiem"
  user:     "opensiem"
  password: "YourStrongPasswordHere"
  ssl_mode: "disable"
  max_open_conns:    25
  max_idle_conns:    10
  conn_max_lifetime: "5m"

auth:
  api_keys:
    - "a3f8b2c1d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1"
  mtls_enabled: false

log:
  level:  "info"
  format: "json"
```

---

## 7. Step 4 — Start with Docker Compose

All commands run from `backend/docker/`.

### Build and start everything

```bash
docker compose up -d --build
```

This will:
1. Pull `timescale/timescaledb:latest-pg16` from Docker Hub (~200 MB, once only)
2. Build the backend Go binary using the multi-stage Dockerfile (~400 MB build image, ~20 MB final)
3. Start `opensiem-db` (TimescaleDB)
4. Wait for the database healthcheck to pass
5. Start `opensiem-backend`
6. Run SQL migrations automatically on startup

First run takes 2–5 minutes depending on your connection speed. Subsequent starts are fast (images are cached).

### Check both containers are running

```bash
docker compose ps
```

Expected output:
```
NAME                 IMAGE                              STATUS
opensiem-backend     docker-opensiem-backend            Up (healthy)
opensiem-db          timescale/timescaledb:latest-pg16  Up (healthy)
```

Both must show `Up (healthy)`. If either shows `Up` without `(healthy)` wait 30 seconds and check again.

### Watch startup logs

```bash
# Both containers together
docker compose logs -f

# Backend only
docker compose logs -f backend

# Database only
docker compose logs -f timescaledb
```

A clean backend startup looks like this:
```json
{"time":"2024-01-15T13:00:01Z","level":"INFO","msg":"database connected","host":"timescaledb","db":"opensiem"}
{"time":"2024-01-15T13:00:01Z","level":"INFO","msg":"migration applied","file":"001_events.sql"}
{"time":"2024-01-15T13:00:01Z","level":"INFO","msg":"migration applied","file":"002_agents.sql"}
{"time":"2024-01-15T13:00:01Z","level":"INFO","msg":"server starting","addr":":8443","tls":true,"mtls":false}
```

If you see migration errors or database connection errors, check [Troubleshooting](#16-troubleshooting).

### Stop the stack

```bash
docker compose down          # stops containers, keeps database volume
docker compose down -v       # stops containers AND deletes all data (destructive!)
```

### Restart just the backend (after config changes)

```bash
docker compose restart backend
```

---

## 8. Step 5 — Verify the Backend is Running

### Health check

The `/health` endpoint is public (no auth required):

```bash
# From the server itself
curl -k https://localhost:8443/health

# From another machine
curl -k https://YOUR_SERVER_IP:8443/health
```

The `-k` flag skips certificate verification. For a self-signed cert this is expected.

Expected response:
```json
{
  "status": "ok",
  "time": "2024-01-15T13:00:05Z",
  "database": "ok"
}
```

If `database` shows an error, the backend cannot reach TimescaleDB. See [Troubleshooting](#16-troubleshooting).

### Test the ingest endpoint with curl

```bash
curl -k -s -X POST https://YOUR_SERVER_IP:8443/api/v1/events \
  -H "Content-Type: application/json" \
  -H "X-API-Key: YOUR_API_KEY" \
  -d '{
    "agent_id": "test-agent",
    "agent_version": "0.1.0",
    "sent_at": "2024-01-15T13:00:00Z",
    "events": [{
      "id": "abc123def456abc1",
      "time": "2024-01-15T13:00:00Z",
      "agent_id": "test-agent",
      "host": "TEST-HOST",
      "os": "windows",
      "event_type": "logon",
      "severity": 1,
      "source": "Security"
    }]
  }'
```

Expected response:
```json
{"accepted": 1, "agent_id": "test-agent"}
```

### Test the query endpoint

```bash
curl -k -s "https://YOUR_SERVER_IP:8443/api/v1/events?limit=5" \
  -H "X-API-Key: YOUR_API_KEY" | python3 -m json.tool
```

### Test the agents endpoint

```bash
curl -k -s "https://YOUR_SERVER_IP:8443/api/v1/agents" \
  -H "X-API-Key: YOUR_API_KEY" | python3 -m json.tool
```

Expected response (after the test POST above):
```json
{
  "agents": [
    {
      "id": "test-agent",
      "hostname": "TEST-HOST",
      "os": "windows",
      "version": "0.1.0",
      "first_seen": "2024-01-15T13:00:01Z",
      "last_seen": "2024-01-15T13:00:01Z",
      "last_ip": "172.18.0.1",
      "event_count": 1
    }
  ],
  "count": 1
}
```

---

## 9. Step 6 — Connect an Agent

On the Windows host, edit `agent.yaml`. Three fields must match the backend exactly.

### agent.yaml — required changes

```yaml
forwarder:
  # 1. Set to your backend server's IP or hostname
  backend_url: "https://192.168.1.100:8443"

  # 2. Paste the same API key you put in docker/server.yaml
  api_key: "a3f8b2c1d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1"

  # 3a. If you used a self-signed cert (Option A above):
  #     Copy certs/server.crt from the backend server to the Windows host
  #     and point ca_file at it so the agent can verify the backend
  ca_file: "certs/server.crt"

  # 3b. If you used a CA-signed cert (Option B above):
  #     Copy certs/ca.crt from the backend server to the Windows host
  ca_file: "certs/ca.crt"

  # Remove or comment out cert_file and key_file — not needed for API key auth
  # cert_file: ...
  # key_file: ...
```

### Copy the CA cert to the Windows host

From the backend server, copy the cert to the Windows machine. How you do this depends on your setup — SCP, a file share, or just pasting the content:

```bash
# From the backend server (Linux) — SCP to Windows
scp backend/docker/certs/server.crt Administrator@192.168.1.50:"C:/Program Files/OpenSIEM/Agent/certs/server.crt"

# Or if using CA-signed:
scp backend/docker/certs/ca.crt Administrator@192.168.1.50:"C:/Program Files/OpenSIEM/Agent/certs/ca.crt"
```

### Test the agent connection interactively

On the Windows host, open an elevated PowerShell and run:

```powershell
cd "C:\Program Files\OpenSIEM\Agent"
.\agent.exe -config agent.yaml
```

Within a few seconds you should see the forwarder connect:
```json
{"level":"INFO","msg":"forwarder: started","url":"https://192.168.1.100:8443"}
{"level":"INFO","msg":"forwarder: sent batch","count":12}
```

On the backend, the logs will show:
```json
{"level":"INFO","msg":"ingest: batch accepted","agent":"WORKSTATION-01","host":"WORKSTATION-01","count":12,"remote_ip":"192.168.1.50"}
{"level":"INFO","msg":"http","method":"POST","path":"/api/v1/events","status":200,"duration_ms":4}
```

### Install as a Windows Service

Once the interactive test passes:

```powershell
# From elevated PowerShell
.\agent.exe -config "C:\Program Files\OpenSIEM\Agent\agent.yaml" install
Start-Service OpenSIEMAgent
Get-Service OpenSIEMAgent
```

Confirm events are flowing:

```bash
# From the backend server
curl -k -s "https://localhost:8443/api/v1/agents" \
  -H "X-API-Key: YOUR_API_KEY" | python3 -m json.tool
```

Your Windows host should appear in the agents list within 30 seconds.

---

## 10. API Reference

All endpoints under `/api/` require authentication. `/health` is public.

**Authentication header:**
```
X-API-Key: your-api-key-here
```

---

### `GET /health`

No authentication required.

**Response 200:**
```json
{
  "status": "ok",
  "time": "2024-01-15T13:00:05Z",
  "database": "ok"
}
```

**Response 503** (database unreachable):
```json
{
  "status": "degraded",
  "time": "2024-01-15T13:00:05Z",
  "database": "error: connection refused"
}
```

---

### `POST /api/v1/events`

Receives a batch of events from an agent. Used by `agent.exe` — you normally do not call this directly.

**Request body:**
```json
{
  "agent_id": "workstation-01",
  "agent_version": "0.1.0",
  "sent_at": "2024-01-15T13:00:00Z",
  "events": [ ...Event objects... ]
}
```

**Response 200:**
```json
{"accepted": 47, "agent_id": "workstation-01"}
```

**Error responses:**

| Code | Meaning |
|---|---|
| 400 | Missing `agent_id` or invalid JSON |
| 401 | Missing or invalid `X-API-Key` |
| 413 | Batch exceeds `max_batch_size` (default 1000) |
| 500 | Database write failed |

---

### `GET /api/v1/events`

Query stored events. All parameters are optional.

**Query parameters:**

| Parameter | Type | Description | Example |
|---|---|---|---|
| `agent_id` | string | Exact match on agent ID | `agent_id=workstation-01` |
| `host` | string | Partial match on hostname (case-insensitive) | `host=workstation` |
| `event_type` | string | Exact match | `event_type=logon` |
| `severity` | integer | Minimum severity (1–5) | `severity=3` |
| `src_ip` | string | Exact IP match | `src_ip=192.168.1.50` |
| `dst_ip` | string | Exact IP match | `dst_ip=8.8.8.8` |
| `user_name` | string | Partial match (case-insensitive) | `user_name=admin` |
| `since` | RFC3339 | Events at or after this time | `since=2024-01-15T00:00:00Z` |
| `until` | RFC3339 | Events at or before this time | `until=2024-01-15T23:59:59Z` |
| `limit` | integer | Max results (default 100, max 1000) | `limit=50` |
| `offset` | integer | Pagination offset (default 0) | `offset=100` |

If `since` and `until` are both omitted, the last 24 hours are returned.

**Example requests:**

```bash
# All high-severity events in the last hour
curl -k "https://localhost:8443/api/v1/events?severity=4&since=$(date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%SZ)" \
  -H "X-API-Key: YOUR_API_KEY"

# All logon events for a specific host
curl -k "https://localhost:8443/api/v1/events?event_type=logon&host=WORKSTATION-01" \
  -H "X-API-Key: YOUR_API_KEY"

# Network events to an external IP
curl -k "https://localhost:8443/api/v1/events?event_type=network&dst_ip=8.8.8.8" \
  -H "X-API-Key: YOUR_API_KEY"

# Page through results
curl -k "https://localhost:8443/api/v1/events?limit=100&offset=0" -H "X-API-Key: YOUR_API_KEY"
curl -k "https://localhost:8443/api/v1/events?limit=100&offset=100" -H "X-API-Key: YOUR_API_KEY"
```

**Response 200:**
```json
{
  "events": [
    {
      "id": "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6",
      "time": "2024-01-15T13:45:00.123Z",
      "agent_id": "workstation-01",
      "host": "WORKSTATION-01",
      "os": "windows",
      "event_type": "logon",
      "severity": 1,
      "source": "Security",
      "event_id": 4624,
      "channel": "Security",
      "user_name": "jsmith",
      "domain": "CORP"
    }
  ],
  "total": 1842,
  "limit": 100,
  "offset": 0
}
```

---

### `GET /api/v1/agents`

Returns all known agents ordered by last-seen time (most recent first).

```bash
curl -k "https://localhost:8443/api/v1/agents" \
  -H "X-API-Key: YOUR_API_KEY"
```

**Response 200:**
```json
{
  "agents": [
    {
      "id": "workstation-01",
      "hostname": "WORKSTATION-01",
      "os": "windows",
      "version": "0.1.0",
      "first_seen": "2024-01-14T09:00:00Z",
      "last_seen": "2024-01-15T13:45:00Z",
      "last_ip": "192.168.1.50",
      "event_count": 48291
    }
  ],
  "count": 1
}
```

---

### `GET /api/v1/agents/{id}`

Returns a single agent by its ID.

```bash
curl -k "https://localhost:8443/api/v1/agents/workstation-01" \
  -H "X-API-Key: YOUR_API_KEY"
```

**Response 200:** Single agent object (same fields as above).  
**Response 404:** Agent not found.

---

## 11. Database Operations

### Connect to TimescaleDB directly

```bash
# From the backend server
docker exec -it opensiem-db psql -U opensiem -d opensiem
```

Useful psql commands:
```sql
-- List tables
\dt

-- Check event count
SELECT COUNT(*) FROM events;

-- Check agent count
SELECT COUNT(*) FROM agents;

-- Show migrations that have been applied
SELECT * FROM schema_migrations;

-- Show the 10 most recent events
SELECT time, host, event_type, severity, source
FROM events
ORDER BY time DESC
LIMIT 10;

-- Count events by type in the last 24 hours
SELECT event_type, COUNT(*) as count
FROM events
WHERE time > NOW() - INTERVAL '24 hours'
GROUP BY event_type
ORDER BY count DESC;

-- Count events by severity
SELECT severity, COUNT(*) as count
FROM events
GROUP BY severity
ORDER BY severity;

-- Show all TimescaleDB chunks
SELECT * FROM timescaledb_information.chunks
ORDER BY range_start DESC;

-- Show compression status
SELECT * FROM timescaledb_information.compression_settings;

-- Show retention policy
SELECT * FROM timescaledb_information.jobs
WHERE application_name LIKE '%Retention%';

-- Exit
\q
```

### Backup the database

```bash
# Dump the full database
docker exec opensiem-db pg_dump -U opensiem opensiem > backup_$(date +%Y%m%d).sql

# Restore from backup
docker exec -i opensiem-db psql -U opensiem opensiem < backup_20240115.sql
```

### Change data retention

By default, events older than 90 days are deleted automatically. To change this, connect to psql and run:

```sql
-- Change to 180 days
SELECT add_retention_policy('events', INTERVAL '180 days', if_not_exists => TRUE);

-- Or remove the retention policy entirely (keep data forever)
SELECT remove_retention_policy('events');
```

### Change compression interval

By default, chunks older than 7 days are compressed. To adjust:

```sql
-- Compress after 14 days instead
SELECT add_compression_policy('events', INTERVAL '14 days', if_not_exists => TRUE);
```

---

## 12. Logs and Monitoring

### View live backend logs

```bash
# Follow logs (Ctrl+C to stop)
docker compose logs -f backend

# Last 100 lines
docker compose logs --tail=100 backend

# Filter for errors only (using grep)
docker compose logs backend 2>&1 | grep '"level":"ERROR"'
```

### Log format

All logs are JSON (configurable to `text` in `server.yaml`). Key fields:

```json
{"time":"2024-01-15T13:45:01Z","level":"INFO","msg":"ingest: batch accepted","agent":"workstation-01","host":"WORKSTATION-01","count":47,"remote_ip":"192.168.1.50"}
{"time":"2024-01-15T13:45:01Z","level":"INFO","msg":"http","method":"POST","path":"/api/v1/events","status":200,"duration_ms":4,"remote":"192.168.1.50:52341"}
```

### Container resource usage

```bash
docker stats opensiem-backend opensiem-db
```

### Check disk usage (database volume)

```bash
docker system df -v | grep timescale
```

---

## 13. Configuration Reference

`docker/server.yaml` — the runtime config file mounted into the container.

### `server` section

| Field | Default | Description |
|---|---|---|
| `listen_addr` | `:8443` | Address and port to listen on. `:8443` means all interfaces on port 8443. |
| `tls_cert_file` | `certs/server.crt` | Path to the server TLS certificate (PEM). Relative to `/app` inside the container. |
| `tls_key_file` | `certs/server.key` | Path to the server TLS private key (PEM). |
| `tls_ca_file` | `""` | Path to the CA cert for verifying agent client certs. Only needed for mTLS. |
| `read_timeout` | `30s` | Max time to read a complete request. |
| `write_timeout` | `30s` | Max time to write a complete response. |
| `max_batch_size` | `1000` | Maximum events accepted in a single POST. Requests exceeding this get 413. |

### `database` section

| Field | Default | Description |
|---|---|---|
| `host` | `timescaledb` | Database hostname. When using Docker Compose, this is the service name — do not change it. |
| `port` | `5432` | PostgreSQL port. |
| `name` | `opensiem` | Database name. |
| `user` | `opensiem` | Database username. |
| `password` | — | **Required.** Must match `POSTGRES_PASSWORD` in `docker-compose.yml`. |
| `ssl_mode` | `disable` | PostgreSQL SSL mode. Use `require` if the database has its own TLS cert. |
| `max_open_conns` | `25` | Maximum open database connections. |
| `max_idle_conns` | `10` | Maximum idle connections kept in the pool. |
| `conn_max_lifetime` | `5m` | Maximum time a connection is reused before being replaced. |

### `auth` section

| Field | Default | Description |
|---|---|---|
| `api_keys` | `[]` | List of accepted `X-API-Key` values. At least one must be set if `mtls_enabled` is false. |
| `mtls_enabled` | `false` | If true, require client certificates instead of API keys. See [Section 14](#14-switching-to-mtls). |

### `log` section

| Field | Options | Description |
|---|---|---|
| `level` | `debug`, `info`, `warn`, `error` | Minimum log level. Use `debug` to log every event dropped or normalised. |
| `format` | `json`, `text` | `json` for production. `text` for human-readable local debugging. |

---

## 14. Switching to mTLS

mTLS gives every agent its own certificate identity. The backend rejects any agent without a certificate signed by your CA. This is stronger than API keys because there is nothing to steal — the key never leaves the Windows host.

### Step 1 — Generate a CA (if you haven't already)

```bash
cd backend/docker
openssl genrsa -out certs/ca.key 4096
openssl req -new -x509 -days 3650 \
  -key certs/ca.key \
  -out certs/ca.crt \
  -subj "/C=US/O=OpenSIEM/CN=OpenSIEM-CA"
```

### Step 2 — Re-sign the server cert using the CA

```bash
openssl genrsa -out certs/server.key 2048
openssl req -new -key certs/server.key -out certs/server.csr \
  -subj "/C=US/O=OpenSIEM/CN=your-server-hostname"
openssl x509 -req -days 825 \
  -in certs/server.csr -CA certs/ca.crt -CAkey certs/ca.key \
  -CAcreateserial -out certs/server.crt \
  -extfile <(printf "subjectAltName=IP:YOUR_SERVER_IP,DNS:your-server-hostname")
```

### Step 3 — Issue a certificate for each agent

```bash
HOSTNAME="workstation-01"
openssl genrsa -out certs/${HOSTNAME}.key 2048
openssl req -new -key certs/${HOSTNAME}.key -out certs/${HOSTNAME}.csr \
  -subj "/C=US/O=OpenSIEM/CN=${HOSTNAME}"
openssl x509 -req -days 825 \
  -in certs/${HOSTNAME}.csr -CA certs/ca.crt -CAkey certs/ca.key \
  -CAcreateserial -out certs/${HOSTNAME}.crt
```

### Step 4 — Enable mTLS in `docker/server.yaml`

```yaml
server:
  tls_cert_file: "certs/server.crt"
  tls_key_file:  "certs/server.key"
  tls_ca_file:   "certs/ca.crt"      # ← add this

auth:
  api_keys: []                        # ← clear this
  mtls_enabled: true                  # ← set to true
```

### Step 5 — Restart the backend

```bash
docker compose restart backend
```

### Step 6 — Update agent.yaml on each Windows host

Deploy the three cert files to the Windows host:

```
C:\Program Files\OpenSIEM\Agent\certs\
    agent.crt   ← copy from certs/workstation-01.crt
    agent.key   ← copy from certs/workstation-01.key
    ca.crt      ← copy from certs/ca.crt
```

Update `agent.yaml`:

```yaml
forwarder:
  backend_url: "https://your-server:8443"
  cert_file: "certs/agent.crt"
  key_file:  "certs/agent.key"
  ca_file:   "certs/ca.crt"
  # api_key: ""    ← remove or comment out
```

Restart the agent service:

```powershell
Restart-Service OpenSIEMAgent
```

---

## 15. Upgrading

### Upgrade the backend binary (code changes)

```bash
cd backend/docker

# Pull latest code
git pull

# Rebuild and restart only the backend container
# (database is untouched, data is preserved)
docker compose up -d --build backend
```

Any new SQL migration files added to `internal/store/migrations/` are applied automatically on startup.

### Upgrade TimescaleDB

```bash
# Pull new image
docker compose pull timescaledb

# Restart — data volume is preserved
docker compose up -d timescaledb

# Verify
docker compose logs timescaledb | tail -20
```

---

## 16. Troubleshooting

### Backend container exits immediately

Check logs for the error:

```bash
docker compose logs backend
```

**Common causes:**

| Error message | Fix |
|---|---|
| `config: open "server.yaml": no such file` | `docker/server.yaml` is missing. Create it from the template. |
| `config: validate: database.password is required` | Set `database.password` in `docker/server.yaml`. |
| `config: validate: auth: at least one api_key or mtls_enabled must be set` | Add at least one entry to `auth.api_keys`. |
| `tls: load server cert: open certs/server.crt: no such file` | Run Step 2 (generate TLS certs). The `certs/` directory must contain `server.crt` and `server.key`. |
| `store: ping: ...connection refused` | TimescaleDB is not ready yet. Wait 30 seconds and try `docker compose restart backend`. |

### TimescaleDB fails to start

```bash
docker compose logs timescaledb
```

Most common cause: the data volume from a previous run has a different password. Fix:

```bash
# WARNING: this deletes all stored data
docker compose down -v
docker compose up -d
```

### Agent shows "send failed after retries" / events not arriving

Check in order:

**1. Can the Windows host reach the backend?**
```powershell
Test-NetConnection -ComputerName YOUR_SERVER_IP -Port 8443
```
If this fails: open port 8443 outbound in your firewall and security group.

**2. Is the API key correct?**
```powershell
# Test from Windows using PowerShell
$key = "your-api-key"
$url = "https://YOUR_SERVER_IP:8443/health"
Invoke-WebRequest -Uri $url -SkipCertificateCheck
```
Expected: `{"status":"ok",...}`

**3. Is the ca_file pointing to the right cert?**

The agent uses `ca_file` to verify the backend's certificate. If the cert doesn't match, the TLS handshake fails silently. Make sure `ca_file` in `agent.yaml` points to the same cert (or CA) that signed `server.crt`.

**4. Check agent logs in debug mode:**

Edit `agent.yaml`, set `log.level: debug`, then run interactively:
```powershell
.\agent.exe -config agent.yaml
```

Look for lines containing `forwarder:` — they show exactly what is failing.

### Migrations fail on startup

```
migrations: apply 001_events.sql: ERROR: could not open extension control file ...
```

TimescaleDB extension is not loading properly. This usually means the database image is wrong (using plain Postgres instead of TimescaleDB):

```bash
docker compose down
docker compose pull timescaledb
docker compose up -d
```

### `GET /api/v1/events` returns empty even though events were ingested

Default time window is the last 24 hours. If you ingested test events a while ago, pass `since` explicitly:

```bash
curl -k "https://localhost:8443/api/v1/events?since=2024-01-01T00:00:00Z" \
  -H "X-API-Key: YOUR_API_KEY"
```

### Check what's actually in the database

```bash
docker exec -it opensiem-db psql -U opensiem -d opensiem -c \
  "SELECT COUNT(*), MIN(time), MAX(time) FROM events;"
```

### Port 8443 is already in use

Change the port in both `docker/server.yaml` and `docker/docker-compose.yml`:

`server.yaml`:
```yaml
server:
  listen_addr: ":9443"
```

`docker-compose.yml`:
```yaml
ports:
  - "9443:9443"
```

Then update `backend_url` in `agent.yaml` on all Windows hosts.

### Reset everything and start fresh

```bash
docker compose down -v          # removes containers AND data volume
docker compose up -d --build    # rebuild and restart from scratch
```

This deletes all stored events and agents. Use only in development.
