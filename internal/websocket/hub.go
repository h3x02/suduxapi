package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/h3x02/suduxapi/internal/config"
	"github.com/h3x02/suduxapi/internal/game"
	"github.com/h3x02/suduxapi/internal/logger"
	"github.com/h3x02/suduxapi/internal/match"
	"github.com/h3x02/suduxapi/internal/token"
	"github.com/redis/go-redis/v9"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Mobile app: no browser origin check
	},
}

type Hub struct {
	mu          sync.RWMutex
	sessions    map[string]*ClientSession // playerID -> session
	rooms       map[string]*roomEntry
	register    chan *ClientSession
	unregister  chan *ClientSession

	matchRepo   *match.Repository
	cfg         *config.Config
	redisClient *redis.Client
}

// roomEntry wraps a room with metadata needed for eviction and janitor checks.
type roomEntry struct {
	room        *game.ActiveRoom
	finishedAt  time.Time // zero while the match is still running
	lastAlerted time.Time
}

func NewHub(matchRepo *match.Repository, cfg *config.Config) *Hub {
	return &Hub{
		sessions:   make(map[string]*ClientSession),
		rooms:      make(map[string]*roomEntry),
		register:   make(chan *ClientSession),
		unregister: make(chan *ClientSession, 64),
		matchRepo:  matchRepo,
		cfg:        cfg,
	}
}

func (h *Hub) Run() {
	janitorTicker := time.NewTicker(10 * time.Second)
	defer janitorTicker.Stop()

	for {
		select {
		case session := <-h.register:
			h.mu.Lock()
			// Reconnect: if the same player already has a session, mark the
			// old one superseded and close its socket so only the newest
			// connection drives the game state. The send channel is NOT closed
			// — a concurrent trySend would panic on a closed channel.
			if old, ok := h.sessions[session.PlayerID]; ok && old != session {
				old.superseded.Store(true)
				old.conn.Close()
			}
			h.sessions[session.PlayerID] = session
			h.mu.Unlock()

		case session := <-h.unregister:
			h.mu.Lock()
			stillRegistered := false
			if existing, ok := h.sessions[session.PlayerID]; ok && existing == session {
				delete(h.sessions, session.PlayerID)
				stillRegistered = true
			}
			h.mu.Unlock()

			// Mark the participant disconnected in any room they synced into.
			// Only if this session was the registered one: on reconnect the old
			// session unregisters after the new one registered, and marking
			// disconnected then would flag a live player as gone.
			if stillRegistered && session.MatchID != "" {
				h.mu.RLock()
				entry, ok := h.rooms[session.MatchID]
				h.mu.RUnlock()
				if ok {
					entry.room.SetConnected(session.PlayerID, false)
				}
			}

		case <-janitorTicker.C:
			h.runJanitor()
		}
	}
}

// runJanitor finalizes expired/abandoned matches and evicts finished rooms.
func (h *Hub) runJanitor() {
	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Cancel matches whose DB deadline passed but that this server still
	// holds a room for (the janitor in main also sweeps matches this process
	// never loaded).
	expired, err := h.matchRepo.FinishExpiredMatches(ctx, now)
	if err != nil {
		logger.Log.Warn("deadline janitor query failed", "err", err)
	}
	for _, id := range expired {
		mID := id.String()
		h.mu.RLock()
		entry, ok := h.rooms[mID]
		h.mu.RUnlock()
		if ok {
			entry.room.CancelMatch()
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// 2. Finalize local rooms that passed their deadline and mark rooms that
	// already ended via ProcessMove (puzzle complete) so the eviction clock
	// starts for them too.
	for id, entry := range h.rooms {
		if entry.finishedAt.IsZero() && entry.room.DeadlinePassed() {
			logger.Log.Info("finalizing match by deadline", "match_id", id)
			entry.room.FinishByTimeout()
			entry.finishedAt = now
		} else if entry.finishedAt.IsZero() && entry.room.IsFinished() {
			entry.finishedAt = now
		}
	}

	// 3. Finalize rooms where every human has been gone past the grace period.
	for id, entry := range h.rooms {
		if !entry.finishedAt.IsZero() {
			continue
		}
		if !entry.room.HasHumanConnected() {
			if entry.lastAlerted.IsZero() {
				entry.lastAlerted = now
			} else if now.Sub(entry.lastAlerted) >= h.cfg.DisconnectGrace {
				logger.Log.Info("finalizing abandoned match", "match_id", id)
				entry.room.FinishAbandoned()
				entry.finishedAt = now
			}
		} else {
			entry.lastAlerted = time.Time{}
		}
	}

	// 4. Evict finished rooms from memory after the configured delay.
	for id, entry := range h.rooms {
		if !entry.finishedAt.IsZero() && now.Sub(entry.finishedAt) >= h.cfg.RoomEvictAfter {
			delete(h.rooms, id)
		}
	}
}

func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	// Prefer the Authorization header (keeps JWTs out of URLs and access
	// logs). Godot can send arbitrary headers on WebSocket requests. The
	// ?token= query param stays supported for backwards compatibility.
	tokenStr := r.Header.Get("Authorization")
	tokenStr = strings.TrimPrefix(tokenStr, "Bearer ")
	if tokenStr == "" {
		tokenStr = r.URL.Query().Get("token")
	}

	claims, err := token.ParseAndValidateToken(tokenStr, h.cfg.JWTAccessSecret, "access")
	if err != nil {
		// Godot cannot read the HTTP response after a failed upgrade, so the
		// auth error is sent as the first WS message before closing.
		conn, uerr := upgrader.Upgrade(w, r, nil)
		if uerr != nil {
			return
		}
		code := "UNAUTHORIZED"
		if errors.Is(err, token.ErrExpiredToken) {
			code = "TOKEN_EXPIRED"
		}
		conn.WriteJSON(map[string]interface{}{
			"type": "error",
			"payload": map[string]string{
				"code":    code,
				"message": "Invalid or expired authentication token",
			},
		})
		conn.Close()
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
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
		h.syncPlayer(session, payload.MatchID, env.RequestID)

	case "game.move":
		var payload struct {
			Cell  int `json:"cell"`
			Value int `json:"value"`
		}
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			session.sendError(env.RequestID, "INVALID_PAYLOAD", "Invalid move payload")
			return
		}

		// The session's match must be synced first; this prevents moves into
		// arbitrary match IDs the client never synced.
		if session.MatchID == "" {
			session.sendError(env.RequestID, "NOT_SYNCED", "Send game.sync before sending moves")
			return
		}

		h.mu.RLock()
		entry, ok := h.rooms[session.MatchID]
		h.mu.RUnlock()
		if !ok {
			session.sendError(env.RequestID, "ROOM_NOT_FOUND", "Active match room not found")
			return
		}

		res, err := entry.room.ProcessMove(env.RequestID, session.PlayerID, payload.Cell, payload.Value)
		if err != nil {
			session.sendError(env.RequestID, "MOVE_FAILED", err.Error())
			return
		}

		h.broadcastToRoom(entry.room, "game.move_result", res)

	case "ping":
		resp, _ := json.Marshal(map[string]string{"type": "pong"})
		session.trySend(resp)
	}
}

func (h *Hub) syncPlayer(session *ClientSession, matchIDStr, reqID string) {
	mID, err := uuid.Parse(matchIDStr)
	if err != nil {
		session.sendError(reqID, "INVALID_MATCH_ID", "Invalid match ID")
		return
	}

	// Authorization: only participants may sync into a match. Prevents any
	// authenticated player from observing arbitrary matches.
	playerID, err := uuid.Parse(session.PlayerID)
	if err != nil {
		session.sendError(reqID, "UNAUTHORIZED", "Invalid player identity")
		return
	}
	isParticipant, err := h.matchRepo.IsPlayerInMatch(context.Background(), mID, playerID)
	if err != nil {
		session.sendError(reqID, "SYNC_FAILED", "Failed to verify match participation")
		return
	}
	if !isParticipant {
		session.sendError(reqID, "FORBIDDEN", "You are not a participant in this match")
		return
	}

	h.mu.Lock()
	entry, ok := h.rooms[matchIDStr]
	if !ok {
		m, err := h.matchRepo.GetMatchByID(context.Background(), mID)
		if err != nil {
			h.mu.Unlock()
			session.sendError(reqID, "MATCH_NOT_FOUND", "Match not found in database")
			return
		}
		if m.Status != match.StatusWaiting && m.Status != match.StatusStarting && m.Status != match.StatusPlaying {
			session.sendError(reqID, "MATCH_NOT_ACTIVE", "Match is no longer active")
			return
		}
		room := game.NewActiveRoom(m, h.matchRepo)
		room.Redis = h.redisClient
		room.Cfg = h.cfg
		room.OnStateChange = func(rm *game.ActiveRoom, eventType string, payload interface{}) {
			h.broadcastToRoom(rm, eventType, payload)
		}
		h.rooms[matchIDStr] = &roomEntry{room: room}

		// Trigger bot runner loop if match contains a bot team.
		if m.Status == match.StatusPlaying {
			room.StartBotRunner(context.Background())
		}
	}
	h.mu.Unlock()
	session.MatchID = matchIDStr
	entry.room.SetConnected(session.PlayerID, true)

	snapshot := entry.room.Snapshot()
	bytes, err := json.Marshal(map[string]interface{}{
		"type":    "game.state",
		"payload": snapshot,
	})
	if err != nil {
		session.sendError(reqID, "SYNC_FAILED", "Failed to serialize game state")
		return
	}
	session.trySend(bytes)
}

func (h *Hub) broadcastToRoom(room *game.ActiveRoom, eventType string, payload interface{}) {
	msg := map[string]interface{}{
		"type":    eventType,
		"payload": payload,
	}
	bytes, err := json.Marshal(msg)
	if err != nil {
		return
	}

	for _, pID := range room.HumanParticipantIDs() {
		h.mu.RLock()
		sess, ok := h.sessions[pID]
		h.mu.RUnlock()
		if !ok {
			continue
		}
		sess.trySend(bytes)
	}
}

// SetRedis wires the Redis client used to release active-match locks when a
// match finishes on this server.
func (h *Hub) SetRedis(c *redis.Client) {
	h.redisClient = c
}
