package cache

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestRedisCacheIntegration(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set TEST_REDIS_ADDR to run Redis integration tests")
	}

	c, err := NewRedisCache(
		addr,
		os.Getenv("TEST_REDIS_PASSWORD"),
		0,
		"go-server:test",
		500*time.Millisecond,
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("NewRedisCache() error = %v", err)
	}
	t.Cleanup(func() {
		_ = c.Close()
	})

	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()

	// 1. 验证 Redis 连接。
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}

	key := "integration:" + time.Now().Format("20060102150405.000000000")
	value := []byte("project-cache-test")

	// 2. 未命中应返回 hit=false，而不是错误。
	got, hit, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get(missing key) error = %v", err)
	}
	if hit {
		t.Fatal("expected cache miss")
	}
	if got != nil {
		t.Fatalf("expected nil value on miss, got %q", got)
	}

	// 3. 写入缓存。
	if err := c.Set(ctx, key, value); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	// 4. 验证 Key 前缀确实生效。
	fullKey := c.prefix + ":" + key
	exists, err := c.client.Exists(ctx, fullKey).Result()
	if err != nil {
		t.Fatalf("Exists() error = %v", err)
	}
	if exists != 1 {
		t.Fatalf("expected prefixed key %q to exist", fullKey)
	}

	// 5. 验证命中与数据内容。
	got, hit, err = c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !hit {
		t.Fatal("expected cache hit")
	}
	if string(got) != string(value) {
		t.Fatalf("got %q, want %q", got, value)
	}

	// 6. 验证 TTL 已设置。
	ttl, err := c.client.TTL(ctx, fullKey).Result()
	if err != nil {
		t.Fatalf("TTL() error = %v", err)
	}
	if ttl <= 0 || ttl > 2*time.Second {
		t.Fatalf("unexpected TTL: %v", ttl)
	}

	// 7. 等待 TTL 到期，验证缓存自动失效。
	time.Sleep(2200 * time.Millisecond)

	got, hit, err = c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get(expired key) error = %v", err)
	}
	if hit {
		t.Fatalf("expected cache miss after expiration, got %q", got)
	}
}

func TestRedisCache_TTLJitter(t *testing.T) {
	baseTTL := 300 * time.Second

	c := &RedisCache{
		ttl: baseTTL,
	}

	minTTL := baseTTL
	maxTTL := baseTTL + baseTTL/10

	observed := make(map[time.Duration]bool)

	for i := 0; i < 100; i++ {
		ttl := c.effectiveTTL()

		if ttl < minTTL || ttl > maxTTL {
			t.Fatalf(
				"TTL 超出范围: %v, 预期 [%v, %v]",
				ttl, minTTL, maxTTL,
			)
		}

		observed[ttl] = true
	}

	if len(observed) < 2 {
		t.Fatal("TTL 没有表现出随机变化")
	}

	t.Logf(
		"PASS: TTL 范围 [%v, %v], 不同取值数=%d",
		minTTL, maxTTL, len(observed),
	)
}
