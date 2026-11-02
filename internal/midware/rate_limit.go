package midware

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"

	"GoServer/internal/ratelimit"

	"github.com/gin-gonic/gin"
)

type Limiter interface {
	Allow(
		ctx context.Context,
		key string,
		limit int64,
		window time.Duration,
	) (ratelimit.Result, error)
}

func RateLimit(
	limiter Limiter,
	scope string,
	limit int64,
	window time.Duration,
) gin.HandlerFunc {
	return func(c *gin.Context) {

		// 1. 从 JWT Middleware 写入的 Request Context 获取用户 ID。
		userID, ok := UserIDFromContext(c.Request.Context())
		if !ok {
			c.AbortWithStatusJSON(
				http.StatusUnauthorized,
				gin.H{
					"code":    "UNAUTHORIZED",
					"message": "authentication required",
				},
			)
			return
		}

		// 2. 构造用户级限流 Key。
		key := fmt.Sprintf(
			"rate_limit:%s:user:%d",
			scope,
			userID,
		)

		// 3. 执行 Redis 限流。
		result, err := limiter.Allow(
			c.Request.Context(),
			key,
			limit,
			window,
		)

		// 4. Redis 故障：Fail-Closed。
		if err != nil {
			log.Printf(
				"rate limit failed: scope=%s user_id=%d err=%v",
				scope, userID, err,
			)

			c.AbortWithStatusJSON(
				http.StatusServiceUnavailable,
				gin.H{
					"code":    "RATE_LIMIT_UNAVAILABLE",
					"message": "Service Unavailable",
				},
			)
			return
		}

		// 5. 超过限额：HTTP 429。
		if !result.Allowed {
			retryAfter := int64(math.Ceil(
				result.Remaining.Seconds(),
			))

			if retryAfter < 1 {
				retryAfter = 1
			}

			c.Header(
				"Retry-After",
				strconv.FormatInt(retryAfter, 10),
			)

			c.AbortWithStatusJSON(
				http.StatusTooManyRequests,
				gin.H{
					"code":    "RATE_LIMITED",
					"message": "Too Many Requests",
				},
			)
			return
		}

		// 6. 放行请求。
		c.Next()
	}
}
