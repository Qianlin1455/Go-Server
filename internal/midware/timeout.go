package midware

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
)

func Timeout(t time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		pctx := c.Request.Context()
		ctx, cancel := context.WithTimeout(pctx, t)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
