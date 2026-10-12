package handler

import (
	"errors"
	"net/http"
	"strconv"

	"GoServer/internal/midware"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

type MessageHandler struct {
	svc *service.MessageService
}

func NewMessageHandler(
	svc *service.MessageService,
) *MessageHandler {
	return &MessageHandler{
		svc: svc,
	}
}

// =========================
// 创建消息
// POST /api/v1/conversations/:id/messages
// =========================

type CreateMessageRequest struct {
	Role    string `json:"role" binding:"required"`
	Content string `json:"content" binding:"required"`
}

func (h *MessageHandler) CreateMessage(c *gin.Context) {

	// 1. 获取 JWT 用户身份
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

	// 2. 解析 conversation id
	conversationID, err := strconv.ParseUint(
		c.Param("id"),
		10,
		64,
	)

	if err != nil || conversationID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "无效的会话 ID",
		})
		return
	}

	// 3. 解析 body
	var req CreateMessageRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "请求参数错误",
		})
		return
	}

	// 4. 调 Service
	msg, err := h.svc.CreateMessage(
		c.Request.Context(),
		userID,
		conversationID,
		req.Role,
		req.Content,
	)

	if err != nil {

		switch {

		case errors.Is(
			err,
			service.ErrConversationNotFound,
		):
			c.JSON(http.StatusNotFound, gin.H{
				"code":    "NOT_FOUND",
				"message": "会话不存在",
			})

		case errors.Is(
			err,
			service.ErrConversationForbidden,
		):
			c.JSON(http.StatusForbidden, gin.H{
				"code":    "FORBIDDEN",
				"message": "无权访问该会话",
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

	// 5. 返回结果
	c.JSON(http.StatusCreated, gin.H{
		"code": "SUCCESS",
		"data": msg,
	})
}

// =========================
// 查询历史消息
// GET /api/v1/conversations/:id/messages
// =========================

func (h *MessageHandler) ListMessages(c *gin.Context) {

	// 1. 获取用户身份
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

	// 2. conversation id
	conversationID, err := strconv.ParseUint(
		c.Param("id"),
		10,
		64,
	)

	if err != nil || conversationID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "无效的会话 ID",
		})
		return
	}

	// 3. 调 Service
	messages, err := h.svc.ListMessages(
		c.Request.Context(),
		userID,
		conversationID,
	)

	if err != nil {

		switch {

		case errors.Is(
			err,
			service.ErrConversationNotFound,
		):
			c.JSON(http.StatusNotFound, gin.H{
				"code":    "NOT_FOUND",
				"message": "会话不存在",
			})

		case errors.Is(
			err,
			service.ErrConversationForbidden,
		):
			c.JSON(http.StatusForbidden, gin.H{
				"code":    "FORBIDDEN",
				"message": "无权访问该会话",
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

	// 4. 返回历史消息
	c.JSON(http.StatusOK, gin.H{
		"code": "SUCCESS",
		"data": messages,
	})
}
