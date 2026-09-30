package player

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/h3x02/suduxapi/internal/middleware"
)

type Handler struct {
	repo *Repository
}

func NewHandler(repo *Repository) *Handler {
	return &Handler{repo: repo}
}

func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	playerIDStr, ok := middleware.GetPlayerID(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "User not authenticated")
		return
	}

	playerID, err := uuid.Parse(playerIDStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_ID", "Invalid player ID format")
		return
	}

	p, err := h.repo.GetByID(r.Context(), playerID)
	if err != nil {
		respondError(w, http.StatusNotFound, "PLAYER_NOT_FOUND", "Player not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(p)
}

func (h *Handler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	playerIDStr, ok := middleware.GetPlayerID(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "User not authenticated")
		return
	}

	playerID, err := uuid.Parse(playerIDStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_ID", "Invalid player ID format")
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		respondError(w, http.StatusBadRequest, "INVALID_REQUEST", "Name is required")
		return
	}

	p, err := h.repo.UpdateName(r.Context(), playerID, body.Name)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update profile")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(p)
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
