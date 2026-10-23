package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// DeadlineExceeded writes the shared public timeout response only before commit.
func DeadlineExceeded(c *gin.Context, err error) {
	_ = c.Error(err)
	c.Abort()
	if c.Writer.Written() {
		return
	}
	c.JSON(http.StatusGatewayTimeout, gin.H{
		"code":    "DEADLINE_EXCEEDED",
		"message": "请求处理超时",
	})
}
