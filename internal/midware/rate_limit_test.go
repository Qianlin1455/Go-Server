package midware

import (
	"GoServer/internal/ratelimit"
	"GoServer/internal/security"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// FakeLimiter：模拟 Redis 限流器，避免测试依赖真实 Redis。
type FakeLimiter struct {
	result  ratelimit.Result
	err     error
	keys    []string
	limits  []int64
	windows []time.Duration
}

func (f *FakeLimiter) Allow(
	ctx context.Context,
	key string,
	limit int64,
	window time.Duration,
) (ratelimit.Result, error) {
	f.keys = append(f.keys, key)
	f.limits = append(f.limits, limit)
	f.windows = append(f.windows, window)

	return f.result, f.err
}

// 执行一次 HTTP 请求，模拟 JWT Middleware 写入 user_id。

func performRateLimitRequest(
	t *testing.T,
	limiter Limiter,
	userID uint64,
	hasUser bool,
) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)

	// 使用真实 JWTManager。
	jwtManager, err := security.NewJWTManager(
		"01234567890123456789012345678901",
		time.Hour,
	)
	if err != nil {
		t.Fatalf("create JWT manager: %v", err)
	}

	router := gin.New()

	router.POST(
		"/test",

		// 先验证 JWT 并写入 Request Context。
		JWTAuth(jwtManager),

		// 再执行限流。
		RateLimit(limiter, "repo_bind", 10, time.Minute),

		// 限流通过后才会执行。
		func(c *gin.Context) {
			c.Status(http.StatusNoContent)
		},
	)

	req := httptest.NewRequest(
		http.MethodPost,
		"/test",
		nil,
	)

	// 模拟客户端携带 JWT。
	if hasUser {
		token, err := jwtManager.Generate(userID)
		if err != nil {
			t.Fatalf("generate JWT: %v", err)
		}

		req.Header.Set(
			"Authorization",
			"Bearer "+token,
		)
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	return recorder
}

// 测试一：正常请求应该放行。
func TestRateLimit_Allowed(t *testing.T) {
	limiter := &FakeLimiter{
		result: ratelimit.Result{
			Allowed:   true,
			Count:     1,
			Remaining: 50 * time.Second,
		},
	}

	recorder := performRateLimitRequest(
		t, limiter, 1001, true,
	)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204", recorder.Code)
	}

	if len(limiter.keys) != 1 {
		t.Fatalf("Allow calls=%d, want 1", len(limiter.keys))
	}

	if limiter.keys[0] != "rate_limit:repo_bind:user:1001" {
		t.Fatalf("unexpected key: %s", limiter.keys[0])
	}

	t.Log("PASS: allowed request reaches handler")
}

// 测试二：超过限额应该返回 HTTP 429。
func TestRateLimit_TooManyRequests(t *testing.T) {
	limiter := &FakeLimiter{
		result: ratelimit.Result{
			Allowed:   false,
			Count:     11,
			Remaining: 30 * time.Second,
		},
	}

	recorder := performRateLimitRequest(
		t, limiter, 1001, true,
	)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d, want 429", recorder.Code)
	}

	if recorder.Header().Get("Retry-After") != "30" {
		t.Fatalf(
			"Retry-After=%s, want 30",
			recorder.Header().Get("Retry-After"),
		)
	}

	t.Log("PASS: exceeded limit returns HTTP 429")
}

// 测试三：Redis 故障应该 Fail-Closed，返回 HTTP 503。
func TestRateLimit_RedisUnavailable(t *testing.T) {
	limiter := &FakeLimiter{
		err: errors.New("redis connection failed"),
	}

	recorder := performRateLimitRequest(
		t, limiter, 1001, true,
	)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", recorder.Code)
	}

	t.Log("PASS: Redis failure returns HTTP 503")
}

// 测试四：没有经过 JWT 认证的请求不能进入限流业务。
func TestRateLimit_MissingUserID(t *testing.T) {
	limiter := &FakeLimiter{
		result: ratelimit.Result{
			Allowed: true,
		},
	}

	recorder := performRateLimitRequest(
		t, limiter, 0, false,
	)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", recorder.Code)
	}

	if len(limiter.keys) != 0 {
		t.Fatalf("limiter should not be called")
	}

	t.Log("PASS: missing user ID returns HTTP 401")
}

// 测试五：不同用户应该生成不同的限流 Key。
func TestRateLimit_UserIsolation(t *testing.T) {
	limiter := &FakeLimiter{
		result: ratelimit.Result{
			Allowed: true,
			Count:   1,
		},
	}

	first := performRateLimitRequest(
		t, limiter, 1001, true,
	)

	second := performRateLimitRequest(
		t, limiter, 1002, true,
	)

	if first.Code != http.StatusNoContent ||
		second.Code != http.StatusNoContent {
		t.Fatal("both users should be allowed")
	}

	if len(limiter.keys) != 2 {
		t.Fatalf("Allow calls=%d, want 2", len(limiter.keys))
	}

	if limiter.keys[0] == limiter.keys[1] {
		t.Fatal("different users must have different keys")
	}

	if limiter.keys[0] != "rate_limit:repo_bind:user:1001" {
		t.Fatalf("unexpected first key: %s", limiter.keys[0])
	}

	if limiter.keys[1] != "rate_limit:repo_bind:user:1002" {
		t.Fatalf("unexpected second key: %s", limiter.keys[1])
	}

	t.Log("PASS: different users have isolated rate limit keys")
}
