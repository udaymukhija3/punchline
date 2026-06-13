# REST API Contract

Base path: `/api`

## Health

### GET `/healthz`

Returns service health.

## Rooms

### POST `/rooms`

Create a new room.

Request:

```json
{}
```

Response:

```json
{
  "code": "ABCD",
  "phase": "lobby",
  "players": []
}
```

### POST `/rooms/{code}/join`

Join a room as a guest.

Request:

```json
{
  "name": "Player 2"
}
```

Response:

```json
{
  "player": {
    "id": "pl_...",
    "name": "Player 2"
  },
  "token": "gt_...",
  "room": {
    "code": "ABCD",
    "phase": "lobby"
  }
}
```

### GET `/rooms/{code}`

Fetch room snapshot. Useful before WebSocket connects or after reconnect.

## Cards / Packs

### GET `/packs`

List available packs.

### GET `/packs/{packId}`

Fetch pack details.

## Daily mode — v1

### POST `/daily/groups`

Create daily group.

### POST `/daily/groups/{groupId}/join`

Join daily group.

### GET `/daily/groups/{groupId}/today`

Fetch today's prompt/round.

### POST `/daily/rounds/{roundId}/submissions`

Submit answer.

### POST `/daily/rounds/{roundId}/votes`

Vote for answer.

## Content platform — v2

### POST `/generation/jobs`

Create AI generation job.

### GET `/generation/jobs/{jobId}`

Fetch generation job status.

### POST `/cards/{cardId}/report`

Report card.

### POST `/cards/{cardId}/rate`

Rate card.
