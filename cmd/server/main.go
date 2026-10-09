package main

import (
	"GoServer/internal/handler"
	"GoServer/internal/midware"
	"GoServer/internal/response"
	"github.com/gin-gonic/gin"
	"net/http"
	"time"
)

func main() {
	r := gin.New()
	r.Use(midware.RequestID())
	r.Use(gin.Logger())
	r.Use(gin.CustomRecovery(func(c *gin.Context, recovered any) {
		c.Abort()

		response.Error(
			c,
			http.StatusInternalServerError,
			response.ErrorCode,
			"internal server error",
		)
	}))
	r.Use(midware.Timeout(3 * time.Second))

	r.GET("/healthz", handler.HandleHealthz)
	r.Run()
}
