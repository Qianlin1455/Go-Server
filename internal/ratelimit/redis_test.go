package ratelimit

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// 创建测试用 Redis 客户端和限流器。
func setupTestLimiter(t *testing.T) (*RedisLimiter, *redis.Client) {
	t.Helper()

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}

	client := redis.NewClient(&redis.Options{
		Addr: addr,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Fatalf("Redis unavailable at %s: %v", addr, err)
	}

	t.Cleanup(func() {
		_ = client.Close()
	})

	return NewRedisLimiter(client), client
}

// 为每个测试创建独立 Key，避免测试之间相互影响。
func newTestKey(t *testing.T, client *redis.Client) string {
	t.Helper()

	key := fmt.Sprintf(
		"go-server:test:ratelimit:%s:%d",
		t.Name(),
		time.Now().UnixNano(),
	)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = client.Del(ctx, key).Err()
	})

	return key
}

// 测试一：30 次顺序请求，前 10 次放行，后 20 次拒绝。
func TestAllow_30Requests(t *testing.T) {
	limiter, client := setupTestLimiter(t)
	key := newTestKey(t, client)

	ctx := context.Background()
	limit := int64(10)
	window := 60 * time.Second

	for i := 1; i <= 30; i++ {
		result, err := limiter.Allow(ctx, key, limit, window)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}

		expectedAllowed := i <= 10

		if result.Allowed != expectedAllowed {
			t.Fatalf(
				"request %d: Allowed=%v, want %v",
				i, result.Allowed, expectedAllowed,
			)
		}

		if result.Count != int64(i) {
			t.Fatalf(
				"request %d: Count=%d, want %d",
				i, result.Count, i,
			)
		}

		if result.Remaining <= 0 || result.Remaining > window {
			t.Fatalf(
				"request %d: invalid remaining=%v",
				i, result.Remaining,
			)
		}
	}

	t.Log("PASS: first 10 allowed, next 20 rejected")
}

// 测试二：30 个 goroutine 并发调用，必须恰好放行 10 个。
func TestAllow_Concurrent30(t *testing.T) {
	limiter, client := setupTestLimiter(t)
	key := newTestKey(t, client)

	const total = 30
	const limit = int64(10)

	type outcome struct {
		result Result
		err    error
	}

	results := make(chan outcome, total)
	var wg sync.WaitGroup

	start := make(chan struct{})

	for i := 0; i < total; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			<-start

			result, err := limiter.Allow(
				context.Background(),
				key,
				limit,
				60*time.Second,
			)

			results <- outcome{result: result, err: err}
		}()
	}

	// 同时释放全部 goroutine，制造并发竞争。
	close(start)

	wg.Wait()
	close(results)

	allowed := 0
	rejected := 0
	counts := make(map[int64]bool)

	for item := range results {
		if item.err != nil {
			t.Fatalf("concurrent request failed: %v", item.err)
		}

		if item.result.Allowed {
			allowed++
		} else {
			rejected++
		}

		count := item.result.Count

		if count < 1 || count > total {
			t.Fatalf("unexpected count: %d", count)
		}
		if counts[count] {
			t.Fatalf("duplicate count: %d", count)
		}
		counts[count] = true
	}

	if allowed != 10 || rejected != 20 {
		t.Fatalf(
			"allowed=%d, rejected=%d, want 10/20",
			allowed, rejected,
		)
	}

	t.Logf("PASS: concurrent=%d, allowed=%d, rejected=%d",
		total, allowed, rejected)
}

// 测试三：不同用户的计数必须隔离。
func TestAllow_UserIsolation(t *testing.T) {
	limiter, client := setupTestLimiter(t)

	keyA := newTestKey(t, client) + ":user:A"
	keyB := newTestKey(t, client) + ":user:B"

	ctx := context.Background()
	window := 60 * time.Second

	t.Cleanup(func() {
		_ = client.Del(context.Background(), keyA, keyB).Err()
	})

	// 用户 A 已经超限。
	for i := 0; i < 11; i++ {
		result, err := limiter.Allow(ctx, keyA, 10, window)
		if err != nil {
			t.Fatal(err)
		}
		if i == 10 && result.Allowed {
			t.Fatal("user A should be rejected")
		}
	}

	// 用户 B 的首次请求不应该受到影响。
	result, err := limiter.Allow(ctx, keyB, 10, window)
	if err != nil {
		t.Fatal(err)
	}

	if !result.Allowed || result.Count != 1 {
		t.Fatalf("user B affected by user A: %+v", result)
	}

	t.Log("PASS: user A limit does not affect user B")
}

// 测试四：窗口过期后应重新计数。
func TestAllow_WindowExpires(t *testing.T) {
	limiter, client := setupTestLimiter(t)
	key := newTestKey(t, client)

	ctx := context.Background()
	window := 250 * time.Millisecond

	// 第一次：允许。
	first, err := limiter.Allow(ctx, key, 1, window)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Allowed || first.Count != 1 {
		t.Fatalf("first request unexpected: %+v", first)
	}

	// 第二次：超限。
	second, err := limiter.Allow(ctx, key, 1, window)
	if err != nil {
		t.Fatal(err)
	}
	if second.Allowed || second.Count != 2 {
		t.Fatalf("second request should be rejected: %+v", second)
	}

	// 等待超过窗口。
	time.Sleep(window + 100*time.Millisecond)

	// 第三次：新窗口，应该重新放行。
	third, err := limiter.Allow(ctx, key, 1, window)
	if err != nil {
		t.Fatal(err)
	}
	if !third.Allowed || third.Count != 1 {
		t.Fatalf("window did not reset: %+v", third)
	}

	t.Log("PASS: expired window resets counter")
}

// 测试五：Redis 不可用时必须返回错误，不能默认放行。
func TestAllow_RedisUnavailable(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:6379",
	})

	limiter := NewRedisLimiter(client)

	// 主动关闭客户端，稳定模拟 Redis 调用失败。
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := limiter.Allow(
		context.Background(),
		"test:redis-unavailable",
		10,
		60*time.Second,
	)

	if err == nil {
		t.Fatal("expected Redis error, got nil")
	}

	if result.Allowed {
		t.Fatal("Redis unavailable must not return Allowed=true")
	}

	t.Logf("PASS: Redis error propagated: %v", err)
}
