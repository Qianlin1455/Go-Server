package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

type Conversation struct {
	ID        uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	UserID    uint64    `gorm:"column:user_id;not null;index:idx_conversations_user_id"`
	Title     string    `gorm:"column:title;type:varchar(255);not null"`
	CreatedAt time.Time `gorm:"column:created_at;type:datetime(3);not null;default:CURRENT_TIMESTAMP(3);autoCreateTime"`
	UpdatedAt time.Time `gorm:"column:updated_at;type:datetime(3);not null;default:CURRENT_TIMESTAMP(3);autoUpdateTime"`

	User Users `gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE"`
}

func CreateConversation(ctx context.Context, db *gorm.DB, con *Conversation) error {
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

func GetConversationByID(ctx context.Context, db *gorm.DB, userId uint64, id uint64) (*Conversation, error) {
	conver := Conversation{}
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

func ListConversation(ctx context.Context, db *gorm.DB, userID uint64) ([]Conversation, error) {
	convers := make([]Conversation, 0)
	result := db.WithContext(ctx).Where("user_id = ?", userID).Order("id ASC").Find(&convers)
	if result.Error == nil {
		return convers, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return nil, fmt.Errorf("查询失败:%w", ErrStorage)

}
