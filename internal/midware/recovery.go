package midware

import (
	"GoServer/internal/response"
	"github.com/gin-gonic/gin"
	"net/http"
)

func Recovery_() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			err := recover()
			if err != nil {
				c.Abort()
				response.Error(
					c,
					http.StatusInternalServerError,
					response.ErrorCode,
					"internal server error",
				)
				return
			}
		}()
		c.Next()

	}
}
