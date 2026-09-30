package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/h3x02/suduxapi/internal/config"
	"github.com/h3x02/suduxapi/internal/email"
	"github.com/h3x02/suduxapi/internal/logger"
	"github.com/h3x02/suduxapi/internal/player"
	"github.com/h3x02/suduxapi/internal/redis"
	"github.com/h3x02/suduxapi/internal/token"
)

type Service struct {
	cfg        *config.Config
	playerRepo *player.Repository
	redis      *redis.Client
	email      email.EmailSender
}

func NewService(cfg *config.Config, playerRepo *player.Repository, rdb *redis.Client, emailSender email.EmailSender) *Service {
	return &Service{
		cfg:        cfg,
		playerRepo: playerRepo,
		redis:      rdb,
		email:      emailSender,
	}
}

func (s *Service) RequestCode(ctx context.Context, reqEmail, ip string) error {
	reqEmail = strings.ToLower(strings.TrimSpace(reqEmail))
	if reqEmail == "" {
		return errors.New("email is required")
	}

	// Rate limiting by email and IP
	cooldownKey := fmt.Sprintf("auth:cooldown:%s", reqEmail)
	if exists, _ := s.redis.Exists(ctx, cooldownKey).Result(); exists > 0 {
		return errors.New("please wait before requesting another code")
	}

	ipKey := fmt.Sprintf("auth:rate:ip:%s", ip)
	ipCount, _ := s.redis.Incr(ctx, ipKey).Result()
	if ipCount == 1 {
		s.redis.Expire(ctx, ipKey, 1*time.Hour)
	}
	if int(ipCount) > s.cfg.AuthMaxRequestsPerIP {
		return errors.New("rate limit exceeded for IP")
	}

	emailReqKey := fmt.Sprintf("auth:rate:email:%s", reqEmail)
	emailCount, _ := s.redis.Incr(ctx, emailReqKey).Result()
	if emailCount == 1 {
		s.redis.Expire(ctx, emailReqKey, 1*time.Hour)
	}
	if int(emailCount) > s.cfg.AuthMaxRequestsPerEmail {
		return errors.New("rate limit exceeded for email")
	}

	code, err := token.Generate6DigitCode()
	if err != nil {
		return fmt.Errorf("failed to generate code: %w", err)
	}

	codeHash := token.HashCode(code)
	codeKey := fmt.Sprintf("auth:code:%s", reqEmail)

	if err := s.redis.Set(ctx, codeKey, codeHash, s.cfg.AuthCodeTTL).Err(); err != nil {
		return fmt.Errorf("failed to store code in redis: %w", err)
	}

	// Reset attempt counter
	attemptsKey := fmt.Sprintf("auth:attempts:%s", reqEmail)
	s.redis.Del(ctx, attemptsKey)

	// Set cooldown
	s.redis.Set(ctx, cooldownKey, "1", s.cfg.AuthResendCooldown)

	// Send email
	if err := s.email.SendLoginCode(ctx, reqEmail, code); err != nil {
		logger.Log.Error("failed to send login email", "email", reqEmail, "err", err)
		return fmt.Errorf("failed to send email: %w", err)
	}

	return nil
}

func (s *Service) VerifyCode(ctx context.Context, reqEmail, code string) (*token.TokenPair, *player.Player, error) {
	reqEmail = strings.ToLower(strings.TrimSpace(reqEmail))
	code = strings.TrimSpace(code)

	attemptsKey := fmt.Sprintf("auth:attempts:%s", reqEmail)
	attempts, _ := s.redis.Incr(ctx, attemptsKey).Result()
	if int(attempts) > s.cfg.AuthCodeMaxAttempts {
		return nil, nil, errors.New("too many failed verification attempts, please request a new code")
	}

	codeKey := fmt.Sprintf("auth:code:%s", reqEmail)
	storedHash, err := s.redis.Get(ctx, codeKey).Result()
	if err != nil {
		return nil, nil, errors.New("verification code expired or invalid")
	}

	if token.HashCode(code) != storedHash {
		return nil, nil, errors.New("invalid verification code")
	}

	// Code is valid, delete code from redis
	s.redis.Del(ctx, codeKey, attemptsKey)

	p, err := s.playerRepo.GetByEmail(ctx, reqEmail)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// New player
			p, err = s.playerRepo.Create(ctx, reqEmail)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to create player: %w", err)
			}
		} else {
			return nil, nil, fmt.Errorf("failed to query player: %w", err)
		}
	}

	tokenPair, err := token.GenerateTokenPair(p.ID.String(), s.cfg.JWTAccessSecret, s.cfg.JWTRefreshSecret, s.cfg.JWTAccessTTL, s.cfg.JWTRefreshTTL)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate token pair: %w", err)
	}

	tokenHash := token.HashToken(tokenPair.RefreshToken)
	err = s.playerRepo.SaveRefreshToken(ctx, p.ID, tokenHash, tokenPair.JTI, time.Now().UTC().Add(s.cfg.JWTRefreshTTL))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to save refresh token: %w", err)
	}

	return tokenPair, p, nil
}

func (s *Service) Refresh(ctx context.Context, refreshTokenStr string) (*token.TokenPair, error) {
	claims, err := token.ParseAndValidateToken(refreshTokenStr, s.cfg.JWTRefreshSecret, "refresh")
	if err != nil {
		return nil, token.ErrInvalidToken
	}

	tokenHash := token.HashToken(refreshTokenStr)
	valid, err := s.playerRepo.IsRefreshTokenValid(ctx, claims.ID, tokenHash)
	if err != nil || !valid {
		return nil, token.ErrInvalidToken
	}

	// Revoke old refresh token (rotation)
	_ = s.playerRepo.RevokeRefreshTokenByJTI(ctx, claims.ID)

	playerID, err := uuid.Parse(claims.PlayerID)
	if err != nil {
		return nil, token.ErrInvalidToken
	}

	p, err := s.playerRepo.GetByID(ctx, playerID)
	if err != nil {
		return nil, token.ErrInvalidToken
	}

	newTokenPair, err := token.GenerateTokenPair(p.ID.String(), s.cfg.JWTAccessSecret, s.cfg.JWTRefreshSecret, s.cfg.JWTAccessTTL, s.cfg.JWTRefreshTTL)
	if err != nil {
		return nil, fmt.Errorf("failed to generate token pair: %w", err)
	}

	newTokenHash := token.HashToken(newTokenPair.RefreshToken)
	err = s.playerRepo.SaveRefreshToken(ctx, p.ID, newTokenHash, newTokenPair.JTI, time.Now().UTC().Add(s.cfg.JWTRefreshTTL))
	if err != nil {
		return nil, fmt.Errorf("failed to save new refresh token: %w", err)
	}

	return newTokenPair, nil
}

func (s *Service) Logout(ctx context.Context, refreshTokenStr string) error {
	claims, err := token.ParseAndValidateToken(refreshTokenStr, s.cfg.JWTRefreshSecret, "refresh")
	if err != nil {
		return nil // idempotent logout
	}

	return s.playerRepo.RevokeRefreshTokenByJTI(ctx, claims.ID)
}

// HTTP Handler
type Handler struct {
	authSvc    *Service
	playerRepo *player.Repository
}

func NewHandler(authSvc *Service, playerRepo *player.Repository) *Handler {
	return &Handler{
		authSvc:    authSvc,
		playerRepo: playerRepo,
	}
}

func (h *Handler) RequestCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	clientIP := r.RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		clientIP = strings.Split(xff, ",")[0]
	}

	err := h.authSvc.RequestCode(r.Context(), body.Email, clientIP)
	if err != nil {
		// Log error, but for privacy/security keep endpoint response clean if error is rate limit
		if strings.Contains(err.Error(), "rate limit") || strings.Contains(err.Error(), "wait") {
			respondError(w, http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", err.Error())
			return
		}
	}

	// Always return generic successful response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "If the email can receive messages, a verification code has been sent.",
	})
}

func (h *Handler) VerifyCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	tokens, p, err := h.authSvc.VerifyCode(r.Context(), body.Email, body.Code)
	if err != nil {
		respondError(w, http.StatusUnauthorized, "VERIFICATION_FAILED", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"tokens": tokens,
		"player": p,
	})
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	tokens, err := h.authSvc.Refresh(r.Context(), body.RefreshToken)
	if err != nil {
		respondError(w, http.StatusUnauthorized, "INVALID_TOKEN", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"tokens": tokens,
	})
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	_ = h.authSvc.Logout(r.Context(), body.RefreshToken)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "Logged out successfully",
	})
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
