package midware

import (
	"context"
	"net/http"
	"strings"

	"GoServer/internal/security"

	"github.com/gin-gonic/gin"
)

// 自定义 Context Key，避免与其他包发生键冲突
type userIDContextKey struct{}

// JWTAuth 验证客户端携带的 JWT
func JWTAuth(manager *security.JWTManager) gin.HandlerFunc {
	return func(c *gin.Context) {

		// 1. 提取 Authorization Header
		authHeader := c.GetHeader("Authorization")

		if authHeader == "" {
			c.AbortWithStatusJSON(
				http.StatusUnauthorized,
				gin.H{
					"code":    "MISSING_TOKEN",
					"message": "缺少身份凭证",
				},
			)
			return
		}

		// 2. 验证 Bearer Token 格式
		parts := strings.Fields(authHeader)

		if len(parts) != 2 ||
			!strings.EqualFold(parts[0], "Bearer") {
			c.AbortWithStatusJSON(
				http.StatusUnauthorized,
				gin.H{
					"code":    "INVALID_TOKEN",
					"message": "Token 格式不正确",
				},
			)
			return
		}

		// 3. 验证 JWT
		userID, err := manager.Verify(parts[1])
		if err != nil {
			c.AbortWithStatusJSON(
				http.StatusUnauthorized,
				gin.H{
					"code":    "INVALID_TOKEN",
					"message": "Token 无效或已过期",
				},
			)
			return
		}

		// 4. 写入标准 Go Request Context
		ctx := context.WithValue(
			c.Request.Context(),
			userIDContextKey{},
			userID,
		)

		c.Request = c.Request.WithContext(ctx)

		// 5. 继续执行后面的 Handler
		c.Next()
	}
}

// 从 Request Context 获取当前用户 ID
func UserIDFromContext(ctx context.Context) (uint64, bool) {
	userID, ok := ctx.Value(userIDContextKey{}).(uint64)

	return userID, ok && userID != 0
}
