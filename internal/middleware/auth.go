package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/h3x02/suduxapi/internal/token"
)

type contextKey string

const PlayerIDKey contextKey = "player_id"

func AuthMiddleware(accessSecret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]string{
						"code":    "UNAUTHORIZED",
						"message": "Missing or invalid Authorization header",
					},
				})
				return
			}

			tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
			claims, err := token.ParseAndValidateToken(tokenStr, accessSecret, "access")
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]string{
						"code":    "INVALID_TOKEN",
						"message": "Token is invalid or expired",
					},
				})
				return
			}

			ctx := context.WithValue(r.Context(), PlayerIDKey, claims.PlayerID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func GetPlayerID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(PlayerIDKey).(string)
	return id, ok
}
