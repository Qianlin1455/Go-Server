package service

import (
	"context"
	"errors"
	"fmt"

	"GoServer/internal/repository"

	"gorm.io/gorm"
)

var (
	ErrConversationNotFound  = errors.New("conversation not found")
	ErrConversationForbidden = errors.New("conversation access forbidden")
)

type ConversationService struct {
	db *gorm.DB
}

func NewConversationService(db *gorm.DB) *ConversationService {
	return &ConversationService{db: db}
}

type ConversationResult struct {
	ID    uint64 `json:"id"`
	Title string `json:"title"`
}

func (s *ConversationService) GetConversation(
	ctx context.Context,
	userID uint64,
	conversationID uint64,
) (*ConversationResult, error) {

	// 1. 查询资源所有者
	ownerID, err := repository.GetConversationOwnerID(
		ctx, s.db, conversationID,
	)

	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrConversationNotFound
		}
		return nil, fmt.Errorf("lookup owner: %w", err)
	}

	// 2. 核心授权判断
	if ownerID != userID {
		return nil, ErrConversationForbidden
	}

	// 3. 再次使用原有的按用户隔离查询
	conversation, err := repository.GetConversationByID(
		ctx, s.db, userID, conversationID,
	)

	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrConversationNotFound
		}
		return nil, fmt.Errorf("get conversation: %w", err)
	}

	// 4. 只返回允许对外展示的字段
	return &ConversationResult{
		ID:    conversation.ID,
		Title: conversation.Title,
	}, nil
}
