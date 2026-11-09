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

	"golang.org/x/sync/singleflight"
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
	Delete(ctx context.Context, key string) error
}

type ProjectService struct {
	db     *gorm.DB
	cache  ProjectCache
	flight singleflight.Group
}

type cachedProject struct {
	UserID  uint64        `json:"user_id"`
	Project ProjectResult `json:"project"`
}

func projectCacheKey(userID, projectID uint64) string {
	return fmt.Sprintf("project:%d:%d", userID, projectID)
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

func (s *ProjectService) readProjectCache(
	ctx context.Context,
	key string,
	userID, projectID uint64,
) (*ProjectResult, bool) {
	if s.cache == nil {
		return nil, false
	}

	data, hit, err := s.cache.Get(ctx, key)
	if err != nil {
		log.Printf(
			"project cache GET failed: user_id=%d project_id=%d err=%v",
			userID, projectID, err,
		)
		return nil, false
	}

	if !hit {
		return nil, false
	}

	var cached cachedProject
	if err := json.Unmarshal(data, &cached); err == nil &&
		cached.UserID == userID &&
		cached.Project.ID == projectID {
		return &cached.Project, true
	}

	log.Printf(
		"project cache contains invalid data: user_id=%d project_id=%d",
		userID, projectID,
	)
	return nil, false
}

func (s *ProjectService) loadProjectFromDB(
	ctx context.Context,
	userID, projectID uint64,
) (*ProjectResult, error) {
	project, err := repository.GetProjectByID(
		ctx,
		s.db,
		userID,
		projectID,
	)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrProjectNotFound
		}

		return nil, fmt.Errorf("get project: %w", err)
	}

	return toProjectResult(project), nil
}

func (s *ProjectService) loadProjectFromDBWithCacheLock(
	ctx context.Context,
	userID, projectID uint64,
	key string,
) (*ProjectResult, error) {
	var result *ProjectResult

	err := s.db.WithContext(ctx).Transaction(
		func(tx *gorm.DB) error {
			// 1. 获取 MySQL 共享锁。
			project, err := repository.GetProjectForShare(
				ctx,
				tx,
				userID,
				projectID,
			)
			if err != nil {
				return err
			}

			result = toProjectResult(project)

			// 2. 序列化缓存数据。
			data, err := json.Marshal(cachedProject{
				UserID:  userID,
				Project: *result,
			})
			if err != nil {
				log.Printf(
					"project cache marshal failed: %v",
					err,
				)
				return nil
			}

			// 3. 必须在共享锁释放前完成 Redis SET。
			if err := s.cache.Set(ctx, key, data); err != nil {
				log.Printf(
					"project cache SET failed: "+
						"user_id=%d project_id=%d err=%v",
					userID,
					projectID,
					err,
				)
			}

			return nil
		},
	)

	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrProjectNotFound
		}

		return nil, fmt.Errorf(
			"get project with cache lock: %w",
			err,
		)
	}

	return result, nil
}

func (s *ProjectService) GetProject(
	ctx context.Context,
	userID, projectID uint64,
) (*ProjectResult, error) {
	if userID == 0 || projectID == 0 {
		return nil, ErrInvalidProjectInput
	}
	//无缓存
	key := projectCacheKey(userID, projectID)
	if s.cache == nil {
		return s.loadProjectFromDB(
			ctx, userID, projectID,
		)
	}

	if result, hit := s.readProjectCache(
		ctx, key, userID, projectID,
	); hit {
		return result, nil
	}

	//redis miss
	resultCh := s.flight.DoChan(key, func() (any, error) {
		// 共享查询使用独立生命周期
		workCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			2*time.Second,
		)
		defer cancel()

		// Double Check
		if result, hit := s.readProjectCache(
			workCtx, key, userID, projectID,
		); hit {
			return result, nil
		}

		return s.loadProjectFromDBWithCacheLock(
			workCtx,
			userID,
			projectID,
			key,
		)
	})

	// 每个请求独立决定是否继续等待
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return nil, result.Err
		}
		return result.Val.(*ProjectResult), nil
	}

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

func (s *ProjectService) UpdateProjectName(
	ctx context.Context,
	userID, projectID uint64,
	newName string,
) (*ProjectResult, error) {
	name := strings.TrimSpace(newName)

	if userID == 0 || projectID == 0 || name == "" || utf8.RuneCountInString(name) > 128 {
		return nil, ErrInvalidProjectInput
	}

	var project *model.Project

	err := s.db.WithContext(ctx).Transaction(
		func(tx *gorm.DB) error {
			// 1. 获取排他锁并更新 MySQL。
			var err error
			project, err = repository.UpdateProjectNameInTx(
				ctx,
				tx,
				userID,
				projectID,
				name,
			)
			if err != nil {
				return err
			}

			// 2. MySQL 事务尚未提交，排他锁仍然持有。
			if s.cache != nil {
				key := projectCacheKey(userID, projectID)

				invalidateCtx, cancel := context.WithTimeout(
					context.WithoutCancel(ctx),
					2*time.Second,
				)
				defer cancel()

				// 3. Redis 删除失败，直接回滚 MySQL。
				if err := s.cache.Delete(
					invalidateCtx,
					key,
				); err != nil {
					log.Printf(
						"project cache DELETE failed: "+
							"user_id=%d project_id=%d "+
							"db_committed=false err=%v",
						userID,
						projectID,
						err,
					)

					return fmt.Errorf(
						"project cache invalidation failed: %w",
						err,
					)
				}
			}

			// 4. 返回 nil，GORM 才会提交事务。
			return nil
		},
	)

	if err != nil {
		switch {
		case errors.Is(err, repository.ErrNotFound):
			return nil, ErrProjectNotFound

		case errors.Is(err, repository.ErrAlreadyExists):
			return nil, ErrProjectAlreadyExists

		default:
			return nil, fmt.Errorf(
				"update project name: %w",
				err,
			)
		}
	}

	return toProjectResult(project), nil
}

func toProjectResult(
	project *model.Project,
) *ProjectResult {
	return &ProjectResult{
		ID:          project.ID,
		Name:        project.Name,
		Description: project.Description,
		Status:      project.Status,
		CreatedAt:   project.CreatedAt,
		UpdatedAt:   project.UpdatedAt,
	}
}
