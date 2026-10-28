package cache

import (
	"context"
	"errors"
	"fmt"
	"github.com/redis/go-redis/v9"
	"math/rand/v2"
	"strings"
	"time"
)

type RedisCache struct {
	client  *redis.Client
	prefix  string
	timeout time.Duration
	ttl     time.Duration
}

var (
	ErrMissingAddrOrPrefix = errors.New("redis address and key prefix are required")
	ErrInvalidInput        = errors.New("invalid redis db,timeout or ttl")
	ErrEmptyKey            = errors.New("cache key must not be empty")
)

func (c *RedisCache) effectiveTTL() time.Duration {
	jitter := c.ttl / 10
	if jitter <= 0 {
		return c.ttl
	}
	return c.ttl + time.Duration(
		rand.Int64N(int64(jitter)+1),
	)
}

func NewRedisCache(addr string, password string, db int, prefix string,
	timeout, ttl time.Duration) (*RedisCache, error) {
	addr = strings.TrimSpace(addr)
	prefix = strings.TrimSpace(prefix)
	prefix = strings.Trim(prefix, ":")

	if addr == "" || prefix == "" {
		return nil, ErrMissingAddrOrPrefix
	}

	if db < 0 || timeout <= 0 || ttl <= 0 {
		return nil, ErrInvalidInput
	}

	client := redis.NewClient(&redis.Options{
		Addr:                  addr,
		Password:              password,
		DB:                    db,
		DialTimeout:           timeout,
		ReadTimeout:           timeout,
		WriteTimeout:          timeout,
		MaxRetries:            -1,
		ContextTimeoutEnabled: true,
	})

	return &RedisCache{
		client:  client,
		prefix:  prefix,
		timeout: timeout,
		ttl:     ttl,
	}, nil
}

func (c *RedisCache) makeKey(key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", ErrEmptyKey
	}
	return c.prefix + ":" + key, nil
}

func (c *RedisCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	fullkey, err := c.makeKey(key)
	if err != nil {
		return nil, false, fmt.Errorf("make fullkey error : %w", err)
	}

	opCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	value, err := c.client.Get(opCtx, fullkey).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("redis GET:%w", err)
	}

	return value, true, nil
}

func (c *RedisCache) Set(ctx context.Context, key string, value []byte) error {
	fullkey, err := c.makeKey(key)
	if err != nil {
		return fmt.Errorf("make fullkey error:%w", err)
	}

	opCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	if err := c.client.Set(opCtx, fullkey, value, c.effectiveTTL()).Err(); err != nil {
		return fmt.Errorf("redis SET:%w", err)
	}
	return nil
}

func (c *RedisCache) Delete(ctx context.Context, key string) error {
	fullkey, err := c.makeKey(key)
	if err != nil {
		return err
	}

	opCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	if err := c.client.Del(opCtx, fullkey).Err(); err != nil {
		return fmt.Errorf("redis DEL:%w", err)
	}
	return nil
}

func (c *RedisCache) Ping(ctx context.Context) error {
	opCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.client.Ping(opCtx).Err()
}

func (c *RedisCache) Close() error {
	return c.client.Close()
}
