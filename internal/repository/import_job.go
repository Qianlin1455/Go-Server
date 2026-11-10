package repository

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
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

var ErrInvalidClaimArgs = errors.New("invalid claim arguments")

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
