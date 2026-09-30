# Sudux Real-Time WebSocket Protocol

WebSocket Endpoint: `ws://<host>:8081/ws`

Authentication: send the access JWT in the `Authorization: Bearer <token>`
header when opening the socket (Godot supports custom headers on
WebSocketPeer requests). `?token=<JWT>` is still accepted for backwards
compatibility, but the header is preferred since URLs end up in access logs.
On failure the server upgrades, sends one `error` message (`UNAUTHORIZED` or
`TOKEN_EXPIRED`) and closes the connection.

## Protocol Envelope
All WebSocket messages use a standard JSON envelope:

```json
{
  "type": "<message_type>",
  "request_id": "<client_generated_id>",
  "payload": {}
}
```

---

## Client Messages

### 1. `game.sync`
Requests complete authoritative state synchronization upon initial connection or reconnection.

```json
{
  "type": "game.sync",
  "request_id": "req-001",
  "payload": {
    "match_id": "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"
  }
}
```

### 2. `game.move`
Submits a cell value choice for validation.

```json
{
  "type": "game.move",
  "request_id": "move-102",
  "payload": {
    "cell": 42,
    "value": 7
  }
}
```

Move throttling: a player must wait at least `MOVE_MIN_INTERVAL` (default
250ms) between moves, and `WRONG_MOVE_COOLDOWN` (default 2s) after a wrong
answer. Wrong answers subtract `WRONG_MOVE_PENALTY` points (default 50, score
floors at 0) and reset the combo. Errors: `MOVE_FAILED` with the reason
("moving too fast", "wrong answer cooldown active", "duplicate request_id",
"cell already solved", ...).

### 3. `ping`
Optional app-level keepalive; the server replies with `pong`.
---

## Server Events

### 1. `game.state`
Transmits the current authoritative board state and player scores.

```json
{
  "type": "game.state",
  "payload": {
    "match_id": "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
    "status": "playing",
    "board_state": [0, 5, 0, ...],
    "cell_owners": ["", "player-uuid-1", ""],
    "participants": {
      "player-uuid-1": {
        "score": 310,
        "combo": 3,
        "solved_cells": 3,
        "connected": true
      }
    }
  }
}
```

### 2. `game.move_result`
Broadcast when a player or bot executes a move.

```json
{
  "type": "game.move_result",
  "payload": {
    "cell": 42,
    "value": 7,
    "is_correct": true,
    "player_id": "player-uuid-1",
    "team_id": "team-uuid-1",
    "points": 120,
    "new_combo": 3,
    "total_score": 310
  }
}
```

### 3. `error`
Sent when a command fails validation or processing.

```json
{
  "type": "error",
  "request_id": "move-102",
  "payload": {
    "code": "CELL_ALREADY_SOLVED",
    "message": "Cell 42 has already been solved by another player"
  }
}
```

Common codes: `UNAUTHORIZED`, `TOKEN_EXPIRED`, `FORBIDDEN` (not a participant
in this match), `NOT_SYNCED` (send `game.sync` first), `ROOM_NOT_FOUND`,
`MATCH_NOT_ACTIVE`, `MOVE_FAILED`.

### 4. `game.finished`
Broadcast to all participants when the match ends (puzzle complete, deadline
expired, or abandoned). `payload.state` carries the final `game.state`-shaped
snapshot.

```json
{
  "type": "game.finished",
  "payload": {
    "match_id": "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
    "reason": "puzzle_complete",
    "winners": "team-uuid-1",
    "state": { }
  }
}
```

