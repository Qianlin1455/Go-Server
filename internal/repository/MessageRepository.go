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
	userID uint64,
	conversationID uint64,
	page int,
	pageSize int,
) ([]model.Message, error) {
	msg := make([]model.Message, 0)
	if page < 1 || pageSize < 1 || pageSize > 100 {
		return nil, ErrInvalidPagination
	}

	maxInt := int(^uint(0) >> 1)

	if page-1 > maxInt/pageSize {
		return nil, ErrInvalidPagination
	}

	result := db.WithContext(ctx).Model(&model.Message{}).
		Select("messages.*").
		Joins("INNER JOIN conversations ON messages.conversation_id = conversations.id").
		Where(
			"conversations.user_id = ? AND messages.conversation_id = ?",
			userID, conversationID,
		).
		Order("messages.id ASC").
		Scopes(Paginate(page, pageSize)).
		Find(&msg)

	if result.Error == nil {
		return msg, nil
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return nil, fmt.Errorf(
		"查询失败:%w",
		errors.Join(ErrStorage, result.Error),
	)
}
