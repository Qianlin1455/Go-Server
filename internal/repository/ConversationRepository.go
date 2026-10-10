package repository

import (
	"GoServer/internal/model"
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

func CreateConversation(ctx context.Context, db *gorm.DB, con *model.Conversation) error {
	result := db.WithContext(ctx).Create(con)
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

func GetConversationByID(ctx context.Context, db *gorm.DB, userId uint64, id uint64) (*model.Conversation, error) {
	conver := model.Conversation{}
	result := db.WithContext(ctx).
		Where("id = ? AND user_id = ?", id, userId).First(&conver)
	if result.Error == nil {
		return &conver, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return nil, ErrStorage

}

func ListConversation(ctx context.Context, db *gorm.DB, userID uint64) ([]model.Conversation, error) {
	convers := make([]model.Conversation, 0)
	result := db.WithContext(ctx).Where("user_id = ?", userID).Order("id ASC").Find(&convers)
	if result.Error == nil {
		return convers, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return nil, fmt.Errorf("查询失败:%w", ErrStorage)

}
