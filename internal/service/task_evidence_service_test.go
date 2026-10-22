package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"GoServer/internal/model"

	"gorm.io/gorm"
)

// 复用现有 Task Fixture。
// 额外清理 task_evidence，必须在删除 Task 之前执行。
func newEvidenceTestFixture(
	t *testing.T,
) (*gorm.DB, *model.Users, *model.Task) {
	t.Helper()

	db, user, task := newTaskStatusTestFixture(t)

	// t.Cleanup 按后注册、先执行的顺序运行。
	// 因此证据删除早于原 Fixture 中的 Task 删除。
	t.Cleanup(func() {
		err := db.
			Where("task_id = ?", task.ID).
			Delete(&model.TaskEvidence{}).Error

		if err != nil {
			t.Errorf("清理 TaskEvidence 失败: %v", err)
		}
	})

	return db, user, task
}

// 查询指定 Task 的证据数量。
func countTaskEvidence(
	t *testing.T,
	db *gorm.DB,
	taskID uint64,
) int64 {
	t.Helper()

	var count int64

	err := db.Model(&model.TaskEvidence{}).
		Where("task_id = ?", taskID).
		Count(&count).Error

	if err != nil {
		t.Fatalf("统计证据失败: %v", err)
	}

	return count
}

// ============================
// Test 1: 正常提交证据
// ============================

func TestCreateEvidence_Success(t *testing.T) {
	db, user, task := newEvidenceTestFixture(t)
	svc := NewEvidenceService(db)

	content := "20个并发请求中只有1个CAS更新成功"

	input := CreateEvidenceInput{
		Type:   model.EvidenceTypeExperimentText,
		Text:   &content,
		Status: model.EvidenceStatusSubmitted,
	}

	result, err := svc.CreateEvidence(
		context.Background(),
		user.ID,
		task.ID,
		input,
	)

	if err != nil {
		t.Fatalf("创建证据失败: %v", err)
	}

	if result == nil || result.ID == 0 {
		t.Fatal("创建成功但返回结果无效")
	}

	if result.TaskID != task.ID {
		t.Fatalf("TaskID 不正确")
	}

	if result.Status != model.EvidenceStatusSubmitted {
		t.Fatalf("Status 不正确")
	}

	if result.CreatedAt.IsZero() {
		t.Fatal("CreatedAt 未生成")
	}

	// 验证数据库实际持久化的内容
	var stored model.TaskEvidence

	err = db.Where("id = ?", result.ID).
		First(&stored).Error

	if err != nil {
		t.Fatalf("查询证据失败: %v", err)
	}

	if stored.CreatedBy != user.ID {
		t.Fatal("CreatedBy 不正确")
	}

	if stored.TaskID != task.ID {
		t.Fatal("TaskID 持久化错误")
	}

	if stored.Type != model.EvidenceTypeExperimentText {
		t.Fatal("Type 持久化错误")
	}

	if stored.Text == nil || *stored.Text != content {
		t.Fatal("Text 持久化错误")
	}

	if stored.Status != model.EvidenceStatusSubmitted {
		t.Fatal("Status 持久化错误")
	}

	if count := countTaskEvidence(t, db, task.ID); count != 1 {
		t.Fatalf("预期1条证据，实际%d条", count)
	}
}

// ============================
// Test 2: 空草稿允许创建
// ============================

func TestCreateEvidence_Draft(t *testing.T) {
	db, user, task := newEvidenceTestFixture(t)
	svc := NewEvidenceService(db)

	input := CreateEvidenceInput{
		Type:   model.EvidenceTypeCommitSHA,
		Status: model.EvidenceStatusDraft,
	}

	result, err := svc.CreateEvidence(
		context.Background(),
		user.ID,
		task.ID,
		input,
	)

	if err != nil {
		t.Fatalf("创建草稿失败: %v", err)
	}

	if result == nil || result.ID == 0 {
		t.Fatal("草稿创建结果无效")
	}

	var stored model.TaskEvidence

	if err := db.First(&stored, result.ID).Error; err != nil {
		t.Fatalf("查询草稿失败: %v", err)
	}

	if stored.Status != model.EvidenceStatusDraft {
		t.Fatal("草稿状态错误")
	}

	if stored.Text != nil || stored.CommitSHA != nil {
		t.Fatal("空草稿不应该包含内容")
	}
}

// ============================
// Test 3: 跨用户提交
// ============================

func TestCreateEvidence_CrossUser(t *testing.T) {
	db, _, task := newEvidenceTestFixture(t)
	svc := NewEvidenceService(db)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	// 创建用户 B
	userB := &model.Users{
		UserName: "evidence_other_" + suffix,
		Email: "evidence_other_" + suffix +
			"@example.com",
		Password: "test-hash",
	}

	if err := db.Create(userB).Error; err != nil {
		t.Fatalf("创建用户B失败: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Delete(userB).Error; err != nil {
			t.Errorf("清理用户B失败: %v", err)
		}
	})

	content := "尝试越权提交证据"

	input := CreateEvidenceInput{
		Type:   model.EvidenceTypeExperimentText,
		Text:   &content,
		Status: model.EvidenceStatusSubmitted,
	}

	// B 尝试操作 A 的 Task
	result, err := svc.CreateEvidence(
		context.Background(),
		userB.ID,
		task.ID,
		input,
	)

	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf(
			"预期 ErrTaskNotFound，实际: %v",
			err,
		)
	}

	if result != nil {
		t.Fatal("越权提交不应返回证据")
	}

	if count := countTaskEvidence(t, db, task.ID); count != 0 {
		t.Fatalf("越权请求写入了%d条证据", count)
	}
}

// ============================
// Test 4: Task 不存在
// ============================

func TestCreateEvidence_TaskNotFound(t *testing.T) {
	db, user, task := newEvidenceTestFixture(t)
	svc := NewEvidenceService(db)

	content := "无效任务"

	input := CreateEvidenceInput{
		Type:   model.EvidenceTypeTestOutput,
		Text:   &content,
		Status: model.EvidenceStatusSubmitted,
	}

	result, err := svc.CreateEvidence(
		context.Background(),
		user.ID,
		^uint64(0),
		input,
	)

	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf(
			"预期 ErrTaskNotFound，实际: %v",
			err,
		)
	}

	if result != nil {
		t.Fatal("不存在的任务不应创建证据")
	}

	if countTaskEvidence(t, db, task.ID) != 0 {
		t.Fatal("任务不存在时发生了意外写入")
	}
}

// ============================
// Test 5: 项目已归档
// ============================

func TestCreateEvidence_ArchivedProject(t *testing.T) {
	db, user, task := newEvidenceTestFixture(t)
	svc := NewEvidenceService(db)

	// 将任务所属项目归档
	result := db.Model(&model.Project{}).
		Where(
			"id = ? AND user_id = ?",
			task.ProjectID,
			user.ID,
		).
		Update("status", model.ProjectStatusArchived)

	if result.Error != nil {
		t.Fatalf("归档项目失败: %v", result.Error)
	}

	if result.RowsAffected != 1 {
		t.Fatalf(
			"预期归档1个项目，实际%d个",
			result.RowsAffected,
		)
	}

	content := "归档项目中的测试输出"

	input := CreateEvidenceInput{
		Type:   model.EvidenceTypeTestOutput,
		Text:   &content,
		Status: model.EvidenceStatusSubmitted,
	}

	created, err := svc.CreateEvidence(
		context.Background(),
		user.ID,
		task.ID,
		input,
	)

	if !errors.Is(err, ErrTaskProjectArchived) {
		t.Fatalf(
			"预期 ErrTaskProjectArchived，实际: %v",
			err,
		)
	}

	if created != nil {
		t.Fatal("归档项目不应该创建证据")
	}

	if countTaskEvidence(t, db, task.ID) != 0 {
		t.Fatal("归档项目仍然写入了证据")
	}
}

// ============================
// Test 6: 非法输入
// ============================

func TestCreateEvidence_InvalidInput(t *testing.T) {
	db, user, task := newEvidenceTestFixture(t)
	svc := NewEvidenceService(db)

	content := "正常内容"
	sha := strings.Repeat("a", 40)

	tests := []struct {
		name    string
		input   CreateEvidenceInput
		wantErr error
	}{
		{
			name: "非法Type",
			input: CreateEvidenceInput{
				Type:   "unknown",
				Status: model.EvidenceStatusDraft,
			},
			wantErr: ErrInvalidEvidenceType,
		},
		{
			name: "非法Status",
			input: CreateEvidenceInput{
				Type:   model.EvidenceTypeTestOutput,
				Status: "unknown",
			},
			wantErr: ErrInvalidEvidenceStatus,
		},
		{
			name: "提交空内容",
			input: CreateEvidenceInput{
				Type:   model.EvidenceTypeTestOutput,
				Status: model.EvidenceStatusSubmitted,
			},
			wantErr: ErrInvalidEvidenceContent,
		},
		{
			name: "字段与类型不匹配",
			input: CreateEvidenceInput{
				Type:      model.EvidenceTypeTestOutput,
				Text:      &content,
				CommitSHA: &sha,
				Status:    model.EvidenceStatusSubmitted,
			},
			wantErr: ErrInvalidEvidenceContent,
		},
		{
			name: "文本超长",
			input: CreateEvidenceInput{
				Type: model.EvidenceTypeExperimentText,
				Text: func() *string {
					s := strings.Repeat("你", 10001)
					return &s
				}(),
				Status: model.EvidenceStatusSubmitted,
			},
			wantErr: ErrInvalidEvidenceContent,
		},
		{
			name: "SHA含非法字符",
			input: CreateEvidenceInput{
				Type: model.EvidenceTypeCommitSHA,
				CommitSHA: func() *string {
					s := strings.Repeat("a", 39) + "z"
					return &s
				}(),
				Status: model.EvidenceStatusSubmitted,
			},
			wantErr: ErrInvalidEvidenceContent,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			created, err := svc.CreateEvidence(
				context.Background(),
				user.ID,
				task.ID,
				tc.input,
			)

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf(
					"预期错误=%v，实际=%v",
					tc.wantErr,
					err,
				)
			}

			if created != nil {
				t.Fatal("非法输入不应该返回证据")
			}

			if countTaskEvidence(t, db, task.ID) != 0 {
				t.Fatal("非法输入写入了数据库")
			}
		})
	}
}

// ============================
// Test 7: 事务失败必须回滚
// ============================

func TestCreateEvidence_Rollback(t *testing.T) {
	db, user, task := newEvidenceTestFixture(t)
	svc := NewEvidenceService(db)

	injectedErr := errors.New("injected evidence write failure")
	const callbackName = "test:evidence:fail_after_insert"

	insertExecuted := false

	// 故障注入：
	// 在 GORM 执行 task_evidence INSERT 后，
	// 让本次 Create 返回错误。
	//
	// Service 必须因此回滚整个事务。
	err := db.Callback().
		Create().
		After("gorm:create").
		Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Schema == nil {
				return
			}

			if tx.Statement.Schema.Table != "task_evidence" {
				return
			}

			if tx.Error != nil {
				return
			}

			insertExecuted = true
			tx.AddError(injectedErr)
		})

	if err != nil {
		t.Fatalf("注册故障注入失败: %v", err)
	}

	// 测试结束移除 Callback，避免影响其他测试。
	t.Cleanup(func() {
		if err := db.Callback().
			Create().
			Remove(callbackName); err != nil {
			t.Errorf("清理 Callback 失败: %v", err)
		}
	})

	content := "这条证据必须被回滚"

	input := CreateEvidenceInput{
		Type:   model.EvidenceTypeTestOutput,
		Text:   &content,
		Status: model.EvidenceStatusSubmitted,
	}

	created, err := svc.CreateEvidence(
		context.Background(),
		user.ID,
		task.ID,
		input,
	)

	// 确认故障注入真的执行
	if !insertExecuted {
		t.Fatal("故障注入没有执行")
	}

	// 确认 Service 向上传播错误
	if !errors.Is(err, injectedErr) {
		t.Fatalf(
			"预期注入错误，实际: %v",
			err,
		)
	}

	// 事务失败不能返回成功结果
	if created != nil {
		t.Fatal("事务失败不应该返回证据")
	}

	// 最重要的断言：
	// INSERT 虽然执行过，但回滚后不应留下记录。
	if count := countTaskEvidence(t, db, task.ID); count != 0 {
		t.Fatalf(
			"事务未正确回滚，残留%d条证据",
			count,
		)
	}
}
