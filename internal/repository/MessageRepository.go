package repository

import (
	"GoServer/internal/model"
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

func CreateMessage(ctx context.Context, db *gorm.DB, msg *model.Message) error {
	result := db.WithContext(ctx).Create(msg)
	if result.Error == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(result.Error, gorm.ErrForeignKeyViolated) {
		return fmt.Errorf("外键冲突:%w", ErrInvalidReference)
	}
	return fmt.Errorf("Create failed: %w", ErrStorage)
}

func ListMessages(
	ctx context.Context,
	db *gorm.DB,
	conversationID uint64,
) ([]model.Message, error) {

	msg := make([]model.Message, 0)

	result := db.WithContext(ctx).
		Where(
			"conversation_id = ?",
			conversationID,
		).
		Order("id ASC").
		Find(&msg)

	if result.Error == nil {
		return msg, nil
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return nil, fmt.Errorf(
		"查询失败:%w",
		ErrStorage,
	)
}
