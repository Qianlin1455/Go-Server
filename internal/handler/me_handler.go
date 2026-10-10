package handler

import (
	"net/http"

	"GoServer/internal/midware"

	"github.com/gin-gonic/gin"
)

func HandleMe(c *gin.Context) {

	// 从 Request Context 获取已经认证的 userID
	userID, ok := midware.UserIDFromContext(
		c.Request.Context(),
	)

	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    "INTERNAL_ERROR",
			"message": "无法获取用户身份",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": "SUCCESS",
		"data": gin.H{
			"user_id": userID,
		},
	})
}
