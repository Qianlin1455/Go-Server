package idempotency

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// 使用真实 Redis 进行集成测试
func setupRedisGate(
	t *testing.T,
	ttl time.Duration,
) (*RedisGate, *redis.Client, string) {
	t.Helper()

	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:6379",
	})

	ctx := context.Background()

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Fatalf("Redis unavailable: %v", err)
	}

	gate, err := NewRedisGate(client, ttl)
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}

	key := fmt.Sprintf(
		"go-server:test:idempotency:%s:%d",
		t.Name(),
		time.Now().UnixNano(),
	)

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			3*time.Second,
		)
		defer cancel()

		if err := client.Del(cleanupCtx, key).Err(); err != nil {
			t.Errorf("cleanup Redis key: %v", err)
		}
		_ = client.Close()
	})

	return gate, client, key
}

// 测试一：50 个 goroutine 竞争同一门禁
func TestRedisGate_Concurrent(t *testing.T) {
	gate, _, key := setupRedisGate(
		t, 30*time.Second,
	)

	const n = 50

	type result struct {
		token    string
		acquired bool
		err      error
	}

	results := make(chan result, n)
	start := make(chan struct{})

	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			<-start

			ctx, cancel := context.WithTimeout(
				context.Background(),
				5*time.Second,
			)
			defer cancel()

			token, acquired, err := gate.TryAcquire(
				ctx, key,
			)

			results <- result{
				token:    token,
				acquired: acquired,
				err:      err,
			}
		}()
	}

	close(start)

	wg.Wait()
	close(results)

	acquiredCount := 0
	var winnerToken string

	for r := range results {
		if r.err != nil {
			t.Fatalf("SETNX failed: %v", r.err)
		}

		if r.acquired {
			acquiredCount++
			winnerToken = r.token
		}
	}

	if acquiredCount != 1 {
		t.Fatalf(
			"expected 1 winner, got %d",
			acquiredCount,
		)
	}

	if winnerToken == "" {
		t.Fatal("winner token is empty")
	}

	t.Logf(
		"PASS: %d concurrent attempts, %d winner",
		n, acquiredCount,
	)
}

// 测试二：TTL 到期之后可以重新获取
func TestRedisGate_TTL(t *testing.T) {
	gate, _, key := setupRedisGate(
		t, 250*time.Millisecond,
	)

	ctx := context.Background()

	_, acquired, err := gate.TryAcquire(ctx, key)
	if err != nil || !acquired {
		t.Fatalf("first acquire failed: %v", err)
	}

	// 尚未过期，不能再次获取
	_, acquired, err = gate.TryAcquire(ctx, key)
	if err != nil || acquired {
		t.Fatalf(
			"second acquire should fail, err=%v",
			err,
		)
	}

	// 等待超过 TTL
	time.Sleep(400 * time.Millisecond)

	_, acquired, err = gate.TryAcquire(ctx, key)
	if err != nil || !acquired {
		t.Fatalf(
			"acquire after TTL failed: %v",
			err,
		)
	}

	t.Log("PASS: gate can be reacquired after TTL")
}

// 测试三：旧 Token 不能删除新门禁
func TestRedisGate_TokenSafety(t *testing.T) {
	gate, client, key := setupRedisGate(
		t, 250*time.Millisecond,
	)

	ctx := context.Background()

	// A 获取门禁
	tokenA, ok, err := gate.TryAcquire(ctx, key)
	if err != nil || !ok {
		t.Fatalf("A acquire failed: %v", err)
	}

	// A 的门禁过期
	time.Sleep(400 * time.Millisecond)

	// B 重新获取门禁
	tokenB, ok, err := gate.TryAcquire(ctx, key)
	if err != nil || !ok {
		t.Fatalf("B acquire failed: %v", err)
	}

	// A 尝试释放已经不属于自己的门禁
	if err := gate.Release(ctx, key, tokenA); err != nil {
		t.Fatal(err)
	}

	// B 的门禁必须仍然存在
	currentToken, err := client.Get(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}

	if currentToken != tokenB {
		t.Fatal("old token deleted new owner's gate")
	}

	// B 正常释放自己的门禁
	if err := gate.Release(ctx, key, tokenB); err != nil {
		t.Fatal(err)
	}

	exists, err := client.Exists(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}

	if exists != 0 {
		t.Fatal("gate should be deleted")
	}

	t.Log("PASS: stale token cannot release new gate")
}
