package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"GoServer/internal/model"
	"GoServer/internal/repository"

	"gorm.io/gorm"
)

var (
	ErrInvalidProjectInput  = errors.New("invalid project input")
	ErrProjectAlreadyExists = errors.New("project already exists")
)

type ProjectService struct {
	db *gorm.DB
}

func NewProjectService(db *gorm.DB) *ProjectService {
	return &ProjectService{db: db}
}

type CreateProjectInput struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

type ProjectResult struct {
	ID          uint64    `json:"id"`
	Name        string    `json:"name"`
	Description *string   `json:"description"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (s *ProjectService) CreateProject(
	ctx context.Context,
	userID uint64,
	input CreateProjectInput,
) (*ProjectResult, error) {
	name := strings.TrimSpace(input.Name)

	// 1. 校验调用者和项目名称。
	if userID == 0 ||
		name == "" ||
		utf8.RuneCountInString(name) > 128 {
		return nil, ErrInvalidProjectInput
	}

	// 2. 构造数据库模型。
	project := &model.Project{
		UserID:      userID,
		Name:        name,
		Description: input.Description,
		Status:      model.ProjectStatusActive,
	}

	// 3. 调用 Repository 写入数据库。
	if err := repository.CreateProject(
		ctx,
		s.db,
		project,
	); err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			return nil, ErrProjectAlreadyExists
		}

		return nil, fmt.Errorf("create project: %w", err)
	}

	// 4. 返回允许对外暴露的字段。
	return &ProjectResult{
		ID:          project.ID,
		Name:        project.Name,
		Description: project.Description,
		Status:      project.Status,
		CreatedAt:   project.CreatedAt,
		UpdatedAt:   project.UpdatedAt,
	}, nil
}
