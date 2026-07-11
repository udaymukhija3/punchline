# Punchline Engineering Necessity Matrix

This matrix is product-specific. It separates what Punchline needs for a public
beta from infrastructure that only becomes necessary after traffic, teams, or
durability requirements grow.

| Area | Necessity for Punchline | Current State | Next Engineering Action |
| --- | --- | --- | --- |
| Web server | Required now | Present: one Go server handles static assets, API, WebSockets, health, readiness, and metrics. | Keep as the production entrypoint. Do not split the frontend until there is a real product reason. |
| Distributed cache | Not required for beta | Partial: durable room state is Postgres-backed; rate limits remain local to an instance with explicit trusted-proxy client IP handling. | Defer Redis/Valkey. Add it only for coordinated rate limits, cross-owner pub/sub, or measured read pressure. |
| CDN | Useful after first public deploy | Partial: immutable cache headers exist for built assets, but no CDN is configured. | Configure platform/CDN caching after domain setup. No app code needed right now. |
| API gateway | Not required now | Partial: app owns routing, CORS, security headers, trusted-proxy client IP parsing, and local rate limits; Fly can provide edge routing. | Defer a dedicated gateway until there are multiple services, auth tiers, or complex routing policies. |
| Asynchronous queue | Not required for core game | Missing. | Defer. Add a queue when analytics, moderation review, reports, emails, or durable event processing become product requirements. |
| Fan-out service | Required in-process now; distributed later | Present: each owner broadcasts to its room sockets; Fly-Replay keeps a room on one owner and Postgres enables recovery. | Keep in-process. Add pub/sub only when rooms need multi-owner broadcasting or spectators. |
| Load balancer | Required, platform-owned | Partial: Fly `http_service` provides load balancing/HTTPS when deployed there. | Manual platform setup. No self-hosted load balancer needed. |
| Object storage | Not required now | Missing; no uploads or generated media. | Defer. Add S3/R2 only for media, user uploads, generated share images, or external card packs. |
| Unique ID generator | Required now | Present: crypto-random room codes, player IDs, submission IDs, and guest tokens. | Keep. Revisit room-code length only if collision/rate metrics show pressure. |
| Sharding and replication | Not required for beta; backups are required | Missing in repo; the app is region-local and uses one managed Postgres primary. | Manual DB setup: automated backups and a tested restore come first. Add replica/read routing only after measured DB pressure. |
| Horizontal scaling | Required once real traffic exceeds one machine | Present in code with Postgres: leases, durable snapshots, `Fly-Replay`, short lease takeover, and drain/release support. | Manual: deploy with `DATABASE_URL`, then add machines only after the baseline smoke and dashboard checks are healthy. |
| Metrics: users and traffic | Required now | Present: HTTP requests, room creation, WS connects/messages, connected human-player gauge, local rooms, rate limits. | Add dashboards and alerts after deploy. No unauthenticated account metric exists because the product has guest sessions. |
| Metrics: queries | Required when Postgres is production-critical | Present: registry/state reserve, lookup, heartbeat, load, save, release, and delete counts/durations plus DB-pool gauges. | Wire dashboards and alerts once `DATABASE_URL` is production. |
| Metrics: storage | Platform-required, app optional | Missing in app. | Use managed Postgres/platform dashboards first. Add app metrics only if storage is user-visible or quota-driven. |
| Metrics: memory and bandwidth usage | Memory required now; bandwidth platform-owned | Partial: app exports Go heap/stack/goroutine gauges. Bandwidth remains platform-owned. | Wire platform bandwidth metrics and alerts after deploy. |
| Single responsibility principle | Useful engineering hygiene | Partial: engine, WS, registry, cards, and HTTP are separated; HTTP handler still orchestrates several concerns. | Accept for beta. Split handler concerns only when changes become slower/riskier. |
| Open closed principle | Useful engineering hygiene | Partial: registry abstraction supports memory/Postgres; other parts are concrete. | No immediate work. Add abstractions only when adding a second implementation. |
| Liskov substitution principle | Useful engineering hygiene | Mostly present: memory and Postgres implement the registry/state-store contracts; CI runs migrations and the smoke flow against Postgres. | Keep integration coverage with every schema change. |
| Interface segregation principle | Useful engineering hygiene | Present where needed: `RoomRegistry`, `RoomStateStore`, and optional DB stats are separated. | No immediate work. Introduce narrower interfaces only for a new consumer. |
| Dependency inversion principle | Useful engineering hygiene | Present for room registry/state storage through injection; metrics/limiter are private handler details. | No immediate work. Invert additional dependencies only if reuse or testing needs it. |
| CORS | Required now | Present: same-origin, loopback dev, and explicit allowed origins are supported. | Manual: set `PUNCHLINE_ALLOWED_ORIGINS` to the production origin. |

## Engineering Priority

1. Provision managed Postgres with backups, restore testing, and a same-region connection string. This is required before public production deploy because `fly.toml` enables `PUNCHLINE_REQUIRE_DATABASE`.
2. Wire dashboards and alerts for `/readyz`, `/metrics`, state/registry operation failures and latency, DB pool pressure, app memory, traffic, and platform bandwidth/storage.
3. Configure the production domain, TLS, and optional CDN. Keep WebSocket pass-through enabled.
4. Run the production smoke gate after every release and rehearse rollback plus a Postgres restore.
5. Add Redis, queues, object storage, replicas, or sharding only when traffic or a product feature makes one necessary.
