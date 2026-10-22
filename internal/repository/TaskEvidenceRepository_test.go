package repository

import (
	"context"
	"errors"
	"testing"

	"GoServer/internal/model"

	"gorm.io/gorm"
)

func TestCreateTaskEvidence(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()
	suffix := testSuffix()

	// ============================
	// 1. 创建用户 A 和用户 B
	// ============================

	userA := &model.Users{
		UserName: "evidence_a_" + suffix,
		Email:    "evidence_a_" + suffix + "@example.com",
		Password: "test-hash",
	}

	userB := &model.Users{
		UserName: "evidence_b_" + suffix,
		Email:    "evidence_b_" + suffix + "@example.com",
		Password: "test-hash",
	}

	for _, user := range []*model.Users{userA, userB} {
		if err := CreateUser(ctx, tx, user); err != nil {
			t.Fatalf("创建测试用户失败: %v", err)
		}
	}

	// ============================
	// 2. 创建 Project
	// ============================

	project := &model.Project{
		UserID: userA.ID,
		Name:   "evidence_project_" + suffix,
		Status: model.ProjectStatusActive,
	}

	if err := CreateProject(ctx, tx, project); err != nil {
		t.Fatalf("创建 Project 失败: %v", err)
	}

	// ============================
	// 3. 创建 Task
	// ============================

	task := &model.Task{
		UserID:    userA.ID,
		ProjectID: project.ID,
		Title:     "验证 CAS 乐观锁",
		Status:    model.TaskStatusTodo,
		Version:   1,
	}

	if err := CreateTask(ctx, tx, task); err != nil {
		t.Fatalf("创建 Task 失败: %v", err)
	}

	// ============================
	// Test 1: 正常提交证据
	// ============================

	t.Run("正常提交证据", func(t *testing.T) {
		content := "20个并发请求中仅有1个CAS成功"

		evidence := &model.TaskEvidence{
			TaskID:    task.ID,
			CreatedBy: userA.ID,
			Type:      model.EvidenceTypeExperimentText,
			Text:      &content,
			Status:    model.EvidenceStatusSubmitted,
		}

		err := CreateTaskEvidence(ctx, tx, evidence)
		if err != nil {
			t.Fatalf("创建证据失败: %v", err)
		}

		if evidence.ID == 0 {
			t.Fatal("证据 ID 未回填")
		}

		// 查询数据库，验证写入结果
		var stored model.TaskEvidence

		err = tx.WithContext(ctx).
			Where("id = ?", evidence.ID).
			First(&stored).Error

		if err != nil {
			t.Fatalf("查询证据失败: %v", err)
		}

		if stored.TaskID != task.ID {
			t.Errorf("TaskID 不正确")
		}

		if stored.CreatedBy != userA.ID {
			t.Errorf("CreatedBy 不正确")
		}

		if stored.Type != model.EvidenceTypeExperimentText {
			t.Errorf("证据类型不正确")
		}

		if stored.Text == nil || *stored.Text != content {
			t.Errorf("证据内容不正确")
		}

		if stored.Status != model.EvidenceStatusSubmitted {
			t.Errorf("证据状态不正确")
		}

		if stored.CreatedAt.IsZero() {
			t.Error("CreatedAt 未生成")
		}
	})

	// ============================
	// Test 2: nil 参数
	// ============================

	t.Run("nil证据被拒绝", func(t *testing.T) {
		err := CreateTaskEvidence(ctx, tx, nil)

		if !errors.Is(err, ErrInvalidReference) {
			t.Fatalf(
				"期望 ErrInvalidReference，实际: %v",
				err,
			)
		}
	})

	// ============================
	// Test 3: Task 不存在
	// ============================

	t.Run("不存在的Task被拒绝", func(t *testing.T) {
		content := "无效任务证据"

		evidence := &model.TaskEvidence{
			TaskID:    ^uint64(0),
			CreatedBy: userA.ID,
			Type:      model.EvidenceTypeExperimentText,
			Text:      &content,
			Status:    model.EvidenceStatusSubmitted,
		}

		err := CreateTaskEvidence(ctx, tx, evidence)

		if !errors.Is(err, ErrInvalidReference) {
			t.Fatalf(
				"期望 ErrInvalidReference，实际: %v",
				err,
			)
		}
	})

	// ============================
	// Test 4: 复合外键归属校验
	// ============================

	t.Run("跨用户证据被复合外键拒绝", func(t *testing.T) {
		content := "用户B尝试给用户A的任务提交证据"

		evidence := &model.TaskEvidence{
			TaskID:    task.ID,
			CreatedBy: userB.ID,
			Type:      model.EvidenceTypeExperimentText,
			Text:      &content,
			Status:    model.EvidenceStatusSubmitted,
		}

		err := CreateTaskEvidence(ctx, tx, evidence)

		if !errors.Is(err, ErrInvalidReference) {
			t.Fatalf(
				"复合外键应拒绝跨用户提交，实际: %v",
				err,
			)
		}
	})

	// ============================
	// Test 5: 非法证据类型
	// ============================

	t.Run("非法证据类型被CHECK拒绝", func(t *testing.T) {
		content := "非法类型测试"

		evidence := &model.TaskEvidence{
			TaskID:    task.ID,
			CreatedBy: userA.ID,
			Type:      "invalid_type",
			Text:      &content,
			Status:    model.EvidenceStatusSubmitted,
		}

		err := CreateTaskEvidence(ctx, tx, evidence)

		if !errors.Is(err, ErrStorage) {
			t.Fatalf(
				"期望 CHECK 失败映射为 ErrStorage，实际: %v",
				err,
			)
		}
	})

	// ============================
	// Test 6: 非法证据状态
	// ============================

	t.Run("非法状态被CHECK拒绝", func(t *testing.T) {
		content := "非法状态测试"

		evidence := &model.TaskEvidence{
			TaskID:    task.ID,
			CreatedBy: userA.ID,
			Type:      model.EvidenceTypeTestOutput,
			Text:      &content,
			Status:    "unknown",
		}

		err := CreateTaskEvidence(ctx, tx, evidence)

		if !errors.Is(err, ErrStorage) {
			t.Fatalf(
				"期望 CHECK 失败映射为 ErrStorage，实际: %v",
				err,
			)
		}
	})

	// ============================
	// Test 7: 空草稿允许创建
	// ============================

	t.Run("空草稿允许创建", func(t *testing.T) {
		evidence := &model.TaskEvidence{
			TaskID:    task.ID,
			CreatedBy: userA.ID,
			Type:      model.EvidenceTypeCommitSHA,
			Status:    model.EvidenceStatusDraft,
		}

		err := CreateTaskEvidence(ctx, tx, evidence)

		if err != nil {
			t.Fatalf("空草稿创建失败: %v", err)
		}

		if evidence.ID == 0 {
			t.Fatal("草稿 ID 未回填")
		}
	})

	// ============================
	// Test 8: 事务失败回滚
	// ============================

	t.Run("事务失败回滚", func(t *testing.T) {
		content := "需要回滚的证据"
		injectedErr := errors.New("模拟事务失败")

		evidence := &model.TaskEvidence{
			TaskID:    task.ID,
			CreatedBy: userA.ID,
			Type:      model.EvidenceTypeTestOutput,
			Text:      &content,
			Status:    model.EvidenceStatusSubmitted,
		}

		// 外层 setupTestTx 已经开启事务。
		// 这里的嵌套 Transaction 使用 GORM Savepoint。
		err := tx.WithContext(ctx).Transaction(
			func(innerTx *gorm.DB) error {
				if err := CreateTaskEvidence(
					ctx, innerTx, evidence,
				); err != nil {
					return err
				}

				// 模拟后续业务失败
				return injectedErr
			},
		)

		if !errors.Is(err, injectedErr) {
			t.Fatalf(
				"期望事务返回注入错误，实际: %v",
				err,
			)
		}

		if evidence.ID == 0 {
			t.Fatal("INSERT 未成功执行，无法验证回滚")
		}

		// INSERT 成功不代表事务已经提交。
		// 回滚后证据必须不存在。
		var count int64

		err = tx.Model(&model.TaskEvidence{}).
			Where("id = ?", evidence.ID).
			Count(&count).Error

		if err != nil {
			t.Fatalf("查询证据失败: %v", err)
		}

		if count != 0 {
			t.Fatalf(
				"事务回滚失败，仍存在 %d 条证据",
				count,
			)
		}
	})
}
