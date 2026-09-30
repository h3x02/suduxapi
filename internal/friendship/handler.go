package friendship

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/h3x02/suduxapi/internal/middleware"
)

type Handler struct {
	repo *Repository
}

func NewHandler(repo *Repository) *Handler {
	return &Handler{repo: repo}
}

func (h *Handler) GetFriends(w http.ResponseWriter, r *http.Request) {
	playerIDStr, ok := middleware.GetPlayerID(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "User not authenticated")
		return
	}
	userID, _ := uuid.Parse(playerIDStr)

	friends, err := h.repo.GetFriends(r.Context(), userID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "DB_ERROR", "Failed to fetch friends")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(friends)
}

func (h *Handler) SendRequest(w http.ResponseWriter, r *http.Request) {
	playerIDStr, ok := middleware.GetPlayerID(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "User not authenticated")
		return
	}
	userID, _ := uuid.Parse(playerIDStr)

	var body struct {
		AddresseeID string `json:"addressee_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AddresseeID == "" {
		respondError(w, http.StatusBadRequest, "INVALID_REQUEST", "addressee_id is required")
		return
	}

	addresseeID, err := uuid.Parse(body.AddresseeID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_ID", "Invalid addressee_id format")
		return
	}

	f, err := h.repo.SendRequest(r.Context(), userID, addresseeID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "FRIEND_REQUEST_FAILED", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(f)
}

func (h *Handler) AcceptRequest(w http.ResponseWriter, r *http.Request) {
	playerIDStr, ok := middleware.GetPlayerID(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "User not authenticated")
		return
	}
	userID, _ := uuid.Parse(playerIDStr)

	reqIDStr := chi.URLParam(r, "id")
	friendshipID, err := uuid.Parse(reqIDStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_ID", "Invalid request ID format")
		return
	}

	f, err := h.repo.AcceptRequest(r.Context(), friendshipID, userID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "ACCEPT_FAILED", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(f)
}

func (h *Handler) RejectRequest(w http.ResponseWriter, r *http.Request) {
	playerIDStr, ok := middleware.GetPlayerID(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "User not authenticated")
		return
	}
	userID, _ := uuid.Parse(playerIDStr)

	reqIDStr := chi.URLParam(r, "id")
	friendshipID, err := uuid.Parse(reqIDStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_ID", "Invalid request ID format")
		return
	}

	if err := h.repo.RejectRequest(r.Context(), friendshipID, userID); err != nil {
		respondError(w, http.StatusBadRequest, "REJECT_FAILED", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Friend request rejected"})
}

func (h *Handler) RemoveFriend(w http.ResponseWriter, r *http.Request) {
	playerIDStr, ok := middleware.GetPlayerID(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "User not authenticated")
		return
	}
	userID, _ := uuid.Parse(playerIDStr)

	friendIDStr := chi.URLParam(r, "id")
	friendID, err := uuid.Parse(friendIDStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_ID", "Invalid friend ID format")
		return
	}

	if err := h.repo.RemoveFriend(r.Context(), friendID, userID); err != nil {
		respondError(w, http.StatusBadRequest, "REMOVE_FAILED", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Friend removed successfully"})
}

func (h *Handler) BlockPlayer(w http.ResponseWriter, r *http.Request) {
	playerIDStr, ok := middleware.GetPlayerID(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "User not authenticated")
		return
	}
	userID, _ := uuid.Parse(playerIDStr)

	targetIDStr := chi.URLParam(r, "id")
	targetID, err := uuid.Parse(targetIDStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_ID", "Invalid target ID format")
		return
	}

	if err := h.repo.BlockPlayer(r.Context(), targetID, userID); err != nil {
		respondError(w, http.StatusBadRequest, "BLOCK_FAILED", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Player blocked successfully"})
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
