package handler

import (
	"net/http"

	"GoServer/internal/midware"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

type ProjectHandler struct {
	svc *service.ProjectService
}

func NewProjectHandler(
	svc *service.ProjectService,
) *ProjectHandler {
	return &ProjectHandler{svc: svc}
}

type CreateProjectRequest struct {
	Name        string  `json:"name" binding:"required"`
	Description *string `json:"description"`
}

func (h *ProjectHandler) CreateProject(c *gin.Context) {
	// 1. 从 JWT 中间件注入的 Request Context 获取用户身份。
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

	// 2. 解析 JSON 请求。
	var req CreateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "项目参数不合法",
		})
		return
	}

	// 3. 调用 Service，不能使用客户端传入的 user_id。
	result, err := h.svc.CreateProject(
		c.Request.Context(),
		userID,
		service.CreateProjectInput{
			Name:        req.Name,
			Description: req.Description,
		},
	)
	if err != nil {
		WriteServiceerror(c, err)
		return
	}

	// 4. 创建资源，返回 HTTP 201。
	c.JSON(http.StatusCreated, gin.H{
		"code": "SUCCESS",
		"data": result,
	})
}
