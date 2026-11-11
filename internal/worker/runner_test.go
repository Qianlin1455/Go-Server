package worker

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 使用 Fake Store 代替真实 MySQL

type fakeJobStore struct {
	claimCount    int
	completeCount int
	failCount     int
	lastErrorCode string

	rejectClaim    bool
	rejectComplete bool
}

func (f *fakeJobStore) RecoverExpiredJobs(
	ctx context.Context,
) (int64, int64, error) {
	return 0, 0, nil
}

func (f *fakeJobStore) ListReadyQueuedJobIDs(
	ctx context.Context,
	limit int,
) ([]uint64, error) {
	return []uint64{101}, nil
}

func (f *fakeJobStore) ClaimQueuedJob(
	ctx context.Context,
	jobID uint64,
	workerID string,
	leaseDuration time.Duration,
) ([]byte, bool, error) {

	f.claimCount++

	if f.rejectClaim {
		return nil, false, nil
	}

	return make([]byte, 16), true, nil
}

func (f *fakeJobStore) CompleteJob(
	ctx context.Context,
	jobID uint64,
	token []byte,
) (bool, error) {

	f.completeCount++

	if f.rejectComplete {
		return false, nil
	}

	return true, nil
}

func (f *fakeJobStore) FailJob(
	ctx context.Context,
	jobID uint64,
	token []byte,
	errorCode string,
) (bool, error) {
	f.failCount++
	f.lastErrorCode = errorCode
	return true, nil
}

// 测试一：执行成功
func TestRunner_Success(t *testing.T) {
	store := &fakeJobStore{}
	executeCount := 0

	executor := ExecutorFunc(func(
		ctx context.Context,
		jobID uint64,
	) error {
		executeCount++

		if jobID != 101 {
			t.Fatalf("unexpected job ID: %d", jobID)
		}

		return nil
	})

	runner, err := NewRunner(
		store, executor, "worker-A",
	)
	if err != nil {
		t.Fatal(err)
	}

	err = runner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if store.claimCount != 1 ||
		executeCount != 1 ||
		store.completeCount != 1 ||
		store.failCount != 0 {
		t.Fatalf(
			"claim=%d execute=%d complete=%d fail=%d",
			store.claimCount,
			executeCount,
			store.completeCount,
			store.failCount,
		)
	}

	t.Log("PASS: 领取、执行、完成流程正确")
}

// 测试二：执行失败
func TestRunner_Failure(t *testing.T) {
	store := &fakeJobStore{}

	executor := ExecutorFunc(func(
		ctx context.Context,
		jobID uint64,
	) error {
		return errors.New("simulated github failure")
	})

	runner, err := NewRunner(
		store, executor, "worker-A",
	)
	if err != nil {
		t.Fatal(err)
	}

	err = runner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if store.claimCount != 1 {
		t.Fatal("任务应被领取一次")
	}

	if store.completeCount != 0 {
		t.Fatal("执行失败不能调用 CompleteJob")
	}

	if store.failCount != 1 {
		t.Fatal("执行失败必须调用 FailJob")
	}

	if store.lastErrorCode != "EXECUTION_FAILED" {
		t.Fatalf(
			"unexpected error code: %s",
			store.lastErrorCode,
		)
	}

	t.Log("PASS: 执行失败正确调用 FailJob")
}

func TestRunner_ClaimRejected(t *testing.T) {
	// 模拟任务被其他 Worker 领取
	store := &fakeJobStore{
		rejectClaim: true,
	}

	executeCount := 0

	executor := ExecutorFunc(func(
		ctx context.Context,
		jobID uint64,
	) error {
		executeCount++
		return nil
	})

	runner, err := NewRunner(
		store, executor, "worker-A",
	)
	if err != nil {
		t.Fatal(err)
	}

	err = runner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// 应该尝试领取一次
	if store.claimCount != 1 {
		t.Fatalf(
			"claimCount=%d, want 1",
			store.claimCount,
		)
	}

	// 领取失败，不能执行业务
	if executeCount != 0 {
		t.Fatal("领取失败不应该执行 Executor")
	}

	// 也不能更新任务状态
	if store.completeCount != 0 ||
		store.failCount != 0 {
		t.Fatal("领取失败不应该修改任务状态")
	}

	t.Log("PASS: 领取失败，未执行任务")
}

func TestRunner_LeaseLost(t *testing.T) {
	// 模拟完成任务时 Lease 已经失效
	store := &fakeJobStore{
		rejectComplete: true,
	}

	executeCount := 0

	executor := ExecutorFunc(func(
		ctx context.Context,
		jobID uint64,
	) error {
		executeCount++
		return nil
	})

	runner, err := NewRunner(
		store, executor, "worker-A",
	)
	if err != nil {
		t.Fatal(err)
	}

	err = runner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if store.claimCount != 1 {
		t.Fatal("应该成功领取一次")
	}

	if executeCount != 1 {
		t.Fatal("Executor 应该执行一次")
	}

	if store.completeCount != 1 {
		t.Fatal("应该尝试完成任务")
	}

	// CompleteJob 返回 false 后，
	// 不应该再调用 FailJob。
	if store.failCount != 0 {
		t.Fatal("Lease 丢失后不应该调用 FailJob")
	}

	t.Log("PASS: Lease 丢失，完成操作被拒绝")
}

func TestRunner_TaskTimeout(t *testing.T) {
	store := &fakeJobStore{}

	executor := ExecutorFunc(func(
		ctx context.Context,
		jobID uint64,
	) error {
		return context.DeadlineExceeded
	})

	runner, err := NewRunner(
		store, executor, "worker-A",
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if store.completeCount != 0 {
		t.Fatal("超时任务不能标记为成功")
	}

	if store.failCount != 1 {
		t.Fatal("超时任务应调用一次 FailJob")
	}

	if store.lastErrorCode != "TASK_TIMEOUT" {
		t.Fatalf(
			"errorCode=%s, want TASK_TIMEOUT",
			store.lastErrorCode,
		)
	}

	t.Log("PASS: 超时错误正确映射为 TASK_TIMEOUT")
}

// TestRunner_Shutdown 验证 Worker 主动退出时
// 不会错误地完成或失败任务。
func TestRunner_Shutdown(t *testing.T) {
	store := &fakeJobStore{}

	// 创建可主动取消的父 Context
	ctx, cancel := context.WithCancel(
		context.Background(),
	)
	defer cancel()

	executeCount := 0

	executor := ExecutorFunc(func(
		taskCtx context.Context,
		jobID uint64,
	) error {
		executeCount++

		// 模拟 Worker 执行中收到 Ctrl+C
		cancel()

		// 等待任务 Context 被取消
		<-taskCtx.Done()

		return taskCtx.Err()
	})

	runner, err := NewRunner(
		store,
		executor,
		"worker-A",
	)
	if err != nil {
		t.Fatal(err)
	}

	// 执行一轮
	err = runner.RunOnce(ctx)

	// 主动退出应返回 context.Canceled
	if !errors.Is(err, context.Canceled) {
		t.Fatalf(
			"expected context.Canceled, got %v",
			err,
		)
	}

	// 任务应该已经领取并开始执行
	if store.claimCount != 1 {
		t.Fatalf(
			"claimCount=%d, want 1",
			store.claimCount,
		)
	}

	if executeCount != 1 {
		t.Fatalf(
			"executeCount=%d, want 1",
			executeCount,
		)
	}

	// 主动退出不能标记为成功
	if store.completeCount != 0 {
		t.Fatal(
			"Worker shutdown must not call CompleteJob",
		)
	}

	// 主动退出不能作为普通业务失败
	if store.failCount != 0 {
		t.Fatal(
			"Worker shutdown must not call FailJob",
		)
	}

	t.Log(
		"PASS: Worker 主动退出，未提交任务结果，等待 Lease 恢复",
	)
}
