package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"marketplace/catalog-svc/internal/cache"
	"marketplace/catalog-svc/internal/listings/model"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisAdapter struct {
	client *redis.Client
}

func NewRedisAdapter(client *redis.Client) *RedisAdapter {
	return &RedisAdapter{
		client: client,
	}
}

func (r *RedisAdapter) Set(
	ctx context.Context,
	key string,
	value model.Listing,
	ttl time.Duration,
) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("redis set marshal listing: %w", err)
	}

	err = r.client.Set(ctx, key, data, ttl).Err()
	if err != nil {
		return fmt.Errorf("redis set: %w", err)
	}
	return nil
}

func (r *RedisAdapter) Get(ctx context.Context, key string) (*model.Listing, error) {
	val, err := r.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("key %s: %w", key, cache.ErrCacheMiss)
	} else if err != nil {
		return nil, fmt.Errorf("redis get: %w", err)
	}

	var listing model.Listing
	if err := json.Unmarshal([]byte(val), &listing); err != nil {
		return nil, fmt.Errorf("redis get unmarshal listing: %w", err)
	}

	return &listing, nil
}

func (r *RedisAdapter) Delete(ctx context.Context, keys ...string) (bool, error) {
	deleted, err := r.client.Del(ctx, keys...).Result()
	if err != nil {
		return false, fmt.Errorf("redis delete: %w", err)
	}

	return deleted > 0, nil
}
