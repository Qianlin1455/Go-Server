package repository

import (
	"context"

	"GoServer/internal/model"

	"gorm.io/gorm"
)

func CountSubmittedTaskEvidence(ctx context.Context, db *gorm.DB, taskID uint64) (int64, error) {
	var count int64
	err := db.WithContext(ctx).Model(&model.TaskEvidence{}).
		Where("task_id = ? AND status = ?", taskID, model.EvidenceStatusSubmitted).
		Count(&count).Error
	return count, databaseError(ctx, "count submitted evidence", err)
}

func CreateTaskEvidence(
	ctx context.Context,
	db *gorm.DB,
	evidence *model.TaskEvidence,
) error {

	// 1. 基础引用检查
	if evidence == nil ||
		evidence.TaskID == 0 ||
		evidence.CreatedBy == 0 {
		return ErrInvalidReference
	}

	// 2. 执行 INSERT
	result := db.WithContext(ctx).Create(evidence)
	return databaseError(ctx, "create task evidence", result.Error)
}
