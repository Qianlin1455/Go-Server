package service

import (
	"context"
	"errors"
	"fmt"

	"GoServer/internal/model"
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
	ID        uint64  `json:"id"`
	Title     string  `json:"title"`
	ProjectID *uint64 `json:"project_id,omitempty"`
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
		ID:        conversation.ID,
		Title:     conversation.Title,
		ProjectID: conversation.ProjectID, // 新增
	}, nil
}

func (s *ConversationService) CreateConversationWithProject(
	ctx context.Context,
	userID uint64,
	title string,
	projectID *uint64,
) (*ConversationResult, error) {

	if userID == 0 {
		return nil, ErrInvalidProjectInput
	}

	// 1. 指定了 ProjectID 时，先验证项目归属
	if projectID != nil {
		if *projectID == 0 {
			return nil, ErrInvalidProjectInput
		}

		_, err := repository.GetProjectByID(
			ctx,
			s.db,
			userID,
			*projectID,
		)

		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil, ErrProjectNotFound
			}

			return nil, fmt.Errorf(
				"check project ownership: %w", err,
			)
		}
	}

	// 2. 构建 Conversation
	conversation := &model.Conversation{
		UserID:    userID,
		ProjectID: projectID,
		Title:     title,
	}

	// 3. 写入数据库
	err := repository.CreateConversation(
		ctx,
		s.db,
		conversation,
	)

	if err != nil {
		// 防止归属检查之后项目被并发删除等情况
		if projectID != nil &&
			errors.Is(err, repository.ErrInvalidReference) {
			return nil, ErrProjectNotFound
		}

		return nil, fmt.Errorf(
			"create conversation: %w", err,
		)
	}

	// 4. 返回创建结果
	return &ConversationResult{
		ID:        conversation.ID,
		Title:     conversation.Title,
		ProjectID: conversation.ProjectID,
	}, nil
}

func (s *ConversationService) CreateConversation(
	ctx context.Context,
	userID uint64,
	title string,
) (*ConversationResult, error) {
	return s.CreateConversationWithProject(
		ctx, userID, title, nil,
	)
}

func (s *ConversationService) ListProjectConversations(
	ctx context.Context,
	userID uint64,
	projectID uint64,
	page int,
	pageSize int,
) ([]ConversationResult, error) {

	// 1. 校验 ID
	if userID == 0 || projectID == 0 {
		return nil, ErrInvalidProjectInput
	}

	// 2. 校验分页参数
	if page < 1 || pageSize < 1 || pageSize > 100 {
		return nil, ErrInvalidPagination
	}

	maxInt := int(^uint(0) >> 1)
	if page-1 > maxInt/pageSize {
		return nil, ErrInvalidPagination
	}

	// 3. 先验证项目是否存在且属于当前用户
	_, err := repository.GetProjectByID(
		ctx,
		s.db,
		userID,
		projectID,
	)

	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrProjectNotFound
		}

		return nil, fmt.Errorf(
			"check project ownership: %w",
			err,
		)
	}

	// 4. 查询项目内的 Conversation
	conversations, err := repository.ListProjectConversations(
		ctx,
		s.db,
		userID,
		projectID,
		page,
		pageSize,
	)

	if err != nil {
		if errors.Is(err, repository.ErrInvalidPagination) {
			return nil, ErrInvalidPagination
		}

		return nil, fmt.Errorf(
			"list project conversations: %w",
			err,
		)
	}

	// 5. Model 转换为 DTO
	results := make([]ConversationResult, 0, len(conversations))

	for _, conversation := range conversations {
		results = append(results, ConversationResult{
			ID:        conversation.ID,
			Title:     conversation.Title,
			ProjectID: conversation.ProjectID,
		})
	}

	return results, nil
}
