# Sudux Real-Time WebSocket Protocol

WebSocket Endpoint: `ws://<host>:8081/ws?token=<JWT_ACCESS_TOKEN>`

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
