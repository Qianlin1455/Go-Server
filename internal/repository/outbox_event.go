package repository

import (
	"context"
	"errors"

	"GoServer/internal/model"

	"gorm.io/gorm"
)

var ErrInvalidOutboxEvent = errors.New(
	"invalid outbox event",
)

type OutboxEventStore struct {
	db *gorm.DB
}

func NewOutboxEventStore(db *gorm.DB) *OutboxEventStore {
	return &OutboxEventStore{db: db}
}

// Create 将 Outbox 事件写入 MySQL。
// 调用方传入事务 tx 时，该写入属于同一个事务。
func (r *OutboxEventStore) Create(
	ctx context.Context,
	event *model.OutboxEvent,
) error {
	if event == nil || event.JobID == 0 {
		return ErrInvalidOutboxEvent
	}

	return r.db.WithContext(ctx).
		Create(event).Error
}
