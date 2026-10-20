package handler

import (
	"net/http"
	"strconv"

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

func (h *ProjectHandler) GetProject(c *gin.Context) {
	// TODO 1:
	// 从 Request Context 读取 userID
	// 未认证返回 401
	userID, ok := midware.UserIDFromContext(c.Request.Context())
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    "UNAUTHORIZED",
			"message": "用户身份无效",
		})
		return
	}

	// TODO 2:
	// 使用 strconv.ParseUint 解析 c.Param("id")
	// ID 非法或为 0 返回 400
	projectID, err := strconv.ParseUint(
		c.Param("id"), 10, 64,
	)
	if err != nil || projectID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "无效的项目ID",
		})
		return
	}

	// TODO 3:
	// 调用 h.svc.GetProject(ctx, userID, projectID)
	result, err := h.svc.GetProject(
		c.Request.Context(),
		userID,
		projectID,
	)

	// TODO 4:
	// 错误交给 WriteServiceerror
	if err != nil {
		WriteServiceerror(c, err)
		return
	}

	// TODO 5:
	// 成功返回 HTTP 200
	// {"code": "SUCCESS", "data": result}
	c.JSON(http.StatusOK, gin.H{
		"code": "SUCCESS",
		"data": result,
	})
}

func (h *ProjectHandler) ListProjects(c *gin.Context) {
	// 1. 从 JWT Context 获取用户身份
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

	// 2. 获取分页参数，设置默认值
	pageStr := c.DefaultQuery("page", "1")
	pageSizeStr := c.DefaultQuery("page_size", "20")

	// 3. 转换 page
	page, err := strconv.Atoi(pageStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "无效的 page 参数",
		})
		return
	}

	// 4. 转换 page_size
	pageSize, err := strconv.Atoi(pageSizeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "无效的 page_size 参数",
		})
		return
	}

	// 5. 调用 Service
	projects, err := h.svc.ListProjects(
		c.Request.Context(),
		userID,
		page,
		pageSize,
	)

	// 6. 统一错误处理
	if err != nil {
		WriteServiceerror(c, err)
		return
	}

	// 7. 返回项目列表
	c.JSON(http.StatusOK, gin.H{
		"code": "SUCCESS",
		"data": projects,
	})
}
