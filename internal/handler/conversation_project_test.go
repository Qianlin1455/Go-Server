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

func TestCreateConversationWithProjectAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tx := setupMessageAPITestDB(t)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	// 1. 创建用户 A 和 B
	userA := &model.Users{
		UserName: "conv_proj_a_" + suffix,
		Email:    "conv_proj_a_" + suffix + "@test.com",
		Password: "test-hash",
	}
	userB := &model.Users{
		UserName: "conv_proj_b_" + suffix,
		Email:    "conv_proj_b_" + suffix + "@test.com",
		Password: "test-hash",
	}

	for _, u := range []*model.Users{userA, userB} {
		if err := tx.Create(u).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 2. 创建不同用户的 Project
	projectA := &model.Project{
		UserID: userA.ID,
		Name:   "conv_project_a_" + suffix,
		Status: model.ProjectStatusActive,
	}
	projectB := &model.Project{
		UserID: userB.ID,
		Name:   "conv_project_b_" + suffix,
		Status: model.ProjectStatusActive,
	}

	for _, p := range []*model.Project{projectA, projectB} {
		if err := tx.Create(p).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 创建后删除，获得一个确定不存在的 Project ID
	missing := &model.Project{
		UserID: userA.ID,
		Name:   "conv_project_missing_" + suffix,
		Status: model.ProjectStatusActive,
	}
	if err := tx.Create(missing).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Delete(missing).Error; err != nil {
		t.Fatal(err)
	}

	// 3. 创建 JWT
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

	// 4. 注册真实路由
	r := gin.New()
	h := NewConversationHandler(
		service.NewConversationService(tx),
	)

	private := r.Group("/api/v1")
	private.Use(midware.JWTAuth(manager))
	private.POST("/conversations", h.CreateConversation)
	private.GET("/conversations/:id", h.GetConversation)
	mh := NewMessageHandler(service.NewMessageService(tx))

	private.POST("/conversations/:id/messages", mh.CreateMessage)
	private.GET("/conversations/:id/messages", mh.ListMessages)

	post := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/conversations",
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

	get := func(token string, id uint64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf("/api/v1/conversations/%d", id),
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+token)

		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	type response struct {
		Code string `json:"code"`
		Data struct {
			ID        uint64  `json:"id"`
			Title     string  `json:"title"`
			ProjectID *uint64 `json:"project_id"`
		} `json:"data"`
	}

	decode := func(t *testing.T, w *httptest.ResponseRecorder) response {
		t.Helper()

		var body response
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	assertMessageRoundTrip := func(t *testing.T, conversationID uint64) {
		t.Helper()

		path := fmt.Sprintf(
			"/api/v1/conversations/%d/messages",
			conversationID,
		)

		// 1. 发送消息
		req := httptest.NewRequest(
			http.MethodPost,
			path,
			strings.NewReader(`{"content":"兼容性测试消息"}`),
		)
		req.Header.Set("Authorization", "Bearer "+tokenA)
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("发送消息失败: %d %s", w.Code, w.Body.String())
		}

		var sent struct {
			Data struct {
				ID             uint64 `json:"id"`
				ConversationID uint64 `json:"conversation_id"`
				Content        string `json:"content"`
			} `json:"data"`
		}

		if err := json.Unmarshal(w.Body.Bytes(), &sent); err != nil {
			t.Fatal(err)
		}

		if sent.Data.ID == 0 ||
			sent.Data.ConversationID != conversationID {
			t.Fatalf("发送消息响应错误: %+v", sent.Data)
		}

		// 2. 查询消息
		req = httptest.NewRequest(
			http.MethodGet,
			path+"?page=1&page_size=20",
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+tokenA)

		w = httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("查询消息失败: %d %s", w.Code, w.Body.String())
		}

		var listed struct {
			Data []struct {
				ID             uint64 `json:"id"`
				ConversationID uint64 `json:"conversation_id"`
				Content        string `json:"content"`
			} `json:"data"`
		}

		if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
			t.Fatal(err)
		}

		if len(listed.Data) != 1 {
			t.Fatalf("期望 1 条消息，实际 %d", len(listed.Data))
		}

		got := listed.Data[0]
		if got.ID != sent.Data.ID ||
			got.ConversationID != conversationID ||
			got.Content != "兼容性测试消息" {
			t.Fatalf("消息收发数据不一致: %+v", got)
		}
	}

	// 5. 旧请求：不传 project_id
	t.Run("旧请求兼容", func(t *testing.T) {
		w := post(tokenA, `{"title":"普通聊天"}`)

		if w.Code != http.StatusCreated {
			t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
		}

		body := decode(t, w)
		if body.Data.ID == 0 || body.Data.ProjectID != nil {
			t.Fatalf("旧请求响应错误: %+v", body.Data)
		}
		// 旧请求兼容
		assertMessageRoundTrip(t, body.Data.ID)

		// 直接检查 MySQL 中的值
		var stored model.Conversation
		if err := tx.First(&stored, body.Data.ID).Error; err != nil {
			t.Fatal(err)
		}

		if stored.UserID != userA.ID || stored.ProjectID != nil {
			t.Fatalf("旧会话数据库兼容失败: %+v", stored)
		}

		// 验证旧会话仍然可以读取
		getW := get(tokenA, body.Data.ID)
		if getW.Code != http.StatusOK {
			t.Fatalf("旧会话 GET 失败: %d %s",
				getW.Code, getW.Body.String())
		}

		got := decode(t, getW)
		if got.Data.ProjectID != nil {
			t.Fatal("旧会话的 project_id 应该为 NULL")
		}
	})

	// 6. 新请求：绑定自己的 Project
	t.Run("绑定自己的Project", func(t *testing.T) {
		payload := fmt.Sprintf(
			`{"title":"项目讨论","project_id":%d}`,
			projectA.ID,
		)

		w := post(tokenA, payload)
		if w.Code != http.StatusCreated {
			t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
		}

		body := decode(t, w)
		if body.Data.ProjectID == nil ||
			*body.Data.ProjectID != projectA.ID {
			t.Fatalf("创建响应 ProjectID 错误: %+v", body.Data)
		}
		// 绑定自己的Project
		assertMessageRoundTrip(t, body.Data.ID)

		var stored model.Conversation
		if err := tx.First(&stored, body.Data.ID).Error; err != nil {
			t.Fatal(err)
		}

		if stored.ProjectID == nil ||
			*stored.ProjectID != projectA.ID ||
			stored.UserID != userA.ID {
			t.Fatalf("数据库项目关联错误: %+v", stored)
		}

		// GET 必须返回相同的 ProjectID
		getW := get(tokenA, body.Data.ID)
		if getW.Code != http.StatusOK {
			t.Fatalf("GET 失败: %s", getW.Body.String())
		}

		got := decode(t, getW)
		if got.Data.ProjectID == nil ||
			*got.Data.ProjectID != projectA.ID {
			t.Fatal("GET 未正确返回 project_id")
		}
	})

	// 7. 非法请求：必须被拒绝，且不能写入数据库
	invalidCases := []struct {
		name       string
		body       string
		token      string
		wantStatus int
	}{
		{
			name: "跨用户项目",
			body: fmt.Sprintf(
				`{"title":"越权","project_id":%d}`,
				projectB.ID,
			),
			token:      tokenA,
			wantStatus: http.StatusNotFound,
		},
		{
			name: "不存在的项目",
			body: fmt.Sprintf(
				`{"title":"不存在","project_id":%d}`,
				missing.ID,
			),
			token:      tokenA,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "project_id为0",
			body:       `{"title":"错误","project_id":0}`,
			token:      tokenA,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "project_id非法类型",
			body:       `{"title":"错误","project_id":"abc"}`,
			token:      tokenA,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "缺少JWT",
			body:       `{"title":"未认证"}`,
			token:      "",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			var before int64
			if err := tx.Model(&model.Conversation{}).
				Count(&before).Error; err != nil {
				t.Fatal(err)
			}

			w := post(tc.token, tc.body)

			if w.Code != tc.wantStatus {
				t.Fatalf(
					"want %d, got %d: %s",
					tc.wantStatus, w.Code, w.Body.String(),
				)
			}

			var after int64
			if err := tx.Model(&model.Conversation{}).
				Count(&after).Error; err != nil {
				t.Fatal(err)
			}

			if before != after {
				t.Fatalf(
					"拒绝请求后不应产生会话: before=%d after=%d",
					before, after,
				)
			}
		})
	}
}
