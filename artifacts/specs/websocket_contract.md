# WebSocket Contract

Endpoint:

```txt
/ws/rooms/{code}?player_id=...&token=...
```

## Client → Server commands

### Start game

```json
{
  "type": "START_GAME",
  "requestId": "uuid"
}
```

### Submit answer

```json
{
  "type": "SUBMIT_ANSWER",
  "requestId": "uuid",
  "payload": {
    "roundId": "uuid",
    "answerCardIds": ["uuid"]
  }
}
```

### Pick winner

```json
{
  "type": "PICK_WINNER",
  "requestId": "uuid",
  "payload": {
    "roundId": "uuid",
    "submissionId": "uuid"
  }
}
```

### Advance round

```json
{
  "type": "ADVANCE_ROUND",
  "requestId": "uuid"
}
```

### End game

```json
{
  "type": "END_GAME",
  "requestId": "uuid"
}
```

## Server → Client events

### Room snapshot

```json
{
  "type": "ROOM_SNAPSHOT",
  "payload": {
    "roomId": "uuid",
    "code": "ABCD",
    "phase": "SUBMITTING",
    "players": [],
    "scores": [],
    "currentRound": {},
    "myHand": []
  }
}
```

### Player joined

```json
{
  "type": "PLAYER_JOINED",
  "payload": {
    "playerId": "uuid",
    "displayName": "Player 2"
  }
}
```

### Round started

```json
{
  "type": "ROUND_STARTED",
  "payload": {
    "roundId": "uuid",
    "judgePlayerId": "uuid",
    "prompt": {
      "id": "uuid",
      "text": "The startup failed because of ___."
    }
  }
}
```

### Submission received

```json
{
  "type": "SUBMISSION_RECEIVED",
  "payload": {
    "roundId": "uuid",
    "playerId": "uuid"
  }
}
```

### Submissions revealed

```json
{
  "type": "SUBMISSIONS_REVEALED",
  "payload": {
    "roundId": "uuid",
    "submissions": []
  }
}
```

### Winner picked

```json
{
  "type": "WINNER_PICKED",
  "payload": {
    "roundId": "uuid",
    "winningSubmissionId": "uuid",
    "winnerPlayerId": "uuid"
  }
}
```

### Error

```json
{
  "type": "ERROR",
  "requestId": "uuid",
  "payload": {
    "code": "INVALID_PHASE",
    "message": "Cannot submit answer during judging phase."
  }
}
```

## Design notes

- All client commands should have `requestId` for idempotency/debugging.
- Server should send a full snapshot after reconnect.
- Server should validate phase, identity, and room membership for every command.
