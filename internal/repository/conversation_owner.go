package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("query conversation owner: %w", err)
	}

	return row.UserID, nil
}
