package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"GoServer/internal/model"
)

// 使用 Fake Store 代替真实 MySQL
type fakeJobStore struct {
	claimCount         int
	completeCount      int
	failCount          int
	lastErrorCode      string
	permanentFailCount int

	// 记录 Runner 传递过来的业务结果
	completeResult model.ImportJobResult

	rejectClaim    bool
	rejectComplete bool
}

func (f *fakeJobStore) RecoverExpiredJobs(
	ctx context.Context,
) (int64, int64, error) {
	return 0, 0, nil
}

func (f *fakeJobStore) FailJobPermanently(
	ctx context.Context,
	jobID uint64,
	token []byte,
	errorCode string,
) (bool, error) {
	f.permanentFailCount++
	f.lastErrorCode = errorCode
	return true, nil
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

// CompleteJobWithResult 模拟结果持久化。
func (f *fakeJobStore) CompleteJobWithResult(
	ctx context.Context,
	jobID uint64,
	token []byte,
	result model.ImportJobResult,
) (bool, error) {
	f.completeCount++
	f.completeResult = result

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

// 测试一：执行成功，且业务结果正确传递到 Store。
func TestRunner_Success(t *testing.T) {
	store := &fakeJobStore{}
	executeCount := 0

	expectedResult := model.ImportJobResult{
		TreeSHA:    "0123456789abcdef0123456789abcdef01234567",
		FileCount:  10,
		TotalBytes: 1024,
	}

	executor := ExecutorFunc(func(
		ctx context.Context,
		jobID uint64,
	) (model.ImportJobResult, error) {
		executeCount++

		if jobID != 101 {
			t.Fatalf("unexpected job ID: %d", jobID)
		}

		return expectedResult, nil
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

	// 核心断言：Executor 返回的结果必须传递给 Store。
	if store.completeResult != expectedResult {
		t.Fatalf(
			"complete result mismatch: got %+v, want %+v",
			store.completeResult,
			expectedResult,
		)
	}

	t.Log("PASS: 领取、执行、结果传递、完成流程正确")
}

// 测试二：执行失败。
func TestRunner_Failure(t *testing.T) {
	store := &fakeJobStore{}

	executor := ExecutorFunc(func(
		ctx context.Context,
		jobID uint64,
	) (model.ImportJobResult, error) {
		return model.ImportJobResult{},
			errors.New("simulated github failure")
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
		t.Fatal("执行失败不能调用 CompleteJobWithResult")
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

// 测试三：领取失败，不执行任务。
func TestRunner_ClaimRejected(t *testing.T) {
	store := &fakeJobStore{
		rejectClaim: true,
	}

	executeCount := 0

	executor := ExecutorFunc(func(
		ctx context.Context,
		jobID uint64,
	) (model.ImportJobResult, error) {
		executeCount++
		return model.ImportJobResult{}, nil
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
		t.Fatalf(
			"claimCount=%d, want 1",
			store.claimCount,
		)
	}

	if executeCount != 0 {
		t.Fatal("领取失败不应该执行 Executor")
	}

	if store.completeCount != 0 ||
		store.failCount != 0 {
		t.Fatal("领取失败不应该修改任务状态")
	}

	t.Log("PASS: 领取失败，未执行任务")
}

// 测试四：Lease 丢失，拒绝完成任务。
func TestRunner_LeaseLost(t *testing.T) {
	store := &fakeJobStore{
		rejectComplete: true,
	}

	executeCount := 0

	expectedResult := model.ImportJobResult{
		TreeSHA:    "0123456789abcdef0123456789abcdef01234567",
		FileCount:  5,
		TotalBytes: 512,
	}

	executor := ExecutorFunc(func(
		ctx context.Context,
		jobID uint64,
	) (model.ImportJobResult, error) {
		executeCount++
		return expectedResult, nil
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

	if store.completeResult != expectedResult {
		t.Fatal("业务结果没有正确传递")
	}

	// 完成操作被拒绝后，不应该调用 FailJob。
	if store.failCount != 0 {
		t.Fatal("Lease 丢失后不应该调用 FailJob")
	}

	t.Log("PASS: Lease 丢失，完成操作被拒绝")
}

// 测试五：任务超时。
func TestRunner_TaskTimeout(t *testing.T) {
	store := &fakeJobStore{}

	executor := ExecutorFunc(func(
		ctx context.Context,
		jobID uint64,
	) (model.ImportJobResult, error) {
		return model.ImportJobResult{},
			context.DeadlineExceeded
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

// 测试六：Worker 主动退出时不提交任务结果。
func TestRunner_Shutdown(t *testing.T) {
	store := &fakeJobStore{}

	ctx, cancel := context.WithCancel(
		context.Background(),
	)
	defer cancel()

	executeCount := 0

	executor := ExecutorFunc(func(
		taskCtx context.Context,
		jobID uint64,
	) (model.ImportJobResult, error) {
		executeCount++

		// 模拟 Worker 执行中收到 Ctrl+C。
		cancel()

		// 等待任务 Context 被取消。
		<-taskCtx.Done()

		return model.ImportJobResult{}, taskCtx.Err()
	})

	runner, err := NewRunner(
		store,
		executor,
		"worker-A",
	)
	if err != nil {
		t.Fatal(err)
	}

	err = runner.RunOnce(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf(
			"expected context.Canceled, got %v",
			err,
		)
	}

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

	if store.completeCount != 0 {
		t.Fatal(
			"Worker shutdown must not call CompleteJobWithResult",
		)
	}

	if store.failCount != 0 {
		t.Fatal(
			"Worker shutdown must not call FailJob",
		)
	}

	t.Log(
		"PASS: Worker 主动退出，未提交任务结果，等待 Lease 恢复",
	)
}
