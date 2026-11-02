package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type Result struct {
	Allowed   bool
	Count     int64
	Remaining time.Duration
}

type RedisLimiter struct {
	client redis.UniversalClient
}

var allowScript = redis.NewScript(`
    local count = redis.call("INCR", KEYS[1])

    if count == 1 then
        redis.call("PEXPIRE", KEYS[1], ARGV[1])
    end

    local ttl = redis.call("PTTL", KEYS[1])

    return {count, ttl}
`)

func NewRedisLimiter(client redis.UniversalClient) *RedisLimiter {
	return &RedisLimiter{client: client}
}

func (r *RedisLimiter) Allow(
	ctx context.Context,
	key string,
	limit int64,
	window time.Duration,
) (Result, error) {
	//参数校验
	if key == "" || limit <= 0 || window < time.Millisecond {
		return Result{}, fmt.Errorf("invalid rate limit parameters")
	}

	//执行Lua脚本
	values, err := allowScript.Run(
		ctx,
		r.client,
		[]string{key},
		window.Milliseconds(),
	).Int64Slice()

	if err != nil {
		return Result{}, fmt.Errorf("execute rate limit script:%w", err)
	}

	//校验redis返回值
	if len(values) != 2 || values[1] < 0 {
		return Result{}, fmt.Errorf("invalid rate limit script result:%v", values)
	}

	//解析计数和ttl
	count := values[0]
	ttl := time.Duration(values[1]) * time.Millisecond
	return Result{
		Allowed:   count <= limit,
		Count:     count,
		Remaining: ttl,
	}, nil
}
