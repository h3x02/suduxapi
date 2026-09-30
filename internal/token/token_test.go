package token_test

import (
	"testing"
	"time"

	"github.com/h3x02/suduxapi/internal/token"
)

func Test6DigitCodeGeneration(t *testing.T) {
	code, err := token.Generate6DigitCode()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("expected 6 digit code, got %s", code)
	}
}

func TestHashCode(t *testing.T) {
	code := "123456"
	hash1 := token.HashCode(code)
	hash2 := token.HashCode(code)

	if hash1 != hash2 {
		t.Fatalf("expected identical hashes for same code")
	}

	hash3 := token.HashCode("654321")
	if hash1 == hash3 {
		t.Fatalf("expected different hashes for different codes")
	}
}

func TestTokenPairGenerationAndValidation(t *testing.T) {
	playerID := "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"
	accessSecret := "access-secret"
	refreshSecret := "refresh-secret"

	tokens, err := token.GenerateTokenPair(playerID, accessSecret, refreshSecret, 15*time.Minute, 24*time.Hour)
	if err != nil {
		t.Fatalf("failed to generate token pair: %v", err)
	}

	claims, err := token.ParseAndValidateToken(tokens.AccessToken, accessSecret, "access")
	if err != nil {
		t.Fatalf("failed to validate access token: %v", err)
	}
	if claims.PlayerID != playerID {
		t.Fatalf("expected player_id %s, got %s", playerID, claims.PlayerID)
	}

	refreshClaims, err := token.ParseAndValidateToken(tokens.RefreshToken, refreshSecret, "refresh")
	if err != nil {
		t.Fatalf("failed to validate refresh token: %v", err)
	}
	if refreshClaims.PlayerID != playerID {
		t.Fatalf("expected player_id %s, got %s", playerID, refreshClaims.PlayerID)
	}

	// Test cross token secret validation fails
	_, err = token.ParseAndValidateToken(tokens.AccessToken, refreshSecret, "access")
	if err == nil {
		t.Fatalf("expected error when parsing access token with refresh secret")
	}
}
