package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

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

	FailJob(
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
	}, nil
}

// Run 启动 Worker 调度循环。
func (r *Runner) Run(ctx context.Context) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	log.Printf("worker %s started", r.workerID)

	for {
		if ctx.Err() != nil {
			log.Printf("worker %s stopped", r.workerID)
			return nil
		}

		// 每次处理最多一个任务
		if err := r.RunOnce(ctx); err != nil {
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
	// 1. 创建数据库扫描 Context
	scanCtx, cancel := context.WithTimeout(
		ctx, scanTimeout,
	)

	// 2. 回收过期任务
	requeued, failed, err := r.store.RecoverExpiredJobs(scanCtx)
	if err != nil {
		cancel()
		return fmt.Errorf("recover expired jobs: %w", err)
	}

	if requeued > 0 || failed > 0 {
		log.Printf(
			"recovered: requeued=%d failed=%d",
			requeued, failed,
		)
	}

	// 3. 查询可领取任务
	ids, err := r.store.ListReadyQueuedJobIDs(
		scanCtx, scanLimit,
	)
	cancel()

	if err != nil {
		return fmt.Errorf("list ready jobs: %w", err)
	}

	// 4. 逐个尝试 CAS 领取
	for _, jobID := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}

		claimCtx, claimCancel := context.WithTimeout(
			ctx, claimTimeout,
		)

		token, claimed, err := r.store.ClaimQueuedJob(
			claimCtx,
			jobID,
			r.workerID,
			leaseDuration,
		)
		claimCancel()

		if err != nil {
			return fmt.Errorf("claim job %d: %w", jobID, err)
		}

		if !claimed {
			// 可能被其他 Worker 抢先领取
			continue
		}

		log.Printf(
			"worker=%s claimed job=%d",
			r.workerID, jobID,
		)

		// 一次只执行一个任务
		return r.executeClaimed(ctx, jobID, token)
	}

	return nil
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

	if taskErr != nil {
		execErr = taskErr
	}

	// Worker 主动关闭时，不提交成功或失败状态。
	// 保留 running，等待 Lease 到期后由其他 Worker 回收。
	if ctx.Err() != nil {
		log.Printf(
			"worker=%s shutting down, job=%d left for lease recovery",
			r.workerID,
			jobID,
		)
		return ctx.Err()
	}

	// 2. 独立收尾 Context
	// 即使任务超时，也允许短时间内写入数据库状态。
	finishCtx, finishCancel := context.WithTimeout(
		context.Background(),
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

	// 4. 业务执行失败
	errorCode := "EXECUTION_FAILED"

	if errors.Is(execErr, context.DeadlineExceeded) {
		errorCode = "TASK_TIMEOUT"
	}

	updated, err := r.store.FailJob(
		finishCtx,
		jobID,
		token,
		errorCode,
	)

	if err != nil {
		return fmt.Errorf(
			"fail job %d: %w",
			jobID,
			err,
		)
	}

	if !updated {
		log.Printf(
			"job=%d failure update rejected: lease lost",
			jobID,
		)
		return nil
	}

	log.Printf(
		"job=%d execution failed: %v",
		jobID,
		execErr,
	)

	return nil
}
