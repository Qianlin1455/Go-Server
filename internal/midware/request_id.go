package midware

import (
	"github.com/gin-gonic/gin"
	"strconv"
	"time"
)

func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		//检查客户端有没有传id,没有就生成默认id
		requestId := c.Request.Header.Get("X-Request-ID")
		if requestId == "" {
			requestId = strconv.FormatInt(time.Now().UnixNano(), 10)
		}
		//把id存入context上下文
		c.Set("request_id", requestId)
		c.Header("X-Request-ID", requestId)
		//往后执行c
		c.Next()
	}
}
