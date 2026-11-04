package repository

import (
	"context"

	"GoServer/internal/model"

	"gorm.io/gorm"
)

type ImportJobStore struct {
	db *gorm.DB
}

func NewImportJobStore(db *gorm.DB) *ImportJobStore {
	return &ImportJobStore{db: db}
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
