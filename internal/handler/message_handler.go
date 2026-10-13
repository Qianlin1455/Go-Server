package handler

import (
	"net/http"
	"strconv"

	"GoServer/internal/midware"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

// MessageHandler 负责消息相关 HTTP 请求
type MessageHandler struct {
	svc *service.MessageService
}

// NewMessageHandler 创建消息 Handler
func NewMessageHandler(
	svc *service.MessageService,
) *MessageHandler {
	return &MessageHandler{
		svc: svc,
	}
}

// ================================
// 1. 创建消息
// POST /api/v1/conversations/:id/messages
// ================================

// CreateMessageRequest 创建消息请求
// Role 由 Service 固定为 user，不允许客户端自行指定
type CreateMessageRequest struct {
	Content string `json:"content" binding:"required"`
}

func (h *MessageHandler) CreateMessage(c *gin.Context) {

	// 1. 从 JWT Middleware 获取用户 ID
	userID, ok := midware.UserIDFromContext(
		c.Request.Context(),
	)

	if !ok || userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    "UNAUTHORIZED",
			"message": "用户身份无效",
		})
		return
	}

	// 2. 解析会话 ID
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

	// 3. 解析并校验 JSON 请求体
	var req CreateMessageRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "消息请求参数不合法",
		})
		return
	}

	// 4. 调用 Service
	// SendMessage 内部负责：
	// ① 参数校验
	// ② 开启数据库事务
	// ③ 校验会话归属并锁定会话行
	// ④ INSERT messages
	// ⑤ UPDATE conversations.updated_at
	// ⑥ COMMIT / ROLLBACK
	msg, err := h.svc.SendMessage(
		c.Request.Context(),
		userID,
		service.SendMessageInput{
			ConversationID: conversationID,
			Content:        req.Content,
		},
	)

	if err != nil {
		WriteServiceerror(c, err)
		return
	}

	// 5. 事务提交成功后返回 201
	c.JSON(http.StatusCreated, gin.H{
		"code": "SUCCESS",
		"data": msg,
	})
}

// ================================
// 2. 查询历史消息
// GET /api/v1/conversations/:id/messages
// ================================

func (h *MessageHandler) ListMessages(c *gin.Context) {

	// 1. 从 JWT Middleware 获取用户 ID
	userID, ok := midware.UserIDFromContext(
		c.Request.Context(),
	)

	if !ok || userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    "UNAUTHORIZED",
			"message": "用户身份无效",
		})
		return
	}

	// 2. 解析会话 ID
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
	pageStr := c.DefaultQuery("page", "1")
	page, err := strconv.Atoi(pageStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "page 必须是整数",
		})
		return
	}
	pageSizeStr := c.DefaultQuery("page_size", "20")
	pageSize, err := strconv.Atoi(pageSizeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "page_size 必须是整数",
		})
		return
	}

	// 3. 调用 Service 查询历史消息
	// Service 负责检查会话是否存在及用户权限
	messages, err := h.svc.ListMessages(
		c.Request.Context(),
		userID,
		conversationID,
		page,
		pageSize,
	)

	if err != nil {
		WriteServiceerror(c, err)
		return
	}

	// 4. 返回查询结果
	c.JSON(http.StatusOK, gin.H{
		"code": "SUCCESS",
		"data": messages,
	})
}
