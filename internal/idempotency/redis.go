package idempotency

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrInvalidLock = errors.New("invalid idempotency lock")

type RedisGate struct {
	client redis.UniversalClient
	ttl    time.Duration
}

func NewRedisGate(client redis.UniversalClient, ttl time.Duration) (*RedisGate, error) {
	if ttl <= 0 || client == nil {
		return nil, ErrInvalidLock
	}
	return &RedisGate{
		client: client,
		ttl:    ttl,
	}, nil
}

// tryacquire尝试获取某个幂等请求的临时执行权
// acquire=true 表示本次请求成功取得门禁
// token用于确保释放时不会删除别人的门禁
func (g *RedisGate) TryAcquire(ctx context.Context, key string) (token string, acquire bool, err error) {
	if key == "" {
		return "", false, ErrInvalidLock
	}

	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", false, err
	}
	token = hex.EncodeToString(random[:])

	acquire, err = g.client.SetNX(ctx, key, token, g.ttl).Result()
	if err != nil {
		return "", false, fmt.Errorf("redis SETNX: %w", err)
	}
	if !acquire {
		return "", false, nil
	}

	return token, true, nil
}

var releaseScript = redis.NewScript(`
    if redis.call("GET", KEYS[1]) == ARGV[1] then
        return redis.call("DEL", KEYS[1])
    end
    return 0
`)

// Release 只释放当前请求自己持有的门禁。
func (g *RedisGate) Release(
	ctx context.Context,
	key, token string,
) error {
	if key == "" || token == "" {
		return ErrInvalidLock
	}

	return releaseScript.Run(
		ctx,
		g.client,
		[]string{key},
		token,
	).Err()
}
