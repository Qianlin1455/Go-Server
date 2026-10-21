package repository

import (
	"context"
	"errors"
	"fmt"

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

	if ctx.Err() != nil {
		return ctx.Err()
	}

	if result.Error == nil {
		return nil
	}

	if errors.Is(result.Error, gorm.ErrDuplicatedKey) {
		return fmt.Errorf(
			"create task status audit: %w",
			errors.Join(ErrAlreadyExists, result.Error),
		)
	}

	if errors.Is(result.Error, gorm.ErrForeignKeyViolated) {
		return fmt.Errorf(
			"create task status audit: %w",
			errors.Join(ErrInvalidReference, result.Error),
		)
	}

	return fmt.Errorf(
		"create task status audit: %w",
		errors.Join(ErrStorage, result.Error),
	)
}
