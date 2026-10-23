package handler

import (
	"net/http"
	"strconv"

	"GoServer/internal/midware"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

type TaskHandler struct {
	svc *service.TaskService
}

func NewTaskHandler(svc *service.TaskService) *TaskHandler {
	return &TaskHandler{svc: svc}
}

// ================================
// Request DTO
// ================================

type CreateTaskRequest struct {
	Title           string  `json:"title" binding:"required"`
	Description     *string `json:"description"`
	SourceMessageID *uint64 `json:"source_message_id"`
}

type ChangeTaskStatusRequest struct {
	Status  string  `json:"status" binding:"required"`
	Version *uint32 `json:"version" binding:"required"`
}

// ================================
// Common Helpers
// ================================

// 从 JWT Context 获取当前用户 ID
func taskUserID(c *gin.Context) (uint64, bool) {
	userID, ok := midware.UserIDFromContext(
		c.Request.Context(),
	)

	if !ok || userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    "UNAUTHORIZED",
			"message": "用户身份无效",
		})
		return 0, false
	}

	return userID, true
}

// 解析路径中的正整数 ID
func parseTaskPathID(
	c *gin.Context,
	param string,
) (uint64, bool) {

	id, err := strconv.ParseUint(
		c.Param(param),
		10,
		64,
	)

	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "无效的资源 ID",
		})
		return 0, false
	}

	return id, true
}

// ================================
// 1. CreateTask
// POST /api/v1/projects/:id/tasks
// ================================

func (h *TaskHandler) CreateTask(c *gin.Context) {

	// 1. 从 JWT Context 获取 userID
	userID, ok := taskUserID(c)
	if !ok {
		return
	}

	// 2. 从路径解析 projectID
	projectID, ok := parseTaskPathID(c, "id")
	if !ok {
		return
	}

	// 3. 解析请求 JSON
	var req CreateTaskRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "任务创建参数不合法",
		})
		return
	}

	// 4. 构造 Service 输入
	input := service.CreateTaskInput{
		ProjectID:       projectID,
		Title:           req.Title,
		Description:     req.Description,
		SourceMessageID: req.SourceMessageID,
	}

	// 5. 调用 Service
	task, err := h.svc.CreateTask(
		c.Request.Context(),
		userID,
		input,
	)

	if err != nil {
		WriteServiceerror(c, err)
		return
	}

	// 6. 返回 HTTP 201
	c.JSON(http.StatusCreated, gin.H{
		"code": "SUCCESS",
		"data": task,
	})
}

// ================================
// 2. ListTasks
// GET /api/v1/projects/:id/tasks
// ================================

func (h *TaskHandler) ListTasks(c *gin.Context) {
	// 读取状态过滤参数
	status := c.Query("status")

	// 校验 status 是否合法
	switch status {
	case "", "todo", "doing", "done":
		// 合法状态
	default:
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "无效的 status 参数",
		})
		return
	}

	// 1. JWT 身份验证
	userID, ok := taskUserID(c)
	if !ok {
		return
	}

	// 2. 解析 projectID
	projectID, ok := parseTaskPathID(c, "id")
	if !ok {
		return
	}

	// 3. 读取分页参数
	pageStr := c.DefaultQuery("page", "1")
	pageSizeStr := c.DefaultQuery("page_size", "20")

	// 4. 转换 page
	page, err := strconv.Atoi(pageStr)

	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "无效的 page 参数",
		})
		return
	}

	// 5. 转换 page_size
	pageSize, err := strconv.Atoi(pageSizeStr)

	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "无效的 page_size 参数",
		})
		return
	}

	// 6. 调用 Service
	tasks, total, err := h.svc.ListTasksPage(
		c.Request.Context(),
		userID,
		projectID,
		page,
		pageSize,
		status,
	)

	if err != nil {
		WriteServiceerror(c, err)
		return
	}

	// 7. 返回任务列表
	c.JSON(http.StatusOK, gin.H{
		"code": "SUCCESS",
		"data": gin.H{
			"items":     tasks,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		},
	})
}

// ================================
// 3. ChangeStatus
// PATCH /api/v1/tasks/:id/status
// ================================

func (h *TaskHandler) ChangeStatus(c *gin.Context) {

	// 1. JWT 身份验证
	userID, ok := taskUserID(c)
	if !ok {
		return
	}

	// 2. 解析 taskID
	taskID, ok := parseTaskPathID(c, "id")
	if !ok {
		return
	}

	// 3. 解析请求 JSON
	var req ChangeTaskStatusRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "任务状态参数不合法",
		})
		return
	}

	// 4. 调用 Service
	err := h.svc.ChangeStatus(
		c.Request.Context(),
		userID,
		taskID,
		req.Status,
		*req.Version,
	)

	if err != nil {
		WriteServiceerror(c, err)
		return
	}

	// 5. 修改成功
	// 当前 ChangeStatus 只返回 error，
	// 因此采用 HTTP 204，无响应正文
	c.Status(http.StatusNoContent)
}

func (h *TaskHandler) Retrospective(c *gin.Context) {
	userID, ok := taskUserID(c)
	if !ok {
		return
	}
	taskID, ok := parseTaskPathID(c, "id")
	if !ok {
		return
	}
	result, err := h.svc.Retrospective(c.Request.Context(), userID, taskID)
	if err != nil {
		WriteServiceerror(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": "SUCCESS", "data": result})
}
