package handler

import (
	"encoding/json"
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

func TestMessageAPI_ResourceOwnership(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 1. 获取测试数据库事务
	tx := setupMessageAPITestDB(t)

	// 2. 创建两个用户
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	userA := &model.Users{
		UserName: "api_A_" + suffix,
		Email:    "api_A_" + suffix + "@test.com",
		Password: "test-hash",
	}
	userB := &model.Users{
		UserName: "api_B_" + suffix,
		Email:    "api_B_" + suffix + "@test.com",
		Password: "test-hash",
	}

	for _, user := range []*model.Users{userA, userB} {
		if err := tx.Create(user).Error; err != nil {
			t.Fatalf("创建用户失败: %v", err)
		}
	}

	// 3. 创建分别归属于 A、B 的会话
	conA := &model.Conversation{
		UserID: userA.ID,
		Title:  "Conversation A",
	}
	conB := &model.Conversation{
		UserID: userB.ID,
		Title:  "Conversation B",
	}

	for _, con := range []*model.Conversation{conA, conB} {
		if err := tx.Create(con).Error; err != nil {
			t.Fatalf("创建会话失败: %v", err)
		}
	}

	// 4. 创建消息
	msgA1 := &model.Message{
		ConversationID: conA.ID,
		Role:           "user",
		Content:        "Message A1",
	}
	msgA2 := &model.Message{
		ConversationID: conA.ID,
		Role:           "user",
		Content:        "Message A2",
	}
	msgB1 := &model.Message{
		ConversationID: conB.ID,
		Role:           "user",
		Content:        "Message B1",
	}

	for _, msg := range []*model.Message{msgA1, msgA2, msgB1} {
		if err := tx.Create(msg).Error; err != nil {
			t.Fatalf("创建消息失败: %v", err)
		}
	}

	// 5. 创建 JWTManager 并签发真实 JWT
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

	// 6. 注册真实 Gin 路由与 JWT 中间件
	r := gin.New()

	messageService := service.NewMessageService(tx)
	messageHandler := NewMessageHandler(messageService)

	private := r.Group("/api/v1")
	private.Use(midware.JWTAuth(manager))

	private.GET(
		"/conversations/:id/messages",
		messageHandler.ListMessages,
	)

	// 7. 定义测试矩阵
	tests := []struct {
		name       string
		token      string
		conID      uint64
		wantStatus int
		wantCode   string
		wantIDs    []uint64
	}{
		{
			name:       "A访问自己的会话",
			token:      tokenA,
			conID:      conA.ID,
			wantStatus: http.StatusOK,
			wantCode:   "SUCCESS",
			wantIDs:    []uint64{msgA1.ID, msgA2.ID},
		},
		{
			name:       "A越权访问B的会话",
			token:      tokenA,
			conID:      conB.ID,
			wantStatus: http.StatusNotFound,
			wantCode:   "NOT_FOUND",
		},
		{
			name:       "B访问自己的会话",
			token:      tokenB,
			conID:      conB.ID,
			wantStatus: http.StatusOK,
			wantCode:   "SUCCESS",
			wantIDs:    []uint64{msgB1.ID},
		},
		{
			name:       "A访问不存在的会话",
			token:      tokenA,
			conID:      ^uint64(0),
			wantStatus: http.StatusNotFound,
			wantCode:   "NOT_FOUND",
		},
	}

	// 8. 执行 HTTP 集成测试
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/api/v1/conversations/" +
				strconv.FormatUint(tt.conID, 10) +
				"/messages?page=1&page_size=20"

			req := httptest.NewRequest(
				http.MethodGet,
				url,
				nil,
			)

			req.Header.Set(
				"Authorization",
				"Bearer "+tt.token,
			)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			// 断言 HTTP 状态码
			if w.Code != tt.wantStatus {
				t.Fatalf(
					"HTTP状态码错误: want=%d got=%d body=%s",
					tt.wantStatus,
					w.Code,
					w.Body.String(),
				)
			}

			var body struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Data    []struct {
					ID             uint64 `json:"id"`
					ConversationID uint64 `json:"conversation_id"`
				} `json:"data"`
			}

			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("解析响应失败: %v", err)
			}

			// 断言业务状态码
			if body.Code != tt.wantCode {
				t.Fatalf(
					"业务码错误: want=%s got=%s",
					tt.wantCode,
					body.Code,
				)
			}

			// 错误响应必须具有 message 字段
			if tt.wantStatus != http.StatusOK {
				if body.Message == "" {
					t.Fatal("错误响应缺少 message 字段")
				}
				return
			}

			// 断言消息数量
			if len(body.Data) != len(tt.wantIDs) {
				t.Fatalf(
					"消息数量错误: want=%d got=%d",
					len(tt.wantIDs),
					len(body.Data),
				)
			}

			// 断言消息 ID、排序及会话归属
			for i, msg := range body.Data {
				if msg.ID != tt.wantIDs[i] {
					t.Fatalf(
						"消息ID不正确: want=%d got=%d",
						tt.wantIDs[i],
						msg.ID,
					)
				}

				if msg.ConversationID != tt.conID {
					t.Fatalf("消息归属错误: id=%d", msg.ID)
				}
			}
		})
	}

	// 9. 验证 HTTP 正常分页、最后一页、空页
	paginationTests := []struct {
		name    string
		page    int
		wantIDs []uint64
	}{
		{
			name:    "第一页",
			page:    1,
			wantIDs: []uint64{msgA1.ID},
		},
		{
			name:    "最后一页",
			page:    2,
			wantIDs: []uint64{msgA2.ID},
		},
		{
			name:    "空页",
			page:    3,
			wantIDs: []uint64{},
		},
	}

	for _, tt := range paginationTests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/api/v1/conversations/" +
				strconv.FormatUint(conA.ID, 10) +
				"/messages?page=" +
				strconv.Itoa(tt.page) +
				"&page_size=1"

			req := httptest.NewRequest(
				http.MethodGet,
				url,
				nil,
			)

			req.Header.Set(
				"Authorization",
				"Bearer "+tokenA,
			)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf(
					"期望 200，实际 %d，body=%s",
					w.Code,
					w.Body.String(),
				)
			}

			var body struct {
				Code string `json:"code"`
				Data []struct {
					ID uint64 `json:"id"`
				} `json:"data"`
			}

			if err := json.Unmarshal(
				w.Body.Bytes(), &body,
			); err != nil {
				t.Fatal(err)
			}

			if body.Code != "SUCCESS" {
				t.Fatalf("业务码错误: %s", body.Code)
			}

			// 确保空页返回 []，而不是 null
			if body.Data == nil {
				t.Fatal("data 应为 JSON 数组，不能是 null")
			}

			if len(body.Data) != len(tt.wantIDs) {
				t.Fatalf(
					"期望 %d 条，实际 %d 条",
					len(tt.wantIDs),
					len(body.Data),
				)
			}

			for i, msg := range body.Data {
				if msg.ID != tt.wantIDs[i] {
					t.Fatalf(
						"第 %d 条消息ID错误: want=%d got=%d",
						i,
						tt.wantIDs[i],
						msg.ID,
					)
				}
			}
		})
	}

}
