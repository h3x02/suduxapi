package redis

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

type Client struct {
	*redis.Client
}

// Typed exposes the underlying go-redis client for packages that depend on
// go-redis types directly (e.g. the game room lock release).
func (c *Client) Typed() *redis.Client {
	return c.Client
}

func Connect(ctx context.Context, redisURL string) (*Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse redis URL: %w", err)
	}

	rdb := redis.NewClient(opts)
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to ping redis: %w", err)
	}

	return &Client{Client: rdb}, nil
}
