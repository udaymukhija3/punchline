# Real-Time Engine

## Core principle

The server owns the game. The client only sends player intent.

## Room lifecycle

```txt
CREATED
→ LOBBY
→ DEALING
→ SUBMITTING
→ REVEALING
→ JUDGING
→ SCORING
→ NEXT_ROUND
→ FINISHED
```

## Room actor responsibilities

The room actor owns:

- current phase
- players
- connected sockets
- hands
- current prompt
- submissions
- current judge
- scores
- timers
- host controls
- broadcasts

## Commands into room actor

```txt
JoinRoom
LeaveRoom
ReconnectPlayer
StartGame
SubmitAnswer
PickWinner
AdvanceRound
EndGame
SendChatOrReaction
```

## Events broadcast to clients

```txt
RoomSnapshot
PlayerJoined
PlayerLeft
GameStarted
RoundStarted
PromptRevealed
SubmissionReceived
AllSubmissionsIn
SubmissionsRevealed
WinnerPicked
ScoreUpdated
RoundEnded
GameEnded
ErrorEvent
```

## Reconnection model

Each guest gets:

- `player_id`
- `room_id`
- `guest_token`

Store token in an httpOnly cookie or local storage for v0 simplicity.

On reconnect:

1. Client opens WebSocket with room code and token.
2. Server validates player belongs to room.
3. Room actor attaches new socket to existing player.
4. Server sends full `RoomSnapshot`.

## State sync strategy

Use event broadcasts for normal flow, but always support a full snapshot.

This avoids fragile client-side recovery.

## Phase validation examples

- A player cannot submit unless phase is `SUBMITTING`.
- Judge cannot submit unless game mode allows it.
- Non-judge cannot pick winner.
- Same player cannot submit twice.
- Winner cannot be picked before reveal/judging phase.

## Timers

v0 can keep timers simple:

- optional submission timer
- optional judging timer
- host can manually advance if stuck

Do not make timers complicated until the room loop works.
