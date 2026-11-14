package repository

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"GoServer/internal/model"

	"gorm.io/gorm"
)

// 创建一个已成功领取的 running 任务。
// 复用项目中的测试事务，结束后自动回滚。
func prepareRecoveryJob(
	t *testing.T,
	maxAttempts uint16,
	expired bool,
) (*gorm.DB, *ImportJobStore, *model.ImportJob, []byte) {
	t.Helper()

	ctx := context.Background()
	tx := setupTestTx(t)

	// 1. 创建测试用户、项目
	user, project := setupGitHubTestProject(
		t, ctx, tx, "recover",
	)

	// 2. 创建 GitHub 仓库绑定
	repo := &model.Repository{
		ProjectID:     project.ID,
		Owner:         "golang",
		Repo:          "go",
		DefaultBranch: "master",
		LastSHA:       "0123456789abcdef0123456789abcdef01234567",
	}

	if err := NewGitHubRepositoryStore(tx).
		Create(ctx, repo); err != nil {
		t.Fatal(err)
	}

	// 3. 创建 queued 任务
	job := &model.ImportJob{
		UserID:       user.ID,
		ProjectID:    project.ID,
		RepositoryID: repo.ID,
		Endpoint:     "POST /api/v1/projects/:id/imports",
		IdempotencyKey: fmt.Sprintf(
			"recover-%s", testSuffix(),
		),
		RequestFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RevisionSHA:        repo.LastSHA,
		Status:             model.ImportJobQueued,
		MaxAttempts:        maxAttempts,
	}

	store := NewImportJobStore(tx)

	if err := store.Create(ctx, job); err != nil {
		t.Fatal(err)
	}

	// 4. 模拟 Worker 成功领取任务
	token, claimed, err := store.ClaimQueuedJob(
		ctx,
		job.ID,
		"test-worker",
		30*time.Second,
	)

	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("初始任务领取失败")
	}

	// 5. 按需模拟 Worker 崩溃后的租约过期
	if expired {
		err := tx.Model(&model.ImportJob{}).
			Where("id = ?", job.ID).
			Update(
				"lease_expires_at",
				gorm.Expr(
					"DATE_SUB(NOW(3), INTERVAL 1 SECOND)",
				),
			).Error

		if err != nil {
			t.Fatal(err)
		}
	}

	return tx, store, job, token
}

// 查询任务最新状态
func loadRecoveryJob(
	t *testing.T,
	tx *gorm.DB,
	id uint64,
) model.ImportJob {
	t.Helper()

	var job model.ImportJob
	if err := tx.First(&job, id).Error; err != nil {
		t.Fatal(err)
	}
	return job
}

// 验证租约字段已经清空
func assertLeaseCleared(t *testing.T, job model.ImportJob) {
	t.Helper()

	if job.LeaseOwner != nil ||
		job.LeaseExpiresAt != nil ||
		len(job.LeaseToken) != 0 {
		t.Fatalf("租约未清空: %+v", job)
	}
}

// 测试一：租约过期，仍然可以重试
func TestRecoverExpiredJobs_Requeue(t *testing.T) {
	tx, store, job, _ := prepareRecoveryJob(
		t, 3, true,
	)

	requeued, failed, err := store.RecoverExpiredJobs(
		context.Background(),
	)
	if err != nil {
		t.Fatal(err)
	}

	got := loadRecoveryJob(t, tx, job.ID)

	if got.Status != model.ImportJobQueued {
		t.Fatalf("status=%s, want queued", got.Status)
	}

	if got.AttemptCount != 1 {
		t.Fatalf(
			"attempt_count=%d, want 1",
			got.AttemptCount,
		)
	}

	if got.FinishedAt != nil {
		t.Fatal("queued 任务不应该有 finished_at")
	}

	assertLeaseCleared(t, got)

	if requeued < 1 {
		t.Fatal("至少应该回收 1 个任务")
	}

	t.Logf(
		"PASS: status=%s, attempts=%d, requeued=%d, failed=%d",
		got.Status, got.AttemptCount, requeued, failed,
	)

	// 回收后应当可以再次领取
	_, claimed, err := store.ClaimQueuedJob(
		context.Background(),
		job.ID,
		"worker-B",
		30*time.Second,
	)
	if err != nil || !claimed {
		t.Fatalf("回收后重新领取失败: %v", err)
	}

	got = loadRecoveryJob(t, tx, job.ID)
	if got.AttemptCount != 2 {
		t.Fatalf(
			"重新领取后 attempts=%d, want 2",
			got.AttemptCount,
		)
	}
}

// 测试二：最大尝试次数耗尽
func TestRecoverExpiredJobs_MaxAttempts(t *testing.T) {
	tx, store, job, _ := prepareRecoveryJob(
		t, 1, true,
	)

	_, failed, err := store.RecoverExpiredJobs(
		context.Background(),
	)
	if err != nil {
		t.Fatal(err)
	}

	got := loadRecoveryJob(t, tx, job.ID)

	if got.Status != model.ImportJobFailed {
		t.Fatalf("status=%s, want failed", got.Status)
	}

	if got.AttemptCount != 1 {
		t.Fatalf(
			"attempt_count=%d, want 1",
			got.AttemptCount,
		)
	}

	if got.FinishedAt == nil {
		t.Fatal("failed 任务必须有 finished_at")
	}

	if got.LastErrorCode == nil ||
		*got.LastErrorCode != "MAX_ATTEMPTS_EXCEEDED" {
		t.Fatalf(
			"错误码不正确: %v",
			got.LastErrorCode,
		)
	}

	assertLeaseCleared(t, got)

	if failed < 1 {
		t.Fatal("至少应该标记 1 个失败任务")
	}

	t.Logf(
		"PASS: status=%s, attempts=%d, failed=%d",
		got.Status, got.AttemptCount, failed,
	)
}

// 测试三：租约尚未过期，不应回收
func TestRecoverExpiredJobs_ActiveLease(t *testing.T) {
	tx, store, job, token := prepareRecoveryJob(
		t, 3, false,
	)

	_, _, err := store.RecoverExpiredJobs(
		context.Background(),
	)
	if err != nil {
		t.Fatal(err)
	}

	got := loadRecoveryJob(t, tx, job.ID)

	if got.Status != model.ImportJobRunning {
		t.Fatalf("status=%s, want running", got.Status)
	}

	if got.AttemptCount != 1 {
		t.Fatalf(
			"attempt_count=%d, want 1",
			got.AttemptCount,
		)
	}

	if got.LeaseOwner == nil ||
		got.LeaseExpiresAt == nil {
		t.Fatal("有效租约不应该被清除")
	}

	if !bytes.Equal(got.LeaseToken, token) {
		t.Fatal("有效 Lease Token 不应该改变")
	}

	if got.FinishedAt != nil {
		t.Fatal("running 任务不应该有 finished_at")
	}

	t.Log("PASS: 有效租约未被回收")
}

func TestCompleteJob_Success(t *testing.T) {
	tx, store, job, token := prepareRecoveryJob(
		t, 3, false,
	)

	completed, err := store.CompleteJob(
		context.Background(),
		job.ID,
		token,
	)

	if err != nil {
		t.Fatal(err)
	}

	if !completed {
		t.Fatal("持有有效 Token 的 Worker 应该完成成功")
	}

	got := loadRecoveryJob(t, tx, job.ID)

	if got.Status != model.ImportJobSucceeded {
		t.Fatalf(
			"status=%s, want succeeded",
			got.Status,
		)
	}

	if got.FinishedAt == nil {
		t.Fatal("完成任务必须记录 finished_at")
	}

	if got.AttemptCount != 1 {
		t.Fatalf(
			"attempt_count=%d, want 1",
			got.AttemptCount,
		)
	}

	assertLeaseCleared(t, got)

	// 重复完成不能再次成功
	completed, err = store.CompleteJob(
		context.Background(),
		job.ID,
		token,
	)

	if err != nil || completed {
		t.Fatalf(
			"重复完成应被拒绝: completed=%v err=%v",
			completed, err,
		)
	}

	t.Log("PASS: 有效 Token 完成成功，重复完成被拒绝")
}

func TestCompleteJob_StaleToken(t *testing.T) {
	ctx := context.Background()

	tx, store, job, oldToken := prepareRecoveryJob(
		t, 3, true,
	)

	// 1. Worker A 租约已经过期
	_, _, err := store.RecoverExpiredJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Worker B 重新领取任务
	newToken, claimed, err := store.ClaimQueuedJob(
		ctx,
		job.ID,
		"worker-B",
		30*time.Second,
	)

	if err != nil || !claimed {
		t.Fatalf("Worker B 领取失败: %v", err)
	}

	// 3. Worker A 使用旧 Token 尝试完成
	completed, err := store.CompleteJob(
		ctx,
		job.ID,
		oldToken,
	)

	if err != nil {
		t.Fatal(err)
	}

	if completed {
		t.Fatal("旧 Worker 不应该完成任务")
	}

	// 4. Worker B 使用新 Token 应当成功
	completed, err = store.CompleteJob(
		ctx,
		job.ID,
		newToken,
	)

	if err != nil || !completed {
		t.Fatalf("Worker B 完成失败: %v", err)
	}

	got := loadRecoveryJob(t, tx, job.ID)

	if got.Status != model.ImportJobSucceeded {
		t.Fatalf("status=%s", got.Status)
	}

	if got.AttemptCount != 2 {
		t.Fatalf(
			"attempt_count=%d, want 2",
			got.AttemptCount,
		)
	}

	t.Log("PASS: 旧 Token 被拒绝，新 Worker 完成成功")
}

// 测试一：还有执行次数，重新进入 queued
func TestFailJob_Requeue(t *testing.T) {
	ctx := context.Background()

	tx, store, job, token := prepareRecoveryJob(
		t, 3, false,
	)

	updated, err := store.FailJob(
		ctx, job.ID, token, "GITHUB_TIMEOUT", 5*time.Second, time.Time{},
	)
	if err != nil || !updated {
		t.Fatalf(
			"FailJob failed: updated=%v err=%v",
			updated, err,
		)
	}

	got := loadRecoveryJob(t, tx, job.ID)

	if got.Status != model.ImportJobQueued {
		t.Fatalf("status=%s, want queued", got.Status)
	}

	if got.AttemptCount != 1 {
		t.Fatalf(
			"attempt_count=%d, want 1",
			got.AttemptCount,
		)
	}

	assertLeaseCleared(t, got)

	if got.FinishedAt != nil {
		t.Fatal("queued 不应该有 finished_at")
	}

	if got.LastErrorCode == nil ||
		*got.LastErrorCode != "GITHUB_TIMEOUT" {
		t.Fatal("错误码不正确")
	}

	// 5 秒延迟期间不能再次领取
	_, claimed, err := store.ClaimQueuedJob(
		ctx, job.ID, "worker-B", 30*time.Second,
	)
	if err != nil || claimed {
		t.Fatalf(
			"延迟期间不应领取: claimed=%v err=%v",
			claimed, err,
		)
	}

	t.Log("PASS: 失败后重新排队，尝试次数不变")
}

// 测试二：达到最大次数后彻底失败
func TestFailJob_MaxAttempts(t *testing.T) {
	ctx := context.Background()

	tx, store, job, token := prepareRecoveryJob(
		t, 1, false,
	)

	updated, err := store.FailJob(
		ctx, job.ID, token, "GITHUB_TIMEOUT", 5*time.Second, time.Time{},
	)
	if err != nil || !updated {
		t.Fatalf(
			"FailJob failed: updated=%v err=%v",
			updated, err,
		)
	}

	got := loadRecoveryJob(t, tx, job.ID)

	if got.Status != model.ImportJobFailed {
		t.Fatalf("status=%s, want failed", got.Status)
	}

	if got.FinishedAt == nil {
		t.Fatal("failed 必须记录 finished_at")
	}

	if got.AttemptCount != 1 {
		t.Fatal("attempt_count 不应该增加")
	}
	if got.LastErrorCode == nil || *got.LastErrorCode != "GITHUB_TIMEOUT" {
		t.Fatal("最终失败应保留业务错误码")
	}

	assertLeaseCleared(t, got)

	t.Log("PASS: 重试次数耗尽，任务进入 failed")
}

// 测试三：过期 Token 不能更新任务
func TestFailJob_ExpiredLease(t *testing.T) {
	ctx := context.Background()

	tx, store, job, token := prepareRecoveryJob(
		t, 3, true,
	)

	updated, err := store.FailJob(
		ctx, job.ID, token, "GITHUB_TIMEOUT", 5*time.Second, time.Time{},
	)

	if err != nil {
		t.Fatal(err)
	}

	if updated {
		t.Fatal("过期 Lease 不允许更新任务")
	}

	got := loadRecoveryJob(t, tx, job.ID)

	if got.Status != model.ImportJobRunning {
		t.Fatalf(
			"status=%s, want running",
			got.Status,
		)
	}

	if got.AttemptCount != 1 {
		t.Fatal("attempt_count 不应该改变")
	}

	t.Log("PASS: 过期 Lease 被拒绝")
}

func TestFailJob_StaleAndRepeatedUpdates(t *testing.T) {
	ctx := context.Background()
	tx, store, job, oldToken := prepareRecoveryJob(t, 3, true)
	if _, _, err := store.RecoverExpiredJobs(ctx); err != nil {
		t.Fatal(err)
	}
	newToken, claimed, err := store.ClaimQueuedJob(ctx, job.ID, "worker-B", 30*time.Second)
	if err != nil || !claimed {
		t.Fatalf("reclaim: claimed=%v, err=%v", claimed, err)
	}

	updated, err := store.FailJob(ctx, job.ID, oldToken, "STALE_FAILURE", time.Minute, time.Time{})
	if err != nil || updated {
		t.Fatalf("stale failure: updated=%v, err=%v", updated, err)
	}
	running := loadRecoveryJob(t, tx, job.ID)
	if running.Status != model.ImportJobRunning || running.AttemptCount != 2 ||
		!bytes.Equal(running.LeaseToken, newToken) || running.LastErrorCode != nil {
		t.Fatalf("stale update changed running job: %+v", running)
	}

	updated, err = store.FailJob(ctx, job.ID, newToken, "GITHUB_TIMEOUT", 10*time.Second, time.Time{})
	if err != nil || !updated {
		t.Fatalf("valid failure: updated=%v, err=%v", updated, err)
	}
	queued := loadRecoveryJob(t, tx, job.ID)
	updated, err = store.FailJob(ctx, job.ID, newToken, "DUPLICATE_FAILURE", time.Minute, time.Time{})
	if err != nil || updated {
		t.Fatalf("duplicate failure: updated=%v, err=%v", updated, err)
	}
	got := loadRecoveryJob(t, tx, job.ID)
	if got.Status != model.ImportJobQueued || got.AttemptCount != 2 || got.FinishedAt != nil ||
		got.LastErrorCode == nil || *got.LastErrorCode != "GITHUB_TIMEOUT" ||
		!got.AvailableAt.Equal(queued.AvailableAt) || !got.UpdatedAt.Equal(queued.UpdatedAt) {
		t.Fatalf("duplicate update changed queued job: %+v", got)
	}
	assertLeaseCleared(t, got)
}
