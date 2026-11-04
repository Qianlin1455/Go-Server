package service

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"GoServer/internal/idempotency"
	"GoServer/internal/model"
	"GoServer/internal/repository"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// ============================================================
// 公共测试辅助函数
// 50 个 goroutine 同时创建相同的 ImportJob
//
// gate == nil：只使用 MySQL
// gate != nil：使用 RedisGate + MySQL
//
// 验收标准：
// 1. 50 个请求全部成功
// 2. 只有 1 个请求真正创建任务
// 3. 50 个请求返回相同的 Job ID
// 4. 数据库最终只有 1 条记录
// ============================================================

func runImportJobConcurrent50(
	t *testing.T,
	gate ImportGate,
) {
	t.Helper()

	// 1. 获取测试数据库 DSN
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Fatal("TEST_MYSQL_DSN 未设置")
	}

	// 强制使用独立测试数据库
	cfg, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("解析测试 DSN 失败: %v", err)
	}
	if cfg.DBName != "go_server_test" {
		t.Fatal("只能使用 go_server_test")
	}

	// 2. 创建数据库连接
	db, err := gorm.Open(
		gormmysql.Open(dsn),
		&gorm.Config{
			TranslateError: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}

	// 使用真实连接池模拟并发请求
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(10)

	ctx := context.Background()

	// 每次测试使用唯一数据，避免测试之间相互干扰
	suffix := strconv.FormatInt(
		time.Now().UnixNano(), 36,
	)

	user := &model.Users{}
	project := &model.Project{}
	repo := &model.Repository{}

	// 3. 注册测试数据清理
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cancel()

		// 按外键依赖顺序删除
		if user.ID != 0 && project.ID != 0 {
			if err := db.WithContext(cleanupCtx).
				Exec(
					"DELETE FROM import_jobs WHERE user_id = ? AND project_id = ?",
					user.ID,
					project.ID,
				).Error; err != nil {
				t.Errorf("清理 import_jobs 失败: %v", err)
			}
		}

		if repo.ID != 0 {
			if err := db.WithContext(cleanupCtx).
				Exec(
					"DELETE FROM repositories WHERE id = ?",
					repo.ID,
				).Error; err != nil {
				t.Errorf("清理 repositories 失败: %v", err)
			}
		}

		if project.ID != 0 {
			if err := db.WithContext(cleanupCtx).
				Exec(
					"DELETE FROM projects WHERE id = ?",
					project.ID,
				).Error; err != nil {
				t.Errorf("清理 projects 失败: %v", err)
			}
		}

		if user.ID != 0 {
			if err := db.WithContext(cleanupCtx).
				Exec(
					"DELETE FROM users WHERE id = ?",
					user.ID,
				).Error; err != nil {
				t.Errorf("清理 users 失败: %v", err)
			}
		}

		if err := sqlDB.Close(); err != nil {
			t.Errorf("关闭数据库连接失败: %v", err)
		}
	})

	// ============================================================
	// 4. 创建已提交的测试数据
	// ============================================================

	user.UserName = "imp_" + suffix
	user.Email = "imp_" + suffix + "@example.com"
	user.Password = "test-hash"

	if err := repository.CreateUser(
		ctx, db, user,
	); err != nil {
		t.Fatal(err)
	}

	project.UserID = user.ID
	project.Name = "import_project_" + suffix

	if err := repository.CreateProject(
		ctx, db, project,
	); err != nil {
		t.Fatal(err)
	}

	repo.ProjectID = project.ID
	repo.Owner = "golang"
	repo.Repo = "go"
	repo.DefaultBranch = "master"
	repo.LastSHA = testCommitSHA

	if err := repository.NewGitHubRepositoryStore(db).
		Create(ctx, repo); err != nil {
		t.Fatal(err)
	}

	// ============================================================
	// 5. 创建 Service
	// ============================================================

	svc := NewImportJobServiceWithGate(db, gate)

	input := CreateImportJobInput{
		ProjectID:      project.ID,
		RepositoryID:   repo.ID,
		IdempotencyKey: "concurrent-" + suffix,
	}

	// ============================================================
	// 6. 准备 50 个并发请求
	// ============================================================

	const n = 50

	type result struct {
		id      uint64
		created bool
		err     error
	}

	results := make(chan result, n)
	start := make(chan struct{})

	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			// 等待统一开始信号
			<-start

			requestCtx, cancel := context.WithTimeout(
				context.Background(),
				30*time.Second,
			)
			defer cancel()

			job, created, err := svc.CreateImportJob(
				requestCtx,
				user.ID,
				input,
			)

			var id uint64
			if job != nil {
				id = job.ID
			}

			results <- result{
				id:      id,
				created: created,
				err:     err,
			}
		}()
	}

	// 同时释放所有 goroutine
	close(start)

	wg.Wait()
	close(results)

	// ============================================================
	// 7. 验证并发结果
	// ============================================================

	var firstID uint64
	createdCount := 0
	successCount := 0

	for r := range results {
		if r.err != nil {
			t.Errorf("并发创建失败: %v", r.err)
			continue
		}

		successCount++

		if r.id == 0 {
			t.Error("返回了无效 Job ID")
			continue
		}

		if firstID == 0 {
			firstID = r.id
		} else if r.id != firstID {
			t.Errorf(
				"Job ID 不一致: got=%d want=%d",
				r.id,
				firstID,
			)
		}

		if r.created {
			createdCount++
		}
	}

	// ============================================================
	// 8. 验证数据库只有一条记录
	// ============================================================

	var count int64

	if err := db.Model(&model.ImportJob{}).
		Where(
			"user_id = ? AND endpoint = ? AND idempotency_key = ?",
			user.ID,
			ImportCreateEndpoint,
			input.IdempotencyKey,
		).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}

	t.Logf("successful requests: %d", successCount)
	t.Logf("newly created: %d", createdCount)
	t.Logf("database rows: %d", count)
	t.Logf("import_job_id: %d", firstID)

	if successCount != n {
		t.Errorf(
			"expected %d successes, got %d",
			n,
			successCount,
		)
	}

	if createdCount != 1 {
		t.Errorf(
			"expected 1 creation, got %d",
			createdCount,
		)
	}

	if count != 1 {
		t.Errorf(
			"expected 1 database row, got %d",
			count,
		)
	}

	if firstID == 0 {
		t.Error("expected nonzero ImportJob ID")
	}
}

// ============================================================
// 测试一：不使用 Redis
// 验证 MySQL UNIQUE 最终防重
// ============================================================

func TestCreateImportJob_Concurrent50(t *testing.T) {
	runImportJobConcurrent50(t, nil)
}

// ============================================================
// 测试二：Redis 正常
// 验证 SETNX + MySQL 并发幂等
// ============================================================

func TestCreateImportJob_WithRedisGate(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:6379",
	})
	defer client.Close()

	if err := client.Ping(
		context.Background(),
	).Err(); err != nil {
		t.Fatalf("Redis unavailable: %v", err)
	}

	gate, err := idempotency.NewRedisGate(
		client,
		15*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}

	runImportJobConcurrent50(t, gate)
}

// ============================================================
// 测试三：Redis 故障
// 验证 Redis 不可用时 MySQL 仍能保证幂等
// ============================================================

func TestCreateImportJob_RedisUnavailable(t *testing.T) {
	// 使用无法连接的本地端口模拟 Redis 故障
	// 不需要真正关闭 Redis 容器

	client := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
		MaxRetries:   -1,
	})
	defer client.Close()

	gate, err := idempotency.NewRedisGate(
		client,
		15*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}

	runImportJobConcurrent50(t, gate)
}
