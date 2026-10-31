package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"GoServer/internal/githubclient"
	"GoServer/internal/model"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

type RepositoryBinder interface {
	BindRepository(
		ctx context.Context,
		userID, projectID uint64,
		owner, repo string,
	) (*model.Repository, error)
}

type GitHubBindingHandler struct {
	svc RepositoryBinder
}

func NewGitHubBindingHandler(
	svc RepositoryBinder,
) *GitHubBindingHandler {
	return &GitHubBindingHandler{svc: svc}
}

type bindRepositoryRequest struct {
	Owner string `json:"owner" binding:"required"`
	Repo  string `json:"repo" binding:"required"`
}

func (h *GitHubBindingHandler) Bind(c *gin.Context) {
	// 1. 从 JWT Middleware 获取身份
	userID := c.GetUint64("user_id")
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    "UNAUTHORIZED",
			"message": "authentication required",
		})
		return
	}

	// 2. 解析路径中的项目 ID
	projectID, err := strconv.ParseUint(
		c.Param("id"), 10, 64,
	)
	if err != nil || projectID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_PROJECT_ID",
			"message": "invalid project ID",
		})
		return
	}

	// 3. 解析请求体
	var req bindRepositoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_REQUEST",
			"message": "invalid repository binding request",
		})
		return
	}

	// 4. 调用 Service，不在 Handler 中直接访问数据库
	binding, err := h.svc.BindRepository(
		c.Request.Context(),
		userID, projectID,
		req.Owner, req.Repo,
	)
	if err != nil {
		writeGitHubBindingError(c, err)
		return
	}

	// 5. 返回明确的响应字段
	c.JSON(http.StatusCreated, gin.H{
		"code":    "SUCCESS",
		"message": "repository bound",
		"data": gin.H{
			"id":             binding.ID,
			"project_id":     binding.ProjectID,
			"owner":          binding.Owner,
			"repo":           binding.Repo,
			"default_branch": binding.DefaultBranch,
			"last_sha":       binding.LastSHA,
		},
	})
}

func writeGitHubBindingError(
	c *gin.Context,
	err error,
) {
	status := http.StatusInternalServerError
	code := "INTERNAL_ERROR"

	switch {
	case errors.Is(err, service.ErrGitHubBindingInvalidInput),
		errors.Is(err, githubclient.ErrInvalidInput):
		status, code = http.StatusBadRequest, "INVALID_INPUT"

	case errors.Is(err, service.ErrGitHubBindingProjectNotFound):
		status, code = http.StatusNotFound, "PROJECT_NOT_FOUND"

	case errors.Is(err, githubclient.ErrNotFound),
		errors.Is(err, githubclient.ErrPrivate):
		status, code = http.StatusNotFound, "REPOSITORY_NOT_FOUND"

	case errors.Is(err, service.ErrGitHubRepositoryAlreadyBound):
		status, code = http.StatusConflict, "REPOSITORY_ALREADY_BOUND"

	case errors.Is(err, githubclient.ErrRateLimited):
		status, code = http.StatusServiceUnavailable, "GITHUB_RATE_LIMITED"

	case errors.Is(err, githubclient.ErrTimeout),
		errors.Is(err, context.DeadlineExceeded):
		status, code = http.StatusGatewayTimeout, "GITHUB_TIMEOUT"

	case errors.Is(err, githubclient.ErrForbidden),
		errors.Is(err, githubclient.ErrRedirect),
		errors.Is(err, githubclient.ErrTooLarge),
		errors.Is(err, githubclient.ErrBadResponse),
		errors.Is(err, githubclient.ErrUpstream):
		status, code = http.StatusBadGateway, "GITHUB_UPSTREAM_ERROR"
	}

	c.JSON(status, gin.H{
		"code":    code,
		"message": http.StatusText(status),
	})
}
