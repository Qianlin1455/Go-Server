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

func TestTwoWorkers_ClaimAndCrashRecovery(t *testing.T) {
	ctx := context.Background()

	// 三个独立数据库连接池
	// admin 负责准备和清理测试数据
	adminDB := setupTestDB(t)
	dbA := setupTestDB(t)
	dbB := setupTestDB(t)

	// 创建测试用户、项目
	user, project := setupGitHubTestProject(
		t, ctx, adminDB, "worker_race",
	)

	repo := &model.Repository{
		ProjectID:     project.ID,
		Owner:         "golang",
		Repo:          "go",
		DefaultBranch: "master",
		LastSHA:       "0123456789abcdef0123456789abcdef01234567",
	}

	if err := NewGitHubRepositoryStore(adminDB).
		Create(ctx, repo); err != nil {
		t.Fatal(err)
	}

	job := &model.ImportJob{
		UserID:       user.ID,
		ProjectID:    project.ID,
		RepositoryID: repo.ID,
		Endpoint:     "POST /api/v1/projects/:id/imports",
		IdempotencyKey: fmt.Sprintf(
			"worker-race-%s", testSuffix(),
		),
		RequestFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RevisionSHA:        repo.LastSHA,
		Status:             model.ImportJobQueued,
		MaxAttempts:        3,
	}

	if err := NewImportJobStore(adminDB).
		Create(ctx, job); err != nil {
		t.Fatal(err)
	}

	// 测试结束自动清理
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(), 10*time.Second,
		)
		defer cancel()

		for _, item := range []struct {
			table string
			id    uint64
		}{
			{"import_jobs", job.ID},
			{"repositories", repo.ID},
			{"projects", project.ID},
			{"users", user.ID},
		} {
			err := adminDB.WithContext(cleanupCtx).
				Table(item.table).
				Where("id = ?", item.id).
				Delete(nil).Error
			if err != nil {
				t.Errorf("cleanup %s: %v", item.table, err)
			}
		}
	})

	type testWorker struct {
		name  string
		db    *gorm.DB
		store *ImportJobStore
	}

	workerA := testWorker{
		name:  "worker-A",
		db:    dbA,
		store: NewImportJobStore(dbA),
	}
	workerB := testWorker{
		name:  "worker-B",
		db:    dbB,
		store: NewImportJobStore(dbB),
	}

	type claimResult struct {
		worker  testWorker
		token   []byte
		claimed bool
		err     error
	}

	// 阶段 1：两个 Worker 同时竞争
	start := make(chan struct{})
	results := make(chan claimResult, 2)

	for _, w := range []testWorker{workerA, workerB} {
		go func(w testWorker) {
			<-start

			claimCtx, cancel := context.WithTimeout(
				ctx, 10*time.Second,
			)
			defer cancel()

			token, claimed, err := w.store.ClaimQueuedJob(
				claimCtx,
				job.ID,
				w.name,
				30*time.Second,
			)

			results <- claimResult{
				worker:  w,
				token:   token,
				claimed: claimed,
				err:     err,
			}
		}(w)
	}

	close(start)

	a := <-results
	b := <-results

	if a.err != nil || b.err != nil {
		t.Fatalf("claim errors: A=%v B=%v", a.err, b.err)
	}

	// 必须恰好一个成功
	if a.claimed == b.claimed {
		t.Fatalf(
			"expected exactly one winner: A=%v B=%v",
			a.claimed, b.claimed,
		)
	}

	winner := a
	survivor := b.worker

	if b.claimed {
		winner = b
		survivor = a.worker
	}

	if len(winner.token) != 16 {
		t.Fatal("winner lease token length != 16")
	}

	t.Logf(
		"PASS: winner=%s, only one worker claimed",
		winner.worker.name,
	)

	var got model.ImportJob
	if err := adminDB.First(&got, job.ID).Error; err != nil {
		t.Fatal(err)
	}

	if got.Status != model.ImportJobRunning ||
		got.AttemptCount != 1 {
		t.Fatalf(
			"unexpected state: status=%s attempts=%d",
			got.Status, got.AttemptCount,
		)
	}

	// 阶段 2：模拟获胜 Worker 崩溃
	winnerSQLDB, err := winner.worker.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := winnerSQLDB.Close(); err != nil {
		t.Fatal(err)
	}

	// 租约还有效时，其他 Worker 不能直接领取
	_, claimed, err := survivor.store.ClaimQueuedJob(
		ctx, job.ID, survivor.name, 30*time.Second,
	)
	if err != nil || claimed {
		t.Fatalf(
			"active lease should block claim: %v, %v",
			claimed, err,
		)
	}

	// 不实际等待 30 秒，直接模拟 Lease 过期
	if err := adminDB.Model(&model.ImportJob{}).
		Where("id = ?", job.ID).
		Update(
			"lease_expires_at",
			gorm.Expr(
				"DATE_SUB(NOW(3), INTERVAL 1 SECOND)",
			),
		).Error; err != nil {
		t.Fatal(err)
	}

	// 由存活 Worker 回收任务
	_, _, err = survivor.store.RecoverExpiredJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}

	got = model.ImportJob{}
	if err := adminDB.First(&got, job.ID).Error; err != nil {
		t.Fatal(err)
	}

	if got.Status != model.ImportJobQueued ||
		got.AttemptCount != 1 {
		t.Fatalf(
			"recovery failed: status=%s attempts=%d",
			got.Status, got.AttemptCount,
		)
	}

	t.Log("PASS: expired job recovered to queued")

	var state struct {
		Status        string
		AttemptCount  uint16
		MaxAttempts   uint16
		AvailableAt   time.Time
		DBNow         time.Time `gorm:"column:db_now"`
		TimeReady     int       `gorm:"column:time_ready"`
		AttemptsReady int       `gorm:"column:attempts_ready"`
	}

	err = adminDB.Raw(`
		SELECT
			status,
			attempt_count,
			max_attempts,
			available_at,
			NOW(3) AS db_now,
			(available_at <= NOW(3)) AS time_ready,
			(attempt_count < max_attempts) AS attempts_ready
		FROM import_jobs
		WHERE id = ?
	`, job.ID).Scan(&state).Error

	if err != nil {
		t.Fatal(err)
	}

	t.Logf(
		"DEBUG: status=%s attempts=%d/%d available_at=%v db_now=%v time_ready=%d attempts_ready=%d",
		state.Status,
		state.AttemptCount,
		state.MaxAttempts,
		state.AvailableAt,
		state.DBNow,
		state.TimeReady,
		state.AttemptsReady,
	)

	// 阶段 3：存活 Worker 重新领取
	// 只允许领取一次
	debugStore := NewImportJobStore(survivor.db.Debug())

	newToken, claimed, err := debugStore.ClaimQueuedJob(
		ctx,
		job.ID,
		survivor.name,
		30*time.Second,
	)

	if err != nil {
		t.Fatalf("reclaim returned error: %v", err)
	}

	if !claimed {
		t.Fatal("reclaim returned claimed=false")
	}

	if len(newToken) != 16 {
		t.Fatalf("new token length=%d, want 16", len(newToken))
	}

	if bytes.Equal(winner.token, newToken) {
		t.Fatal("new claim must have a different token")
	}

	t.Logf(
		"PASS: survivor=%s reclaimed job with new token",
		survivor.name,
	)

	result := model.ImportJobResult{
		TreeSHA:    "abcdef0123456789abcdef0123456789abcdef01",
		FileCount:  10,
		TotalBytes: 1024,
	}

	// 阶段 4：旧 Token 不能提交结果
	completed, err := survivor.store.CompleteJobWithResult(
		ctx,
		job.ID,
		winner.token,
		result,
	)

	if err != nil {
		t.Fatalf("stale token completion error: %v", err)
	}

	if completed {
		t.Fatal("stale token must not complete job")
	}

	// 验证旧 Token 没有污染任务
	got = model.ImportJob{}

	if err := adminDB.First(&got, job.ID).Error; err != nil {
		t.Fatal(err)
	}

	if got.Status != model.ImportJobRunning {
		t.Fatalf("status=%s, want running", got.Status)
	}

	if got.ResultTreeSHA != nil ||
		got.ResultFileCount != nil ||
		got.ResultTotalBytes != nil {
		t.Fatal("stale token modified job result")
	}

	if !bytes.Equal(got.LeaseToken, newToken) {
		t.Fatal("new lease token should remain unchanged")
	}

	t.Log("PASS: stale token rejected without side effects")

	// 阶段 5：新 Token 提交业务结果
	completed, err = survivor.store.CompleteJobWithResult(
		ctx,
		job.ID,
		newToken,
		result,
	)

	if err != nil {
		t.Fatalf("new worker completion error: %v", err)
	}

	if !completed {
		t.Fatal("new worker should complete job successfully")
	}

	// 阶段 6：验证最终数据库状态
	got = model.ImportJob{}

	if err := adminDB.First(&got, job.ID).Error; err != nil {
		t.Fatal(err)
	}

	if got.Status != model.ImportJobSucceeded {
		t.Fatalf(
			"status=%s, want succeeded",
			got.Status,
		)
	}

	if got.AttemptCount != 2 {
		t.Fatalf(
			"attempt_count=%d, want 2",
			got.AttemptCount,
		)
	}

	if got.ResultTreeSHA == nil ||
		*got.ResultTreeSHA != result.TreeSHA {
		t.Fatal("result tree SHA not persisted")
	}

	if got.ResultFileCount == nil ||
		*got.ResultFileCount != result.FileCount {
		t.Fatal("result file count not persisted")
	}

	if got.ResultTotalBytes == nil ||
		*got.ResultTotalBytes != result.TotalBytes {
		t.Fatal("result total bytes not persisted")
	}

	assertLeaseCleared(t, got)

	t.Log("PASS: new worker persisted result successfully")
	t.Log("PASS: completed job has correct result and cleared lease")
}
