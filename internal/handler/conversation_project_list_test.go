package handler

import (
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

func TestListProjectConversationsAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tx := setupMessageAPITestDB(t)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	// 1. 创建用户 A、B
	userA := &model.Users{
		UserName: "list_conv_a_" + suffix,
		Email:    "list_conv_a_" + suffix + "@test.com",
		Password: "test-hash",
	}
	userB := &model.Users{
		UserName: "list_conv_b_" + suffix,
		Email:    "list_conv_b_" + suffix + "@test.com",
		Password: "test-hash",
	}

	for _, u := range []*model.Users{userA, userB} {
		if err := tx.Create(u).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 2. 创建项目
	createProject := func(userID uint64, name string) uint64 {
		t.Helper()

		p := model.Project{
			UserID: userID,
			Name:   name + suffix,
			Status: model.ProjectStatusActive,
		}
		if err := tx.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
		return p.ID
	}

	projectA := createProject(userA.ID, "project_a_")
	projectA2 := createProject(userA.ID, "project_a2_")
	projectB := createProject(userB.ID, "project_b_")
	emptyProject := createProject(userA.ID, "empty_")

	// 创建后删除，获得确定不存在的项目 ID
	missingProject := createProject(userA.ID, "missing_")

	if err := tx.Delete(
		&model.Project{}, missingProject,
	).Error; err != nil {
		t.Fatal(err)
	}

	// 3. 创建测试会话
	base := time.Date(
		2026, 10, 20, 10, 0, 0, 0, time.UTC,
	)

	createConversation := func(
		userID uint64,
		projectID *uint64,
		title string,
		createdAt time.Time,
	) uint64 {
		t.Helper()

		c := model.Conversation{
			UserID:    userID,
			ProjectID: projectID,
			Title:     title,
			CreatedAt: createdAt,
			UpdatedAt: createdAt,
		}

		if err := tx.Create(&c).Error; err != nil {
			t.Fatal(err)
		}
		return c.ID
	}

	// 项目 A 创建 21 个会话，验证默认分页 20 条
	ids := make([]uint64, 0, 21)

	for i := 0; i < 21; i++ {
		createdAt := base.Add(time.Minute)

		// 最后一个会话创建时间更早
		if i == 20 {
			createdAt = base
		}

		id := createConversation(
			userA.ID,
			&projectA,
			fmt.Sprintf("conversation_%d", i),
			createdAt,
		)
		ids = append(ids, id)
	}

	// 干扰数据：其他项目、其他用户、历史 NULL 会话
	otherID := createConversation(
		userA.ID, &projectA2,
		"other project", base,
	)
	userBConvID := createConversation(
		userB.ID, &projectB,
		"user B conversation", base,
	)
	legacyID := createConversation(
		userA.ID, nil,
		"legacy conversation", base,
	)

	// 4. 创建 JWT
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

	// 5. 注册真实 Handler
	r := gin.New()
	h := NewConversationHandler(
		service.NewConversationService(tx),
	)

	private := r.Group("/api/v1")
	private.Use(midware.JWTAuth(manager))
	private.GET(
		"/projects/:id/conversations",
		h.ListProjectConversations,
	)

	// 6. HTTP 请求辅助函数
	request := func(
		projectID string,
		query string,
		token string,
	) *httptest.ResponseRecorder {
		path := "/api/v1/projects/" +
			projectID + "/conversations" + query

		req := httptest.NewRequest(
			http.MethodGet, path, nil,
		)
		if token != "" {
			req.Header.Set(
				"Authorization", "Bearer "+token,
			)
		}

		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 7. 响应结构和解析
	type listResponse struct {
		Code string `json:"code"`
		Data []struct {
			ID        uint64  `json:"id"`
			Title     string  `json:"title"`
			ProjectID *uint64 `json:"project_id"`
		} `json:"data"`
	}

	decode := func(
		t *testing.T,
		w *httptest.ResponseRecorder,
	) listResponse {
		t.Helper()

		if w.Code != http.StatusOK {
			t.Fatalf(
				"期望 200，实际 %d: %s",
				w.Code, w.Body.String(),
			)
		}

		var body listResponse
		if err := json.Unmarshal(
			w.Body.Bytes(), &body,
		); err != nil {
			t.Fatal(err)
		}

		if body.Code != "SUCCESS" {
			t.Fatalf("错误业务码: %s", body.Code)
		}

		return body
	}

	projectAID := strconv.FormatUint(projectA, 10)

	// 8. 默认分页和稳定排序
	t.Run("默认分页和排序", func(t *testing.T) {
		body := decode(t, request(projectAID, "", tokenA))

		if len(body.Data) != 20 {
			t.Fatalf(
				"期望 20 条，实际 %d",
				len(body.Data),
			)
		}

		for i, c := range body.Data {
			wantID := ids[19-i]

			if c.ID != wantID {
				t.Fatalf(
					"排序错误 index=%d want=%d got=%d",
					i, wantID, c.ID,
				)
			}

			if c.ProjectID == nil ||
				*c.ProjectID != projectA {
				t.Fatalf("项目关联错误: %+v", c)
			}

			if c.ID == otherID ||
				c.ID == legacyID ||
				c.ID == userBConvID {
				t.Fatal("出现跨项目或跨用户的数据")
			}
		}
	})

	// 9. 第二页
	t.Run("第二页", func(t *testing.T) {
		body := decode(t, request(
			projectAID, "?page=2", tokenA,
		))

		if len(body.Data) != 1 ||
			body.Data[0].ID != ids[20] {
			t.Fatalf("第二页结果错误: %+v", body.Data)
		}
	})

	// 10. 自定义分页
	t.Run("自定义分页", func(t *testing.T) {
		body := decode(t, request(
			projectAID,
			"?page=1&page_size=2",
			tokenA,
		))

		if len(body.Data) != 2 ||
			body.Data[0].ID != ids[19] ||
			body.Data[1].ID != ids[18] {
			t.Fatalf("自定义分页错误: %+v", body.Data)
		}
	})

	// 11. 同一用户的不同项目
	t.Run("项目隔离", func(t *testing.T) {
		body := decode(t, request(
			strconv.FormatUint(projectA2, 10),
			"",
			tokenA,
		))

		if len(body.Data) != 1 ||
			body.Data[0].ID != otherID {
			t.Fatalf("项目隔离失败: %+v", body.Data)
		}
	})

	// 12. 用户 B 查询自己的项目
	t.Run("用户B正常查询", func(t *testing.T) {
		body := decode(t, request(
			strconv.FormatUint(projectB, 10),
			"",
			tokenB,
		))

		if len(body.Data) != 1 ||
			body.Data[0].ID != userBConvID {
			t.Fatalf("用户B查询失败: %+v", body.Data)
		}
	})

	// 13. 空项目返回 []
	t.Run("空项目", func(t *testing.T) {
		body := decode(t, request(
			strconv.FormatUint(emptyProject, 10),
			"",
			tokenA,
		))

		if body.Data == nil || len(body.Data) != 0 {
			t.Fatalf(
				"期望非 nil 空数组: %+v",
				body.Data,
			)
		}
	})

	// 14. 错误场景
	maxInt := int(^uint(0) >> 1)

	cases := []struct {
		name       string
		projectID  string
		query      string
		token      string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "缺少JWT",
			projectID:  projectAID,
			token:      "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "非法JWT",
			projectID:  projectAID,
			token:      "invalid.token.value",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "跨用户项目",
			projectID:  strconv.FormatUint(projectB, 10),
			token:      tokenA,
			wantStatus: http.StatusNotFound,
			wantCode:   "NOT_FOUND",
		},
		{
			name:       "项目不存在",
			projectID:  strconv.FormatUint(missingProject, 10),
			token:      tokenA,
			wantStatus: http.StatusNotFound,
			wantCode:   "NOT_FOUND",
		},
		{
			name:       "项目ID为0",
			projectID:  "0",
			token:      tokenA,
			wantStatus: http.StatusBadRequest,
			wantCode:   "INVALID_ARGUMENT",
		},
		{
			name:       "非法项目ID",
			projectID:  "abc",
			token:      tokenA,
			wantStatus: http.StatusBadRequest,
			wantCode:   "INVALID_ARGUMENT",
		},
		{
			name:       "page为0",
			projectID:  projectAID,
			query:      "?page=0",
			token:      tokenA,
			wantStatus: http.StatusBadRequest,
			wantCode:   "INVALID_ARGUMENT",
		},
		{
			name:       "page非法字符串",
			projectID:  projectAID,
			query:      "?page=abc",
			token:      tokenA,
			wantStatus: http.StatusBadRequest,
			wantCode:   "INVALID_ARGUMENT",
		},
		{
			name:       "page_size为101",
			projectID:  projectAID,
			query:      "?page_size=101",
			token:      tokenA,
			wantStatus: http.StatusBadRequest,
			wantCode:   "INVALID_ARGUMENT",
		},
		{
			name:       "page_size非法字符串",
			projectID:  projectAID,
			query:      "?page_size=abc",
			token:      tokenA,
			wantStatus: http.StatusBadRequest,
			wantCode:   "INVALID_ARGUMENT",
		},
		{
			name:       "OFFSET溢出",
			projectID:  projectAID,
			query:      fmt.Sprintf("?page=%d&page_size=100", maxInt),
			token:      tokenA,
			wantStatus: http.StatusBadRequest,
			wantCode:   "INVALID_ARGUMENT",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := request(
				tc.projectID, tc.query, tc.token,
			)

			if w.Code != tc.wantStatus {
				t.Fatalf(
					"期望 HTTP %d，实际 %d: %s",
					tc.wantStatus,
					w.Code,
					w.Body.String(),
				)
			}

			if tc.wantCode != "" {
				var body struct {
					Code string `json:"code"`
				}
				if err := json.Unmarshal(
					w.Body.Bytes(), &body,
				); err != nil {
					t.Fatal(err)
				}

				if body.Code != tc.wantCode {
					t.Fatalf(
						"期望业务码 %s，实际 %s",
						tc.wantCode, body.Code,
					)
				}
			}
		})
	}
}
