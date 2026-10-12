package model

import (
	"time"
)

type Message struct {
	ID             uint64    `gorm:"column:id;primaryKey;autoIncrement;index:idx_messages_conversation_id_id,priority:2"`
	ConversationID uint64    `gorm:"column:conversation_id;not null;index:idx_messages_conversation_id_id,priority:1"`
	Role           string    `gorm:"column:role;type:varchar(20);not null"`
	Content        string    `gorm:"column:content;type:longtext;not null"`
	CreatedAt      time.Time `gorm:"column:created_at;type:datetime(3);not null;autoCreateTime"`

	Conversation Conversation `gorm:"foreignKey:ConversationID;references:ID;constraint:OnDelete:CASCADE"`
}
