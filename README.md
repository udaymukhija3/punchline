# Punchline

Punchline is a web-first, real-time multiplayer fill-in-the-blank party game.
Players create a live room, share a 4-character code or invite link, and play
from any phone or browser without installing an app.

It is also a backend systems project: a server-authoritative Go game engine,
custom WebSocket transport, reconnectable guest sessions, a mobile-first React
client, a single-container production build, and an optional Postgres-backed
room ownership registry for multi-machine routing.

## Current state

This repository currently ships a playable v0 live game. It is not yet the full
long-term platform described in some planning docs under `artifacts/`.

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
- Optionally use Postgres as a shared room ownership registry. With
  `DATABASE_URL`, room codes are leased to an instance id so wrong-machine API
  and WebSocket requests can return `421 Misdirected Request` plus
  `Fly-Replay: instance=<owner>` before upgrade.
- Expose health/readiness probes, security headers, origin checks, request-size
  limits, WebSocket message-size limits, keepalive pings, bounded socket send
  queues, idle-room eviction, and local room caps.
- Validate the runtime with Go tests, a frontend production build, a Docker
  build, and `scripts/smoke-realtime.mjs`.

What is not shipped yet:

- Full durable room-state recovery after the owner process restarts.
- Persistent accounts, user profiles, match history, or durable game results.
- Runtime database-backed card/deck loading. The current game loads
  `seed/cards.json` at startup; the SQL schema and planning docs include future
  content tables.
- Async daily mode, public content packs, moderation workflow, reporting UI, or
  AI-assisted card generation.
- Payments, app-store mobile clients, or a permanent hosted production URL in
  this repo.

## Portfolio and AI-agent brief

Use this section if you are summarizing the project for a portfolio website,
resume blurb, recruiter note, or another AI agent.

Good one-line summary:

> Punchline is a production-leaning real-time multiplayer party game built with
> Go, WebSockets, React, Docker, and optional Postgres room ownership routing.

Accurate bullets:

- Built a server-authoritative Go room engine for a live multiplayer party game,
  including lobby management, rotating judges, timers, answer submission,
  anonymous judging, scoring, reconnects, and host-controlled settings.
- Implemented a custom WebSocket layer with per-viewer redacted room snapshots,
  keepalive pings, bounded send queues, message-size limits, and automatic
  browser reconnection through guest session tokens.
- Added production-oriented deployment hardening: one-container API/WS/UI
  runtime, health/readiness probes, security headers, origin checks, Docker/CI,
  smoke testing, and optional Postgres room ownership leases for Fly.io replay
  routing.

Do not claim these as shipped unless the implementation changes after this
README:

- Do not call it a Next.js or TypeScript app. The current frontend is Vite +
  React JavaScript.
- Do not claim durable recovery of active games after server restart. Active
  gameplay state is still in memory on the owning process.
- Do not claim persistent accounts, match history, durable game results, or
  database-backed deck serving.
- Do not claim the async daily mode, moderation workflow, reporting UI, or
  AI-assisted content pipeline is live. Those are planning/backlog surfaces.
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
  actors, optional Postgres registry via `github.com/jackc/pgx/v5`.
- Frontend: Vite, React 19, plain JavaScript, mobile-first responsive UI.
- Data: `seed/cards.json` for current gameplay; Postgres migrations for room
  ownership leases and future content/game tables.
- Runtime: multi-stage Docker image that builds the frontend and serves API,
  WebSockets, and static UI from one Go process.
- Deployment target: Fly.io config is included, but any container host with
  WebSocket upgrades can run it.

## Repo layout

```txt
backend/     Go API, WebSocket transport, room manager, and game engine
frontend/    Vite + React client with reconnecting guest sessions
migrations/  Postgres schema, including room ownership lease columns
seed/        Prompt and answer card seed deck loaded by the server at startup
scripts/     End-to-end real-time smoke test
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

## Optional Postgres room registry

Local dev works without a database. Without `DATABASE_URL`, the server uses an
in-memory room registry and should run as one process.

With `DATABASE_URL`, the server reserves each room code in Postgres with an
owning `instance_id`, heartbeat timestamp, and expiry:

```bash
docker compose up -d postgres
export DATABASE_URL=postgres://punchline:punchline@localhost:5432/punchline?sslmode=disable
cd backend
go run ./cmd/api
```

Fresh Docker Compose databases apply `migrations/` automatically. Existing
databases must have `migrations/002_room_instance_leases.sql` applied before
turning on `DATABASE_URL`.

Useful registry env vars:

```txt
DATABASE_URL             Enables the Postgres room registry.
PUNCHLINE_INSTANCE_ID    Overrides the process instance id.
ROOM_LEASE_TTL           Active-room lease duration, default 6h.
DB_MAX_OPEN_CONNS        Postgres pool cap, default 10.
DB_MAX_IDLE_CONNS        Postgres idle pool cap, default 5.
```

## Run the production image locally

The container builds the frontend, compiles the Go server, and serves
everything from one origin:

```bash
docker build -t punchline .
docker run -p 8080:8080 punchline
```

Open `http://localhost:8080`.

Run the real-time smoke test against any running instance:

```bash
node scripts/smoke-realtime.mjs http://localhost:8080
```

The smoke creates a room, joins three players, opens three authenticated
WebSockets, starts a round, submits answers, picks a winner, advances the next
round, checks judge rotation, and prints phase-transition timings.

## Demo deployment checklist

Before sharing a demo link, run the same checks a reviewer will care about:

```bash
cd backend
go test ./...

cd ../frontend
npm run build

cd ..
docker build -t punchline .
```

Run the production image locally:

```bash
docker run --rm -p 8080:8080 punchline
```

Open `http://localhost:8080`, create a room, join from two more tabs or
phones, start a round, submit answers, pick a winner, and advance to the next
round.

Run the automated smoke test against the same local image:

```bash
node scripts/smoke-realtime.mjs http://localhost:8080
```

## Fast public demo on Render

This is the quickest free path to a shareable URL for the current demo. It
deploys the existing Dockerfile as one Render web service using `render.yaml`.
No database is required for a single-instance demo.

1. Push this repo to GitHub.
2. In Render, click **New** > **Blueprint**.
3. Connect the GitHub repo and select the branch with `render.yaml`.
4. Review the `punchline-demo` free web service and click **Deploy Blueprint**.
5. Open the generated `https://punchline-demo-....onrender.com` URL.
6. Run:

```bash
node scripts/smoke-realtime.mjs https://<your-render-app>.onrender.com
```

Free Render services can cold-start after being idle. Open the URL once before
sharing it so the first reviewer does not wait through the spin-up page.

## Deploy on Fly.io

```bash
fly launch --copy-config --no-deploy
fly deploy
fly status
node scripts/smoke-realtime.mjs https://<your-app>.fly.dev
```

For the current demo, run one machine unless `DATABASE_URL` is configured:

```bash
fly scale count 1
```

After `fly deploy`, verify the public URL the same way: open the page, create a
room, join two more players, complete a round, and run the smoke script above
against the Fly URL.

With `DATABASE_URL` configured, Fly can replay wrong-machine traffic to the
owning machine because the server returns `Fly-Replay: instance=<owner>` before
the WebSocket upgrade. This prevents split-brain room creation across machines.

Without `DATABASE_URL`, keep the app at one machine. The fallback registry is
memory-only.

Even with Postgres room ownership enabled, active gameplay state still lives in
the owning process. A restart of that owner ends its active rooms until durable
room-state restore is implemented.

Production env vars worth setting before sharing a public link:

```txt
DATABASE_URL                 Postgres room registry for multi-machine routing.
PUNCHLINE_ALLOWED_ORIGINS    Optional comma-separated extra browser origins.
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

The most important next production step is durable active-room recovery: persist
enough room state and command history for an owner-machine restart to restore a
live game, not only route traffic to the current owner.

After that, likely next slices are persistent results, accounts, DB-backed deck
loading, content moderation/reporting, async daily rooms, and a public hosted
demo that matches the exact runtime described here.
