package handler

import (
	"errors"
	"net/http"
	"strconv"

	"GoServer/internal/midware"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

type ConversationHandler struct {
	svc *service.ConversationService
}

func NewConversationHandler(
	svc *service.ConversationService,
) *ConversationHandler {
	return &ConversationHandler{svc: svc}
}

func (h *ConversationHandler) GetConversation(c *gin.Context) {
	// 1. 从 JWT Middleware 写入的 Context 获取身份
	userID, ok := midware.UserIDFromContext(
		c.Request.Context(),
	)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    "UNAUTHORIZED",
			"message": "用户身份无效",
		})
		return
	}

	// 2. 解析 URL 中的会话 ID
	conversationID, err := strconv.ParseUint(
		c.Param("id"), 10, 64,
	)

	if err != nil || conversationID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "无效的会话 ID",
		})
		return
	}

	// 3. 调用 Service 检查权限并获取资源
	result, err := h.svc.GetConversation(
		c.Request.Context(),
		userID,
		conversationID,
	)

	if err != nil {
		switch {
		case errors.Is(err, service.ErrConversationForbidden):
			c.JSON(http.StatusForbidden, gin.H{
				"code":    "FORBIDDEN",
				"message": "无权访问该会话",
			})

		case errors.Is(err, service.ErrConversationNotFound):
			c.JSON(http.StatusNotFound, gin.H{
				"code":    "NOT_FOUND",
				"message": "会话不存在",
			})

		default:
			_ = c.Error(err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "服务器内部错误",
			})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": "SUCCESS",
		"data": result,
	})
}
