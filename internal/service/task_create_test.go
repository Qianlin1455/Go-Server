package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"GoServer/internal/model"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// 该测试连接真实 MySQL；严格要求 TEST_MYSQL_DSN 指向 go_server_test。
// 不复用 repository 包的 setupTestTx：CreateTask 自身会开启事务。
type createTaskFixture struct {
	db              *gorm.DB
	userA, userB    model.Users
	project         model.Project
	otherProject    model.Project
	archivedProject model.Project
	foreignProject  model.Project
	validMessage    model.Message
	otherMessage    model.Message
	foreignMessage  model.Message
	looseMessage    model.Message
	missingProject  uint64
	missingMessage  uint64
}

func newCreateTaskFixture(t *testing.T) createTaskFixture {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("需要配置 TEST_MYSQL_DSN（仅允许 go_server_test）")
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("连接测试数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取数据库连接池失败: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	var databaseName string
	if err := db.Raw("SELECT DATABASE()").Row().Scan(&databaseName); err != nil {
		t.Fatalf("获取数据库名失败: %v", err)
	}
	if databaseName != "go_server_test" {
		t.Fatalf("拒绝在非测试库执行: 实际数据库=%q，期望 go_server_test", databaseName)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	f := createTaskFixture{db: db}

	mustCreate := func(value any) {
		t.Helper()
		if err := db.Create(value).Error; err != nil {
			t.Fatalf("准备测试数据失败 (%T): %v", value, err)
		}
	}

	f.userA = model.Users{UserName: "task_create_a_" + suffix, Email: "task_create_a_" + suffix + "@example.com", Password: "test-hash"}
	mustCreate(&f.userA)

	// 及时注册清理：即使后续 fixture 初始化失败，也会清理用户 A。
	// 删除顺序遵循外键：Task -> Message -> Conversation -> Project -> User。
	t.Cleanup(func() {
		queries := []string{
			"DELETE FROM tasks WHERE user_id IN (?, ?)",
			"DELETE FROM messages WHERE conversation_id IN (SELECT id FROM conversations WHERE user_id IN (?, ?))",
			"DELETE FROM conversations WHERE user_id IN (?, ?)",
			"DELETE FROM projects WHERE user_id IN (?, ?)",
			"DELETE FROM users WHERE id IN (?, ?)",
		}
		for _, query := range queries {
			if err := db.Exec(query, f.userA.ID, f.userB.ID).Error; err != nil {
				t.Errorf("清理测试数据失败: %v", err)
			}
		}
	})

	f.userB = model.Users{UserName: "task_create_b_" + suffix, Email: "task_create_b_" + suffix + "@example.com", Password: "test-hash"}
	mustCreate(&f.userB)

	f.project = model.Project{UserID: f.userA.ID, Name: "main_" + suffix, Status: model.ProjectStatusActive}
	f.otherProject = model.Project{UserID: f.userA.ID, Name: "other_" + suffix, Status: model.ProjectStatusActive}
	f.archivedProject = model.Project{UserID: f.userA.ID, Name: "archived_" + suffix, Status: model.ProjectStatusArchived}
	f.foreignProject = model.Project{UserID: f.userB.ID, Name: "foreign_" + suffix, Status: model.ProjectStatusActive}
	for _, project := range []*model.Project{&f.project, &f.otherProject, &f.archivedProject, &f.foreignProject} {
		mustCreate(project)
	}

	// 创建并删除一个临时项目，取得确保不存在的 project_id。
	ghostProject := model.Project{UserID: f.userA.ID, Name: "removed_" + suffix}
	mustCreate(&ghostProject)
	f.missingProject = ghostProject.ID
	if err := db.Delete(&ghostProject).Error; err != nil {
		t.Fatalf("删除临时项目失败: %v", err)
	}

	newMessage := func(userID uint64, projectID *uint64, content string) model.Message {
		t.Helper()
		conversation := model.Conversation{UserID: userID, ProjectID: projectID, Title: "source " + content}
		mustCreate(&conversation)
		message := model.Message{ConversationID: conversation.ID, Role: "user", Content: content}
		mustCreate(&message)
		return message
	}

	f.validMessage = newMessage(f.userA.ID, &f.project.ID, "valid")
	f.otherMessage = newMessage(f.userA.ID, &f.otherProject.ID, "other-project")
	f.foreignMessage = newMessage(f.userB.ID, &f.foreignProject.ID, "foreign-user")
	f.looseMessage = newMessage(f.userA.ID, nil, "unassigned")

	ghostMessage := newMessage(f.userA.ID, &f.project.ID, "deleted-message")
	f.missingMessage = ghostMessage.ID
	if err := db.Delete(&ghostMessage).Error; err != nil {
		t.Fatalf("删除临时消息失败: %v", err)
	}

	return f
}

func countCreatedTasks(t *testing.T, db *gorm.DB, userID uint64) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&model.Task{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		t.Fatalf("统计 Task 失败: %v", err)
	}
	return count
}

func TestCreateTask_Integration(t *testing.T) {
	f := newCreateTaskFixture(t)
	svc := NewTaskService(f.db)
	ctx := context.Background()

	t.Run("正常创建并验证持久化", func(t *testing.T) {
		desc := "验证任务创建"
		before := countCreatedTasks(t, f.db, f.userA.ID)
		got, err := svc.CreateTask(ctx, f.userA.ID, CreateTaskInput{
			ProjectID:   f.project.ID,
			Title:       "  实现 CAS 测试  ",
			Description: &desc,
		})
		if err != nil {
			t.Fatalf("CreateTask 失败: %v", err)
		}
		if got == nil || got.ID == 0 {
			t.Fatalf("未返回有效 TaskResult: %+v", got)
		}
		if got.ProjectID != f.project.ID || got.Title != "实现 CAS 测试" ||
			got.Description == nil || *got.Description != desc ||
			got.SourceMessageID != nil || got.Status != model.TaskStatusTodo ||
			got.Version != 1 || got.CompletedAt != nil {
			t.Fatalf("返回 DTO 不正确: %+v", got)
		}
		if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
			t.Fatalf("时间未正确生成: %+v", got)
		}

		var saved model.Task
		if err := f.db.Where("id = ? AND user_id = ?", got.ID, f.userA.ID).First(&saved).Error; err != nil {
			t.Fatalf("查询数据库中新建 Task 失败: %v", err)
		}
		if saved.Title != got.Title || saved.Status != got.Status ||
			saved.Version != got.Version || saved.CompletedAt != nil {
			t.Fatalf("数据库值与返回 DTO 不一致: %+v", saved)
		}
		if after := countCreatedTasks(t, f.db, f.userA.ID); after != before+1 {
			t.Fatalf("预期创建 1 条任务: before=%d after=%d", before, after)
		}
	})

	t.Run("合法来源消息", func(t *testing.T) {
		got, err := svc.CreateTask(ctx, f.userA.ID, CreateTaskInput{
			ProjectID:       f.project.ID,
			Title:           "根据对话创建任务",
			SourceMessageID: &f.validMessage.ID,
		})
		if err != nil {
			t.Fatalf("合法来源消息应成功: %v", err)
		}
		if got == nil || got.SourceMessageID == nil || *got.SourceMessageID != f.validMessage.ID {
			t.Fatalf("来源消息未正确返回: %+v", got)
		}
		var saved model.Task
		if err := f.db.First(&saved, got.ID).Error; err != nil {
			t.Fatal(err)
		}
		if saved.SourceMessageID == nil || *saved.SourceMessageID != f.validMessage.ID {
			t.Fatalf("数据库来源消息不正确: %+v", saved)
		}
	})

	t.Run("200字符标题允许", func(t *testing.T) {
		title := strings.Repeat("测", 200)
		got, err := svc.CreateTask(ctx, f.userA.ID, CreateTaskInput{
			ProjectID: f.project.ID, Title: title,
		})
		if err != nil || got == nil || got.Title != title {
			t.Fatalf("200 字符标题应成功: result=%+v err=%v", got, err)
		}
	})

	zero := uint64(0)
	invalidUTF8 := string([]byte{0xff})
	invalidCases := []struct {
		name   string
		userID uint64
		input  CreateTaskInput
		want   error
	}{
		{"用户ID为零", 0, CreateTaskInput{ProjectID: f.project.ID, Title: "task"}, ErrInvalidTaskInput},
		{"ProjectID为零", f.userA.ID, CreateTaskInput{ProjectID: 0, Title: "task"}, ErrInvalidTaskInput},
		{"标题空白", f.userA.ID, CreateTaskInput{ProjectID: f.project.ID, Title: "   "}, ErrInvalidTaskInput},
		{"标题201字符", f.userA.ID, CreateTaskInput{ProjectID: f.project.ID, Title: strings.Repeat("测", 201)}, ErrInvalidTaskInput},
		{"标题包含非法UTF8", f.userA.ID, CreateTaskInput{ProjectID: f.project.ID, Title: invalidUTF8}, ErrInvalidTaskInput},
		{"来源消息ID为零", f.userA.ID, CreateTaskInput{ProjectID: f.project.ID, Title: "task", SourceMessageID: &zero}, ErrInvalidTaskInput},
		{"项目不存在", f.userA.ID, CreateTaskInput{ProjectID: f.missingProject, Title: "task"}, ErrTaskNotFound},
		{"跨用户项目", f.userA.ID, CreateTaskInput{ProjectID: f.foreignProject.ID, Title: "task"}, ErrTaskNotFound},
		{"项目已归档", f.userA.ID, CreateTaskInput{ProjectID: f.archivedProject.ID, Title: "task"}, ErrTaskProjectArchived},
		{"来源消息不存在", f.userA.ID, CreateTaskInput{ProjectID: f.project.ID, Title: "task", SourceMessageID: &f.missingMessage}, ErrTaskSourceMessageInvalid},
		{"来源消息属于其他项目", f.userA.ID, CreateTaskInput{ProjectID: f.project.ID, Title: "task", SourceMessageID: &f.otherMessage.ID}, ErrTaskSourceMessageInvalid},
		{"来源消息属于其他用户", f.userA.ID, CreateTaskInput{ProjectID: f.project.ID, Title: "task", SourceMessageID: &f.foreignMessage.ID}, ErrTaskSourceMessageInvalid},
		{"来源消息所在会话没有项目", f.userA.ID, CreateTaskInput{ProjectID: f.project.ID, Title: "task", SourceMessageID: &f.looseMessage.ID}, ErrTaskSourceMessageInvalid},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			before := countCreatedTasks(t, f.db, f.userA.ID)
			got, err := svc.CreateTask(ctx, tc.userID, tc.input)
			if !errors.Is(err, tc.want) {
				t.Fatalf("期望错误 %v，实际 %v", tc.want, err)
			}
			if got != nil {
				t.Fatalf("创建失败不能返回 DTO: %+v", got)
			}
			if after := countCreatedTasks(t, f.db, f.userA.ID); after != before {
				t.Fatalf("失败后不应产生任务: before=%d after=%d", before, after)
			}
		})
	}

	t.Run("相同项目允许同名任务", func(t *testing.T) {
		input := CreateTaskInput{ProjectID: f.project.ID, Title: "允许重复标题"}
		first, err := svc.CreateTask(ctx, f.userA.ID, input)
		if err != nil {
			t.Fatal(err)
		}
		second, err := svc.CreateTask(ctx, f.userA.ID, input)
		if err != nil {
			t.Fatal(err)
		}
		if first.ID == second.ID {
			t.Fatalf("两次创建不能返回相同任务ID: %d", first.ID)
		}
	})
}
