package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"GoServer/internal/model"

	"gorm.io/gorm"
)

var (
	ErrInvalidGetOutboxEventArgs = errors.New(
		"invalid get outbox event arguments",
	)

	ErrInvalidOutboxEvent = errors.New(
		"invalid outbox event",
	)

	ErrOutboxEventNotPending = errors.New(
		"outbox event not found or not pending",
	)
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

// ListPending查询指定事件类型的待发布事件
// 按照ID升序，保证处理顺序稳定
func (r *OutboxEventStore) ListPending(
	ctx context.Context,
	eventType string,
	limit int,
) ([]model.OutboxEvent, error) {
	if eventType == "" || limit <= 0 {
		return nil, ErrInvalidOutboxEvent
	}
	var events []model.OutboxEvent

	err := r.db.WithContext(ctx).
		Where(
			"status = ? AND event_type = ?",
			model.OutboxPending, eventType,
		).Order("id ASC").
		Limit(limit).Find(&events).Error
	if err != nil {
		return nil, err
	}
	return events, nil
}

// MarkSent只有在Kafka确认发送完成后调用
func (r *OutboxEventStore) MarkSent(
	ctx context.Context,
	id uint64,
	sentAt time.Time,
) error {
	if id == 0 || sentAt.IsZero() {
		return ErrInvalidOutboxEvent
	}

	result := r.db.WithContext(ctx).Model(&model.OutboxEvent{}).
		Where("id = ? AND status = ?", id, model.OutboxPending).
		Updates(map[string]any{
			"status":     model.OutboxSent,
			"sent_at":    sentAt,
			"last_error": nil,
		})

	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrOutboxEventNotPending
	}
	return nil
}

// MarkPublishFailed记录失败信息
// 失败后仍保持pending，供后续publisher重试
func (r *OutboxEventStore) MarkPublishFailed(
	ctx context.Context,
	id uint64,
	publishErr error,
) error {
	if id == 0 || publishErr == nil {
		return ErrInvalidOutboxEvent
	}

	message := truncateOutboxError(publishErr.Error(), 512)
	result := r.db.WithContext(ctx).Model(&model.OutboxEvent{}).
		Where(
			"id = ? AND status = ?",
			id, model.OutboxPending,
		).Updates(map[string]any{
		"attempts":   gorm.Expr("attempts+1"),
		"last_error": message,
	})

	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected != 1 {
		return ErrOutboxEventNotPending
	}
	return nil
}

// 数据库 last_error 字段最多保存 512 个字符。
func truncateOutboxError(message string, max int) string {
	runes := []rune(message)

	if len(runes) <= max {
		return message
	}

	return string(runes[:max])
}

// GetByEventID 根据事件 ID 查询 Outbox 事件。
// 用于 Consumer 验证 Kafka 消息是否来自真实的 Outbox 记录。
func (r *OutboxEventStore) GetByEventID(
	ctx context.Context,
	eventID string,
) (*model.OutboxEvent, error) {
	if eventID == "" {
		return nil, ErrInvalidGetOutboxEventArgs
	}

	var event model.OutboxEvent

	err := r.db.WithContext(ctx).
		Where("event_id = ?", eventID).
		Take(&event).Error

	if err != nil {
		return nil, fmt.Errorf(
			"get outbox event by event id %s: %w",
			eventID,
			err,
		)
	}

	return &event, nil
}
