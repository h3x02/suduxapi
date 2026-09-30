package websocket

import (
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 4096
)

type MessageEnvelope struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type ClientSession struct {
	PlayerID string
	MatchID  string
	// superseded marks a session replaced by a newer connection from the same
	// player. trySend drops messages for superseded sessions instead of
	// closing the channel — closing would panic when readPump races a
	// sendError into the same channel.
	superseded atomic.Bool
	conn       *websocket.Conn
	send       chan []byte
	hub        *Hub
}

// trySend delivers a message without ever blocking or panicking on a closed
// channel. Non-blocking sends are intentional: a slow client must not stall
// move processing or broadcasts for everyone else.
func (s *ClientSession) trySend(msg []byte) {
	if s.superseded.Load() {
		return
	}
	select {
	case s.send <- msg:
	default:
	}
}

func (s *ClientSession) readPump() {
	defer func() {
		s.hub.unregister <- s
		s.conn.Close()
	}()

	s.conn.SetReadLimit(maxMessageSize)
	s.conn.SetReadDeadline(time.Now().Add(pongWait))
	s.conn.SetPongHandler(func(string) error {
		s.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := s.conn.ReadMessage()
		if err != nil {
			break
		}

		var env MessageEnvelope
		if err := json.Unmarshal(message, &env); err != nil {
			s.sendError("", "INVALID_JSON", "Failed to parse JSON envelope")
			continue
		}

		s.hub.handleMessage(s, &env)
	}
}

func (s *ClientSession) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		s.conn.Close()
	}()

	for {
		select {
		case message, ok := <-s.send:
			s.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				s.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := s.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			s.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := s.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (s *ClientSession) sendError(reqID, code, msg string) {
	resp := map[string]interface{}{
		"type":       "error",
		"request_id": reqID,
		"payload": map[string]string{
			"code":    code,
			"message": msg,
		},
	}
	bytes, _ := json.Marshal(resp)
	s.trySend(bytes)
}
