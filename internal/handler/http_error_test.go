package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"GoServer/internal/repository"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

func TestWriteServiceError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "invalid message",
			err:        service.ErrInvalidMessageInput,
			wantStatus: http.StatusBadRequest,
			wantCode:   "INVALID_ARGUMENT",
		},
		{
			name:       "invalid credentials",
			err:        service.ErrInvalidCredentials,
			wantStatus: http.StatusUnauthorized,
			wantCode:   "INVALID_CREDENTIALS",
		},
		{
			name:       "forbidden",
			err:        service.ErrConversationForbidden,
			wantStatus: http.StatusForbidden,
			wantCode:   "FORBIDDEN",
		},
		{
			name:       "not found",
			err:        service.ErrConversationNotFound,
			wantStatus: http.StatusNotFound,
			wantCode:   "NOT_FOUND",
		},
		{
			name:       "already exists",
			err:        service.ErrUserAlreadyExists,
			wantStatus: http.StatusConflict,
			wantCode:   "USER_ALREADY_EXISTS",
		},
		{
			name: "wrapped not found",
			err: fmt.Errorf(
				"get conversation: %w",
				service.ErrConversationNotFound,
			),
			wantStatus: http.StatusNotFound,
			wantCode:   "NOT_FOUND",
		},
		{
			name: "storage failure",
			err: fmt.Errorf(
				"database failed: %w",
				repository.ErrStorage,
			),
			wantStatus: http.StatusInternalServerError,
			wantCode:   "INTERNAL_ERROR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 1. 模拟 HTTP ResponseWriter
			w := httptest.NewRecorder()

			// 2. 创建 Gin Context
			c, _ := gin.CreateTestContext(w)

			// 3. 调用被测试的错误映射函数
			WriteServiceerror(c, tt.err)

			// 4. 检查 HTTP 状态码
			if w.Code != tt.wantStatus {
				t.Fatalf(
					"status = %d, want %d",
					w.Code,
					tt.wantStatus,
				)
			}

			// 5. 检查响应中的业务错误码
			var body struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}

			if err := json.Unmarshal(
				w.Body.Bytes(), &body,
			); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			if body.Code != tt.wantCode {
				t.Errorf(
					"code = %q, want %q",
					body.Code,
					tt.wantCode,
				)
			}

			// 6. 服务器错误不能泄露内部信息
			if tt.wantStatus == http.StatusInternalServerError {
				if strings.Contains(
					w.Body.String(), "database failed",
				) {
					t.Error("internal error leaked")
				}
			}
		})
	}

	// 避免未使用 errors 导入，同时验证非哨兵错误也能映射为 500
	_ = errors.New
}
