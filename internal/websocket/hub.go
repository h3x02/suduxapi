package websocket

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/h3x02/suduxapi/internal/game"
	"github.com/h3x02/suduxapi/internal/match"
	"github.com/h3x02/suduxapi/internal/token"
)

type Hub struct {
	mu           sync.RWMutex
	sessions     map[string]*ClientSession // playerID -> session
	rooms        map[string]*game.ActiveRoom
	register     chan *ClientSession
	unregister   chan *ClientSession
	matchRepo    *match.Repository
	jwtAccessSec string
}

func NewHub(matchRepo *match.Repository, jwtAccessSec string) *Hub {
	return &Hub{
		sessions:     make(map[string]*ClientSession),
		rooms:        make(map[string]*game.ActiveRoom),
		register:     make(chan *ClientSession),
		unregister:   make(chan *ClientSession),
		matchRepo:    matchRepo,
		jwtAccessSec: jwtAccessSec,
	}
}

func (h *Hub) Run() {
	for {
		select {
		case session := <-h.register:
			h.mu.Lock()
			h.sessions[session.PlayerID] = session
			h.mu.Unlock()
		case session := <-h.unregister:
			h.mu.Lock()
			if existing, ok := h.sessions[session.PlayerID]; ok && existing == session {
				delete(h.sessions, session.PlayerID)
			}
			h.mu.Unlock()
		}
	}
}

func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	tokenStr := r.URL.Query().Get("token")
	claims, err := token.ParseAndValidateToken(tokenStr, h.jwtAccessSec, "access")
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type": "error",
			"payload": map[string]string{
				"code":    "UNAUTHORIZED",
				"message": "Invalid authentication token",
			},
		})
		conn.Close()
		return
	}

	session := &ClientSession{
		PlayerID: claims.PlayerID,
		conn:     conn,
		send:     make(chan []byte, 256),
		hub:      h,
	}

	h.register <- session

	go session.writePump()
	go session.readPump()
}

func (h *Hub) handleMessage(session *ClientSession, env *MessageEnvelope) {
	switch env.Type {
	case "game.sync":
		var payload struct {
			MatchID string `json:"match_id"`
		}
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			session.sendError(env.RequestID, "INVALID_PAYLOAD", "Invalid sync payload")
			return
		}

		session.MatchID = payload.MatchID
		h.syncPlayer(session, payload.MatchID)

	case "game.move":
		var payload struct {
			Cell  int `json:"cell"`
			Value int `json:"value"`
		}
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			session.sendError(env.RequestID, "INVALID_PAYLOAD", "Invalid move payload")
			return
		}

		h.mu.RLock()
		room, ok := h.rooms[session.MatchID]
		h.mu.RUnlock()

		if !ok {
			session.sendError(env.RequestID, "ROOM_NOT_FOUND", "Active match room not found")
			return
		}

		res, err := room.ProcessMove(env.RequestID, session.PlayerID, payload.Cell, payload.Value)
		if err != nil {
			session.sendError(env.RequestID, "MOVE_FAILED", err.Error())
			return
		}

		h.broadcastToRoom(room, "game.move_result", res)

	case "ping":
		resp, _ := json.Marshal(map[string]string{"type": "pong"})
		session.send <- resp
	}
}

func (h *Hub) syncPlayer(session *ClientSession, matchIDStr string) {
	mID, err := uuid.Parse(matchIDStr)
	if err != nil {
		session.sendError("", "INVALID_MATCH_ID", "Invalid match ID")
		return
	}

	h.mu.Lock()
	room, ok := h.rooms[matchIDStr]
	if !ok {
		m, err := h.matchRepo.GetMatchByID(context.Background(), mID)
		if err != nil {
			h.mu.Unlock()
			session.sendError("", "MATCH_NOT_FOUND", "Match not found in database")
			return
		}
		room = game.NewActiveRoom(m, h.matchRepo)
		room.OnStateChange = func(rm *game.ActiveRoom, eventType string, payload interface{}) {
			h.broadcastToRoom(rm, eventType, payload)
		}
		h.rooms[matchIDStr] = room

		// Trigger bot runner loop if match contains a bot team
		room.StartBotRunner(context.Background())
	}
	h.mu.Unlock()

	// Update player connection status
	if pState, ok := room.Participants[session.PlayerID]; ok {
		pState.Connected = true
	}

	// Send current game state sync
	syncResp := map[string]interface{}{
		"type": "game.state",
		"payload": map[string]interface{}{
			"match_id":     room.MatchID.String(),
			"status":       room.Match.Status,
			"board_state":  room.BoardState,
			"cell_owners":  room.CellOwners,
			"participants": room.Participants,
			"teams":        room.Match.Teams,
		},
	}
	bytes, _ := json.Marshal(syncResp)
	session.send <- bytes
}

func (h *Hub) broadcastToRoom(room *game.ActiveRoom, eventType string, payload interface{}) {
	msg := map[string]interface{}{
		"type":    eventType,
		"payload": payload,
	}
	bytes, _ := json.Marshal(msg)

	h.mu.RLock()
	defer h.mu.RUnlock()

	for pID := range room.Participants {
		if sess, ok := h.sessions[pID]; ok {
			select {
			case sess.send <- bytes:
			default:
			}
		}
	}
}
