# Build Plan

## Phase 0 — Repo setup

- Monorepo with `/backend` and `/frontend`
- Docker Compose with PostgreSQL
- SQL migrations
- Health check endpoint
- CI for build/test

## Phase 1 — Cards and rooms via REST

- Seed prompt cards and answer cards
- Create room endpoint
- Join room endpoint
- Fetch room snapshot endpoint
- Guest player token/session

## Phase 2 — WebSocket gateway

- WebSocket connect endpoint
- Authenticate guest token
- Attach socket to room
- Broadcast basic room snapshot
- Handle disconnect/reconnect

## Phase 3 — Room actor

- One actor per active room
- Command channel
- Phase transitions
- Player registry
- Host controls

## Phase 4 — Full game loop

- Start game
- Deal hands
- Start round
- Submit answers
- Reveal submissions
- Judge picks winner
- Score updates
- Rotate judge
- Next round
- End game

## Phase 5 — Persistence

- Persist rooms
- Persist players
- Persist rounds
- Persist submissions
- Persist winners/scores
- Restore room snapshot after refresh/reconnect

## Phase 6 — Frontend polish

- Lobby UI
- Player hand
- Prompt display
- Submission state
- Reveal screen
- Judge picking UI
- Scoreboard
- Mobile-first layout

## Phase 7 — Daily mode

- Daily groups
- Daily prompt
- Submission window
- Reveal/vote
- Streaks
- Share card

## Phase 8 — Content platform

- Packs
- AI generation job table
- Admin approval
- Card ratings/reports
- Retire bad cards

## First demo target

A recruiter or friend should be able to open a link, create a room, share a code, play three rounds, refresh mid-game, reconnect, and continue.
