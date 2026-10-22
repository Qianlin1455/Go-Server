package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"GoServer/internal/model"
	"GoServer/internal/repository"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// newTaskStatusTestFixture 使用真正提交的初始化数据，避免把被测 Service 事务
// 包在测试外层事务中。清理时按外键依赖的相反顺序删除测试数据。
func newTaskStatusTestFixture(t *testing.T) (*gorm.DB, *model.Users, *model.Task) {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Fatal("请设置 TEST_MYSQL_DSN，并指向 go_server_test")
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("连接测试数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	var dbName string
	if err := db.Raw("SELECT DATABASE()").Row().Scan(&dbName); err != nil {
		t.Fatalf("获取当前数据库名称失败: %v", err)
	}
	if dbName != "go_server_test" {
		t.Fatalf("拒绝测试：当前数据库为 %q，必须是 go_server_test", dbName)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	user := &model.Users{
		UserName: "task_tx_" + suffix,
		Email:    "task_tx_" + suffix + "@example.com",
		Password: "test-hash",
	}
	project := &model.Project{
		UserID: 0, // 在创建用户后填充
		Name:   "task_tx_project_" + suffix,
	}
	task := &model.Task{
		Title: "状态变更事务测试",
	}

	// 先提交 fixture，后面的 ChangeStatus 才是在真实独立事务中执行。
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(user).Error; err != nil {
			return err
		}
		project.UserID = user.ID
		if err := tx.Create(project).Error; err != nil {
			return err
		}
		task.UserID = user.ID
		task.ProjectID = project.ID
		return tx.Create(task).Error
	})
	if err != nil {
		t.Fatalf("创建测试数据失败: %v", err)
	}

	t.Cleanup(func() {
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("task_id = ?", task.ID).
				Delete(&model.TaskEvidence{}).Error; err != nil {
				return err
			}
			if err := tx.Where("task_id = ?", task.ID).
				Delete(&model.TaskStatusAudit{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id = ?", task.ID).
				Delete(&model.Task{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id = ?", project.ID).
				Delete(&model.Project{}).Error; err != nil {
				return err
			}
			return tx.Where("id = ?", user.ID).
				Delete(&model.Users{}).Error
		})
		if err != nil {
			t.Errorf("清理测试数据失败: %v", err)
		}
	})

	return db, user, task
}

func loadTaskState(t *testing.T, db *gorm.DB, taskID uint64) model.Task {
	t.Helper()
	var stored model.Task
	if err := db.Where("id = ?", taskID).First(&stored).Error; err != nil {
		t.Fatalf("重新查询 Task 失败: %v", err)
	}
	return stored
}

func loadTaskAudits(t *testing.T, db *gorm.DB, taskID uint64) []model.TaskStatusAudit {
	t.Helper()
	var audits []model.TaskStatusAudit
	if err := db.Where("task_id = ?", taskID).
		Order("to_version ASC").Find(&audits).Error; err != nil {
		t.Fatalf("查询审计记录失败: %v", err)
	}
	return audits
}

func TestChangeStatus_Commit(t *testing.T) {
	db, user, task := newTaskStatusTestFixture(t)
	svc := NewTaskService(db)
	ctx := context.Background()

	// 第一次转换：todo -> doing。
	if err := svc.ChangeStatus(ctx, user.ID, task.ID, model.TaskStatusDoing, 1); err != nil {
		t.Fatalf("todo -> doing 失败: %v", err)
	}
	stored := loadTaskState(t, db, task.ID)
	if stored.Status != model.TaskStatusDoing || stored.Version != 2 {
		t.Fatalf("首次提交后 Task 不正确: status=%s version=%d", stored.Status, stored.Version)
	}
	if stored.CompletedAt != nil {
		t.Fatalf("doing 的 completed_at 应为 NULL，实际=%v", *stored.CompletedAt)
	}
	audits := loadTaskAudits(t, db, task.ID)
	if len(audits) != 1 {
		t.Fatalf("第一次成功变更应有 1 条审计，实际 %d 条", len(audits))
	}
	if audits[0].ActorUserID != user.ID ||
		audits[0].FromStatus != model.TaskStatusTodo ||
		audits[0].ToStatus != model.TaskStatusDoing ||
		audits[0].FromVersion != 1 || audits[0].ToVersion != 2 {
		t.Fatalf("首次审计记录不正确: %+v", audits[0])
	}

	content := "go test -race ./... PASS"

	evidence := &model.TaskEvidence{
		TaskID:    task.ID,
		CreatedBy: user.ID,
		Type:      model.EvidenceTypeTestOutput,
		Text:      &content,
		Status:    model.EvidenceStatusSubmitted,
	}

	if err := db.Create(evidence).Error; err != nil {
		t.Fatalf("准备完成证据失败: %v", err)
	}

	// 第二次转换：doing -> done。
	if err := svc.ChangeStatus(ctx, user.ID, task.ID, model.TaskStatusDone, 2); err != nil {
		t.Fatalf("doing -> done 失败: %v", err)
	}
	stored = loadTaskState(t, db, task.ID)
	if stored.Status != model.TaskStatusDone || stored.Version != 3 || stored.CompletedAt == nil {
		t.Fatalf("第二次提交后 Task 不正确: %+v", stored)
	}
	audits = loadTaskAudits(t, db, task.ID)
	if len(audits) != 2 {
		t.Fatalf("两次成功变更应有 2 条审计，实际 %d 条", len(audits))
	}
	if audits[1].FromStatus != model.TaskStatusDoing ||
		audits[1].ToStatus != model.TaskStatusDone ||
		audits[1].FromVersion != 2 || audits[1].ToVersion != 3 {
		t.Fatalf("第二条审计记录不正确: %+v", audits[1])
	}
}

func TestChangeStatus_RollbackWhenAuditFails(t *testing.T) {
	db, user, task := newTaskStatusTestFixture(t)
	svc := NewTaskService(db)
	ctx := context.Background()

	// 故障注入：提前插入占用 (task_id, to_version=2) 的审计记录。
	// 这是一份故意构造的不一致数据，仅用于使后续 INSERT 必定触发唯一约束。
	seed := &model.TaskStatusAudit{
		TaskID:      task.ID,
		ActorUserID: user.ID,
		FromStatus:  model.TaskStatusTodo,
		ToStatus:    model.TaskStatusDoing,
		FromVersion: 1,
		ToVersion:   2,
	}
	if err := db.Create(seed).Error; err != nil {
		t.Fatalf("注入审计冲突失败: %v", err)
	}

	err := svc.ChangeStatus(ctx, user.ID, task.ID, model.TaskStatusDoing, 1)
	if !errors.Is(err, repository.ErrAlreadyExists) {
		t.Fatalf("期望审计唯一冲突 ErrAlreadyExists，实际: %v", err)
	}

	// CAS UPDATE 在 INSERT 审计之前执行，但必须随着 INSERT 失败而回滚。
	stored := loadTaskState(t, db, task.ID)
	if stored.Status != model.TaskStatusTodo || stored.Version != 1 || stored.CompletedAt != nil {
		t.Fatalf("审计失败后 Task 未回滚: %+v", stored)
	}
	audits := loadTaskAudits(t, db, task.ID)
	if len(audits) != 1 || audits[0].ID != seed.ID {
		t.Fatalf("审计失败后应只保留预置记录: %+v", audits)
	}
}

func TestChangeStatus_StaleVersion(t *testing.T) {
	db, user, task := newTaskStatusTestFixture(t)
	svc := NewTaskService(db)
	ctx := context.Background()

	if err := svc.ChangeStatus(ctx, user.ID, task.ID, model.TaskStatusDoing, 1); err != nil {
		t.Fatalf("首次变更失败: %v", err)
	}

	// 同一请求携带旧版本再次发送，应返回版本冲突，而非视为成功。
	err := svc.ChangeStatus(ctx, user.ID, task.ID, model.TaskStatusDoing, 1)
	if !errors.Is(err, ErrTaskVersionConflict) {
		t.Fatalf("旧版本应返回 ErrTaskVersionConflict，实际: %v", err)
	}

	// 使用当前版本重复设置相同状态，应该是非法转换。
	err = svc.ChangeStatus(ctx, user.ID, task.ID, model.TaskStatusDoing, 2)
	if !errors.Is(err, ErrInvalidTaskTransition) {
		t.Fatalf("同状态更新应返回 ErrInvalidTaskTransition，实际: %v", err)
	}

	stored := loadTaskState(t, db, task.ID)
	if stored.Status != model.TaskStatusDoing || stored.Version != 2 {
		t.Fatalf("重复请求改变了任务状态: %+v", stored)
	}
	if audits := loadTaskAudits(t, db, task.ID); len(audits) != 1 {
		t.Fatalf("重复请求不应新增审计，实际审计数=%d", len(audits))
	}
}

func TestChangeStatus_InvalidTransition(t *testing.T) {
	db, user, task := newTaskStatusTestFixture(t)
	svc := NewTaskService(db)

	err := svc.ChangeStatus(context.Background(), user.ID, task.ID, model.TaskStatusDone, 1)
	if !errors.Is(err, ErrInvalidTaskTransition) {
		t.Fatalf("todo -> done 应被拒绝，实际: %v", err)
	}
	stored := loadTaskState(t, db, task.ID)
	if stored.Status != model.TaskStatusTodo || stored.Version != 1 || stored.CompletedAt != nil {
		t.Fatalf("非法转换修改了 Task: %+v", stored)
	}
	if audits := loadTaskAudits(t, db, task.ID); len(audits) != 0 {
		t.Fatalf("非法转换不应产生审计，实际审计数=%d", len(audits))
	}
}

func TestChangeStatus_ConcurrentCAS(t *testing.T) {
	db, user, task := newTaskStatusTestFixture(t)
	svc := NewTaskService(db)

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取数据库连接池失败: %v", err)
	}

	// 允许两个事务使用不同的数据库连接
	sqlDB.SetMaxOpenConns(4)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		15*time.Second,
	)
	defer cancel()

	const competitors = 2

	start := make(chan struct{})
	ready := make(chan struct{}, competitors)
	results := make(chan error, competitors)

	var wg sync.WaitGroup

	// 1. 创建两个并发请求
	for i := 0; i < competitors; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			// 通知主 goroutine 已经就绪
			ready <- struct{}{}

			// 等待统一开始信号
			<-start

			err := svc.ChangeStatus(
				ctx,
				user.ID,
				task.ID,
				model.TaskStatusDoing,
				1,
			)

			results <- err
		}()
	}

	// 2. 等待所有请求就绪
	for i := 0; i < competitors; i++ {
		<-ready
	}

	// 3. 同时放行
	close(start)

	wg.Wait()
	close(results)

	// 4. 统计执行结果
	successCount := 0
	conflictCount := 0

	for err := range results {
		switch {
		case err == nil:
			successCount++

		case errors.Is(err, ErrTaskVersionConflict):
			conflictCount++

		default:
			t.Errorf("出现非预期错误: %v", err)
		}
	}

	if successCount != 1 || conflictCount != 1 {
		t.Fatalf(
			"期望成功1次、版本冲突1次，实际 success=%d, conflict=%d",
			successCount,
			conflictCount,
		)
	}

	// 5. 验证数据库最终状态
	stored := loadTaskState(t, db, task.ID)

	if stored.Status != model.TaskStatusDoing {
		t.Errorf(
			"Status: want=doing, got=%s",
			stored.Status,
		)
	}

	if stored.Version != 2 {
		t.Errorf(
			"Version: want=2, got=%d",
			stored.Version,
		)
	}

	if stored.CompletedAt != nil {
		t.Errorf("doing 状态下 completed_at 应为 NULL")
	}

	// 6. 验证审计记录
	audits := loadTaskAudits(t, db, task.ID)

	if len(audits) != 1 {
		t.Fatalf(
			"预期恰好1条审计记录，实际=%d",
			len(audits),
		)
	}

	audit := audits[0]

	if audit.TaskID != task.ID ||
		audit.ActorUserID != user.ID ||
		audit.FromStatus != model.TaskStatusTodo ||
		audit.ToStatus != model.TaskStatusDoing ||
		audit.FromVersion != 1 ||
		audit.ToVersion != 2 {

		t.Fatalf("审计记录不正确: %+v", audit)
	}

	t.Logf(
		"并发验证成功: success=%d, conflict=%d, status=%s, version=%d, audits=%d",
		successCount,
		conflictCount,
		stored.Status,
		stored.Version,
		len(audits),
	)
}

func TestChangeStatus_EvidenceRequired(t *testing.T) {
	db, user, task := newTaskStatusTestFixture(t)
	svc := NewTaskService(db)
	ctx := context.Background()

	// 1. todo -> doing
	err := svc.ChangeStatus(
		ctx, user.ID, task.ID,
		model.TaskStatusDoing, 1,
	)
	if err != nil {
		t.Fatalf("todo -> doing 失败: %v", err)
	}

	// 2. 没有证据，不能完成
	err = svc.ChangeStatus(
		ctx, user.ID, task.ID,
		model.TaskStatusDone, 2,
	)

	if !errors.Is(err, ErrTaskEvidenceRequired) {
		t.Fatalf("无证据应拒绝完成，实际: %v", err)
	}

	// 3. 创建一条空草稿
	draft := &model.TaskEvidence{
		TaskID:    task.ID,
		CreatedBy: user.ID,
		Type:      model.EvidenceTypeTestOutput,
		Status:    model.EvidenceStatusDraft,
	}

	if err := db.Create(draft).Error; err != nil {
		t.Fatalf("创建草稿失败: %v", err)
	}

	// 4. 只有草稿，仍不能完成
	err = svc.ChangeStatus(
		ctx, user.ID, task.ID,
		model.TaskStatusDone, 2,
	)

	if !errors.Is(err, ErrTaskEvidenceRequired) {
		t.Fatalf("只有草稿应拒绝完成，实际: %v", err)
	}

	// 5. 检查失败请求没有修改状态
	stored := loadTaskState(t, db, task.ID)

	if stored.Status != model.TaskStatusDoing ||
		stored.Version != 2 ||
		stored.CompletedAt != nil {
		t.Fatalf("拒绝完成后状态异常: %+v", stored)
	}

	if audits := loadTaskAudits(t, db, task.ID); len(audits) != 1 {
		t.Fatalf("拒绝完成不应新增审计，实际: %d", len(audits))
	}

	// 6. 创建正式提交的证据
	content := "go test -race ./... PASS"

	submitted := &model.TaskEvidence{
		TaskID:    task.ID,
		CreatedBy: user.ID,
		Type:      model.EvidenceTypeTestOutput,
		Text:      &content,
		Status:    model.EvidenceStatusSubmitted,
	}

	if err := db.Create(submitted).Error; err != nil {
		t.Fatalf("创建正式证据失败: %v", err)
	}

	// 7. 现在允许完成
	err = svc.ChangeStatus(
		ctx, user.ID, task.ID,
		model.TaskStatusDone, 2,
	)
	if err != nil {
		t.Fatalf("有正式证据却无法完成: %v", err)
	}

	// 8. 验证最终状态与审计
	stored = loadTaskState(t, db, task.ID)

	if stored.Status != model.TaskStatusDone ||
		stored.Version != 3 ||
		stored.CompletedAt == nil {
		t.Fatalf("任务完成状态异常: %+v", stored)
	}

	if audits := loadTaskAudits(t, db, task.ID); len(audits) != 2 {
		t.Fatalf("预期2条状态审计，实际: %d", len(audits))
	}
}

func TestChangeStatus_DoneRollbackWhenAuditFails(t *testing.T) {
	db, user, task := newTaskStatusTestFixture(t)
	svc := NewTaskService(db)
	ctx := context.Background()

	// 1. 先进入 doing
	if err := svc.ChangeStatus(
		ctx, user.ID, task.ID,
		model.TaskStatusDoing, 1,
	); err != nil {
		t.Fatal(err)
	}

	// 2. 准备正式证据
	content := "并发测试通过"

	evidence := &model.TaskEvidence{
		TaskID:    task.ID,
		CreatedBy: user.ID,
		Type:      model.EvidenceTypeTestOutput,
		Text:      &content,
		Status:    model.EvidenceStatusSubmitted,
	}

	if err := db.Create(evidence).Error; err != nil {
		t.Fatal(err)
	}

	// 3. 提前占用审计版本 3
	seed := &model.TaskStatusAudit{
		TaskID:      task.ID,
		ActorUserID: user.ID,
		FromStatus:  model.TaskStatusDoing,
		ToStatus:    model.TaskStatusDone,
		FromVersion: 2,
		ToVersion:   3,
	}

	if err := db.Create(seed).Error; err != nil {
		t.Fatal(err)
	}

	// 4. 尝试完成任务，审计 INSERT 应触发唯一冲突
	err := svc.ChangeStatus(
		ctx, user.ID, task.ID,
		model.TaskStatusDone, 2,
	)

	if !errors.Is(err, repository.ErrAlreadyExists) {
		t.Fatalf("预期审计唯一冲突，实际: %v", err)
	}

	// 5. 确认 CAS UPDATE 被回滚
	stored := loadTaskState(t, db, task.ID)

	if stored.Status != model.TaskStatusDoing ||
		stored.Version != 2 ||
		stored.CompletedAt != nil {
		t.Fatalf("审计失败后任务没有正确回滚: %+v", stored)
	}

	// 6. 只存在原本的 doing 审计和预置审计
	audits := loadTaskAudits(t, db, task.ID)

	if len(audits) != 2 {
		t.Fatalf("预期保留2条原有审计，实际: %d", len(audits))
	}
}
