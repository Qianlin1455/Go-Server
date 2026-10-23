package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		{name: "invalid register", err: service.ErrInvalidRegisterInput, wantStatus: http.StatusBadRequest, wantCode: "INVALID_ARGUMENT"},
		{name: "invalid pagination", err: service.ErrInvalidPagination, wantStatus: http.StatusBadRequest, wantCode: "INVALID_ARGUMENT"},
		{name: "invalid project", err: service.ErrInvalidProjectInput, wantStatus: http.StatusBadRequest, wantCode: "INVALID_ARGUMENT"},
		{name: "wrapped deadline", err: fmt.Errorf("storage: %w", errors.Join(repository.ErrStorage, context.DeadlineExceeded)), wantStatus: http.StatusGatewayTimeout, wantCode: "DEADLINE_EXCEEDED"},
		{name: "deadline takes precedence", err: errors.Join(service.ErrInvalidMessageInput, context.DeadlineExceeded), wantStatus: http.StatusGatewayTimeout, wantCode: "DEADLINE_EXCEEDED"},
		{name: "private database cause", err: errors.Join(repository.ErrStorage, errors.New("database failed: password=secret SELECT * FROM users")), wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL_ERROR"},
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
				if strings.Contains(w.Body.String(), "database failed") || strings.Contains(w.Body.String(), "secret") {
					t.Error("internal error leaked")
				}
				if len(c.Errors) != 1 || !errors.Is(c.Errors[0].Err, tt.err) {
					t.Fatal("internal error cause was not retained")
				}
			}
		})
	}

}

func TestWriteServiceErrorDeadlineFromRequest(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	WriteServiceerror(c, sql.ErrTxDone)
	if w.Code != http.StatusGatewayTimeout || !strings.Contains(w.Body.String(), "DEADLINE_EXCEEDED") {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}

func TestWriteServiceErrorPreservesCommittedResponse(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.String(http.StatusAccepted, "already sent")
	WriteServiceerror(c, context.DeadlineExceeded)
	if w.Code != http.StatusAccepted || w.Body.String() != "already sent" {
		t.Fatalf("committed response changed: %d %s", w.Code, w.Body.String())
	}
	if len(c.Errors) != 1 || !errors.Is(c.Errors[0].Err, context.DeadlineExceeded) || !c.IsAborted() {
		t.Fatal("late error was not recorded and aborted")
	}
}
