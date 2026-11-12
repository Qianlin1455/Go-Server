package worker

import (
	"context"
	"errors"

	"GoServer/internal/githubclient"

	"gorm.io/gorm"
)

// classifyExecutionError 根据执行错误判断是否永久失败。
// 返回值：errorCode、permanent。
func classifyExecutionError(err error) (string, bool) {
	switch {
	// 可重试错误
	case errors.Is(err, githubclient.ErrRateLimited):
		return "GITHUB_RATE_LIMITED", false

	case errors.Is(err, githubclient.ErrTimeout),
		errors.Is(err, context.DeadlineExceeded):
		return "GITHUB_TIMEOUT", false

	case errors.Is(err, githubclient.ErrUpstream):
		return "GITHUB_UPSTREAM", false

	case errors.Is(err, githubclient.ErrBadResponse):
		return "GITHUB_BAD_RESPONSE", false

	// 永久错误
	case errors.Is(err, githubclient.ErrNotFound):
		return "GITHUB_NOT_FOUND", true

	case errors.Is(err, githubclient.ErrPrivate):
		return "GITHUB_PRIVATE", true

	case errors.Is(err, githubclient.ErrForbidden):
		return "GITHUB_FORBIDDEN", true

	case errors.Is(err, githubclient.ErrInvalidInput):
		return "GITHUB_INVALID_INPUT", true

	case errors.Is(err, githubclient.ErrRedirect):
		return "GITHUB_REDIRECT_REJECTED", true

	case errors.Is(err, githubclient.ErrTooLarge):
		return "GITHUB_RESPONSE_TOO_LARGE", true

	case errors.Is(err, githubclient.ErrTreeBudgetExceeded):
		return "GITHUB_TREE_BUDGET_EXCEEDED", true

	case errors.Is(err, gorm.ErrRecordNotFound):
		return "RESOURCE_NOT_FOUND", true

	// 未知错误默认可重试，避免丢失任务。
	default:
		return "EXECUTION_FAILED", false
	}
}
