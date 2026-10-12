package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"GoServer/internal/model"
	"GoServer/internal/repository"

	"gorm.io/gorm"
)

var (
	ErrMessageNotFound = errors.New("message not found")

	// conversation 相关错误复用已有定义
)

// MessageService 负责消息相关业务逻辑
type MessageService struct {
	db *gorm.DB
}

type MessageResult struct {
	ID             uint64    `json:"id"`
	ConversationID uint64    `json:"conversation_id"`
	Role           string    `json:"role"`
	Content        string    `json:"content"`
	CreatedAt      time.Time `json:"created_at"`
}

func toMessageResult(
	msg *model.Message,
) *MessageResult {

	return &MessageResult{
		ID:             msg.ID,
		ConversationID: msg.ConversationID,
		Role:           msg.Role,
		Content:        msg.Content,
		CreatedAt:      msg.CreatedAt,
	}
}

func NewMessageService(
	db *gorm.DB,
) *MessageService {
	return &MessageService{
		db: db,
	}
}

// CreateMessage 创建消息
// 前提：conversation 必须属于当前用户
func (s *MessageService) CreateMessage(
	ctx context.Context,
	userID uint64,
	conversationID uint64,
	role string,
	content string,
) (*MessageResult, error) {

	// 1. 校验 conversation 归属
	_, err := repository.GetConversationByID(
		ctx,
		s.db,
		userID,
		conversationID,
	)

	if err != nil {
		switch {
		case errors.Is(err, repository.ErrNotFound):
			// conversation不存在，或者不属于当前用户
			return nil, ErrConversationNotFound

		default:
			return nil, fmt.Errorf(
				"check conversation ownership: %w",
				err,
			)
		}
	}

	// 2. 创建消息
	msg := &model.Message{
		ConversationID: conversationID,
		Role:           role,
		Content:        content,
	}

	err = repository.CreateMessage(
		ctx,
		s.db,
		msg,
	)

	if err != nil {
		return nil, fmt.Errorf(
			"create message: %w",
			err,
		)
	}

	// 重新查询数据库生成字段
	dbMsg := &model.Message{}

	err = s.db.WithContext(ctx).
		First(dbMsg, msg.ID).
		Error

	if err != nil {
		return nil, err
	}

	return toMessageResult(dbMsg), nil
}

// ListMessages 查询会话历史消息
// 前提：conversation 必须属于当前用户
func (s *MessageService) ListMessages(
	ctx context.Context,
	userID uint64,
	conversationID uint64,
) ([]MessageResult, error) {

	// 1. 权限校验
	_, err := repository.GetConversationByID(
		ctx,
		s.db,
		userID,
		conversationID,
	)

	if err != nil {
		switch {
		case errors.Is(err, repository.ErrNotFound):
			return nil, ErrConversationNotFound

		default:
			return nil, fmt.Errorf(
				"check conversation ownership: %w",
				err,
			)
		}
	}

	// 2. 查询消息
	messages, err := repository.ListMessages(
		ctx,
		s.db,
		conversationID,
	)

	if err != nil {
		return nil, fmt.Errorf(
			"list messages: %w",
			err,
		)
	}

	// 3. model 转 DTO
	result := make(
		[]MessageResult,
		0,
		len(messages),
	)

	for i := range messages {
		result = append(
			result,
			*toMessageResult(&messages[i]),
		)
	}

	return result, nil
}
