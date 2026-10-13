package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"GoServer/internal/midware"
	"GoServer/internal/security"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// 创建用于 JWT 安全测试的 Gin Engine
func setupMessageAuthTestRouter(t *testing.T) *gin.Engine {
	t.Helper()

	gin.SetMode(gin.TestMode)

	// 1. 创建测试用 JWTManager
	manager, err := security.NewJWTManager(
		"0123456789abcdef0123456789abcdef",
		time.Hour,
	)
	if err != nil {
		t.Fatalf("创建 JWTManager 失败: %v", err)
	}

	// 2. 创建 Gin Engine
	r := gin.New()

	// 3. 创建 Service 和 Handler
	// 故意使用 nil DB，验证未认证请求不会进入 Service
	svc := service.NewMessageService(nil)
	h := NewMessageHandler(svc)

	// 4. 创建需要 JWT 认证的路由组
	private := r.Group("/api/v1")
	private.Use(midware.JWTAuth(manager))

	// 5. 注册消息列表路由
	private.GET(
		"/conversations/:id/messages",
		h.ListMessages,
	)

	return r
}

// TestListMessages_MissingJWT
// 验证未携带 JWT 时返回 HTTP 401
func TestListMessages_MissingJWT(t *testing.T) {
	r := setupMessageAuthTestRouter(t)

	// 1. 构造 HTTP 请求
	// 故意不设置 Authorization Header
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/conversations/73/messages?page=1&page_size=20",
		nil,
	)

	// 2. 创建响应记录器
	w := httptest.NewRecorder()

	// 3. 将请求交给 Gin Engine
	r.ServeHTTP(w, req)

	// 4. 验证 HTTP 状态码
	if w.Code != http.StatusUnauthorized {
		t.Fatalf(
			"期望 HTTP 401，实际 %d，响应: %s",
			w.Code,
			w.Body.String(),
		)
	}

	// 5. 解析 JSON 响应
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应 JSON 失败: %v", err)
	}

	// 6. 验证业务错误码
	if body.Code != "MISSING_TOKEN" {
		t.Fatalf(
			"期望 MISSING_TOKEN，实际 %s",
			body.Code,
		)
	}

	// 7. 验证错误信息
	if body.Message != "缺少身份凭证" {
		t.Fatalf(
			"错误信息不符合预期: %s",
			body.Message,
		)
	}

	t.Log("PASS: 未携带 JWT 时返回 401 MISSING_TOKEN")
}

// TestListMessages_InvalidJWT
// 验证无效 Token 和错误认证格式
func TestListMessages_InvalidJWT(t *testing.T) {
	r := setupMessageAuthTestRouter(t)

	tests := []struct {
		name          string
		authorization string
		wantCode      string
	}{
		{
			name:          "无效JWT",
			authorization: "Bearer invalid.jwt.token",
			wantCode:      "INVALID_TOKEN",
		},
		{
			name:          "错误Authorization格式",
			authorization: "Basic abc123",
			wantCode:      "INVALID_TOKEN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			// 1. 创建 HTTP 请求
			req := httptest.NewRequest(
				http.MethodGet,
				"/api/v1/conversations/73/messages?page=1&page_size=20",
				nil,
			)

			// 2. 设置 Authorization Header
			req.Header.Set(
				"Authorization",
				tt.authorization,
			)

			// 3. 创建响应记录器
			w := httptest.NewRecorder()

			// 4. 执行 Gin HTTP 请求
			r.ServeHTTP(w, req)

			// 5. 验证状态码
			if w.Code != http.StatusUnauthorized {
				t.Fatalf(
					"期望 HTTP 401，实际 %d，响应: %s",
					w.Code,
					w.Body.String(),
				)
			}

			// 6. 解析响应 JSON
			var body struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}

			if err := json.Unmarshal(
				w.Body.Bytes(), &body,
			); err != nil {
				t.Fatalf("解析响应失败: %v", err)
			}

			// 7. 验证业务错误码
			if body.Code != tt.wantCode {
				t.Fatalf(
					"期望 %s，实际 %s",
					tt.wantCode,
					body.Code,
				)
			}
		})
	}
}

func TestListMessages_InvalidPaginationAPI(t *testing.T) {
	r := setupMessageAuthTestRouter(t)

	// 使用与测试 Router 相同的 JWT 密钥
	manager, err := security.NewJWTManager(
		"0123456789abcdef0123456789abcdef",
		time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}

	token, err := manager.Generate(1)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		url  string
	}{
		{
			name: "page格式错误",
			url:  "/api/v1/conversations/73/messages?page=abc",
		},
		{
			name: "page为0",
			url:  "/api/v1/conversations/73/messages?page=0",
		},
		{
			name: "page_size格式错误",
			url:  "/api/v1/conversations/73/messages?page_size=abc",
		},
		{
			name: "page_size超过上限",
			url:  "/api/v1/conversations/73/messages?page_size=101",
		},
		{
			name: "page_size为负数",
			url:  "/api/v1/conversations/73/messages?page_size=-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodGet,
				tt.url,
				nil,
			)

			req.Header.Set(
				"Authorization",
				"Bearer "+token,
			)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf(
					"期望 HTTP 400，实际 %d，响应: %s",
					w.Code,
					w.Body.String(),
				)
			}

			var body struct {
				Code string `json:"code"`
			}

			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}

			if body.Code != "INVALID_ARGUMENT" {
				t.Fatalf(
					"期望 INVALID_ARGUMENT，实际 %s",
					body.Code,
				)
			}
		})
	}
}

func setupMessageAPITestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("需要配置 TEST_MYSQL_DSN")
	}

	db, err := gorm.Open(
		mysql.Open(dsn),
		&gorm.Config{TranslateError: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}

	t.Cleanup(func() { _ = tx.Rollback().Error })

	return tx
}
