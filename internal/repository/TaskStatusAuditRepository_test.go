package repository

import (
	"context"
	"errors"
	"testing"

	"GoServer/internal/model"
)

func TestCreateTaskStatusAudit(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()
	suffix := testSuffix()

	// 1. 创建测试用户
	user := &model.Users{
		UserName: "audit_user_" + suffix,
		Email:    "audit_user_" + suffix + "@example.com",
		Password: "test-hash",
	}

	if err := CreateUser(ctx, tx, user); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	// 2. 创建测试项目
	project := &model.Project{
		UserID: user.ID,
		Name:   "audit_project_" + suffix,
	}

	if err := CreateProject(ctx, tx, project); err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}

	// 3. 创建测试任务
	task := &model.Task{
		UserID:    user.ID,
		ProjectID: project.ID,
		Title:     "审计记录测试",
	}

	if err := CreateTask(ctx, tx, task); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 4. 构造审计记录
	audit := &model.TaskStatusAudit{
		TaskID:      task.ID,
		ActorUserID: user.ID,
		FromStatus:  model.TaskStatusTodo,
		ToStatus:    model.TaskStatusDoing,
		FromVersion: 1,
		ToVersion:   2,
	}

	// ===========================
	// Test 1: 正常插入
	// ===========================
	t.Run("正常插入审计记录", func(t *testing.T) {
		err := CreateTaskStatusAudit(ctx, tx, audit)

		if err != nil {
			t.Fatalf("插入审计记录失败: %v", err)
		}

		if audit.ID == 0 {
			t.Fatal("审计记录 ID 未回填")
		}

		t.Logf("AuditID=%d", audit.ID)
	})

	if audit.ID == 0 {
		t.Fatal("初始审计记录未创建，停止后续测试")
	}

	// ===========================
	// Test 2: 数据持久化校验
	// ===========================
	t.Run("审计字段持久化正确", func(t *testing.T) {
		var stored model.TaskStatusAudit

		err := tx.WithContext(ctx).
			Where("id = ?", audit.ID).
			First(&stored).Error

		if err != nil {
			t.Fatalf("查询审计记录失败: %v", err)
		}

		if stored.TaskID != task.ID {
			t.Errorf("TaskID 不正确")
		}

		if stored.ActorUserID != user.ID {
			t.Errorf("ActorUserID 不正确")
		}

		if stored.FromStatus != model.TaskStatusTodo {
			t.Errorf(
				"FromStatus: want=todo, got=%s",
				stored.FromStatus,
			)
		}

		if stored.ToStatus != model.TaskStatusDoing {
			t.Errorf(
				"ToStatus: want=doing, got=%s",
				stored.ToStatus,
			)
		}

		if stored.FromVersion != 1 {
			t.Errorf(
				"FromVersion: want=1, got=%d",
				stored.FromVersion,
			)
		}

		if stored.ToVersion != 2 {
			t.Errorf(
				"ToVersion: want=2, got=%d",
				stored.ToVersion,
			)
		}

		if stored.CreatedAt.IsZero() {
			t.Error("CreatedAt 未正确生成")
		}
	})

	// ===========================
	// Test 3: 重复版本约束
	// ===========================
	t.Run("重复版本插入被拒绝", func(t *testing.T) {
		duplicate := &model.TaskStatusAudit{
			TaskID:      task.ID,
			ActorUserID: user.ID,
			FromStatus:  model.TaskStatusTodo,
			ToStatus:    model.TaskStatusDoing,
			FromVersion: 1,
			ToVersion:   2,
		}

		err := CreateTaskStatusAudit(ctx, tx, duplicate)

		if !errors.Is(err, ErrAlreadyExists) {
			t.Fatalf(
				"期望 ErrAlreadyExists，实际: %v",
				err,
			)
		}

		// 验证没有重复写入
		var count int64

		err = tx.WithContext(ctx).
			Model(&model.TaskStatusAudit{}).
			Where("task_id = ?", task.ID).
			Count(&count).Error

		if err != nil {
			t.Fatalf("统计审计记录失败: %v", err)
		}

		if count != 1 {
			t.Fatalf(
				"期望 1 条审计记录，实际 %d 条",
				count,
			)
		}
	})

	// ===========================
	// Test 4: 外键约束
	// ===========================
	t.Run("不存在的Task被外键拒绝", func(t *testing.T) {
		// 创建临时 Task 再删除，确保 ID 不存在
		ghostTask := &model.Task{
			UserID:    user.ID,
			ProjectID: project.ID,
			Title:     "临时任务",
		}

		if err := CreateTask(ctx, tx, ghostTask); err != nil {
			t.Fatalf("创建临时任务失败: %v", err)
		}

		result := tx.WithContext(ctx).
			Delete(&model.Task{}, ghostTask.ID)

		if result.Error != nil || result.RowsAffected != 1 {
			t.Fatalf(
				"删除临时任务失败: err=%v, rows=%d",
				result.Error,
				result.RowsAffected,
			)
		}

		invalidAudit := &model.TaskStatusAudit{
			TaskID:      ghostTask.ID,
			ActorUserID: user.ID,
			FromStatus:  "todo",
			ToStatus:    "doing",
			FromVersion: 1,
			ToVersion:   2,
		}

		err := CreateTaskStatusAudit(ctx, tx, invalidAudit)

		if !errors.Is(err, ErrInvalidReference) {
			t.Fatalf(
				"期望 ErrInvalidReference，实际: %v",
				err,
			)
		}
	})

	// ===========================
	// Test 5: nil 参数校验
	// ===========================
	t.Run("nil审计对象被拒绝", func(t *testing.T) {
		err := CreateTaskStatusAudit(ctx, tx, nil)

		if !errors.Is(err, ErrInvalidReference) {
			t.Fatalf(
				"期望 ErrInvalidReference，实际: %v",
				err,
			)
		}
	})
}
