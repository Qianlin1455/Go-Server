package model

import "time"

const (
	OutboxPending = "pending"
	OutboxSent    = "sent"
)

type OutboxEvent struct {
	ID uint64 `gorm:"column:id;primaryKey;autoIncrement"`

	EventID string `gorm:"column:event_id;not null"`

	JobID uint64 `gorm:"column:job_id;not null"`

	EventType string `gorm:"column:event_type;not null"`

	RevisionSHA string `gorm:"column:revision_sha;not null"`

	SchemaVersion uint32 `gorm:"column:schema_version;not null"`

	Payload string `gorm:"column:payload;type:json;not null"`

	Status string `gorm:"column:status;not null"`

	Attempts uint32 `gorm:"column:attempts;not null"`

	LastError *string `gorm:"column:last_error"`

	CreatedAt time.Time `gorm:"column:created_at"`

	SentAt *time.Time `gorm:"column:sent_at"`
}

func (OutboxEvent) TableName() string {
	return "outbox_events"
}
