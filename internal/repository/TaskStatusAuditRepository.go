package repository

import (
	"context"

	"GoServer/internal/model"

	"gorm.io/gorm"
)

func CreateTaskStatusAudit(
	ctx context.Context,
	db *gorm.DB,
	audit *model.TaskStatusAudit,
) error {
	if audit == nil {
		return ErrInvalidReference
	}

	result := db.WithContext(ctx).Create(audit)
	return databaseError(ctx, "create task status audit", result.Error)
}
