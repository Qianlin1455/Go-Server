package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"GoServer/internal/midware"
	"GoServer/internal/model"
	"GoServer/internal/security"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type taskAPIFixture struct {
	db       *gorm.DB
	router   *gin.Engine
	userA    model.Users
	userB    model.Users
	projectA model.Project
	projectB model.Project
	archived model.Project
	other    model.Project
	messageA model.Message
	messageB model.Message
	tokenA   string
	tokenB   string
}

func newTaskAPIFixture(t *testing.T) *taskAPIFixture {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("需要配置 TEST_MYSQL_DSN，指向 go_server_test")
	}

	db, err := gorm.Open(
		mysql.Open(dsn),
		&gorm.Config{TranslateError: true},
	)
	if err != nil {
		t.Fatalf("连接测试数据库失败: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	// 防止误操作开发数据库
	var dbName string
	if err := db.Raw("SELECT DATABASE()").
		Scan(&dbName).Error; err != nil {
		t.Fatal(err)
	}
	if dbName != "go_server_test" {
		t.Fatalf(
			"拒绝执行：当前数据库为 %q，不是 go_server_test",
			dbName,
		)
	}

	f := &taskAPIFixture{db: db}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	f.userA = model.Users{
		UserName: "task_http_a_" + suffix,
		Email:    "task_http_a_" + suffix + "@example.com",
		Password: "test-hash",
	}
	f.userB = model.Users{
		UserName: "task_http_b_" + suffix,
		Email:    "task_http_b_" + suffix + "@example.com",
		Password: "test-hash",
	}

	var convA, convB model.Conversation

	// 初始化数据并提交，让 HTTP Service 使用真实事务
	err = db.Transaction(func(tx *gorm.DB) error {
		for _, u := range []*model.Users{
			&f.userA,
			&f.userB,
		} {
			if err := tx.Create(u).Error; err != nil {
				return err
			}
		}

		f.projectA = model.Project{
			UserID: f.userA.ID,
			Name:   "task_http_a_" + suffix,
			Status: model.ProjectStatusActive,
		}
		f.archived = model.Project{
			UserID: f.userA.ID,
			Name:   "task_http_archived_" + suffix,
			Status: model.ProjectStatusArchived,
		}
		f.other = model.Project{
			UserID: f.userA.ID,
			Name:   "task_http_other_" + suffix,
			Status: model.ProjectStatusActive,
		}
		f.projectB = model.Project{
			UserID: f.userB.ID,
			Name:   "task_http_b_" + suffix,
			Status: model.ProjectStatusActive,
		}

		for _, p := range []*model.Project{
			&f.projectA,
			&f.archived,
			&f.other,
			&f.projectB,
		} {
			if err := tx.Create(p).Error; err != nil {
				return err
			}
		}

		convA = model.Conversation{
			UserID:    f.userA.ID,
			ProjectID: &f.projectA.ID,
			Title:     "Task 来源会话 A",
		}
		convB = model.Conversation{
			UserID:    f.userB.ID,
			ProjectID: &f.projectB.ID,
			Title:     "Task 来源会话 B",
		}

		for _, c := range []*model.Conversation{
			&convA,
			&convB,
		} {
			if err := tx.Create(c).Error; err != nil {
				return err
			}
		}

		f.messageA = model.Message{
			ConversationID: convA.ID,
			Role:           "user",
			Content:        "来自用户 A 的消息",
		}
		f.messageB = model.Message{
			ConversationID: convB.ID,
			Role:           "user",
			Content:        "来自用户 B 的消息",
		}

		for _, m := range []*model.Message{
			&f.messageA,
			&f.messageB,
		} {
			if err := tx.Create(m).Error; err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("准备测试数据失败: %v", err)
	}

	// 清理：必须按照外键依赖的反方向删除
	t.Cleanup(func() {
		userIDs := []uint64{
			f.userA.ID,
			f.userB.ID,
		}
		projectIDs := []uint64{
			f.projectA.ID,
			f.archived.ID,
			f.other.ID,
			f.projectB.ID,
		}
		conversationIDs := []uint64{
			convA.ID,
			convB.ID,
		}

		err := db.Transaction(func(tx *gorm.DB) error {
			// 1. 删除证据
			if err := tx.Exec(
				`DELETE FROM task_evidence
				WHERE task_id IN (
					SELECT id FROM tasks
					WHERE user_id IN (?, ?)
				)`,
				f.userA.ID,
				f.userB.ID,
			).Error; err != nil {
				return fmt.Errorf("cleanup evidence: %w", err)
			}

			// 2. 删除审核
			if err := tx.Exec(
				`DELETE FROM task_reviews
				WHERE task_id IN (
					SELECT id FROM tasks
					WHERE user_id IN (?, ?)
				)`,
				f.userA.ID,
				f.userB.ID,
			).Error; err != nil {
				return fmt.Errorf("cleanup reviews: %w", err)
			}

			// 3. 删除状态审计
			if err := tx.Exec(
				`DELETE FROM task_status_audits
				WHERE task_id IN (
					SELECT id FROM tasks
					WHERE user_id IN (?, ?)
				)`,
				f.userA.ID,
				f.userB.ID,
			).Error; err != nil {
				return fmt.Errorf("cleanup audits: %w", err)
			}

			// 4. 删除 Task
			if err := tx.Where(
				"user_id IN ?", userIDs,
			).Delete(&model.Task{}).Error; err != nil {
				return fmt.Errorf("cleanup tasks: %w", err)
			}

			if err := tx.Where(
				"conversation_id IN ?", conversationIDs,
			).Delete(&model.Message{}).Error; err != nil {
				return err
			}

			if err := tx.Where(
				"id IN ?", conversationIDs,
			).Delete(&model.Conversation{}).Error; err != nil {
				return err
			}

			if err := tx.Where(
				"id IN ?", projectIDs,
			).Delete(&model.Project{}).Error; err != nil {
				return err
			}

			return tx.Where(
				"id IN ?", userIDs,
			).Delete(&model.Users{}).Error
		})

		if err != nil {
			t.Errorf("清理 HTTP 测试数据失败: %v", err)
		}
	})

	// 生成真实 JWT
	manager, err := security.NewJWTManager(
		"0123456789abcdef0123456789abcdef",
		time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}

	f.tokenA, err = manager.Generate(f.userA.ID)
	if err != nil {
		t.Fatal(err)
	}

	f.tokenB, err = manager.Generate(f.userB.ID)
	if err != nil {
		t.Fatal(err)
	}

	// 注册真实 Gin Handler
	f.router = gin.New()
	h := NewTaskHandler(service.NewTaskService(db))
	evidenceHandler := NewEvidenceHandler(
		service.NewEvidenceService(db),
	)

	private := f.router.Group("/api/v1")
	private.Use(midware.JWTAuth(manager))

	private.POST("/projects/:id/tasks", h.CreateTask)
	private.GET("/projects/:id/tasks", h.ListTasks)
	private.PATCH("/tasks/:id/status", h.ChangeStatus)

	private.POST(
		"/tasks/:id/evidence",
		evidenceHandler.CreateEvidence,
	)
	return f
}

// 发送 HTTP 请求
func (f *taskAPIFixture) request(
	method, path, token, body string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(
		method,
		path,
		strings.NewReader(body),
	)

	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

// 断言 HTTP 状态和业务码
func expectTaskHTTP(
	t *testing.T,
	w *httptest.ResponseRecorder,
	status int,
	code string,
) {
	t.Helper()

	if w.Code != status {
		t.Fatalf(
			"HTTP 状态码: want=%d got=%d body=%s",
			status, w.Code, w.Body.String(),
		)
	}

	if status == http.StatusNoContent {
		if w.Body.Len() != 0 {
			t.Fatalf("204 不应有响应正文: %s", w.Body.String())
		}
		return
	}

	if code == "" {
		return
	}

	var out struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}

	if out.Code != code {
		t.Fatalf(
			"业务码: want=%s got=%s body=%s",
			code, out.Code, w.Body.String(),
		)
	}
}

// ======================================
// Task HTTP 集成测试
// ======================================

func TestTaskHandlerAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)

	f := newTaskAPIFixture(t)

	projectPath := fmt.Sprintf(
		"/api/v1/projects/%d/tasks",
		f.projectA.ID,
	)

	taskPath := func(taskID uint64) string {
		return fmt.Sprintf(
			"/api/v1/tasks/%d/status",
			taskID,
		)
	}

	// 1. JWT 和非法参数
	t.Run("认证和参数校验", func(t *testing.T) {
		cases := []struct {
			name   string
			method string
			path   string
			token  string
			body   string
			status int
			code   string
		}{
			{
				"POST缺少JWT", http.MethodPost,
				projectPath, "",
				`{"title":"test"}`,
				401, "MISSING_TOKEN",
			},
			{
				"GET非法JWT", http.MethodGet,
				projectPath, "wrong.token.value",
				"",
				401, "INVALID_TOKEN",
			},
			{
				"PATCH缺少JWT", http.MethodPatch,
				"/api/v1/tasks/1/status", "",
				`{"status":"doing","version":1}`,
				401, "MISSING_TOKEN",
			},
			{
				"非法项目ID", http.MethodPost,
				"/api/v1/projects/abc/tasks", f.tokenA,
				`{"title":"test"}`,
				400, "INVALID_ARGUMENT",
			},
			{
				"零项目ID", http.MethodGet,
				"/api/v1/projects/0/tasks", f.tokenA,
				"",
				400, "INVALID_ARGUMENT",
			},
			{
				"非法任务ID", http.MethodPatch,
				"/api/v1/tasks/abc/status", f.tokenA,
				`{"status":"doing","version":1}`,
				400, "INVALID_ARGUMENT",
			},
			{
				"创建JSON错误", http.MethodPost,
				projectPath, f.tokenA,
				`{"title":`,
				400, "INVALID_ARGUMENT",
			},
			{
				"创建缺少标题", http.MethodPost,
				projectPath, f.tokenA,
				`{}`,
				400, "INVALID_ARGUMENT",
			},
			{
				"创建空白标题", http.MethodPost,
				projectPath, f.tokenA,
				`{"title":"  "}`,
				400, "INVALID_ARGUMENT",
			},
			{
				"分页字符串错误", http.MethodGet,
				projectPath + "?page=abc", f.tokenA,
				"",
				400, "INVALID_ARGUMENT",
			},
			{
				"page为零", http.MethodGet,
				projectPath + "?page=0", f.tokenA,
				"",
				400, "INVALID_ARGUMENT",
			},
			{
				"pageSize过大", http.MethodGet,
				projectPath + "?page_size=101", f.tokenA,
				"",
				400, "INVALID_ARGUMENT",
			},
			{
				"状态缺少版本", http.MethodPatch,
				"/api/v1/tasks/1/status", f.tokenA,
				`{"status":"doing"}`,
				400, "INVALID_ARGUMENT",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				w := f.request(
					tc.method, tc.path,
					tc.token, tc.body,
				)
				expectTaskHTTP(t, w, tc.status, tc.code)
			})
		}
	})

	// 2. 正常创建 Task
	var taskID uint64

	t.Run("创建任务并验证落库", func(t *testing.T) {
		body := fmt.Sprintf(
			`{"title":"  实现Task状态机  ","source_message_id":%d}`,
			f.messageA.ID,
		)

		w := f.request(
			http.MethodPost,
			projectPath,
			f.tokenA,
			body,
		)

		expectTaskHTTP(t, w, 201, "SUCCESS")

		var out struct {
			Code string             `json:"code"`
			Data service.TaskResult `json:"data"`
		}

		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}

		taskID = out.Data.ID

		if taskID == 0 ||
			out.Data.ProjectID != f.projectA.ID ||
			out.Data.Title != "实现Task状态机" ||
			out.Data.Status != model.TaskStatusTodo ||
			out.Data.Version != 1 ||
			out.Data.CompletedAt != nil ||
			out.Data.SourceMessageID == nil ||
			*out.Data.SourceMessageID != f.messageA.ID {

			t.Fatalf("TaskResult 不正确: %+v", out.Data)
		}

		var saved model.Task
		if err := f.db.First(&saved, taskID).Error; err != nil {
			t.Fatal(err)
		}

		if saved.UserID != f.userA.ID ||
			saved.ProjectID != f.projectA.ID ||
			saved.Version != 1 {
			t.Fatalf("数据库 Task 不正确: %+v", saved)
		}
	})

	if taskID == 0 {
		t.Fatal("创建任务失败，停止后续测试")
	}

	// 3. 创建任务的权限和业务限制
	t.Run("创建权限和来源消息隔离", func(t *testing.T) {
		cases := []struct {
			name   string
			path   string
			body   string
			code   string
			status int
		}{
			{
				"跨用户项目",
				fmt.Sprintf(
					"/api/v1/projects/%d/tasks",
					f.projectB.ID,
				),
				`{"title":"cross-user"}`,
				"NOT_FOUND",
				404,
			},
			{
				"归档项目",
				fmt.Sprintf(
					"/api/v1/projects/%d/tasks",
					f.archived.ID,
				),
				`{"title":"archived"}`,
				"PROJECT_ARCHIVED",
				409,
			},
			{
				"跨用户来源消息",
				projectPath,
				fmt.Sprintf(
					`{"title":"bad-source","source_message_id":%d}`,
					f.messageB.ID,
				),
				"INVALID_ARGUMENT",
				400,
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				w := f.request(
					http.MethodPost,
					tc.path,
					f.tokenA,
					tc.body,
				)
				expectTaskHTTP(t, w, tc.status, tc.code)
			})
		}
	})

	// 4. GET 分页和资源隔离
	t.Run("列表查询和项目隔离", func(t *testing.T) {
		w := f.request(
			http.MethodGet,
			projectPath+"?page=1&page_size=20",
			f.tokenA,
			"",
		)
		expectTaskHTTP(t, w, 200, "SUCCESS")

		var out struct {
			Data struct {
				Items    []service.TaskResult `json:"items"`
				Page     int                  `json:"page"`
				PageSize int                  `json:"page_size"`
			} `json:"data"`
		}

		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}

		if len(out.Data.Items) != 1 ||
			out.Data.Items[0].ID != taskID ||
			out.Data.Page != 1 ||
			out.Data.PageSize != 20 {
			t.Fatalf("任务列表错误: %+v", out.Data)
		}

		// 用户 A 查询用户 B 的 Project
		w = f.request(
			http.MethodGet,
			fmt.Sprintf(
				"/api/v1/projects/%d/tasks",
				f.projectB.ID,
			),
			f.tokenA,
			"",
		)
		expectTaskHTTP(t, w, 404, "NOT_FOUND")

		// 同一用户的其他 Project 应该是空列表
		w = f.request(
			http.MethodGet,
			fmt.Sprintf(
				"/api/v1/projects/%d/tasks",
				f.other.ID,
			),
			f.tokenA,
			"",
		)
		expectTaskHTTP(t, w, 200, "SUCCESS")

		var empty struct {
			Data struct {
				Items []service.TaskResult `json:"items"`
			} `json:"data"`
		}

		if err := json.Unmarshal(w.Body.Bytes(), &empty); err != nil {
			t.Fatal(err)
		}
		if empty.Data.Items == nil || len(empty.Data.Items) != 0 {
			t.Fatalf("应返回非nil空列表: %+v", empty.Data.Items)
		}

		// 归档项目允许读取
		w = f.request(
			http.MethodGet,
			fmt.Sprintf(
				"/api/v1/projects/%d/tasks",
				f.archived.ID,
			),
			f.tokenA,
			"",
		)
		expectTaskHTTP(t, w, 200, "SUCCESS")
	})

	// 查询审计记录数量
	auditCount := func(t *testing.T) int64 {
		t.Helper()

		var count int64
		if err := f.db.
			Model(&model.TaskStatusAudit{}).
			Where("task_id = ?", taskID).
			Count(&count).Error; err != nil {
			t.Fatal(err)
		}

		return count
	}

	// 5. 非法状态转换和越权
	t.Run("非法状态转换与越权", func(t *testing.T) {
		// 用户 B 无权操作用户 A 的 Task
		w := f.request(
			http.MethodPatch,
			taskPath(taskID),
			f.tokenB,
			`{"status":"doing","version":1}`,
		)
		expectTaskHTTP(t, w, 404, "NOT_FOUND")

		// 不允许 todo -> done
		w = f.request(
			http.MethodPatch,
			taskPath(taskID),
			f.tokenA,
			`{"status":"done","version":1}`,
		)
		expectTaskHTTP(t, w, 409, "INVALID_STATE_TRANSITION")

		if auditCount(t) != 0 {
			t.Fatal("非法状态转换不应生成审计记录")
		}
	})

	// 6. todo -> doing
	t.Run("todo到doing成功并写审计", func(t *testing.T) {
		w := f.request(
			http.MethodPatch,
			taskPath(taskID),
			f.tokenA,
			`{"status":"doing","version":1}`,
		)

		expectTaskHTTP(t, w, 204, "")

		var saved model.Task
		if err := f.db.First(&saved, taskID).Error; err != nil {
			t.Fatal(err)
		}

		if saved.Status != model.TaskStatusDoing ||
			saved.Version != 2 ||
			saved.CompletedAt != nil ||
			auditCount(t) != 1 {

			t.Fatalf("状态或审计异常: %+v", saved)
		}

		var audit model.TaskStatusAudit
		if err := f.db.
			Where("task_id = ?", taskID).
			First(&audit).Error; err != nil {
			t.Fatal(err)
		}

		if audit.ActorUserID != f.userA.ID ||
			audit.FromStatus != model.TaskStatusTodo ||
			audit.ToStatus != model.TaskStatusDoing ||
			audit.FromVersion != 1 ||
			audit.ToVersion != 2 {

			t.Fatalf("审计内容错误: %+v", audit)
		}
	})

	// 7. 旧版本和重复状态变更
	t.Run("旧版本和重复转换被拒绝", func(t *testing.T) {
		// 客户端仍然提交旧 version=1
		w := f.request(
			http.MethodPatch,
			taskPath(taskID),
			f.tokenA,
			`{"status":"done","version":1}`,
		)
		expectTaskHTTP(t, w, 409, "VERSION_CONFLICT")

		// 当前已经是 doing，不允许再次 doing
		w = f.request(
			http.MethodPatch,
			taskPath(taskID),
			f.tokenA,
			`{"status":"doing","version":2}`,
		)
		expectTaskHTTP(t, w, 409, "INVALID_STATE_TRANSITION")

		if auditCount(t) != 1 {
			t.Fatal("冲突请求不应新增审计记录")
		}
	})

	// 8. doing -> done
	t.Run("doing到done成功", func(t *testing.T) {

		// 为任务准备一条正式提交的证据
		content := "go test ./... PASS"

		evidence := &model.TaskEvidence{
			TaskID:    taskID,
			CreatedBy: f.userA.ID,
			Type:      model.EvidenceTypeTestOutput,
			Text:      &content,
			Status:    model.EvidenceStatusSubmitted,
		}

		if err := f.db.Create(evidence).Error; err != nil {
			t.Fatalf("准备 submitted 证据失败: %v", err)
		}

		w := f.request(
			http.MethodPatch,
			taskPath(taskID),
			f.tokenA,
			`{"status":"done","version":2}`,
		)
		expectTaskHTTP(t, w, 204, "")

		var saved model.Task
		if err := f.db.First(&saved, taskID).Error; err != nil {
			t.Fatal(err)
		}

		if saved.Status != model.TaskStatusDone ||
			saved.Version != 3 ||
			saved.CompletedAt == nil ||
			auditCount(t) != 2 {

			t.Fatalf("最终状态或审计异常: %+v", saved)
		}

		// done 是终态，不允许重新回到 todo
		w = f.request(
			http.MethodPatch,
			taskPath(taskID),
			f.tokenA,
			`{"status":"todo","version":3}`,
		)
		expectTaskHTTP(t, w, 409, "INVALID_STATE_TRANSITION")

		if auditCount(t) != 2 {
			t.Fatal("done 终态不能新增审计记录")
		}
	})
}
