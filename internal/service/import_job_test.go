package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"GoServer/events"
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

func TestCreateWithOutbox_OutboxInsertFailureRollsBackJob(t *testing.T) {
	ctx := context.Background()
	tx := newGitHubBindingTestTx(t)

	user, project := createBindingTestProject(t, tx)

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

	store := repository.NewImportJobStore(tx)

	// 1. 创建一个基准 Job
	seedJob := &model.ImportJob{
		UserID:             user.ID,
		ProjectID:          project.ID,
		RepositoryID:       repo.ID,
		Endpoint:           ImportCreateEndpoint,
		IdempotencyKey:     "outbox-seed",
		RequestFingerprint: strings.Repeat("a", 64),
		RevisionSHA:        testCommitSHA,
		Status:             model.ImportJobQueued,
		MaxAttempts:        3,
	}

	if err := store.Create(ctx, seedJob); err != nil {
		t.Fatal(err)
	}

	// 2. 创建基准 Outbox 事件
	seedEvent := &model.OutboxEvent{
		EventID:       "550e8400-e29b-41d4-a716-446655440000",
		JobID:         seedJob.ID,
		EventType:     events.ImportRequestedType,
		RevisionSHA:   testCommitSHA,
		SchemaVersion: 1,
		Payload:       `{}`,
		Status:        model.OutboxPending,
	}

	if err := repository.NewOutboxEventStore(tx).
		Create(ctx, seedEvent); err != nil {
		t.Fatal(err)
	}

	// 3. 准备第二个 Job
	candidateJob := &model.ImportJob{
		UserID:             user.ID,
		ProjectID:          project.ID,
		RepositoryID:       repo.ID,
		Endpoint:           ImportCreateEndpoint,
		IdempotencyKey:     "outbox-rollback",
		RequestFingerprint: strings.Repeat("b", 64),
		RevisionSHA:        testCommitSHA,
		Status:             model.ImportJobQueued,
		MaxAttempts:        3,
	}

	// 4. 故意使用已经存在的 event_id
	err := store.CreateWithOutbox(
		ctx,
		candidateJob,
		func(job *model.ImportJob) (*model.OutboxEvent, error) {
			return &model.OutboxEvent{
				EventID:       seedEvent.EventID,
				JobID:         job.ID,
				EventType:     events.ImportRequestedType,
				RevisionSHA:   job.RevisionSHA,
				SchemaVersion: 1,
				Payload:       `{}`,
				Status:        model.OutboxPending,
			}, nil
		},
	)

	if err == nil {
		t.Fatal("expected duplicate event_id error")
	}

	t.Logf("PASS: duplicate event_id rejected: %v", err)

	// 5. 验证第二个 Job 已回滚
	var jobCount int64

	if err := tx.Model(&model.ImportJob{}).
		Where(
			"user_id = ? AND endpoint = ? AND idempotency_key = ?",
			user.ID,
			ImportCreateEndpoint,
			"outbox-rollback",
		).
		Count(&jobCount).Error; err != nil {
		t.Fatal(err)
	}

	if jobCount != 0 {
		t.Fatalf("expected 0 candidate jobs, got %d", jobCount)
	}

	// 6. 验证基准事件仍然存在
	var eventCount int64

	if err := tx.Model(&model.OutboxEvent{}).
		Where("event_id = ?", seedEvent.EventID).
		Count(&eventCount).Error; err != nil {
		t.Fatal(err)
	}

	if eventCount != 1 {
		t.Fatalf("expected original event to remain, got %d", eventCount)
	}

	t.Log("PASS: import_job rolled back")
	t.Log("PASS: original outbox event preserved")
}
