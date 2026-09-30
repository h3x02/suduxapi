package matchmaking

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/h3x02/suduxapi/internal/middleware"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) JoinQueue(w http.ResponseWriter, r *http.Request) {
	playerIDStr, ok := middleware.GetPlayerID(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "User not authenticated")
		return
	}

	var req JoinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	req.PlayerID = playerIDStr
	if req.GameMode == "" {
		req.GameMode = "cell_race"
	}
	if req.MatchFormat == "" {
		req.MatchFormat = "1v1"
	}
	if req.Difficulty == "" {
		req.Difficulty = "medium"
	}

	event, err := h.svc.JoinQueue(r.Context(), &req)
	if err != nil {
		switch {
		case errors.Is(err, ErrPlayerAlreadyInMatch):
			respondError(w, http.StatusConflict, "ALREADY_IN_MATCH", err.Error())
		case errors.Is(err, ErrPlayerAlreadyInQueue):
			respondError(w, http.StatusConflict, "ALREADY_IN_QUEUE", err.Error())
		default:
			// Covers enum validation errors and transient failures.
			respondError(w, http.StatusBadRequest, "MATCHMAKING_FAILED", err.Error())
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if event != nil {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "matched",
			"match":  event,
		})
	} else {
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "queued",
			"message": "Player added to matchmaking queue",
		})
	}
}

func (h *Handler) LeaveQueue(w http.ResponseWriter, r *http.Request) {
	playerIDStr, ok := middleware.GetPlayerID(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "User not authenticated")
		return
	}

	err := h.svc.LeaveQueue(r.Context(), playerIDStr)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "LEAVE_FAILED", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Left matchmaking queue successfully"})
}

func (h *Handler) GetStatus(w http.ResponseWriter, r *http.Request) {
	playerIDStr, ok := middleware.GetPlayerID(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "User not authenticated")
		return
	}

	hasMatch, matchID, err := h.svc.HasActiveMatch(r.Context(), playerIDStr)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "DB_ERROR", "Failed to check match status")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if hasMatch {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "in_match",
			"match_id": matchID,
		})
	} else {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "idle",
		})
	}
}

func respondError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
