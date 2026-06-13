# Architecture

## Recommended stack

### Backend

- Go
- PostgreSQL
- WebSockets
- Redis later, only when scaling across multiple backend instances
- Background worker for AI/content jobs in v2

### Frontend

- Next.js
- TypeScript
- Tailwind CSS
- WebSocket client store
- Mobile-first responsive UI

### Deployment

For portfolio/demo:

- Frontend: Vercel or single deployed Next.js service
- Backend: Render/Fly.io/Railway
- Database: managed PostgreSQL
- Redis: optional later

## High-level architecture

```txt
Browser Client
   |
   | REST: create room, join room, fetch decks
   | WS: live game state/events
   v
Go Backend
   |
   |-- HTTP API Layer
   |-- WebSocket Gateway
   |-- Room Manager
   |-- Room Actors
   |-- Game State Machine
   |-- Content Service
   |-- Persistence Layer
   v
PostgreSQL
```

## Core backend modules

```txt
/backend
  /cmd/api
  /internal/http
  /internal/ws
  /internal/rooms
  /internal/game
  /internal/cards
  /internal/daily
  /internal/moderation
  /internal/db
  /internal/auth
```

## Core frontend modules

```txt
/frontend
  /app
    /room/[code]
    /daily/[groupId]
  /components
    RoomLobby
    PlayerHand
    PromptCard
    SubmissionReveal
    Scoreboard
  /lib
    api.ts
    websocket.ts
    room-store.ts
```

## Key architectural decision

Live rooms should be controlled by server-side actors, not client-side state.

Each active room gets a single in-memory controller:

```txt
RoomActor(room_id)
  receives commands
  validates phase rules
  mutates canonical state
  persists important events
  broadcasts snapshots/events
```

Clients are dumb renderers. They send intents. The server decides what is valid.

## Scaling path

### Single instance

- In-memory room actors
- WebSockets connected directly to backend instance
- PostgreSQL for durable storage

This is enough for portfolio and early product validation.

### Multi-instance later

- Sticky sessions or room-to-instance routing
- Redis pub/sub for fan-out
- Redis/DB-backed room recovery
- Dedicated room process/service if needed

Do not introduce Redis too early unless it solves a real problem.
