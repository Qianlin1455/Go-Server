package repository

import (
	"context"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GetConversationOwnerID 只查询会话的所有者 ID
func GetConversationOwnerID(
	ctx context.Context,
	db *gorm.DB,
	conversationID uint64,
) (uint64, error) {

	var row struct {
		UserID uint64 `gorm:"column:user_id"`
	}

	err := db.WithContext(ctx).
		Table("conversations").
		Select("user_id").
		Where("id = ?", conversationID).
		Take(&row).Error

	if err != nil {
		return 0, databaseError(ctx, "query conversation owner", err)
	}

	return row.UserID, nil
}

func GetConversationOwnerForUpdate(
	ctx context.Context,
	tx *gorm.DB,
	conversationID uint64,
) (uint64, error) {

	var row struct {
		UserID uint64 `gorm:"column:user_id"`
	}

	err := tx.WithContext(ctx).
		Table("conversations").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("user_id").
		Where("id = ?", conversationID).
		Take(&row).Error

	if err != nil {
		return 0, databaseError(ctx, "get conversation owner for update", err)
	}

	return row.UserID, nil
}
