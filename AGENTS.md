# Punchline Agent Handoff

This repo is a public web game/app production-hardening pass in progress.
Start with this file, then read:

- `README.md`
- `ops/engineering-necessity-matrix.md`
- `ops/production-runbook.md`
- `scripts/deploy-check.mjs`
- `scripts/smoke-realtime.mjs`

## Current Production Shape

- One Go container serves the React bundle, HTTP API, WebSockets, health,
  readiness, and Prometheus-style metrics.
- Fly.io is the production-shaped deploy target. `fly.toml` runs `/app/migrate`
  as the release command and probes `/readyz`.
- Postgres is required for production. It stores room ownership leases and
  durable active-room snapshots.
- Without `DATABASE_URL`, the app is local/demo only and uses memory-backed room
  ownership/state.
- Active rooms can recover from Postgres snapshots after graceful release or
  after an ungraceful owner's `ROOM_LEASE_TTL` expires. This is not a
  zero-downtime guarantee.

## Code-Owned Hardening Already Shipped

- Durable active-room state: `backend/internal/realtime/state.go`,
  `backend/internal/realtime/manager.go`, `backend/internal/roomstore/postgres.go`.
- Release migration runner: `backend/cmd/migrate/main.go`.
- Postgres snapshot migration: `migrations/003_room_state_snapshots.sql`.
- Graceful drain/shutdown and immediate lease release on deploy.
- Bounded background persistence for timer/computer-driven room transitions.
- Health/readiness/metrics endpoints with optional metrics bearer token.
- Security headers, CORS/origin checks, request-size limits, WebSocket frame
  limits, slow-socket protection, and stricter WebSocket protocol validation.
- Local rate limits with explicit trusted-proxy client IP handling.
- CI now includes backend vet/build/test/race, frontend build, Docker build, and
  a Postgres-backed realtime smoke job.

## Infrastructure Necessity Decisions

Do not add Redis, a queue, object storage, sharding, read replicas, a separate
API gateway, or an external fan-out service unless a product feature or measured
traffic creates the need. For this product's next production step, managed
Postgres plus platform load balancing/CDN is enough.

Required manual/platform work is intentionally left outside the repo:

- Managed Postgres in the app region, with backups and a restore drill.
- Production secrets: `DATABASE_URL`, `PUNCHLINE_ALLOWED_ORIGINS`,
  optional `PUNCHLINE_METRICS_TOKEN`.
- Trusted proxy/IP-header decision: set `PUNCHLINE_TRUSTED_PROXY_CIDRS` or, only
  when CIDRs are unavailable and the edge is verified safe,
  `PUNCHLINE_TRUST_PROXY_HEADERS=true`.
- DNS/TLS/CDN setup. Cache only `/assets/*`; bypass `/api/*`, `/ws/*`,
  `/healthz`, `/readyz`, and `/metrics`; allow WebSocket upgrades.
- Monitoring dashboards/alerts for `/readyz`, `/metrics`, DB pool pressure,
  state save/load errors, registry latency/errors, memory, traffic, bandwidth,
  and Postgres storage.

## Verification Commands

Run from repo root unless a command says otherwise:

```bash
cd backend
go vet ./...
go test ./... -timeout 30s
go test -race ./...

cd ../frontend
npm run build

cd ..
node --check scripts/deploy-check.mjs
node --check scripts/smoke-realtime.mjs
git diff --check
```

For an end-to-end local smoke:

```bash
cd backend
PORT=18080 go run ./cmd/api

# In another shell from repo root:
node scripts/deploy-check.mjs --api-only http://127.0.0.1:18080
```

If Docker is available:

```bash
docker build -t punchline-production-check .
docker run --rm -p 18081:8080 punchline-production-check
node scripts/deploy-check.mjs http://127.0.0.1:18081
```

## Manual Deploy Flow

1. Provision managed Postgres and set `DATABASE_URL`.
2. Set `PUNCHLINE_ALLOWED_ORIGINS` and optional `PUNCHLINE_METRICS_TOKEN`.
3. Confirm trusted proxy behavior and set the proxy-header env var only if safe.
4. Deploy with `fly deploy`; `/app/migrate` runs in the release phase.
5. Run `node scripts/deploy-check.mjs https://<app-url>`.
6. Start with one machine, then scale to two only after smoke and dashboards are
   healthy.
7. Rehearse rollback and a Postgres restore before calling it production-ready.

## Watch Outs

- Do not claim persistent accounts, match history, durable game results, or
  database-backed deck serving. Those are not shipped.
- Do not claim zero-downtime recovery. Graceful deploy recovery is immediate;
  ungraceful owner failure waits up to `ROOM_LEASE_TTL`.
- Do not trust forwarded client IP headers by default. The app deliberately uses
  the socket peer unless proxy trust is configured.
- Keep the production topology simple until traffic or product requirements say
  otherwise.
