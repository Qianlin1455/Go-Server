package repository

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"GoServer/internal/model"

	"gorm.io/gorm"
)

type ImportJobStore struct {
	db *gorm.DB
}

func NewImportJobStore(db *gorm.DB) *ImportJobStore {
	return &ImportJobStore{db: db}
}

var ErrInvalidCreateWithOutboxArgs = errors.New(
	"invalid create import job with outbox arguments",
)

var ErrInvalidCompleteJobArgs = errors.New(
	"invalid complete job arguments",
)

var ErrInvalidClaimArgs = errors.New("invalid claim arguments")
var ErrInvalidFailJobArgs = errors.New(
	"invalid fail job arguments",
)
var ErrInvalidGetJobArgs = errors.New(
	"invalid get import job arguments",
)

var ErrInvalidCompleteJobResult = errors.New(
	"invalid complete job result",
)

var importJobTreeSHAPattern = regexp.MustCompile(
	`^[0-9a-fA-F]{40}$`,
)

var failJobErrorCodePattern = regexp.MustCompile(
	`^[A-Z][A-Z0-9_]{0,63}$`,
)
var workerIDPattern = regexp.MustCompile(
	`^[A-Za-z0-9._:-]{1,128}$`,
)

// ClaimQueuedJob 尝试领取指定的 queued 任务。
// claimed=true 表示当前 Worker 领取成功。
// claimed=false 表示任务不满足领取条件或已被其他 Worker 领取。
func (r *ImportJobStore) ClaimQueuedJob(
	ctx context.Context,
	jobID uint64,
	workerID string,
	leaseDuration time.Duration,
) ([]byte, bool, error) {

	if jobID == 0 ||
		!workerIDPattern.MatchString(workerID) ||
		leaseDuration < time.Second ||
		leaseDuration > 24*time.Hour {
		return nil, false, ErrInvalidClaimArgs
	}

	// 生成随机租约 Token
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, false, fmt.Errorf(
			"generate lease token: %w", err,
		)
	}

	// 使用数据库时间，避免应用与 MySQL 时钟不一致。
	// 同一条 UPDATE 原子修改状态、计数和租约。
	result := r.db.WithContext(ctx).
		Model(&model.ImportJob{}).
		Where(
			`id = ?
             AND status = ?
             AND attempt_count < max_attempts
             AND available_at <= NOW(3)`,
			jobID,
			model.ImportJobQueued,
		).
		Updates(map[string]interface{}{
			"status": model.ImportJobRunning,
			"attempt_count": gorm.Expr(
				"attempt_count + 1",
			),
			"started_at": gorm.Expr(
				"COALESCE(started_at, NOW(3))",
			),
			"last_attempt_at": gorm.Expr(
				"NOW(3)",
			),
			"lease_owner": workerID,
			"lease_token": token,
			"lease_expires_at": gorm.Expr(
				"DATE_ADD(NOW(3), INTERVAL ? MICROSECOND)",
				leaseDuration.Microseconds(),
			),
		})

	if result.Error != nil {
		return nil, false, fmt.Errorf(
			"claim import job: %w", result.Error,
		)
	}

	if result.RowsAffected != 1 {
		return nil, false, nil
	}

	return token, true, nil
}

// Create 将导入任务写入 MySQL。
func (r *ImportJobStore) Create(
	ctx context.Context,
	job *model.ImportJob,
) error {
	return r.db.WithContext(ctx).
		Create(job).Error
}

// CreateWithOutbox 在同一个 MySQL 事务中创建 ImportJob 和 OutboxEvent。
//
// buildEvent 在 ImportJob 插入成功后执行，
// 此时 job.ID 已经由 MySQL 回填。
func (r *ImportJobStore) CreateWithOutbox(
	ctx context.Context,
	job *model.ImportJob,
	buildEvent func(
		job *model.ImportJob,
	) (*model.OutboxEvent, error),
) error {
	if job == nil || buildEvent == nil {
		return ErrInvalidCreateWithOutboxArgs
	}

	return r.db.WithContext(ctx).Transaction(
		func(tx *gorm.DB) error {
			// 1. 创建业务任务
			if err := tx.Create(job).Error; err != nil {
				return err
			}

			// 2. 根据已生成的 Job ID 构造事件
			event, err := buildEvent(job)
			if err != nil {
				return err
			}

			if event == nil || event.JobID != job.ID {
				return ErrInvalidCreateWithOutboxArgs
			}

			// 3. 创建 Outbox 事件
			if err := NewOutboxEventStore(tx).
				Create(ctx, event); err != nil {
				return err
			}

			return nil
		},
	)
}

// GetByIdempotency 根据幂等作用域查询已有任务。
func (r *ImportJobStore) GetByIdempotency(
	ctx context.Context,
	userID uint64,
	endpoint string,
	key string,
) (*model.ImportJob, error) {
	var job model.ImportJob

	err := r.db.WithContext(ctx).
		Where(
			"user_id = ? AND endpoint = ? AND idempotency_key = ?",
			userID,
			endpoint,
			key,
		).
		Take(&job).Error

	if err != nil {
		return nil, err
	}

	return &job, nil
}

// 查询当前可以领取的任务ID
func (r *ImportJobStore) ListReadyQueuedJobIDs(
	ctx context.Context,
	limit int,
) ([]uint64, error) {
	if limit <= 0 || limit > 100 {
		return nil, fmt.Errorf(
			"invalid ready job limit:%d", limit,
		)
	}

	var ids []uint64

	err := r.db.WithContext(ctx).
		Model(&model.ImportJob{}).
		Where("status = ?", model.ImportJobQueued).
		Where("available_at <= NOW(3)").
		Where("attempt_count < max_attempts").
		Order("available_at ASC, id ASC").
		Limit(limit).
		Pluck("id", &ids).Error

	if err != nil {
		return nil, fmt.Errorf(
			"list ready queued jobs:%w", err,
		)
	}
	return ids, nil
}

func (r *ImportJobStore) GetByID(
	ctx context.Context,
	jobID uint64,
) (*model.ImportJob, error) {
	if jobID == 0 {
		return nil, ErrInvalidGetJobArgs
	}
	var job model.ImportJob

	err := r.db.WithContext(ctx).First(&job, jobID).Error
	if err != nil {
		return nil, fmt.Errorf(
			"get import job %d:%w", jobID, err,
		)
	}
	return &job, nil
}

// 回收租约已过期的任务
func (r *ImportJobStore) RecoverExpiredJobs(
	ctx context.Context,
) (requeued int64, failed int64, err error) {
	var requeuedCount int64
	var failedCount int64

	//两类回收操作放入事务中
	err = r.db.WithContext(ctx).Transaction(
		func(tx *gorm.DB) error {
			//回收租约过期、仍可以重试的任务
			result := tx.Model(&model.ImportJob{}).
				Where("status = ? AND lease_expires_at <= NOW(3)", model.ImportJobRunning).
				Where("attempt_count < max_attempts").
				Updates(map[string]interface{}{
					"status":           model.ImportJobQueued,
					"available_at":     gorm.Expr("NOW(3)"),
					"lease_owner":      nil,
					"lease_token":      nil,
					"lease_expires_at": nil,
				})

			if result.Error != nil {
				return fmt.Errorf("requeue expired jobs:%w", result.Error)
			}
			requeuedCount = result.RowsAffected

			//租约过期且重试次数已耗尽
			result = tx.Model(&model.ImportJob{}).
				Where(
					"status = ? AND lease_expires_at <= NOW(3)",
					model.ImportJobRunning,
				).
				Where("attempt_count >= max_attempts").
				Updates(map[string]interface{}{
					"status":           model.ImportJobFailed,
					"finished_at":      gorm.Expr("NOW(3)"),
					"last_error_code":  "MAX_ATTEMPTS_EXCEEDED",
					"lease_owner":      nil,
					"lease_token":      nil,
					"lease_expires_at": nil,
				})

			if result.Error != nil {
				return fmt.Errorf(
					"fail expired jobs: %w",
					result.Error,
				)
			}
			failedCount = result.RowsAffected
			return nil
		},
	)

	if err != nil {
		return 0, 0, fmt.Errorf("recover expired import jobs:%w", err)
	}
	return requeuedCount, failedCount, nil
}

// 只有持有有效租约的token的worker才能完成任务，completed = false 表示不满足完成条件
func (r *ImportJobStore) CompleteJob(
	ctx context.Context,
	jobID uint64,
	leaseToken []byte,
) (completed bool, err error) {
	//1、校验参数
	if jobID == 0 || len(leaseToken) != 16 {
		return false, ErrInvalidCompleteJobArgs
	}

	//2、CAS更新
	result := r.db.WithContext(ctx).
		Model(&model.ImportJob{}).
		Where(
			`id = ?
             AND status = ?
             AND lease_token = ?
             AND lease_expires_at > NOW(3)`,
			jobID,
			model.ImportJobRunning,
			leaseToken,
		).
		Updates(map[string]interface{}{
			"status":             model.ImportJobSucceeded,
			"finished_at":        gorm.Expr("NOW(3)"),
			"lease_owner":        nil,
			"lease_token":        nil,
			"lease_expires_at":   nil,
			"last_error_code":    nil,
			"last_error_message": nil,
		})
	//数据库执行失败
	if result.Error != nil {
		return false, fmt.Errorf("complete import job:%w", result.Error)
	}

	//没有更新记录
	if result.RowsAffected != 1 {
		return false, nil
	}
	return true, nil

}

func (r *ImportJobStore) FailJob(
	ctx context.Context,
	jobID uint64,
	leaseToken []byte,
	errorCode string,
) (updated bool, err error) {
	//参数校验
	if jobID == 0 ||
		len(leaseToken) != 16 ||
		!failJobErrorCodePattern.MatchString(errorCode) {
		return false, ErrInvalidFailJobArgs
	}
	//尝试重新排队
	result := r.db.WithContext(ctx).
		Model(&model.ImportJob{}).
		Where(
			`id = ?
             AND status = ?
             AND lease_token = ?
             AND lease_expires_at > NOW(3)
             AND attempt_count < max_attempts`,
			jobID,
			model.ImportJobRunning,
			leaseToken,
		).
		Updates(map[string]interface{}{
			"status": model.ImportJobQueued,

			// 5 秒后允许下一次领取
			"available_at": gorm.Expr(
				"DATE_ADD(NOW(3), INTERVAL 5 SECOND)",
			),

			"finished_at":        nil,
			"lease_owner":        nil,
			"lease_token":        nil,
			"lease_expires_at":   nil,
			"last_error_code":    errorCode,
			"last_error_message": nil,
		})
	if result.Error != nil {
		return false, fmt.Errorf(
			"requeue import job: %w",
			result.Error,
		)
	}

	if result.RowsAffected == 1 {
		return true, nil
	}

	// 3. 尝试标记为最终失败（次数耗尽）
	result = r.db.WithContext(ctx).
		Model(&model.ImportJob{}).
		Where(
			`id = ?
             AND status = ?
             AND lease_token = ?
             AND lease_expires_at > NOW(3)
             AND attempt_count >= max_attempts`,
			jobID,
			model.ImportJobRunning,
			leaseToken,
		).
		Updates(map[string]interface{}{
			"status": model.ImportJobFailed,

			"finished_at": gorm.Expr("NOW(3)"),

			"lease_owner":        nil,
			"lease_token":        nil,
			"lease_expires_at":   nil,
			"last_error_code":    errorCode,
			"last_error_message": nil,
		})

	if result.Error != nil {
		return false, fmt.Errorf(
			"fail import job: %w",
			result.Error,
		)
	}
	if result.RowsAffected == 1 {
		return true, nil
	}
	return false, nil
}

func (r *ImportJobStore) CompleteJobWithResult(
	ctx context.Context,
	jobID uint64,
	leaseToken []byte,
	result model.ImportJobResult,
) (completed bool, err error) {
	//参数校验
	if jobID == 0 || len(leaseToken) != 16 {
		return false, ErrInvalidCompleteJobArgs
	}
	if !importJobTreeSHAPattern.MatchString(result.TreeSHA) {
		return false, ErrInvalidCompleteJobResult
	}

	//CAS更新：结果持久化与状态变更在同一条sql
	dbResult := r.db.WithContext(ctx).Model(&model.ImportJob{}).
		Where(
			"id = ? AND status = ? AND lease_token = ? AND lease_expires_at>NOW(3)",
			jobID, model.ImportJobRunning, leaseToken,
		).Updates(map[string]any{
		"result_tree_sha":    strings.ToLower(result.TreeSHA),
		"result_file_count":  result.FileCount,
		"result_total_bytes": result.TotalBytes,

		"status":      model.ImportJobSucceeded,
		"finished_at": gorm.Expr("NOW(3)"),

		"lease_owner":      nil,
		"lease_token":      nil,
		"lease_expires_at": nil,

		"last_error_code":    nil,
		"last_error_message": nil,
	})

	if dbResult.Error != nil {
		return false, fmt.Errorf(
			"complete import job with result:%w",
			dbResult.Error,
		)
	}
	return dbResult.RowsAffected == 1, nil
}

func (r *ImportJobStore) FailJobPermanently(
	ctx context.Context,
	jobID uint64,
	leaseToken []byte,
	errorCode string,
) (bool, error) {
	if jobID == 0 || len(leaseToken) != 16 ||
		!failJobErrorCodePattern.MatchString(errorCode) {
		return false, ErrInvalidFailJobArgs
	}

	//CAS更新：只有持有有效的lease的worker才能标记为永久失败
	result := r.db.WithContext(ctx).
		Model(&model.ImportJob{}).
		Where(
			`id = ?
			 AND status = ?
			 AND lease_token = ?
			 AND lease_expires_at > NOW(3)`,
			jobID,
			model.ImportJobRunning,
			leaseToken,
		).
		Updates(map[string]any{
			"status":      model.ImportJobFailed,
			"finished_at": gorm.Expr("NOW(3)"),

			"last_error_code":    errorCode,
			"last_error_message": nil,

			"lease_owner":      nil,
			"lease_token":      nil,
			"lease_expires_at": nil,
		})

	if result.Error != nil {
		return false, fmt.Errorf(
			"fail import job permanently:%w", result.Error,
		)
	}

	return result.RowsAffected == 1, nil
}
