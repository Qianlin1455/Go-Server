package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"GoServer/internal/midware"
	"GoServer/internal/model"
	"GoServer/internal/security"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

func TestCreateProjectAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 1. 使用现有测试数据库事务，测试结束后回滚。
	tx := setupMessageAPITestDB(t)

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	userA := &model.Users{
		UserName: "project_api_a_" + suffix,
		Email:    "project_api_a_" + suffix + "@test.com",
		Password: "test-hash",
	}
	userB := &model.Users{
		UserName: "project_api_b_" + suffix,
		Email:    "project_api_b_" + suffix + "@test.com",
		Password: "test-hash",
	}

	for _, user := range []*model.Users{userA, userB} {
		if err := tx.Create(user).Error; err != nil {
			t.Fatalf("创建测试用户失败: %v", err)
		}
	}

	// 2. 创建真实 JWT。
	manager, err := security.NewJWTManager(
		"0123456789abcdef0123456789abcdef",
		time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}

	tokenA, err := manager.Generate(userA.ID)
	if err != nil {
		t.Fatal(err)
	}

	tokenB, err := manager.Generate(userB.ID)
	if err != nil {
		t.Fatal(err)
	}

	// 3. 注册真实 Handler 和 JWT 中间件。
	r := gin.New()
	h := NewProjectHandler(service.NewProjectService(tx))

	private := r.Group("/api/v1")
	private.Use(midware.JWTAuth(manager))
	private.POST("/projects", h.CreateProject)

	// 4. 统一构造 HTTP 请求。
	request := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/projects",
			strings.NewReader(body),
		)
		req.Header.Set("Content-Type", "application/json")

		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 5. 未认证请求：应该在进入 Handler 前被拒绝。
	t.Run("缺少JWT", func(t *testing.T) {
		w := request("", `{"name":"project-a"}`)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("期望 401，实际 %d: %s", w.Code, w.Body.String())
		}
	})

	// 6. 非法 JSON：返回 400。
	t.Run("非法JSON", func(t *testing.T) {
		w := request(tokenA, `{`)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("期望 400，实际 %d: %s", w.Code, w.Body.String())
		}
	})

	// 7. 空白名称：由 Service 拒绝。
	t.Run("空白项目名称", func(t *testing.T) {
		w := request(tokenA, `{"name":"   "}`)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("期望 400，实际 %d: %s", w.Code, w.Body.String())
		}
	})

	// 8. 正常创建：验证 HTTP 响应与数据库写入。
	t.Run("正常创建", func(t *testing.T) {
		w := request(tokenA, `{
			"name":"  project-api-success  ",
			"description":"integration test"
		}`)

		if w.Code != http.StatusCreated {
			t.Fatalf("期望 201，实际 %d: %s", w.Code, w.Body.String())
		}

		var body struct {
			Code string `json:"code"`
			Data struct {
				ID          uint64  `json:"id"`
				Name        string  `json:"name"`
				Description *string `json:"description"`
				Status      string  `json:"status"`
			} `json:"data"`
		}

		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}

		if body.Code != "SUCCESS" {
			t.Fatalf("业务码错误: %s", body.Code)
		}
		if body.Data.ID == 0 {
			t.Fatal("项目 ID 不应为 0")
		}
		if body.Data.Name != "project-api-success" {
			t.Fatalf("名称未规范化: %q", body.Data.Name)
		}
		if body.Data.Status != model.ProjectStatusActive {
			t.Fatalf("初始状态错误: %s", body.Data.Status)
		}

		var stored model.Project
		if err := tx.First(&stored, body.Data.ID).Error; err != nil {
			t.Fatalf("数据库中未找到项目: %v", err)
		}
		if stored.UserID != userA.ID {
			t.Fatalf("项目归属错误: want=%d got=%d", userA.ID, stored.UserID)
		}
	})

	// 9. 同一用户重复创建同名项目：返回 409。
	t.Run("重复项目名称", func(t *testing.T) {
		name := "project-api-duplicate-" + suffix

		first := request(tokenA, fmt.Sprintf(`{"name":%q}`, name))
		if first.Code != http.StatusCreated {
			t.Fatalf("首次创建失败: %d %s", first.Code, first.Body.String())
		}

		second := request(tokenA, fmt.Sprintf(`{"name":%q}`, name))
		if second.Code != http.StatusConflict {
			t.Fatalf("期望 409，实际 %d: %s", second.Code, second.Body.String())
		}

		var body struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(second.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Code != "PROJECT_ALREADY_EXISTS" {
			t.Fatalf("业务码错误: %s", body.Code)
		}
	})

	// 10. 不同用户可以创建同名项目。
	t.Run("不同用户可以同名", func(t *testing.T) {
		name := "project-api-shared-name-" + suffix

		wA := request(tokenA, fmt.Sprintf(`{"name":%q}`, name))
		wB := request(tokenB, fmt.Sprintf(`{"name":%q}`, name))

		if wA.Code != http.StatusCreated {
			t.Fatalf("用户 A 创建失败: %d %s", wA.Code, wA.Body.String())
		}
		if wB.Code != http.StatusCreated {
			t.Fatalf("用户 B 创建失败: %d %s", wB.Code, wB.Body.String())
		}
	})

	// 11. 请求体伪造 user_id：不能改变实际归属。
	t.Run("不能伪造项目归属", func(t *testing.T) {
		name := "project-api-owner-" + suffix
		payload := fmt.Sprintf(
			`{"name":%q,"user_id":%d}`,
			name,
			userB.ID,
		)

		w := request(tokenA, payload)
		if w.Code != http.StatusCreated {
			t.Fatalf("创建失败: %d %s", w.Code, w.Body.String())
		}

		var stored model.Project
		if err := tx.Where("name = ? AND user_id = ?", name, userA.ID).
			First(&stored).Error; err != nil {
			t.Fatalf("项目没有归属于 JWT 用户 A: %v", err)
		}

		if stored.UserID != userA.ID {
			t.Fatalf("归属被伪造: want=%d got=%d", userA.ID, stored.UserID)
		}
	})
}
