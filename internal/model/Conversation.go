package model

import (
	"time"
)

type Conversation struct {
	ID        uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	UserID    uint64    `gorm:"column:user_id;not null;index:idx_conversations_user_id"`
	Title     string    `gorm:"column:title;type:varchar(255);not null"`
	CreatedAt time.Time `gorm:"column:created_at;type:datetime(3);not null;default:CURRENT_TIMESTAMP(3);autoCreateTime"`
	UpdatedAt time.Time `gorm:"column:updated_at;type:datetime(3);not null;default:CURRENT_TIMESTAMP(3);autoUpdateTime"`

	User Users `gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE"`
}
