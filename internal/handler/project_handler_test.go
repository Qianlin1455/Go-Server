package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"GoServer/internal/midware"
	"GoServer/internal/model"
	"GoServer/internal/security"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

func TestListProjectsAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tx := setupMessageAPITestDB(t)
	ctx := context.Background()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	// 1. 创建测试用户
	userA := &model.Users{
		UserName: "list_api_a_" + suffix,
		Email:    "list_api_a_" + suffix + "@test.com",
		Password: "test-hash",
	}
	userB := &model.Users{
		UserName: "list_api_b_" + suffix,
		Email:    "list_api_b_" + suffix + "@test.com",
		Password: "test-hash",
	}

	for _, user := range []*model.Users{userA, userB} {
		if err := tx.WithContext(ctx).Create(user).Error; err != nil {
			t.Fatalf("创建用户失败: %v", err)
		}
	}

	// 2. 创建用户 A 的 21 个项目
	base := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)

	projectIDs := make([]uint64, 0, 21)

	for i := 0; i < 21; i++ {
		createdAt := base.Add(time.Minute)

		// 最后插入的项目 ID 最大，但创建时间最早
		// 验证 created_at 的排序优先于 id
		if i == 20 {
			createdAt = base
		}

		project := &model.Project{
			UserID:    userA.ID,
			Name:      fmt.Sprintf("list_a_%s_%d", suffix, i),
			Status:    model.ProjectStatusActive,
			CreatedAt: createdAt,
		}

		if err := tx.WithContext(ctx).Create(project).Error; err != nil {
			t.Fatalf("创建项目失败: %v", err)
		}

		projectIDs = append(projectIDs, project.ID)
	}

	// 3. 创建用户 B 的项目
	projectB := &model.Project{
		UserID:    userB.ID,
		Name:      "list_b_" + suffix,
		Status:    model.ProjectStatusActive,
		CreatedAt: base.Add(2 * time.Minute),
	}

	if err := tx.WithContext(ctx).Create(projectB).Error; err != nil {
		t.Fatal(err)
	}

	// 4. JWT
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

	// 5. Router
	r := gin.New()
	h := NewProjectHandler(service.NewProjectService(tx))

	private := r.Group("/api/v1")
	private.Use(midware.JWTAuth(manager))
	private.GET("/projects", h.ListProjects)

	request := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)

		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	type listResponse struct {
		Code string `json:"code"`
		Data []struct {
			ID   uint64 `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}

	decode := func(t *testing.T, w *httptest.ResponseRecorder) listResponse {
		t.Helper()

		if w.Code != http.StatusOK {
			t.Fatalf("期望 200，实际 %d: %s", w.Code, w.Body.String())
		}

		var body listResponse
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}

		if body.Code != "SUCCESS" {
			t.Fatalf("业务码错误: %s", body.Code)
		}

		return body
	}

	// 6. 默认分页：第一页应该返回 20 条
	t.Run("默认分页", func(t *testing.T) {
		body := decode(t, request("/api/v1/projects", tokenA))

		if len(body.Data) != 20 {
			t.Fatalf("期望 20 条，实际 %d", len(body.Data))
		}

		// 前 20 个项目创建时间相同，按 ID 降序
		for i, p := range body.Data {
			wantID := projectIDs[19-i]

			if p.ID != wantID {
				t.Fatalf(
					"排序错误: index=%d want=%d got=%d",
					i, wantID, p.ID,
				)
			}

			if p.ID == projectB.ID {
				t.Fatal("查询到了用户 B 的项目")
			}
		}
	})

	// 7. 第二页只有创建时间最早的项目
	t.Run("第二页", func(t *testing.T) {
		body := decode(t,
			request("/api/v1/projects?page=2", tokenA),
		)

		if len(body.Data) != 1 {
			t.Fatalf("期望 1 条，实际 %d", len(body.Data))
		}

		if body.Data[0].ID != projectIDs[20] {
			t.Fatal("第二页项目错误")
		}
	})

	// 8. 自定义分页
	t.Run("自定义分页", func(t *testing.T) {
		body := decode(t,
			request("/api/v1/projects?page=1&page_size=2", tokenA),
		)

		if len(body.Data) != 2 {
			t.Fatalf("期望 2 条，实际 %d", len(body.Data))
		}

		if body.Data[0].ID != projectIDs[19] ||
			body.Data[1].ID != projectIDs[18] {
			t.Fatal("自定义分页排序错误")
		}
	})

	// 9. 用户 B 只能看到自己的项目
	t.Run("用户隔离", func(t *testing.T) {
		body := decode(t, request("/api/v1/projects", tokenB))

		if len(body.Data) != 1 ||
			body.Data[0].ID != projectB.ID {
			t.Fatalf("用户隔离失败: %+v", body.Data)
		}
	})

	// 10. 超出最后一页
	t.Run("空页", func(t *testing.T) {
		body := decode(t,
			request("/api/v1/projects?page=3", tokenA),
		)

		if body.Data == nil || len(body.Data) != 0 {
			t.Fatalf("应返回空数组: %+v", body.Data)
		}
	})

	// 11. JWT 校验
	authCases := []struct {
		name  string
		token string
	}{
		{"缺少JWT", ""},
		{"非法JWT", "invalid.token.value"},
	}

	for _, tc := range authCases {
		t.Run(tc.name, func(t *testing.T) {
			w := request("/api/v1/projects", tc.token)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf(
					"期望 401，实际 %d: %s",
					w.Code, w.Body.String(),
				)
			}
		})
	}

	// 12. 非法分页参数
	maxInt := int(^uint(0) >> 1)

	invalidCases := []struct {
		name string
		path string
	}{
		{"page为0", "/api/v1/projects?page=0"},
		{"page为负数", "/api/v1/projects?page=-1"},
		{"page非法字符串", "/api/v1/projects?page=abc"},
		{"pageSize为0", "/api/v1/projects?page_size=0"},
		{"pageSize超过100", "/api/v1/projects?page_size=101"},
		{"pageSize非法字符串", "/api/v1/projects?page_size=abc"},
		{
			"OFFSET溢出",
			fmt.Sprintf(
				"/api/v1/projects?page=%d&page_size=100",
				maxInt,
			),
		},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			w := request(tc.path, tokenA)

			if w.Code != http.StatusBadRequest {
				t.Fatalf(
					"期望 400，实际 %d: %s",
					w.Code, w.Body.String(),
				)
			}
		})
	}
}
