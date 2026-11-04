package service

import (
	"context"
	"errors"
	"testing"

	"GoServer/internal/model"
	"GoServer/internal/repository"
)

func TestCreateImportJob_Idempotency(t *testing.T) {
	ctx := context.Background()

	// 使用独立测试库中的事务，结束时自动回滚
	tx := newGitHubBindingTestTx(t)

	// 1. 创建测试用户、项目
	user, project := createBindingTestProject(t, tx)

	// 2. 创建测试仓库绑定
	repo := &model.Repository{
		ProjectID:     project.ID,
		Owner:         "golang",
		Repo:          "go",
		DefaultBranch: "master",
		LastSHA:       testCommitSHA,
	}

	if err := repository.NewGitHubRepositoryStore(tx).
		Create(ctx, repo); err != nil {
		t.Fatal(err)
	}

	svc := NewImportJobService(tx)

	input := CreateImportJobInput{
		ProjectID:      project.ID,
		RepositoryID:   repo.ID,
		IdempotencyKey: "import-test-001",
	}

	// 3. 第一次创建
	first, created, err := svc.CreateImportJob(
		ctx, user.ID, input,
	)

	if err != nil {
		t.Fatalf("first create failed: %v", err)
	}

	if !created || first == nil || first.ID == 0 {
		t.Fatal("expected newly created import job")
	}

	t.Logf("first import_job_id = %d", first.ID)

	// 4. 相同请求重复 10 次
	for i := 0; i < 10; i++ {
		got, isNew, err := svc.CreateImportJob(
			ctx, user.ID, input,
		)

		if err != nil {
			t.Fatalf("retry %d failed: %v", i, err)
		}

		if isNew {
			t.Fatalf("retry %d unexpectedly created new job", i)
		}

		if got == nil || got.ID != first.ID {
			t.Fatalf("retry %d returned different job ID", i)
		}
	}

	// 5. 验证数据库最终只有一条记录
	var count int64

	err = tx.Model(&model.ImportJob{}).
		Where(
			"user_id = ? AND endpoint = ? AND idempotency_key = ?",
			user.ID,
			ImportCreateEndpoint,
			input.IdempotencyKey,
		).
		Count(&count).Error

	if err != nil {
		t.Fatal(err)
	}

	if count != 1 {
		t.Fatalf("expected 1 job, got %d", count)
	}

	t.Log("PASS: 10 retries returned the same job ID")

	// 6. 相同 Key，不同 Payload
	different := input
	different.RepositoryID = repo.ID + 999999

	_, _, err = svc.CreateImportJob(
		ctx, user.ID, different,
	)

	if !errors.Is(err, ErrImportIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}

	t.Log("PASS: same key with different payload rejected")

	// 7. 不同 Key，相同 Payload
	newInput := input
	newInput.IdempotencyKey = "import-test-002"

	second, isNew, err := svc.CreateImportJob(
		ctx, user.ID, newInput,
	)

	if err != nil {
		t.Fatalf("different key create failed: %v", err)
	}

	if !isNew || second == nil || second.ID == first.ID {
		t.Fatal("different key should create a new job")
	}

	t.Log("PASS: different key created a separate job")
}
