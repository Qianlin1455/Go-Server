package repository

import (
	"GoServer/internal/model"
	"context"

	"gorm.io/gorm"
)

func SourceMessageBelongsToProject(ctx context.Context, db *gorm.DB, userID, projectID, messageID uint64) (bool, error) {
	var count int64
	err := db.WithContext(ctx).Model(&model.Message{}).
		Joins("INNER JOIN conversations ON conversations.id = messages.conversation_id").
		Where("messages.id = ? AND conversations.user_id = ? AND conversations.project_id = ?", messageID, userID, projectID).
		Count(&count).Error
	return count == 1, databaseError(ctx, "verify source message", err)
}

func GetMessageByID(ctx context.Context, db *gorm.DB, userID, conversationID, messageID uint64) (*model.Message, error) {
	var message model.Message
	err := db.WithContext(ctx).Model(&model.Message{}).Select("messages.*").
		Joins("INNER JOIN conversations ON conversations.id = messages.conversation_id").
		Where("messages.id = ? AND messages.conversation_id = ? AND conversations.user_id = ?", messageID, conversationID, userID).
		First(&message).Error
	if err != nil {
		return nil, databaseError(ctx, "get message", err)
	}
	return &message, nil
}

func CreateMessage(ctx context.Context, db *gorm.DB, msg *model.Message) error {
	result := db.WithContext(ctx).Create(msg)
	return databaseError(ctx, "create message", result.Error)
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
	if result.Error != nil {
		return nil, databaseError(ctx, "list messages", result.Error)
	}
	return msg, nil
}
