package repository

import (
	"GoServer/internal/model"
	"context"
	"fmt"
	"gorm.io/gorm"
	"time"
)

func CreateConversation(ctx context.Context, db *gorm.DB, con *model.Conversation) error {
	result := db.WithContext(ctx).Create(con)
	return databaseError(ctx, "create conversation", result.Error)
}

func GetConversationByID(ctx context.Context, db *gorm.DB, userId uint64, id uint64) (*model.Conversation, error) {
	conver := model.Conversation{}
	result := db.WithContext(ctx).
		Where("id = ? AND user_id = ?", id, userId).First(&conver)
	if result.Error != nil {
		return nil, databaseError(ctx, "get conversation", result.Error)
	}
	return &conver, nil
}

func ListConversation(ctx context.Context, db *gorm.DB, userID uint64) ([]model.Conversation, error) {
	convers := make([]model.Conversation, 0)
	result := db.WithContext(ctx).Where("user_id = ?", userID).Order("id ASC").Find(&convers)
	if result.Error != nil {
		return nil, databaseError(ctx, "list conversations", result.Error)
	}
	return convers, nil
}

func TouchConversation(
	ctx context.Context,
	tx *gorm.DB,
	conversationID uint64,
) error {
	result := tx.WithContext(ctx).
		Model(&model.Conversation{}).
		Where("id = ?", conversationID).
		UpdateColumn("updated_at", time.Now())

	if result.Error != nil {
		return databaseError(ctx, "touch conversation", result.Error)
	}

	// MySQL 默认报告实际修改的行数。
	// RowsAffected == 0 也可能是更新时间与旧值相同。
	if result.RowsAffected == 0 {
		var count int64
		err := tx.WithContext(ctx).
			Model(&model.Conversation{}).
			Where("id = ?", conversationID).
			Count(&count).Error

		if err != nil {
			return databaseError(ctx, "verify conversation", err)
		}
		if count == 0 {
			return ErrNotFound
		}
	}

	return nil
}

func ListProjectConversations(
	ctx context.Context,
	db *gorm.DB,
	userID uint64,
	projectID uint64,
	page int,
	pageSize int,
) ([]model.Conversation, error) {

	if userID < 1 || projectID < 1 {
		return nil, fmt.Errorf("Invalid Argument:%w", ErrInvalidReference)
	}
	if page < 1 || pageSize < 1 || pageSize > 100 {
		return nil, ErrInvalidPagination
	}

	maxInt := int(^uint(0) >> 1)

	if page-1 > maxInt/pageSize {
		return nil, ErrInvalidPagination
	}

	res := make([]model.Conversation, 0)

	result := db.WithContext(ctx).Model(&model.Conversation{}).
		Select("*").Where("user_id = ? AND project_id = ?", userID, projectID).
		Order("created_at DESC").Order("id DESC").Scopes(Paginate(page, pageSize)).Find(&res)
	if result.Error != nil {
		return nil, databaseError(ctx, "list project conversations", result.Error)
	}
	return res, nil
}
