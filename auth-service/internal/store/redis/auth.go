package rds

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type RedisAuthManager struct {
	client      *goredis.Client
	PrefixState string
	defaultTTL  time.Duration
}

func NewAuthRedisManager(rds *goredis.Client) *RedisAuthManager {
	return &RedisAuthManager{
		client:      rds,
		PrefixState: "st",
		defaultTTL:  5 * time.Minute,
	}
}
func (r *RedisAuthManager) buildKeyState(state string) string {
	return fmt.Sprintf("%s:%s", r.PrefixState, state)
}

func (r *RedisAuthManager) SetState(ctx context.Context, state string) error {
	key := r.buildKeyState(state)
	expiration := r.defaultTTL

	err := r.client.Set(ctx, key, state, expiration).Err()
	if err != nil {
		return fmt.Errorf("failed to set session in Redis: %w", err)
	}
	return nil
}

func (r *RedisAuthManager) GetState(
	ctx context.Context,
	state string,
) (string, error) {
	key := r.buildKeyState(state)
	stateData, err := r.client.Get(ctx, key).Result()
	if err != nil {
		return "", fmt.Errorf("failed to get state data from Redis: %w", err)
	}
	return stateData, nil
}
func (r *RedisAuthManager) DeleteState(
	ctx context.Context,
	state string,
) error {
	key := r.buildKeyState(state)
	if err := r.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to remove state data from Redis: %w", err)
	}
	return nil
}
