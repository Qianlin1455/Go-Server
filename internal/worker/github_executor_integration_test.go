package worker

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"GoServer/internal/githubclient"
	"GoServer/internal/model"
	"GoServer/internal/repository"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// targetJobStore 用于隔离当前集成测试的任务范围。
// 其他数据库操作仍然使用真实 ImportJobStore。
type targetJobStore struct {
	JobStore
	jobID uint64
}

// 本测试不测试过期任务回收。
// 避免修改测试数据库中其他任务。
func (s targetJobStore) RecoverExpiredJobs(
	ctx context.Context,
) (int64, int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	return 0, 0, nil
}

// 只扫描当前测试创建的 Job。
func (s targetJobStore) ListReadyQueuedJobIDs(
	ctx context.Context,
	limit int,
) ([]uint64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if limit <= 0 {
		return nil, nil
	}

	return []uint64{s.jobID}, nil
}

// 创建独立 MySQL 事务，测试结束自动回滚。
func setupWorkerIntegrationTx(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Fatal("TEST_MYSQL_DSN 未设置")
	}

	db, err := gorm.Open(
		mysql.Open(dsn),
		&gorm.Config{TranslateError: true},
	)
	if err != nil {
		t.Fatalf("连接 MySQL 失败: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	// 防止误操作开发数据库。
	var dbName string
	if err := db.Raw("SELECT DATABASE()").
		Row().Scan(&dbName); err != nil {
		t.Fatal(err)
	}

	if dbName != "go_server_test" {
		t.Fatalf("拒绝测试，当前数据库: %s", dbName)
	}

	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}

	t.Cleanup(func() {
		_ = tx.Rollback().Error
	})

	return tx
}

// 创建本次集成测试所需的用户、项目、仓库和任务。
func createWorkerIntegrationJob(
	t *testing.T,
	tx *gorm.DB,
) (*model.ImportJob, string) {
	t.Helper()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	// 任务固定 Revision。
	revisionSHA := strings.Repeat("1", 40)

	// 仓库最新 SHA 故意与任务 Revision 不同。
	latestSHA := strings.Repeat("2", 40)

	user := model.Users{
		UserName: "worker_it_" + suffix,
		Email:    "worker_it_" + suffix + "@example.com",
		Password: "test-hash",
	}

	if err := tx.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	project := model.Project{
		UserID: user.ID,
		Name:   "worker_it_" + suffix,
	}

	if err := tx.Create(&project).Error; err != nil {
		t.Fatal(err)
	}

	repo := model.Repository{
		ProjectID:     project.ID,
		Owner:         "golang",
		Repo:          "go",
		DefaultBranch: "master",
		LastSHA:       latestSHA,
	}

	if err := tx.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}

	job := &model.ImportJob{
		UserID:             user.ID,
		ProjectID:          project.ID,
		RepositoryID:       repo.ID,
		Endpoint:           "POST /api/v1/projects/:id/imports",
		IdempotencyKey:     "worker-it-" + suffix,
		RequestFingerprint: strings.Repeat("a", 64),
		RevisionSHA:        revisionSHA,
		Status:             model.ImportJobQueued,
		MaxAttempts:        3,
	}

	if err := tx.Create(job).Error; err != nil {
		t.Fatal(err)
	}

	return job, revisionSHA
}

// 模拟 GitHub API。
type integrationRevisionInspector struct {
	summary githubclient.RevisionSummary

	owner    string
	repo     string
	revision string
	calls    int
}

func (f *integrationRevisionInspector) InspectRevision(
	ctx context.Context,
	owner, repo, sha string,
) (githubclient.RevisionSummary, error) {
	if err := ctx.Err(); err != nil {
		return githubclient.RevisionSummary{}, err
	}

	f.calls++
	f.owner = owner
	f.repo = repo
	f.revision = sha

	return f.summary, nil
}

// 测试真实 Runner + GitHubExecutor + MySQL 完整链路。
func TestRunner_GitHubExecutor_MySQLIntegration(
	t *testing.T,
) {
	tx := setupWorkerIntegrationTx(t)

	job, revisionSHA := createWorkerIntegrationJob(t, tx)

	treeSHA := strings.Repeat("A", 40)

	inspector := &integrationRevisionInspector{
		summary: githubclient.RevisionSummary{
			CommitSHA:  revisionSHA,
			TreeSHA:    treeSHA,
			FileCount:  10,
			TotalBytes: 1024,
		},
	}

	// 1. 创建真实 MySQL Store。
	baseStore := repository.NewImportJobStore(tx)

	// 2. 隔离任务扫描范围。
	scopedStore := targetJobStore{
		JobStore: baseStore,
		jobID:    job.ID,
	}

	// 3. 创建真实 GitHubExecutor。
	// 只有 GitHub API 使用 Fake Inspector。
	executor := NewGitHubExecutor(tx, inspector)

	// 4. 创建 Runner。
	runner, err := NewRunner(
		scopedStore,
		executor,
		"worker-integration",
	)
	if err != nil {
		t.Fatal(err)
	}

	// 5. 执行一轮完整任务。
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("Runner 执行失败: %v", err)
	}

	// 6. 验证 GitHub API 调用。
	if inspector.calls != 1 {
		t.Fatalf(
			"inspector calls=%d, want 1",
			inspector.calls,
		)
	}

	// 验证仓库绑定。
	var expectedRepo model.Repository

	if err := tx.First(
		&expectedRepo,
		job.RepositoryID,
	).Error; err != nil {
		t.Fatalf("load expected repository: %v", err)
	}

	if inspector.owner != expectedRepo.Owner ||
		inspector.repo != expectedRepo.Repo {
		t.Fatalf(
			"unexpected repository: got=%s/%s want=%s/%s",
			inspector.owner,
			inspector.repo,
			expectedRepo.Owner,
			expectedRepo.Repo,
		)
	}

	// 验证使用任务创建时固定的 Revision，
	// 而不是仓库当前最新 SHA。
	if inspector.revision != revisionSHA {
		t.Fatalf(
			"revision mismatch: got=%s want=%s",
			inspector.revision,
			revisionSHA,
		)
	}

	t.Log("PASS: GitHub API called with correct repository and revision")

	// 7. 查询真实 MySQL 中的任务结果。
	var got model.ImportJob

	if err := tx.First(&got, job.ID).Error; err != nil {
		t.Fatal(err)
	}

	// 验证任务状态。
	if got.Status != model.ImportJobSucceeded {
		t.Fatalf(
			"status=%s, want succeeded",
			got.Status,
		)
	}

	if got.AttemptCount != 1 {
		t.Fatalf(
			"attempt_count=%d, want 1",
			got.AttemptCount,
		)
	}

	if got.FinishedAt == nil {
		t.Fatal("finished_at 不能为 nil")
	}

	// 8. 验证业务结果持久化。
	if got.ResultTreeSHA == nil ||
		*got.ResultTreeSHA != strings.ToLower(treeSHA) {
		t.Fatalf(
			"unexpected tree SHA: %v",
			got.ResultTreeSHA,
		)
	}

	if got.ResultFileCount == nil ||
		*got.ResultFileCount != 10 {
		t.Fatal("file_count 持久化错误")
	}

	if got.ResultTotalBytes == nil ||
		*got.ResultTotalBytes != 1024 {
		t.Fatal("total_bytes 持久化错误")
	}

	t.Log("PASS: import result persisted correctly")

	// 9. 验证 Lease 清理。
	if got.LeaseOwner != nil ||
		got.LeaseExpiresAt != nil ||
		len(got.LeaseToken) != 0 {
		t.Fatal("任务完成后 Lease 应该被清理")
	}

	t.Log("PASS: lease fields cleared")

	// 10. 验证最终任务状态。
	if got.Status != model.ImportJobSucceeded {
		t.Fatal("job should remain succeeded")
	}

	t.Log(
		"PASS: Runner + GitHubExecutor + MySQL 完整执行成功",
	)
}
