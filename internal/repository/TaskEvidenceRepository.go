package repository

import (
	"context"
	"errors"
	"fmt"

	"GoServer/internal/model"

	"gorm.io/gorm"
)

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

	// 3. Context 取消或超时
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// 4. 插入成功
	if result.Error == nil {
		return nil
	}

	// 5. 外键约束失败
	if errors.Is(
		result.Error,
		gorm.ErrForeignKeyViolated,
	) {
		return fmt.Errorf(
			"create task evidence: %w",
			errors.Join(
				ErrInvalidReference,
				result.Error,
			),
		)
	}

	// 6. 其他数据库错误
	return fmt.Errorf(
		"create task evidence: %w",
		errors.Join(
			ErrStorage,
			result.Error,
		),
	)
}
