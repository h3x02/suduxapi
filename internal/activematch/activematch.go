package activematch

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

const keyPrefix = "active_match:"

// Key returns the Redis key that locks a player into a single active match.
func Key(playerID string) string {
	return keyPrefix + playerID
}

// Lock claims the active-match slot for a player. Returns false if the player
// is already locked into a match.
func Lock(ctx context.Context, rdb *redis.Client, playerID, matchID string, ttl time.Duration) (bool, error) {
	return rdb.SetNX(ctx, Key(playerID), matchID, ttl).Result()
}

// Unlock releases the active-match slot. Called when a match finishes or is
// cancelled so the player can queue again.
func Unlock(ctx context.Context, rdb *redis.Client, playerID string) error {
	return rdb.Del(ctx, Key(playerID)).Err()
}

// Get returns the match ID the player is locked into, or "" if none.
func Get(ctx context.Context, rdb *redis.Client, playerID string) (string, error) {
	return rdb.Get(ctx, Key(playerID)).Result()
}
