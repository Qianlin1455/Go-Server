package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	ErrProjectNotFound      = errors.New("project not found")
)

type ProjectCache interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte) error
}

type ProjectService struct {
	db    *gorm.DB
	cache ProjectCache
}

func NewProjectService(db *gorm.DB) *ProjectService {
	return &ProjectService{db: db}
}

func NewProjectServiceWithCache(
	db *gorm.DB,
	projectCache ProjectCache,
) *ProjectService {
	return &ProjectService{
		db:    db,
		cache: projectCache,
	}
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

func (s *ProjectService) GetProject(
	ctx context.Context,
	userID uint64,
	projectID uint64,
) (*ProjectResult, error) {
	if userID == 0 || projectID == 0 {
		return nil, ErrInvalidProjectInput
	}

	// 必须同时包含 userID 和 projectID。
	key := fmt.Sprintf("project:%d:%d", userID, projectID)

	// 1. 优先读取 Redis。
	if s.cache != nil {
		data, hit, err := s.cache.Get(ctx, key)

		if err != nil {
			log.Printf(
				"project cache GET failed: user_id=%d project_id=%d err=%v",
				userID, projectID, err,
			)
		} else if hit {
			var cached ProjectResult

			if err := json.Unmarshal(data, &cached); err == nil &&
				cached.ID == projectID {
				return &cached, nil
			}

			log.Printf(
				"project cache contains invalid data: user_id=%d project_id=%d",
				userID, projectID,
			)
		}
	}

	// 2. 缓存未命中或异常，查询 MySQL。
	project, err := repository.GetProjectByID(
		ctx, s.db, userID, projectID,
	)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrProjectNotFound
		}
		return nil, fmt.Errorf("get project: %w", err)
	}

	result := &ProjectResult{
		ID:          project.ID,
		Name:        project.Name,
		Description: project.Description,
		Status:      project.Status,
		CreatedAt:   project.CreatedAt,
		UpdatedAt:   project.UpdatedAt,
	}

	// 3. 回填缓存，写入失败不影响数据库查询结果。
	if s.cache != nil {
		data, err := json.Marshal(result)
		if err != nil {
			log.Printf("project cache marshal failed: %v", err)
		} else if err := s.cache.Set(ctx, key, data); err != nil {
			log.Printf(
				"project cache SET failed: user_id=%d project_id=%d err=%v",
				userID, projectID, err,
			)
		}
	}

	return result, nil
}

func (s *ProjectService) ListProjects(
	ctx context.Context,
	userID uint64,
	page int,
	pageSize int,
) ([]ProjectResult, error) {

	// 1. 校验用户身份
	if userID == 0 {
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

	// 3. Repository 查询
	projects, err := repository.ListProjects(
		ctx,
		s.db,
		userID,
		page,
		pageSize,
	)

	if err != nil {
		if errors.Is(err, repository.ErrInvalidPagination) {
			return nil, ErrInvalidPagination
		}
		return nil, fmt.Errorf("list projects: %w", err)
	}

	// 4. Model 转 DTO
	result := make([]ProjectResult, 0, len(projects))

	for _, p := range projects {
		result = append(result, ProjectResult{
			ID:          p.ID,
			Name:        p.Name,
			Description: p.Description,
			Status:      p.Status,
			CreatedAt:   p.CreatedAt,
			UpdatedAt:   p.UpdatedAt,
		})
	}

	return result, nil
}
