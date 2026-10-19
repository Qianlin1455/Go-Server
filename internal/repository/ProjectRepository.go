package repository

import (
	"context"
	"errors"
	"fmt"

	"GoServer/internal/model"
	"gorm.io/gorm"
)

func CreateProject(
	ctx context.Context,
	db *gorm.DB,
	project *model.Project,
) error {
	result := db.WithContext(ctx).Create(project)
	if result.Error == nil {
		return nil
	}

	// 优先处理 Context 取消或超时。
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// 同一用户创建重名项目。
	if errors.Is(result.Error, gorm.ErrDuplicatedKey) {
		return ErrAlreadyExists
	}

	// 项目引用了不存在的用户。
	if errors.Is(result.Error, gorm.ErrForeignKeyViolated) {
		return fmt.Errorf(
			"create project: %w",
			ErrInvalidReference,
		)
	}

	// 其他数据库错误。
	return fmt.Errorf(
		"create project failed: %w",
		ErrStorage,
	)
}
