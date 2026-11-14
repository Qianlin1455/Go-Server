package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"time"

	"GoServer/internal/githubclient"
	"GoServer/internal/model"
)

// 第一版配置：单 Worker 串行执行任务
const (
	pollInterval  = 2 * time.Second
	scanTimeout   = 2 * time.Second
	claimTimeout  = 2 * time.Second
	taskTimeout   = 30 * time.Second
	leaseDuration = 60 * time.Second
	finishTimeout = 3 * time.Second
	scanLimit     = 20
)

// JobStore 定义 Runner 需要的数据库操作。
// 你的 repository.ImportJobStore 已实现这些方法。
type JobStore interface {
	RecoverExpiredJobs(
		ctx context.Context,
	) (int64, int64, error)

	ListReadyQueuedJobIDs(
		ctx context.Context,
		limit int,
	) ([]uint64, error)

	ClaimQueuedJob(
		ctx context.Context,
		jobID uint64,
		workerID string,
		leaseDuration time.Duration,
	) ([]byte, bool, error)

	CompleteJobWithResult(
		ctx context.Context,
		jobID uint64,
		leaseToken []byte,
		result model.ImportJobResult,
	) (bool, error)

	GetByID(ctx context.Context, jobID uint64) (*model.ImportJob, error)

	FailJob(
		ctx context.Context,
		jobID uint64,
		leaseToken []byte,
		errorCode string,
		retryDelay time.Duration,
		retryAt time.Time,
	) (bool, error)

	FailJobPermanently(
		ctx context.Context,
		jobID uint64,
		leaseToken []byte,
		errorCode string,
	) (bool, error)
}

// Executor 定义实际任务执行行为。
// 后续由 GitHub 导入业务实现。
type Executor interface {
	Execute(
		ctx context.Context,
		jobID uint64,
	) (model.ImportJobResult, error)
}

// ExecutorFunc 便于在测试中注入模拟执行器。
type ExecutorFunc func(
	ctx context.Context,
	jobID uint64,
) (model.ImportJobResult, error)

func (f ExecutorFunc) Execute(
	ctx context.Context,
	jobID uint64,
) (model.ImportJobResult, error) {
	return f(ctx, jobID)
}

type Runner struct {
	store    JobStore
	executor Executor
	workerID string
	retry    retryPolicy
}

func NewRunner(
	store JobStore,
	executor Executor,
	workerID string,
) (*Runner, error) {
	if store == nil || executor == nil || workerID == "" {
		return nil, errors.New("invalid worker configuration")
	}

	return &Runner{
		store:    store,
		executor: executor,
		workerID: workerID,
		retry:    retryPolicy{int64n: rand.Int64N},
	}, nil
}

// Run stops claiming on shutdown and gives in-flight work time to finish.
func (r *Runner) Run(ctx context.Context, grace time.Duration) error {
	if grace <= 0 {
		return errors.New("worker shutdown grace must be positive")
	}

	executionCtx, cancelExecution := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelExecution()
	go func() {
		select {
		case <-executionCtx.Done():
			return
		case <-ctx.Done():
		}
		log.Printf("worker=%s draining grace=%s", r.workerID, grace)
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-executionCtx.Done():
		case <-timer.C:
			cancelExecution()
		}
	}()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	log.Printf("worker %s started", r.workerID)

	for {
		if ctx.Err() != nil {
			log.Printf("worker %s stopped", r.workerID)
			return nil
		}

		// 每次处理最多一个任务
		if err := r.runOnce(ctx, executionCtx); err != nil {
			if ctx.Err() == nil {
				log.Printf("worker poll failed: %v", err)
			}
		}

		select {
		case <-ctx.Done():
			log.Printf("worker %s stopped", r.workerID)
			return nil

		case <-ticker.C:
			// 下一轮扫描
		}
	}
}

// RunOnce 执行一轮任务回收、扫描和领取。

func (r *Runner) RunOnce(ctx context.Context) error {
	return r.runOnce(ctx, ctx)
}

func (r *Runner) runOnce(ctx, executionCtx context.Context) error {
	scanCtx, cancel := context.WithTimeout(
		ctx,
		scanTimeout,
	)

	// 1. 回收过期任务
	requeued, failed, err := r.store.RecoverExpiredJobs(scanCtx)
	if err != nil {
		cancel()
		return fmt.Errorf("recover expired jobs: %w", err)
	}

	if requeued > 0 || failed > 0 {
		log.Printf(
			"recovered: requeued=%d failed=%d",
			requeued,
			failed,
		)
	}

	// 2. 扫描可领取任务
	ids, err := r.store.ListReadyQueuedJobIDs(
		scanCtx,
		scanLimit,
	)
	cancel()

	if err != nil {
		return fmt.Errorf("list ready jobs: %w", err)
	}

	// 3. 将具体任务交给统一执行入口
	for _, jobID := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}

		claimed, err := r.processJob(ctx, executionCtx, jobID)
		if err != nil {
			return err
		}

		if claimed {
			// 单 Worker 每轮最多处理一个任务
			return nil
		}

		// CAS 领取失败，尝试下一个任务
	}

	return nil
}

// ProcessJob 执行指定 Job。
// 返回 claimed=false 表示本次没有成功取得任务 Lease。
// 返回 claimed=true 表示已取得 Lease，任务处理流程已执行。
func (r *Runner) ProcessJob(
	ctx context.Context,
	jobID uint64,
) (bool, error) {
	return r.processJob(ctx, ctx, jobID)
}

func (r *Runner) processJob(ctx, executionCtx context.Context, jobID uint64) (bool, error) {
	if jobID == 0 {
		return false, errors.New("invalid job id")
	}

	if err := ctx.Err(); err != nil {
		return false, err
	}

	// 1. CAS 领取指定任务
	claimCtx, claimCancel := context.WithTimeout(
		ctx,
		claimTimeout,
	)

	token, claimed, err := r.store.ClaimQueuedJob(
		claimCtx,
		jobID,
		r.workerID,
		leaseDuration,
	)

	claimCancel()

	if err != nil {
		return false, fmt.Errorf(
			"claim job %d: %w",
			jobID,
			err,
		)
	}

	if !claimed {
		log.Printf(
			"worker=%s skipped job=%d: claim rejected",
			r.workerID,
			jobID,
		)
		return false, nil
	}

	log.Printf(
		"worker=%s claimed job=%d",
		r.workerID,
		jobID,
	)

	// 2. 执行任务并持久化结果
	started := time.Now()
	err = r.executeClaimed(executionCtx, jobID, token)
	log.Printf("worker=%s job_id=%d elapsed=%s process_error=%v", r.workerID, jobID, time.Since(started), err)
	if err != nil {
		return true, err
	}

	return true, nil
}

// executeClaimed 执行已经成功领取的任务。
func (r *Runner) executeClaimed(
	ctx context.Context,
	jobID uint64,
	token []byte,
) error {
	// 1. 限制单任务执行时间
	taskCtx, cancel := context.WithTimeout(
		ctx, taskTimeout,
	)

	// Executor 返回业务结果和错误
	result, execErr := r.executor.Execute(taskCtx, jobID)

	// 必须在 cancel() 之前读取状态
	taskErr := taskCtx.Err()
	cancel()

	var rateLimit *githubclient.RateLimitError
	errors.As(execErr, &rateLimit)
	// Keep the server's retry deadline if it arrived as the task timed out.
	if taskErr != nil && rateLimit == nil {
		execErr = taskErr
	}

	// 排空到期或 Kafka Session 取消时，保留任务供租约恢复。
	// 保留 running，等待 Lease 到期后由其他 Worker 回收。
	if ctx.Err() != nil {
		log.Printf(
			"worker=%s job_id=%d execution canceled; left for lease recovery",
			r.workerID,
			jobID,
		)
		return ctx.Err()
	}

	// 2. 独立收尾 Context
	// 即使任务超时，也允许短时间内写入数据库状态。
	finishCtx, finishCancel := context.WithTimeout(
		ctx,
		finishTimeout,
	)
	defer finishCancel()

	// 3. 业务执行成功：持久化结果并完成任务
	if execErr == nil {
		completed, err := r.store.CompleteJobWithResult(
			finishCtx,
			jobID,
			token,
			result,
		)

		if err != nil {
			return fmt.Errorf(
				"complete job %d with result: %w",
				jobID,
				err,
			)
		}

		if !completed {
			log.Printf(
				"job=%d completion rejected: lease lost",
				jobID,
			)
			return nil
		}

		log.Printf(
			"job=%d succeeded: tree_sha=%s files=%d bytes=%d",
			jobID,
			result.TreeSHA,
			result.FileCount,
			result.TotalBytes,
		)
		return nil
	}

	// 4. 业务执行失败：区分可重试和永久错误

	// 独立取消不属于普通业务失败。
	// 父 Context 未取消时，也不能把它当作永久失败。
	if errors.Is(execErr, context.Canceled) {
		return fmt.Errorf(
			"execute job %d canceled: %w",
			jobID,
			execErr,
		)
	}

	errorCode, permanent := classifyExecutionError(execErr)

	var updated bool
	var updateErr error

	if permanent {
		// 永久错误：直接进入 failed
		updated, updateErr = r.store.FailJobPermanently(
			finishCtx,
			jobID,
			token,
			errorCode,
		)
	} else {
		// 临时错误：重新排队，等待下一次重试
		job, err := r.store.GetByID(finishCtx, jobID)
		if err != nil {
			return fmt.Errorf("load job %d attempt count for retry: %w", jobID, err)
		}
		var retryAt time.Time
		if rateLimit != nil {
			retryAt = rateLimit.RetryAt
		}
		updated, updateErr = r.store.FailJob(
			finishCtx,
			jobID,
			token,
			errorCode,
			r.retry.delay(job.AttemptCount),
			retryAt,
		)
	}

	if updateErr != nil {
		return fmt.Errorf(
			"update job %d failure state: %w",
			jobID,
			updateErr,
		)
	}

	if !updated {
		log.Printf(
			"job=%d failure update rejected: lease lost",
			jobID,
		)
		return nil
	}

	if permanent {
		log.Printf(
			"job=%d permanently failed: code=%s err=%v",
			jobID,
			errorCode,
			execErr,
		)
	} else {
		log.Printf(
			"job=%d retryable failure recorded: code=%s err=%v",
			jobID,
			errorCode,
			execErr,
		)
	}

	return nil

}
