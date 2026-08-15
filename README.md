# Punchline

Punchline is a web-first fill-in-the-blank party game with two rhythms: live
rooms for game night and durable daily groups for asynchronous play. Players
can open either mode from a phone or browser without installing an app.

It is also a backend systems project: a server-authoritative Go game engine,
custom WebSocket transport, reconnectable guest sessions, a mobile-first React
client, a single-container production build, and a Postgres-backed daily store
plus room ownership registry for multi-machine routing.

## Current state

This repository ships a playable live game and a v1 daily/async loop. It is not
yet the full long-term platform described in some planning docs under
`artifacts/`.

What works today:

- Create a room, join with a name, and share an invite link with a room code.
- Play a complete 3+ player party-game loop: lobby, prompt, answer submission,
  anonymous judging, scoring, rotating judge, next round, game over, play again.
- Use host-only controls for start game, next round, end game, play again, skip
  prompt, score limit, answer timer, max players, and family/party content tier.
- Keep game state server-authoritative. The browser sends compact commands; the
  server validates the current phase, host/judge permissions, hand ownership,
  capacity, settings, timers, and score progression.
- Reconnect after refresh or short connection loss using a per-player guest
  token. Player ids are not treated as secrets.
- Broadcast per-viewer room snapshots over WebSockets. Each player sees only
  their own hand, submitted answers stay hidden during submission, and authors
  stay hidden while the judge is choosing.
- Run from one production container that serves the API, WebSockets, and built
  frontend from the same origin.
- Optionally use Postgres for room ownership and durable active-room snapshots.
  With `DATABASE_URL`, room codes are leased to an instance id, wrong-machine
  API/WebSocket requests return `421 Misdirected Request` plus
  `Fly-Replay: instance=<owner>`, and a new owner can restore a room after a
  graceful release or expired lease.
- Expose health/readiness probes, security headers, origin checks, request-size
  limits, WebSocket message-size limits, keepalive pings, bounded socket send
  queues, idle-room eviction, and local room caps.
- Validate the runtime with Go tests, a frontend production build, a Docker
  build, and `scripts/smoke-realtime.mjs`.
- Create or join a durable daily friend group with a six-character invite code
  and a browser-held membership token that is hashed in Postgres.
- Receive one timezone-aware prompt per group/day, edit an answer until the
  reveal, vote anonymously, then see winners, vote totals, and group streaks.
- Run deadline transitions safely from every app machine. Postgres constraints,
  row locks, and idempotent upserts prevent duplicate rounds and retry damage.
- Generate a spoiler-safe daily result PNG that includes only the round number,
  winner name, and streak—not the submitted answers.
- Serve cards from Postgres (`packs`, `prompt_cards`, `answer_cards`) with the
  seed file as the authoring format and fallback.
- Count how cards perform in play (played/won/skipped) through a non-blocking
  buffered recorder, and surface those counts in the admin card browser.
- Turn daily winners into candidate cards, deduped and vote-ranked, for an admin
  to promote into the community pack. This is the content pipeline; there is no
  AI generation step.
- Let any player report a card mid-game, auto-retire a card at three distinct
  reporters, and moderate the result from a token-gated `/admin` desk.

What is not shipped yet:

- Persistent accounts, user profiles, live match history, or durable live-game
  results. Daily group rounds and results are durable.
- Per-room pack selection (rooms draw from every approved pack), user accounts,
  and therefore user-level bans — moderation acts on content, not people.
- Push or email notifications for daily rounds.
- Payments, app-store mobile clients, or a permanent hosted production URL in
  this repo.

## Portfolio and AI-agent brief

Use this section if you are summarizing the project for a portfolio website,
resume blurb, recruiter note, or another AI agent.

Good one-line summary:

> Punchline is a production-leaning live and asynchronous multiplayer party
> game built with Go, WebSockets, React, Docker, and Postgres.

Accurate bullets:

- Built a server-authoritative Go room engine for a live multiplayer party game,
  including lobby management, rotating judges, timers, answer submission,
  anonymous judging, scoring, reconnects, and host-controlled settings.
- Implemented a custom WebSocket layer with per-viewer redacted room snapshots,
  keepalive pings, bounded send queues, message-size limits, and automatic
  browser reconnection through guest session tokens.
- Added production-oriented deployment hardening: one-container API/WS/UI
  runtime, health/readiness probes, security headers, origin checks, Docker/CI,
  Postgres-backed room snapshots, owner leases, and Fly.io replay routing.
- Built a transactional daily engine with timezone-aware rounds, multi-instance
  deadline workers, scoped guest authorization, anonymous voting, idempotent
  mutations, database invariants, streaks, and spoiler-safe sharing.
- Shipped a database-backed content platform: card telemetry collected without
  blocking the game loop, a player-sourced card pipeline fed by daily winners,
  report-driven auto-retirement, and a token-gated moderation desk.

Evidence map: `docs/RECRUITER_EVIDENCE.md`.

Do not claim these as shipped unless the implementation changes after this
README:

- Do not call it a Next.js or TypeScript app. The current frontend is Vite +
  React JavaScript.
- Do not claim persistent live match history or zero-downtime recovery from every
  failure. Active rooms recover from Postgres snapshots after graceful release
  or lease expiry, while guest sessions remain browser-local.
- Do not claim persistent accounts, live match history, or durable live results.
  Daily mode has durable group-scoped guest memberships and results, not
  accounts. Card content is database-backed and is fine to claim.
- Do not claim an AI content pipeline. Content comes from players via the daily
  candidate queue; there is deliberately no generation step.
- Do not claim user-level moderation or bans. Moderation acts on content only,
  because there are no accounts to ban.
- Do not infer shipped behavior only from `artifacts/`; verify against
  `backend/`, `frontend/`, tests, Docker, and the smoke script.

## Gameplay

- Host creates a room and shares the code or invite link.
- 3+ players join as guests.
- Each round shows one prompt card and rotates one player into the judge role.
- Everyone except the judge plays one answer card from their private hand.
- Cards reveal without authors; the judge picks the funniest.
- The winner scores a point. First to the score limit wins.
- Timers keep rounds moving. If a judge times out, the server chooses a random
  submission so the room does not stall.

## Tech stack

- Backend: Go 1.24, `net/http`, custom WebSocket implementation, in-memory room
  actors, and Postgres via `github.com/jackc/pgx/v5` for production durability.
- Frontend: Vite, React 19, plain JavaScript, mobile-first responsive UI.
- Data: Postgres holds the card content (`packs`, `prompt_cards`,
  `answer_cards`), room leases/snapshots, and durable daily groups, rounds,
  submissions, and votes/results. `seed/cards.json` is the authoring format for
  the launch deck and the fallback when no database is configured.
- Runtime: multi-stage Docker image that builds the frontend and serves API,
  WebSockets, and static UI from one Go process.
- Deployment target: Fly.io config is included, but any container host with
  WebSocket upgrades can run it.

## Repo layout

```txt
backend/     Go API, WebSocket transport, room manager, and game engine
docs/        Deployment, daily engine, content platform, and evidence map
frontend/    Vite + React client with reconnecting guest sessions
migrations/  Postgres schema, including room ownership lease columns
seed/        Launch deck in authoring form; migration 005 loads it into Postgres
scripts/     End-to-end live and daily smoke/deploy gates
Dockerfile   Multi-stage API + UI production image
fly.toml     Fly.io deployment config
artifacts/   Product, architecture, and future-platform planning docs
```

For implementation truth, start with:

- `backend/cmd/api/main.go`: process startup, env vars, Postgres registry wiring.
- `backend/internal/realtime/room.go`: phase machine, permissions, scoring,
  timers, redacted snapshots, guest tokens.
- `backend/internal/realtime/manager.go`: room creation, registry lookups,
  leases, heartbeats, idle eviction, capacity.
- `backend/internal/realtime/registry.go`: shared registry interface and memory
  implementation.
- `backend/internal/roomstore/postgres.go`: Postgres room ownership registry.
- `backend/internal/daily/service.go`: daily identity, persistence, privacy,
  state transitions, idempotent actions, results, and worker.
- `backend/internal/httpapi/daily.go`: daily REST authorization and contracts.
- `backend/internal/httpapi/handler.go`: REST API, WebSocket attach path,
  `Fly-Replay`, CORS, security headers.
- `backend/internal/ws/ws.go`: custom server-side WebSocket implementation.
- `frontend/src/main.jsx`: client session storage, API calls, WebSocket URL,
  reconnection loop, gameplay UI.

## Run locally

Use two terminals for the fast development path.

Backend:

```bash
cd backend
go run ./cmd/api
```

Frontend:

```bash
cd frontend
npm install
npm run dev
```

Open `http://localhost:5173`. Vite proxies `/api` and `/ws` to the backend on
`:8080`.

## Postgres-backed daily mode and room registry

Local dev works without a database. Without `DATABASE_URL`, the server uses an
in-memory live-room registry and should run as one process. Daily mode is
intentionally unavailable because its product contract is durable; use
Postgres to develop or demo `/daily`.

With `DATABASE_URL`, the server reserves each room code in Postgres with an
owning `instance_id`, heartbeat timestamp, and expiry:

```bash
docker compose run --rm migrate
export DATABASE_URL=postgres://punchline:punchline@localhost:5432/punchline?sslmode=disable
cd backend
go run ./cmd/api
```

Docker Compose runs the same checksum-guarded `/app/migrate` binary used by the
production release. Existing databases must run that migration runner before
the new image starts; `004_daily_async.sql` is additive.

Useful registry env vars:

```txt
DATABASE_URL                Enables Postgres ownership and durable state.
PUNCHLINE_INSTANCE_ID       Overrides the process instance id.
PUNCHLINE_REQUIRE_DATABASE  Fails startup when production DB is missing.
ROOM_LEASE_TTL              Active-room lease duration, default 90s.
ROOM_HEARTBEAT_INTERVAL     Lease heartbeat interval, default 15s.
DB_MAX_OPEN_CONNS           Postgres pool cap, default 10.
DB_MAX_IDLE_CONNS           Postgres idle pool cap, default 5.
DAILY_WORKER_INTERVAL       Daily transition scan interval, default 1m.
PUNCHLINE_DECK_SOURCE       auto (default), database, or seed. auto prefers
                            Postgres and falls back to seed/cards.json.
PUNCHLINE_ADMIN_TOKEN       Enables the /admin desk. Unset = desk returns 404.
PUNCHLINE_REPORT_LIMIT_PER_MIN  Card reports per client per minute, default 20.
CARD_TELEMETRY_FLUSH_INTERVAL   Card counter flush interval, default 5s.
```

### Admin desk

With `PUNCHLINE_ADMIN_TOKEN` set, `/admin` opens a moderation desk: the report
queue, the candidate queue fed by daily winners, and a card browser sorted by
play/win/skip telemetry. Unset the variable and every admin route 404s.

See [docs/CONTENT_PLATFORM.md](docs/CONTENT_PLATFORM.md) for how cards enter the
deck, earn their place, and get retired.

### Where cards come from

`seed/cards.json` is the authoring format for the launch deck. Migration
`005_seed_official_pack.sql` loads it into `packs`/`prompt_cards`/`answer_cards`,
after which the running process reads cards from Postgres and each card carries
its database row id. The seed file stays as the fallback so local dev and
database-less runs still work.

The migration is generated, not hand-written. After editing `seed/cards.json`:

```bash
node scripts/generate-card-seed-migration.mjs
```

CI regenerates it and fails on drift. Once migration 005 has been applied
anywhere, the runner rejects edits to it by checksum — later content changes
belong in a new migration, or in the admin desk once that exists.

## Run the production image locally

Docker Compose builds the container, starts Postgres, runs release migrations,
and serves everything from one origin:

```bash
docker compose up --build -d app
```

Open `http://localhost:8080`.

Run the deploy check against the production-shaped container:

```bash
node scripts/deploy-check.mjs http://localhost:8080
```

The check verifies the React app shell and built assets, `/readyz`, `/metrics`,
the complete live WebSocket round, and the daily create/join/today/submit/privacy
flow. Its temporary daily group is deleted at the end.

## Demo deployment checklist

Before sharing a demo link, run the same checks a reviewer will care about:

```bash
cd backend
go test ./...

cd ../frontend
npm run build

cd ..
docker build -t punchline .
node --check scripts/smoke-daily.mjs
```

Run the production image locally with `docker compose up --build -d app`; the
daily engine deliberately does not start in memory-only mode. See
`docs/DEPLOYMENT.md` for the exact production-shaped local and Fly flows.

Open `http://localhost:8080`, create a room, join from two more tabs or
phones, start a round, submit answers, pick a winner, and advance to the next
round.

Run the automated deploy check against the same local image:

```bash
node scripts/deploy-check.mjs http://localhost:8080
```

## Fast public demo on Render

This is the quickest free path to a shareable URL for the current demo. It
deploys the existing Dockerfile as one Render web service using `render.yaml`.
No database is required for a live-room-only single-instance demo. The `/daily`
route will report that Postgres is required, so use Fly or attach managed
Postgres before presenting both modes.

1. Push this repo to GitHub.
2. In Render, click **New** > **Blueprint**.
3. Connect the GitHub repo and select the branch with `render.yaml`.
4. Review the `punchline-demo` free web service and click **Deploy Blueprint**.
5. Open the generated `https://punchline-demo-....onrender.com` URL.
6. Run:

```bash
node scripts/deploy-check.mjs https://<your-render-app>.onrender.com
```

Free Render services can cold-start after being idle. Open the URL once before
sharing it so the first reviewer does not wait through the spin-up page.

## Deploy on Fly.io

```bash
fly launch --copy-config --no-deploy
fly deploy
fly status
node scripts/deploy-check.mjs https://<your-app>.fly.dev
```

For the current demo, run one machine unless `DATABASE_URL` is configured:

```bash
fly scale count 1
```

After `fly deploy`, verify the public URL the same way: open the page, create a
room, join two more players, complete a round, and run the deploy check above
against the Fly URL.

With `DATABASE_URL` configured, Fly can replay wrong-machine traffic to the
owning machine because the server returns `Fly-Replay: instance=<owner>` before
the WebSocket upgrade. Active state is snapshotted in Postgres, graceful
shutdown releases leases for immediate recovery, and an ungraceful owner loss
can be claimed after the room lease expires.

Without `DATABASE_URL`, keep the app at one machine. The fallback registry is
memory-only.

Production env vars worth setting before sharing a public link:

```txt
DATABASE_URL                 Postgres ownership and active-room state store.
PUNCHLINE_ALLOWED_ORIGINS    Optional comma-separated extra browser origins.
PUNCHLINE_REQUIRE_DATABASE   Set true for production.
PUNCHLINE_METRICS_TOKEN      Optional bearer token for /metrics.
PUNCHLINE_TRUST_PROXY_HEADERS
                             Trust Fly-Client-IP/X-Real-IP/X-Forwarded-For from any peer.
PUNCHLINE_TRUSTED_PROXY_CIDRS
                             Comma-separated proxy CIDRs allowed to set client IP headers.
MAX_LOCAL_ROOMS              Per-process room cap; Fly default is 500.
ROOM_IDLE_TTL                Idle empty-room eviction window; Fly default is 20m.
DB_MAX_OPEN_CONNS            Postgres pool cap, default 10.
```

## Tests and verification

```bash
cd backend
go vet ./...
go build ./...
go test ./...

cd ../frontend
npm run build

cd ..
docker build -t punchline .
node scripts/smoke-realtime.mjs http://localhost:8080
node scripts/smoke-daily.mjs http://localhost:8080
```

GitHub Actions (`.github/workflows/ci.yml`) runs backend vet/build/tests,
frontend build, and Docker image validation on push and pull request.

## HTTP and WebSocket contract

```txt
GET  /healthz
GET  /readyz
POST /api/rooms                 -> room snapshot
POST /api/rooms/{code}/join     -> { player, token, room }
GET  /api/rooms/{code}          -> room snapshot
GET  /ws/rooms/{code}?player_id=...&token=...   WebSocket
POST /api/daily/groups                  -> { group, membership, token }
POST /api/daily/groups/{code}/join      -> { group, membership, token }
GET  /api/daily/groups/{code}/today     -> today, previous result, streak
POST /api/daily/rounds/{id}/submit      -> idempotent create/update
POST /api/daily/rounds/{id}/vote        -> idempotent create/change
DELETE /api/daily/groups/{code}         -> owner-only cascade delete
```

Client WebSocket messages:

```json
{"type":"start_game"}
{"type":"submit_answer","answer_card_id":"..."}
{"type":"pick_winner","submission_id":"..."}
{"type":"skip_prompt"}
{"type":"next_round"}
{"type":"end_game"}
{"type":"play_again"}
{"type":"update_settings","settings":{
  "score_limit":3,
  "round_seconds":60,
  "max_players":8,
  "content_tier":"family"
}}
```

Host-only commands: `start_game`, `skip_prompt`, `next_round`, `end_game`,
`play_again`, and `update_settings`.

Judge-only command: `pick_winner`.

Answerer command: `submit_answer`.

The server pushes:

```json
{"type":"room_state","room":{...}}
```

Room snapshots are redacted for each viewer. Players only see their own hand;
submission content is hidden during the answer phase; authors are hidden during
judging.

## Roadmap

Likely next slices are persistent live results, optional cross-device accounts,
DB-backed deck loading, content moderation/reporting, notifications, and a
public hosted demo that matches the exact runtime described here.
