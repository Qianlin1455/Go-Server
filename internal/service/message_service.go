package service

import (
	"GoServer/internal/model"
	"GoServer/internal/repository"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"gorm.io/gorm"
)

var (
	ErrMessageNotFound     = errors.New("message not found")
	ErrInvalidMessageInput = errors.New("invalid message input")
	// conversation 相关错误复用已有定义
	ErrInvalidPagination = errors.New("invalid pagination")
)

type SendMessageInput struct {
	ConversationID uint64
	Content        string
}

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

func validateSendMessageInput(input SendMessageInput) error {
	if input.ConversationID == 0 {
		return ErrInvalidMessageInput
	}

	if strings.TrimSpace(input.Content) == "" {
		return ErrInvalidMessageInput
	}

	return nil
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
	dbMsg, err := repository.GetMessageByID(ctx, s.db, userID, conversationID, msg.ID)

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
	page int,
	pageSize int,
) ([]MessageResult, error) {
	start := time.Now()

	defer func() {
		log.Printf(
			"[PERF] ListMessages service_total=%s",
			time.Since(start),
		)
	}()

	//0、分页参数校验
	if page < 1 || pageSize < 1 || pageSize > 100 {
		return nil, ErrInvalidPagination
	}
	maxInt := int(^uint(0) >> 1)

	if page-1 > maxInt/pageSize {
		return nil, ErrInvalidPagination
	}

	ownershipStart := time.Now()

	// 1. 权限校验
	_, err := repository.GetConversationByID(
		ctx,
		s.db,
		userID,
		conversationID,
	)

	// 记录耗时
	log.Printf(
		"[PERF] GetConversationByID=%s",
		time.Since(ownershipStart),
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

	// 记录消息查询开始时间
	queryStart := time.Now()

	// 2. 查询消息
	messages, err := repository.ListMessages(
		ctx,
		s.db,
		userID,
		conversationID,
		page,
		pageSize,
	)

	// 记录消息查询耗时
	log.Printf(
		"[PERF] Repository.ListMessages=%s",
		time.Since(queryStart),
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

func (s *MessageService) SendMessage(
	ctx context.Context,
	userID uint64,
	input SendMessageInput,
) (*MessageResult, error) {

	// 1. 参数校验
	if err := validateSendMessageInput(input); err != nil {
		return nil, err
	}

	var message model.Message

	// 2. 开启事务
	err := repository.Transaction(ctx, s.db,
		func(tx *gorm.DB) error {

			// ① 查询会话归属，并锁定会话行
			ownerID, err := repository.GetConversationOwnerForUpdate(
				ctx, tx, input.ConversationID,
			)

			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return ErrConversationNotFound
				}
				return fmt.Errorf("lookup owner: %w", err)
			}

			// 权限校验
			if ownerID != userID {
				return ErrConversationForbidden
			}

			// ② 插入消息
			message = model.Message{
				ConversationID: input.ConversationID,
				Role:           "user",
				Content:        input.Content,
			}

			if err := repository.CreateMessage(
				ctx, tx, &message,
			); err != nil {
				return fmt.Errorf("create message: %w", err)
			}
			fmt.Printf(
				"After CreateMessage: ID=%d, CreatedAt=%v\n",
				message.ID,
				message.CreatedAt,
			)

			// ③ 更新会话时间
			if err := repository.TouchConversation(
				ctx, tx, input.ConversationID,
			); err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return ErrConversationNotFound
				}
				return fmt.Errorf("touch conversation: %w", err)
			}

			// ④ 告诉 GORM 可以提交事务
			return nil
		},
	)

	// 包括执行失败和提交失败
	if err != nil {
		return nil, err
	}

	fmt.Printf(
		"Before Return: CreatedAt=%v\n",
		message.CreatedAt,
	)

	// 3. 只有事务提交成功才返回结果
	return &MessageResult{
		ID:             message.ID,
		ConversationID: message.ConversationID,
		Role:           message.Role,
		Content:        message.Content,
		CreatedAt:      message.CreatedAt,
	}, nil
}
