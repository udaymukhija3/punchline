# Punchline Production Runbook

This repo can build and smoke-test the app, but production still needs manual
platform setup: app ownership, Postgres provisioning, secrets, DNS, and deploy
permissions.

Use `ops/engineering-necessity-matrix.md` to decide which infrastructure work is
actually necessary for the next launch stage. Use `AGENTS.md` as the compact
handoff for a new coding agent or fresh chat.

## Topology

- Serve the React bundle, HTTP API, and WebSockets from the Go container.
- Prefer Fly.io for production because `fly.toml` is wired for `/readyz`,
  release migrations, HTTPS, and room-owner replay.
- Keep managed Postgres in the same primary region as the app. Postgres holds
  room ownership leases and durable active-room snapshots; it is required for
  the production Fly configuration.
- Treat Render free as demo-only unless it is upgraded and given equivalent
  database, migration, and rollback controls.

## Required Production Secrets

- `DATABASE_URL`: managed Postgres connection string.
- `PUNCHLINE_ALLOWED_ORIGINS`: comma-separated public origins.
- `PUNCHLINE_REQUIRE_DATABASE=true`: fail release migrations if DB is missing.
- `PUNCHLINE_METRICS_TOKEN`: optional bearer token protecting `/metrics`.
- `PUNCHLINE_TRUST_PROXY_HEADERS` or `PUNCHLINE_TRUSTED_PROXY_CIDRS`: enable
  forwarded client IP headers only after the edge/load balancer is confirmed to
  overwrite or strip spoofed values.

## Useful Runtime Knobs

- `MAX_LOCAL_ROOMS`
- `ROOM_IDLE_TTL`
- `ROOM_LEASE_TTL`
- `ROOM_HEARTBEAT_INTERVAL`
- `SHUTDOWN_DRAIN_GRACE`
- `DB_MAX_OPEN_CONNS`
- `DB_MAX_IDLE_CONNS`
- `PUNCHLINE_ROOM_CREATE_LIMIT_PER_MIN`
- `PUNCHLINE_ROOM_JOIN_LIMIT_PER_MIN`
- `PUNCHLINE_WS_CONNECT_LIMIT_PER_MIN`
- `PUNCHLINE_WS_MESSAGE_LIMIT_PER_MIN`
- `PUNCHLINE_TRUSTED_PROXY_CIDRS` accepts comma-separated CIDRs plus `loopback`
  and `private` shortcuts. Prefer CIDRs over trusting every peer.

## Release

1. Confirm CI is green, including the Postgres-backed smoke job.
2. Provision/update `DATABASE_URL`, `PUNCHLINE_ALLOWED_ORIGINS`, and, if used,
   `PUNCHLINE_METRICS_TOKEN` as platform secrets.
3. Confirm the database provider has automated backups enabled before deploy.
4. Deploy the Docker image. Fly runs `/app/migrate` through `release_command`;
   migration execution is serialized and checksums prevent edited history.
5. Run `PUNCHLINE_METRICS_TOKEN=... node scripts/deploy-check.mjs "$PUNCHLINE_BASE_URL"`
   when metrics are protected; omit the variable otherwise. The full deploy
   check expects the same container to serve the React app shell, built assets,
   `/readyz`, `/metrics`, and the realtime WebSocket flow. Use `--api-only`
   only for backend-only local development servers.
6. Confirm `/metrics` shows healthy state/registry saves and loads, low DB pool
   wait time, expected traffic, and no rising error/rate-limit counters.

## Rollback

1. Stop and investigate if a migration fails. Do not roll the database back
   automatically.
2. Roll back to the previous platform image/release only when it remains
   compatible with the applied additive schema.
3. Run the deploy smoke against the rolled-back URL.
4. If a destructive database rollback is ever required, restore from the
   managed-provider backup in a controlled maintenance window and record the
   decision; this repo intentionally ships forward-only migrations.

## Incident Checks

- `/readyz` non-200: check Postgres connectivity and migration state.
- `punchline_rooms_local` high: lower `MAX_LOCAL_ROOMS`, scale machines, or
  shorten `ROOM_IDLE_TTL`.
- `punchline_rate_limited_total` high: inspect source IPs and tune app/platform
  limits.
- `punchline_instance_draining` is 1: instance is intentionally leaving service
  and `/readyz` should fail until shutdown completes.
- `punchline_registry_operation_duration_seconds` rising: inspect Postgres
  connectivity, pool limits, and regional placement.
- `punchline_registry_operations_total{operation="save_room_state",result!="ok"}`
  rising: treat it as a durability incident. Check DB availability, pool wait
  metrics, and recent lease ownership changes before scaling or restarting.
- `punchline_database_connections_in_use` near
  `punchline_database_max_open_connections`, or a rising wait counter: raise
  the app pool only after checking the database connection budget.
- Recovery after an ungraceful owner loss can take up to `ROOM_LEASE_TTL`.
  Graceful deploys drain sockets, release room leases, and allow immediate
  recovery on another machine.

## Manual Platform Checklist

1. Create a managed Postgres database in the Fly primary region. Enable daily
   backups, point-in-time recovery if offered, and retain access for a restore
   drill. Copy its TLS connection string into `DATABASE_URL`.
2. Set Fly secrets with `fly secrets set DATABASE_URL=... PUNCHLINE_ALLOWED_ORIGINS=https://<domain>`.
   Add `PUNCHLINE_METRICS_TOKEN=<random-secret>` when the monitoring service
   can send an `Authorization: Bearer` header.
3. Confirm how the platform presents client IPs to the app. If the immediate
   peer is a trusted edge that overwrites forwarded headers, set
   `PUNCHLINE_TRUSTED_PROXY_CIDRS` to that edge range, or set
   `PUNCHLINE_TRUST_PROXY_HEADERS=true` only when CIDRs are not available.
4. Deploy once with one machine. Run the deploy check. Only then add capacity
   with `fly scale count 2` or a Fly autoscaling policy, keeping all machines
   in the primary region until latency data justifies another region.
5. Point the production DNS name at Fly and enable TLS. If using Cloudflare or
   another CDN, cache only immutable `/assets/*` files, bypass `/api/*`,
   `/ws/*`, `/healthz`, `/readyz`, and `/metrics`, and allow WebSocket upgrade.
6. Configure the monitoring provider to scrape `/metrics` and probe `/readyz`.
   Alert on readiness failure, state-save errors, registry latency/errors, DB
   pool saturation, heap growth, WebSocket disconnect spikes, and bandwidth or
   Postgres storage thresholds from the platform dashboard.
7. Perform one restore drill before calling the service production-ready: restore
   a recent backup to an isolated database, run `/app/migrate`, start a
   temporary app with that database, and complete `scripts/deploy-check.mjs`.
