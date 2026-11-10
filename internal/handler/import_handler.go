package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"GoServer/internal/midware"
	"GoServer/internal/model"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

type ImportJobCreator interface {
	CreateImportJob(
		ctx context.Context,
		userID uint64,
		input service.CreateImportJobInput,
	) (*model.ImportJob, bool, error)
}

type ImportHandler struct {
	svc ImportJobCreator
}

func NewImportHandler(svc ImportJobCreator) *ImportHandler {
	return &ImportHandler{svc: svc}
}

type createImportRequest struct {
	RepositoryID uint64 `json:"repository_id"`
}

func (h *ImportHandler) Create(c *gin.Context) {
	// 1. 获取 JWT 用户身份
	userID, ok := midware.UserIDFromContext(
		c.Request.Context(),
	)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"code":    "UNAUTHORIZED",
			"message": "authentication required",
		})
		return
	}

	// 2. 解析项目 ID
	projectID, err := strconv.ParseUint(
		c.Param("id"), 10, 64,
	)
	if err != nil || projectID == 0 {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_PROJECT_ID",
			"message": "invalid project ID",
		})
		return
	}

	// 3. 读取幂等键
	keys := c.Request.Header.Values("Idempotency-Key")
	if len(keys) != 1 || keys[0] == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_IDEMPOTENCY_KEY",
			"message": "one Idempotency-Key is required",
		})
		return
	}
	key := keys[0]

	// 4. 解析 JSON
	// 限制大小并拒绝未知字段
	c.Request.Body = http.MaxBytesReader(
		c.Writer, c.Request.Body, 4096,
	)

	var req createImportRequest
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_REQUEST",
			"message": "invalid JSON request",
		})
		return
	}

	// 拒绝多个连续的 JSON 对象
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_REQUEST",
			"message": "unexpected extra JSON data",
		})
		return
	}

	// 5. 调用 Service
	job, created, err := h.svc.CreateImportJob(
		c.Request.Context(),
		userID,
		service.CreateImportJobInput{
			ProjectID:      projectID,
			RepositoryID:   req.RepositoryID,
			IdempotencyKey: key,
		},
	)

	if err != nil {
		writeImportError(c, err)
		return
	}

	// 6. 根据是否新建决定响应状态
	status := http.StatusOK
	message := "existing import job returned"

	if created {
		status = http.StatusAccepted
		message = "import job accepted"
	}

	c.JSON(status, gin.H{
		"code":    "SUCCESS",
		"message": message,
		"data": gin.H{
			"job_id":        job.ID,
			"project_id":    job.ProjectID,
			"repository_id": job.RepositoryID,
			"revision_sha":  job.RevisionSHA,
			"status":        job.Status,
		},
	})
}

func writeImportError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	code := "INTERNAL_ERROR"

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
		code = "DEADLINE_EXCEEDED"

	case errors.Is(err, context.Canceled):
		status = http.StatusRequestTimeout
		code = "REQUEST_CANCELED"

	case errors.Is(err, service.ErrImportInvalidInput):
		status = http.StatusBadRequest
		code = "INVALID_INPUT"

	case errors.Is(err, service.ErrImportProjectNotFound):
		status = http.StatusNotFound
		code = "PROJECT_NOT_FOUND"

	case errors.Is(err, service.ErrImportRepositoryNotFound):
		status = http.StatusNotFound
		code = "REPOSITORY_NOT_FOUND"

	case errors.Is(err, service.ErrImportProjectArchived):
		status = http.StatusConflict
		code = "PROJECT_ARCHIVED"

	case errors.Is(err, service.ErrImportIdempotencyConflict):
		status = http.StatusConflict
		code = "IDEMPOTENCY_KEY_CONFLICT"

	default:
		_ = c.Error(err)
	}

	c.AbortWithStatusJSON(status, gin.H{
		"code":    code,
		"message": http.StatusText(status),
	})
}
