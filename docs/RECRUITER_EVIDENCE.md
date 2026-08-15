# Punchline Recruiter Evidence

## Engineering thesis

Punchline is a web-first live and asynchronous party game whose strongest case
is correctness across two different multiplayer rhythms. It proves a
server-authoritative realtime engine, privacy-preserving scoped guest identity,
transactional and idempotent daily workflows under concurrent workers, bounded
runtime behavior, Postgres recovery, and a one-container deploy path with
meaningful health, readiness, smoke, and metrics gates.

## Product contract summary

- Primary user: friend groups opening a phone or browser for a live game night
  or one asynchronous prompt between game nights.
- Core loops: the complete live judge/score loop and the durable daily
  submit/reveal/vote/result/streak loop.
- Current status: playable live and daily modes with scoped guest identity, a
  database-backed card platform with telemetry, player reporting, and a
  token-gated moderation desk. Not persistent accounts, live match history, or
  notifications.
- Deployment intent: one Go container serving API, WebSockets, health/readiness,
  metrics, and the built React client. Fly.io is production-shaped; Render is a
  single-instance demo path.

## Evidence matrix

| Capability | Why product needs it | Existing code evidence | Verification evidence | Deployment/ops evidence | Status | Next action |
|---|---|---|---|---|---|---|
| Server-authoritative realtime game flow | A party game must prevent browsers from deciding phases, scoring, judge role, card ownership, or timers. | `backend/internal/realtime/room.go` owns phase transitions, host/judge permissions, timers, scoring, computer turns, hand drawing, and snapshots. `backend/internal/httpapi/handler.go` only maps API/WS commands into room actions. | `backend/internal/realtime/room_test.go`, `settings_test.go`, `skipprompt_test.go`, and `scripts/smoke-realtime.mjs` cover lobby, start, submit, judging, scoring, next round, settings, prompt skip, and judge rotation. | `scripts/smoke-realtime.mjs` is used by `scripts/deploy-check.mjs` and GitHub Actions to validate the live loop against a running server. | PROVEN | Keep adding focused tests only at new game-rule boundaries. |
| Per-viewer privacy and guest-session reconnect auth | Players need private hands, hidden submissions during answering, blind judging, and reconnect without accounts. | `Player.GuestToken` is JSON-hidden in `backend/internal/realtime/types.go`; `Room.Attach` validates token in `room.go`; `snapshotLocked` redacts hands and submissions per viewer. The React client stores only room/player/token in `localStorage` for reconnect. | `TestGuestTokenIsRequiredForAttachAndHiddenFromSnapshots`, `TestJoinReturnsGuestTokenAndSnapshotsDoNotLeakIt`, and `TestJudgingSnapshotRevealsCardsButHidesAuthorsAndOtherHands` prove token and snapshot privacy. | Same-origin API/WS routing plus origin checks and security headers in `backend/internal/httpapi/handler.go`; no account or long-lived auth claim is made. | PROVEN | If accounts are added later, add resource-level auth tests before exposing user history. |
| Transactional daily workflow and tenant privacy | Async groups need one timezone-correct round, private pre-reveal answers, anonymous voting, retry-safe mutations, and deterministic results even when every app machine runs the worker. | `backend/internal/daily/service.go` hashes scoped membership tokens, locks group/round rows, enforces absolute deadlines, upserts submissions/votes, redacts phase-specific views, computes tied winners/streaks, and runs idempotent transitions. `migrations/004_daily_async.sql` adds composite same-group foreign keys and unique round/action constraints. | `TestPostgresDailyLifecyclePrivacyIdempotencyAndConcurrentWorker` verifies auth, plaintext-token absence, privacy, idempotency, self-vote rejection, deadlines, results, streaks, and eight concurrent worker runs creating one round. `scripts/smoke-daily.mjs` covers the deployed API and cleanup. | Every app instance runs the bounded worker; `/readyz` detects the daily schema and metrics expose actions, transitions, errors, and last success. `docs/DAILY_ENGINE.md` states the exact semantics and boundaries. | PROVEN | Add cross-device account recovery and notifications only when the product needs them. |
| Bounded WebSocket/runtime behavior | A live room cannot let a slow socket, giant frame, idle connection, or traffic spike stall the process. | `backend/internal/ws/ws.go` uses a single writer pump, keepalive pings, read/write deadlines, max frame/message bytes, and bounded send queue. `backend/internal/httpapi/ratelimit.go` bounds create/join/connect/message rates with trusted-proxy client IP handling. `backend/cmd/api/main.go` sets server timeouts and DB pool caps. | `backend/internal/ws/ws_test.go` covers invalid protocol frames and slow-consumer close behavior. `backend/internal/httpapi/handler_test.go` covers rate limits, trusted proxy behavior, and metrics auth. | `/metrics` exports request, WebSocket, rate-limit, room, DB-pool, registry, heap, stack, and goroutine signals; `ops/production-runbook.md` maps them to incident checks. | PROVEN | Real traffic should tune limits from observed metrics before adding Redis or external fan-out. |
| Postgres ownership and active-room recovery | Multi-machine hosting needs a single owner for each room, wrong-machine replay, and recoverable active state after graceful deploy or lease expiry. | `backend/internal/realtime/manager.go` reserves, looks up, heartbeats, claims, restores, drains, and releases rooms. `backend/internal/roomstore/postgres.go` stores owner leases and JSONB room snapshots. `migrations/002_room_instance_leases.sql` and `003_room_state_snapshots.sql` define the schema. | `manager_test.go` proves wrong-owner errors, restore after restart, claim after lease expiry, shutdown release, and pending-state flush. `postgres_integration_test.go` proves graceful release keeps snapshots and new reservation clears stale state when `DATABASE_URL` is available. | `fly.toml` runs `/app/migrate`, requires Postgres for production, probes `/readyz`, and relies on HTTP `421` plus `Fly-Replay`. `TestRoomOwnedElsewhereSetsFlyReplayHeader` covers that response contract. | PROVEN | Manual production still needs managed Postgres backups and a restore drill before claiming production readiness. |
| Deployable one-container topology with observability gates | A reviewer needs to build, run, health-check, and smoke-test the same shape that would be deployed. | `Dockerfile` builds React, Go API, and migration runner into one runtime image. `backend/cmd/migrate/main.go` runs ordered checksum-guarded migrations. `backend/internal/httpapi/metrics.go` and readiness endpoints expose operational state. | `.github/workflows/ci.yml` runs backend vet/build/test/race, frontend build, Docker build, and a Postgres-backed smoke job. `scripts/deploy-check.mjs` checks the React app shell, built assets, `/readyz`, `/metrics`, and the realtime smoke flow. | `fly.toml`, `render.yaml`, `ops/production-runbook.md`, and `ops/engineering-necessity-matrix.md` define the smallest credible deployment and explicitly reject unnecessary Redis/queues/object storage for beta. | PROVEN | Deploy manually with real secrets, dashboards, DNS/TLS, and a restore drill; the repo cannot honestly claim a permanent production URL yet. |

## Why this is more convincing than a CRUD tutorial

The central behavior is not create/read/update/delete records. A running room is
a concurrent state machine with role-specific commands, timers, hidden
information, reconnect semantics, WebSocket fan-out, room-owner routing, and
durable active-state recovery. The strongest evidence is executable:
`scripts/smoke-realtime.mjs` drives a complete live round through HTTP and
WebSockets, while `scripts/smoke-daily.mjs` drives a durable group flow and
cleans it up. Go/Postgres tests cover authorization, redaction, idempotency,
concurrent workers, state recovery, lease transfer, and slow-socket bounds.

| Database-backed content platform with player-sourced curation | A party game dies when its deck goes stale or ships a card that hurts someone. Content must be swappable without a redeploy, measurable in play, reportable by players, and moderatable by one operator. | `backend/internal/cards/store.go` loads approved cards from approved packs and revalidates playable minimums; `migrations/005_seed_official_pack.sql` is generated from `seed/cards.json` so the two cannot drift. `backend/internal/telemetry/cards.go` aggregates play/win/skip events and flushes batched updates, dropping rather than blocking when full. `backend/internal/daily/promote.go` harvests voted daily answers into deduped candidates. `backend/internal/content/service.go` dedupes reports per reporter, auto-retires at three, and promotes candidates into the community pack. `backend/internal/httpapi/admin.go` fails closed when `PUNCHLINE_ADMIN_TOKEN` is unset. | `TestPostgresDeckMatchesSeedDeckAndCarriesRowIDs` proves the database deck is interchangeable with the seed deck; `TestPostgresDeckRejectsOverRetiredContent` and `TestPostgresDeckIgnoresUnapprovedPacks` prove the loader fails loudly instead of dealing a broken hand. `TestPostgresReportsAutoRetireThenRestore` proves repeat reports from one client do not move the counter. `TestPostgresFinalizationHarvestsCandidates` proves the pipeline produces candidates and that deleting a group withdraws them. `scripts/smoke-content.mjs` covers the deployed API. | `PUNCHLINE_DECK_SOURCE` pins deck origin; `/metrics` exposes content actions, auto-retirements, and telemetry flushed/dropped/failed; `docs/CONTENT_PLATFORM.md` states the pipeline and its boundaries. | PROVEN | Add per-room pack selection when players ask to choose packs. |

## Scope boundaries

Do not present this repo as shipping persistent accounts, live match history,
durable live results, notifications, payments, app-store clients, or
zero-downtime recovery from every failure. Do not claim AI content generation:
new cards come from players through the daily candidate queue, by design. Do
not claim user-level moderation or bans; with no accounts, moderation acts on
content only. The honest public-beta claim is playable live and daily modes on
a database-backed content platform with production-shaped deployment, ready
once manual platform work is complete.
