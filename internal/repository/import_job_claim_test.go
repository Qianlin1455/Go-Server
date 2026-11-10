package repository

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"GoServer/internal/model"

	mysqlDriver "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestClaimQueuedJob_Concurrent50(t *testing.T) {
	// 1. 强制使用独立测试库
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Fatal("TEST_MYSQL_DSN 未设置")
	}

	cfg, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBName != "go_server_test" {
		t.Fatal("禁止在非 go_server_test 数据库运行")
	}

	db, err := gorm.Open(
		gormmysql.Open(dsn),
		&gorm.Config{TranslateError: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(10)

	ctx := context.Background()

	var user *model.Users
	var project *model.Project
	repo := &model.Repository{}
	job := &model.ImportJob{}

	// 2. 注册清理逻辑
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(), 10*time.Second,
		)
		defer cancel()

		cleanup := func(query string, id uint64) {
			if id == 0 {
				return
			}
			if err := db.WithContext(cleanupCtx).
				Exec(query, id).Error; err != nil {
				t.Errorf("cleanup failed: %v", err)
			}
		}

		cleanup("DELETE FROM import_jobs WHERE id = ?", job.ID)
		cleanup("DELETE FROM repositories WHERE id = ?", repo.ID)

		if project != nil {
			cleanup("DELETE FROM projects WHERE id = ?", project.ID)
		}
		if user != nil {
			cleanup("DELETE FROM users WHERE id = ?", user.ID)
		}

		_ = sqlDB.Close()
	})

	// 3. 创建已提交的测试数据
	user, project = setupGitHubTestProject(
		t, ctx, db, "claim_cas",
	)

	repo.ProjectID = project.ID
	repo.Owner = "golang"
	repo.Repo = "go"
	repo.DefaultBranch = "master"
	repo.LastSHA = "0123456789abcdef0123456789abcdef01234567"

	if err := NewGitHubRepositoryStore(db).
		Create(ctx, repo); err != nil {
		t.Fatal(err)
	}

	job = &model.ImportJob{
		UserID:             user.ID,
		ProjectID:          project.ID,
		RepositoryID:       repo.ID,
		Endpoint:           "POST /api/v1/projects/:id/imports",
		IdempotencyKey:     fmt.Sprintf("claim-%d", time.Now().UnixNano()),
		RequestFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RevisionSHA:        repo.LastSHA,
		Status:             model.ImportJobQueued,
		MaxAttempts:        3,
	}

	store := NewImportJobStore(db)
	if err := store.Create(ctx, job); err != nil {
		t.Fatal(err)
	}

	// 4. 同时启动 50 个领取操作
	const workers = 50

	type claimResult struct {
		claimed bool
		token   []byte
		err     error
	}

	results := make(chan claimResult, workers)
	start := make(chan struct{})

	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()
			<-start

			claimCtx, cancel := context.WithTimeout(
				context.Background(), 20*time.Second,
			)
			defer cancel()

			token, claimed, err := store.ClaimQueuedJob(
				claimCtx,
				job.ID,
				fmt.Sprintf("worker-%d", i),
				30*time.Second,
			)

			results <- claimResult{
				claimed: claimed,
				token:   token,
				err:     err,
			}
		}(i)
	}

	close(start)
	wg.Wait()
	close(results)

	// 5. 统计领取结果
	successCount := 0
	errorCount := 0

	for result := range results {
		if result.err != nil {
			errorCount++
			t.Errorf("claim error: %v", result.err)
			continue
		}

		if result.claimed {
			successCount++
			if len(result.token) != 16 {
				t.Errorf("token length = %d, want 16",
					len(result.token))
			}
		}
	}

	if errorCount != 0 {
		t.Fatalf("unexpected errors: %d", errorCount)
	}
	if successCount != 1 {
		t.Fatalf("successful claims = %d, want 1",
			successCount)
	}

	// 6. 查询数据库最终状态
	var got model.ImportJob
	if err := db.First(&got, job.ID).Error; err != nil {
		t.Fatal(err)
	}

	if got.Status != model.ImportJobRunning {
		t.Errorf("status = %s, want running", got.Status)
	}
	if got.AttemptCount != 1 {
		t.Errorf("attempt_count = %d, want 1",
			got.AttemptCount)
	}
	if got.LeaseOwner == nil || *got.LeaseOwner == "" {
		t.Error("lease_owner is missing")
	}
	if len(got.LeaseToken) != 16 {
		t.Errorf("persisted token length = %d, want 16",
			len(got.LeaseToken))
	}
	if got.StartedAt == nil || got.LastAttemptAt == nil {
		t.Error("attempt timestamps are missing")
	}
	if got.LeaseExpiresAt == nil {
		t.Fatal("lease_expires_at is NULL")
	}

	remaining := time.Until(*got.LeaseExpiresAt)

	t.Logf("remaining lease duration = %v", remaining)

	if remaining <= 0 || remaining > 30*time.Second {
		t.Errorf("invalid lease duration: %v", remaining)
	}

	// 7. 再次领取必须失败
	_, claimed, err := store.ClaimQueuedJob(
		ctx, job.ID, "another-worker", 30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("running job must not be claimed again")
	}

	t.Logf("successful claims: %d", successCount)
	t.Logf("attempt_count: %d", got.AttemptCount)
	t.Logf("final status: %s", got.Status)
}
