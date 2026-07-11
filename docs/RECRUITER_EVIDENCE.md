# Punchline Recruiter Evidence

## Engineering thesis

Punchline is a web-first, real-time multiplayer party game whose strongest
engineering case is not platform breadth. It proves a production-leaning live
game loop: a server-authoritative Go room engine, per-viewer WebSocket
snapshots, reconnectable guest sessions, bounded runtime behavior, Postgres
room ownership/recovery for multi-machine deployments, and a single-container
deploy path with meaningful health, readiness, smoke, and metrics gates.

## Product contract summary

- Primary user: friends opening a phone or browser to play a live party game
  without installing an app.
- Core loop: create room, join with a name/code/link, submit answer cards,
  judge anonymous submissions, score, rotate judge, continue or play again.
- Current status: playable v0 live game with guest sessions, not persistent
  accounts, match history, durable results, async daily mode, moderation, or
  DB-backed deck loading.
- Deployment intent: one Go container serving API, WebSockets, health/readiness,
  metrics, and the built React client. Fly.io is production-shaped; Render is a
  single-instance demo path.

## Evidence matrix

| Capability | Why product needs it | Existing code evidence | Verification evidence | Deployment/ops evidence | Status | Next action |
|---|---|---|---|---|---|---|
| Server-authoritative realtime game flow | A party game must prevent browsers from deciding phases, scoring, judge role, card ownership, or timers. | `backend/internal/realtime/room.go` owns phase transitions, host/judge permissions, timers, scoring, computer turns, hand drawing, and snapshots. `backend/internal/httpapi/handler.go` only maps API/WS commands into room actions. | `backend/internal/realtime/room_test.go`, `settings_test.go`, `skipprompt_test.go`, and `scripts/smoke-realtime.mjs` cover lobby, start, submit, judging, scoring, next round, settings, prompt skip, and judge rotation. | `scripts/smoke-realtime.mjs` is used by `scripts/deploy-check.mjs` and GitHub Actions to validate the live loop against a running server. | PROVEN | Keep adding focused tests only at new game-rule boundaries. |
| Per-viewer privacy and guest-session reconnect auth | Players need private hands, hidden submissions during answering, blind judging, and reconnect without accounts. | `Player.GuestToken` is JSON-hidden in `backend/internal/realtime/types.go`; `Room.Attach` validates token in `room.go`; `snapshotLocked` redacts hands and submissions per viewer. The React client stores only room/player/token in `localStorage` for reconnect. | `TestGuestTokenIsRequiredForAttachAndHiddenFromSnapshots`, `TestJoinReturnsGuestTokenAndSnapshotsDoNotLeakIt`, and `TestJudgingSnapshotRevealsCardsButHidesAuthorsAndOtherHands` prove token and snapshot privacy. | Same-origin API/WS routing plus origin checks and security headers in `backend/internal/httpapi/handler.go`; no account or long-lived auth claim is made. | PROVEN | If accounts are added later, add resource-level auth tests before exposing user history. |
| Bounded WebSocket/runtime behavior | A live room cannot let a slow socket, giant frame, idle connection, or traffic spike stall the process. | `backend/internal/ws/ws.go` uses a single writer pump, keepalive pings, read/write deadlines, max frame/message bytes, and bounded send queue. `backend/internal/httpapi/ratelimit.go` bounds create/join/connect/message rates with trusted-proxy client IP handling. `backend/cmd/api/main.go` sets server timeouts and DB pool caps. | `backend/internal/ws/ws_test.go` covers invalid protocol frames and slow-consumer close behavior. `backend/internal/httpapi/handler_test.go` covers rate limits, trusted proxy behavior, and metrics auth. | `/metrics` exports request, WebSocket, rate-limit, room, DB-pool, registry, heap, stack, and goroutine signals; `ops/production-runbook.md` maps them to incident checks. | PROVEN | Real traffic should tune limits from observed metrics before adding Redis or external fan-out. |
| Postgres ownership and active-room recovery | Multi-machine hosting needs a single owner for each room, wrong-machine replay, and recoverable active state after graceful deploy or lease expiry. | `backend/internal/realtime/manager.go` reserves, looks up, heartbeats, claims, restores, drains, and releases rooms. `backend/internal/roomstore/postgres.go` stores owner leases and JSONB room snapshots. `migrations/002_room_instance_leases.sql` and `003_room_state_snapshots.sql` define the schema. | `manager_test.go` proves wrong-owner errors, restore after restart, claim after lease expiry, shutdown release, and pending-state flush. `postgres_integration_test.go` proves graceful release keeps snapshots and new reservation clears stale state when `DATABASE_URL` is available. | `fly.toml` runs `/app/migrate`, requires Postgres for production, probes `/readyz`, and relies on HTTP `421` plus `Fly-Replay`. `TestRoomOwnedElsewhereSetsFlyReplayHeader` covers that response contract. | PROVEN | Manual production still needs managed Postgres backups and a restore drill before claiming production readiness. |
| Deployable one-container topology with observability gates | A reviewer needs to build, run, health-check, and smoke-test the same shape that would be deployed. | `Dockerfile` builds React, Go API, and migration runner into one runtime image. `backend/cmd/migrate/main.go` runs ordered checksum-guarded migrations. `backend/internal/httpapi/metrics.go` and readiness endpoints expose operational state. | `.github/workflows/ci.yml` runs backend vet/build/test/race, frontend build, Docker build, and a Postgres-backed smoke job. `scripts/deploy-check.mjs` checks the React app shell, built assets, `/readyz`, `/metrics`, and the realtime smoke flow. | `fly.toml`, `render.yaml`, `ops/production-runbook.md`, and `ops/engineering-necessity-matrix.md` define the smallest credible deployment and explicitly reject unnecessary Redis/queues/object storage for beta. | PROVEN | Deploy manually with real secrets, dashboards, DNS/TLS, and a restore drill; the repo cannot honestly claim a permanent production URL yet. |

## Why this is more convincing than a CRUD tutorial

The central behavior is not create/read/update/delete records. A running room is
a concurrent state machine with role-specific commands, timers, hidden
information, reconnect semantics, WebSocket fan-out, room-owner routing, and
durable active-state recovery. The strongest evidence is executable:
`scripts/smoke-realtime.mjs` drives a complete live round through HTTP and
WebSockets, while Go tests cover authorization, redaction, state recovery,
Postgres lease transfer, slow-socket bounds, and deployment routing headers.

## Scope boundaries

Do not present this repo as shipping persistent accounts, match history, durable
game results, database-backed deck loading, moderation/reporting, async daily
mode, payments, app-store clients, or zero-downtime recovery from every failure.
The honest public-beta claim is a playable realtime game with production-shaped
deployment and recovery controls, ready once manual platform work is complete.
