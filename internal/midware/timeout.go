package midware

import (
	"GoServer/internal/response"
	"context"
	"errors"
	"time"

	"github.com/gin-gonic/gin"
)

// Timeout cooperatively cancels downstream work. It cannot preempt a handler
// that ignores context, or replace headers/body that have already been sent.
func Timeout(t time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		pctx := c.Request.Context()
		ctx, cancel := context.WithTimeout(pctx, t)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && !c.Writer.Written() {
			response.DeadlineExceeded(c, ctx.Err())
		}
	}
}
