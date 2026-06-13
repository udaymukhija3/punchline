# MVP Scope

## v0 — Live Game Engine

### Goal

A full live game can be played start-to-finish in a browser room.

### User story

1. Host creates a room.
2. Host gets a room code/link.
3. Players join as guests.
4. Host starts game.
5. Prompt appears.
6. Players submit answer cards.
7. Judge picks winner.
8. Score updates.
9. Next round starts.
10. Players can refresh/reconnect without killing the game.

### v0 includes

- Guest player identity
- Room creation
- Join by code/link
- WebSocket connection per player
- Server-authoritative room state
- In-memory room actor per active room
- PostgreSQL persistence for rooms, players, cards, rounds, results
- Prompt and answer cards from a seed deck
- Rotating judge
- Submissions
- Scoring
- Reconnection
- Host controls: start game, next round, end game
- Basic room settings: max players, score limit, content tier

### v0 excludes

- AI generation
- AI judge
- solo mode
- audience mode
- daily mode
- accounts
- payment
- marketplace
- UGC packs
- complex moderation
- friend graph
- global profiles

## v1 — Daily / Async Engine

### Goal

A group gets one prompt per day, submits asynchronously, reveals answers, votes, and tracks streaks.

### Includes

- Daily group creation
- Daily prompt generation/selection
- Submit before reveal time
- Reveal when all have submitted or timer expires
- Voting
- Daily winner
- Streaks
- Spoiler-safe share card
- Guest-to-account claiming

## v2 — Content Platform

### Goal

The game stops depending on a static deck.

### Includes

- Pack model
- Card source/provenance
- AI generation jobs
- Draft/generated cards
- Safety filtering
- Admin approval
- Community packs
- Rating/reporting
- Card retirement
- Pack install into room

## v3 — Expansion

Only after v0-v2 are solid.

- AI opponents
- AI judge
- audience/stream mode
- creator marketplace
- company/event rooms
- branded packs
- analytics dashboard
