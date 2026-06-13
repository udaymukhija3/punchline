# Codex Build Prompt

I want to build PUNCHLINE, a portfolio-grade real-time multiplayer party game platform.

Use the artifact pack as the source of truth. Start with v0 only.

## v0 goal

Build a working live room engine:

- Host creates room
- Players join by code/link as guests
- WebSocket room connection
- Server-authoritative room state
- Room actor per active room
- Game phases: lobby, dealing, submitting, revealing, judging, scoring, finished
- Seed prompt/answer cards
- Deal hands
- Players submit answer cards
- Judge picks winner
- Score updates
- Judge rotates
- Full room snapshot after reconnect

## Stack

Backend:

- Go
- PostgreSQL
- SQL migrations
- WebSockets
- Clean internal packages

Frontend:

- Next.js
- TypeScript
- Tailwind
- Mobile-first room UI

## Constraints

Do not implement v1/v2 yet except for database fields that do not complicate v0.
Do not add AI generation yet.
Do not add accounts yet.
Do not over-engineer with Redis yet.
Clients should send commands; server validates and owns game state.

## Deliverables

1. Repo structure
2. Database migrations
3. Go backend with REST + WebSocket endpoints
4. Room actor implementation
5. Game state machine
6. Frontend room flow
7. Basic seed deck
8. README with local run instructions
9. Minimal tests for state transitions
