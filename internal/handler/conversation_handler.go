package handler

import (
	"net/http"
	"strconv"

	"GoServer/internal/midware"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

type ConversationHandler struct {
	svc *service.ConversationService
}

type CreateConversationRequest struct {
	Title     string  `json:"title" binding:"required"`
	ProjectID *uint64 `json:"project_id"`
}

func NewConversationHandler(
	svc *service.ConversationService,
) *ConversationHandler {
	return &ConversationHandler{svc: svc}
}

func (h *ConversationHandler) CreateConversation(c *gin.Context) {
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

	var req CreateConversationRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "参数错误",
		})
		return
	}

	result, err := h.svc.CreateConversationWithProject(
		c.Request.Context(),
		userID,
		req.Title,
		req.ProjectID,
	)

	if err != nil {
		WriteServiceerror(c, err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"code": "SUCCESS",
		"data": result,
	})
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
		WriteServiceerror(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": "SUCCESS",
		"data": result,
	})
}

func (h *ConversationHandler) ListProjectConversations(
	c *gin.Context,
) {
	// 1. 获取 JWT 用户身份
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

	// 2. 解析 URL 中的项目 ID
	projectID, err := strconv.ParseUint(
		c.Param("id"),
		10,
		64,
	)

	if err != nil || projectID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "无效的项目 ID",
		})
		return
	}

	// 3. 解析分页参数
	pageStr := c.DefaultQuery("page", "1")
	pageSizeStr := c.DefaultQuery("page_size", "20")

	page, err := strconv.Atoi(pageStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "page 必须是整数",
		})
		return
	}

	pageSize, err := strconv.Atoi(pageSizeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "page_size 必须是整数",
		})
		return
	}

	// 4. 调用 Service
	conversations, err := h.svc.ListProjectConversations(
		c.Request.Context(),
		userID,
		projectID,
		page,
		pageSize,
	)

	// 5. 统一错误映射
	if err != nil {
		WriteServiceerror(c, err)
		return
	}

	// 6. 返回项目内会话列表
	c.JSON(http.StatusOK, gin.H{
		"code": "SUCCESS",
		"data": conversations,
	})
}
