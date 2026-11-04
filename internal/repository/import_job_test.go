package repository

import (
	"context"
	"errors"
	"testing"

	"GoServer/internal/model"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func TestImportJob_Duplicate(t *testing.T) {
	ctx := context.Background()

	// 使用项目现有的测试事务辅助函数
	tx := setupTestTx(t)

	// 创建测试用户和项目
	user, project := setupGitHubTestProject(
		t, ctx, tx, "import_dup",
	)

	// 创建仓库绑定
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

	store := NewImportJobStore(tx)

	first := &model.ImportJob{
		UserID:             user.ID,
		ProjectID:          project.ID,
		RepositoryID:       repo.ID,
		Endpoint:           "POST /api/v1/projects/:id/imports",
		IdempotencyKey:     "test-key-001",
		RequestFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RevisionSHA:        repo.LastSHA,
		Status:             model.ImportJobPending,
	}

	// 第一次插入应该成功
	if err := store.Create(ctx, first); err != nil {
		t.Fatalf("first insert failed: %v", err)
	}

	// 第二次使用相同的唯一键
	second := *first
	second.ID = 0

	err := store.Create(ctx, &second)

	// 必须触发数据库唯一约束
	if err == nil {
		t.Fatal("expected duplicate key error")
	}

	var mysqlErr *mysql.MySQLError

	if !errors.Is(err, gorm.ErrDuplicatedKey) &&
		!(errors.As(err, &mysqlErr) && mysqlErr.Number == 1062) {
		t.Fatalf("unexpected error: %v", err)
	}

	// 查询已经存在的任务
	existing, err := store.GetByIdempotency(
		ctx,
		user.ID,
		first.Endpoint,
		first.IdempotencyKey,
	)

	if err != nil {
		t.Fatal(err)
	}

	if existing.ID != first.ID {
		t.Fatalf(
			"expected job ID %d, got %d",
			first.ID,
			existing.ID,
		)
	}

	t.Log("PASS: duplicate key rejected, original job preserved")
}
