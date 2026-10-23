package service

import (
	"GoServer/internal/model"
	"GoServer/internal/repository"
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrTaskNotFound             = errors.New("Task Not Fouond")
	ErrTaskVersionConflict      = errors.New("Task Version conflict")
	ErrInvalidTaskTransition    = errors.New("Task Transition invalid")
	ErrInvalidTaskInput         = errors.New("invalid task input")
	ErrTaskAlreadyExists        = errors.New("task already exists")
	ErrTaskProjectArchived      = errors.New("Task project has been archived")
	ErrTaskSourceMessageInvalid = errors.New("invalid task source message")
	ErrTaskEvidenceRequired     = errors.New("task requires submitted evidence")
)

type TaskService struct {
	db *gorm.DB
}

func NewTaskService(db *gorm.DB) *TaskService {
	return &TaskService{db: db}
}

type CreateTaskInput struct {
	ProjectID       uint64
	Title           string
	Description     *string
	SourceMessageID *uint64
}
type TaskResult struct {
	ID              uint64  `json:"id"`
	ProjectID       uint64  `json:"project_id"`
	SourceMessageID *uint64 `json:"source_message_id"`

	Title       string  `json:"title"`
	Description *string `json:"description"`

	Status      string     `json:"status"`
	Version     uint32     `json:"version"`
	CompletedAt *time.Time `json:"completed_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toTaskResult(task *model.Task) *TaskResult {
	if task == nil {
		return nil
	}

	return &TaskResult{
		ID:              task.ID,
		ProjectID:       task.ProjectID,
		SourceMessageID: task.SourceMessageID,

		Title:       task.Title,
		Description: task.Description,

		Status:      task.Status,
		Version:     task.Version,
		CompletedAt: task.CompletedAt,

		CreatedAt: task.CreatedAt,
		UpdatedAt: task.UpdatedAt,
	}
}

func (s *TaskService) ChangeStatus(
	ctx context.Context,
	userID uint64,
	taskID uint64,
	toStatus string,
	expectedVersion uint32,
) error {
	if userID == 0 || taskID == 0 || expectedVersion == 0 {
		return ErrInvalidTaskInput
	}
	switch toStatus {
	case model.TaskStatusTodo,
		model.TaskStatusDoing,
		model.TaskStatusDone:
		// 合法目标状态
	default:
		return ErrInvalidTaskInput
	}
	return repository.Transaction(ctx, s.db,
		func(tx *gorm.DB) error {
			task, err := repository.GetTaskByUserID(ctx, tx, userID, taskID)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return ErrTaskNotFound
				}
				return err
			}

			proj, err := repository.GetProjectByID(ctx, tx, userID, task.ProjectID)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return ErrTaskNotFound
				}
				return err
			}
			if proj.Status != model.ProjectStatusActive {
				return ErrTaskProjectArchived
			}

			if task.Version != expectedVersion {
				return ErrTaskVersionConflict
			}

			if !CanTransition(task.Status, toStatus) {
				return ErrInvalidTaskTransition
			}

			var completedAt *time.Time

			if toStatus == model.TaskStatusDone {
				now := time.Now().UTC()
				completedAt = &now
			}
			// 进入 done 前，必须至少存在一条正式提交的证据
			if toStatus == model.TaskStatusDone {
				count, err := repository.CountSubmittedTaskEvidence(ctx, tx, task.ID)
				if err != nil {
					return err
				}

				if count == 0 {
					return ErrTaskEvidenceRequired
				}
			}

			rowsAffected, err := repository.UpdateTaskStatusCAS(ctx, tx, userID, proj.ID, taskID, task.Status, toStatus, expectedVersion, completedAt)
			if err != nil {
				return err
			}

			if rowsAffected != 1 {
				return ErrTaskVersionConflict
			}

			audit := &model.TaskStatusAudit{
				TaskID:      task.ID,
				ActorUserID: userID,
				FromStatus:  task.Status,
				ToStatus:    toStatus,
				FromVersion: task.Version,
				ToVersion:   task.Version + 1,
			}

			if err := repository.CreateTaskStatusAudit(ctx, tx, audit); err != nil {
				return fmt.Errorf("create task status audit:%w", err)
			}
			return nil
		},
	)
}

func (s *TaskService) CreateTask(
	ctx context.Context,
	userID uint64,
	input CreateTaskInput,
) (*TaskResult, error) {

	// 1. 基础参数校验
	title := strings.TrimSpace(input.Title)

	if userID == 0 ||
		input.ProjectID == 0 ||
		title == "" ||
		!utf8.ValidString(title) ||
		utf8.RuneCountInString(title) > 200 {
		return nil, ErrInvalidTaskInput
	}

	if input.SourceMessageID != nil &&
		*input.SourceMessageID == 0 {
		return nil, ErrInvalidTaskInput
	}

	var created *TaskResult

	err := repository.Transaction(ctx, s.db,
		func(tx *gorm.DB) error {

			// 2. 查询 Project，校验用户归属
			project, err := repository.GetProjectForUpdate(ctx, tx, userID, input.ProjectID)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return ErrTaskNotFound
				}
				return err
			}

			// 3. 归档项目禁止创建任务
			if project.Status != model.ProjectStatusActive {
				return ErrTaskProjectArchived
			}

			// 4. 可选：验证来源消息的归属
			if input.SourceMessageID != nil {
				valid, err := repository.SourceMessageBelongsToProject(ctx, tx, userID, input.ProjectID, *input.SourceMessageID)
				if err != nil {
					return err
				}
				if !valid {
					return ErrTaskSourceMessageInvalid
				}
			}

			// 5. 构造任务模型
			task := &model.Task{
				UserID:          userID,
				ProjectID:       input.ProjectID,
				SourceMessageID: input.SourceMessageID,
				Title:           title,
				Description:     input.Description,
				Status:          model.TaskStatusTodo,
				Version:         1,
				CompletedAt:     nil,
			}

			// 6. Repository 创建任务
			if err := repository.CreateTask(
				ctx, tx, task,
			); err != nil {
				if errors.Is(
					err, repository.ErrInvalidReference,
				) {
					return ErrInvalidTaskInput
				}

				return fmt.Errorf(
					"create task: %w", err,
				)
			}

			// 7. Model 转业务 DTO
			created = toTaskResult(task)
			return nil
		},
	)

	// 8. 确保事务提交成功后才返回结果
	if err != nil {
		return nil, err
	}

	return created, nil
}

func (s *TaskService) ListTasks(
	ctx context.Context,
	userID uint64,
	projectID uint64,
	page int,
	pageSize int,
) ([]TaskResult, error) {

	// 1. 基础参数校验
	if userID == 0 || projectID == 0 {
		return nil, ErrInvalidTaskInput
	}

	// 2. 分页参数校验
	if page < 1 || pageSize < 1 || pageSize > 100 {
		return nil, ErrInvalidPagination
	}

	// 3. OFFSET 溢出保护
	maxInt := int(^uint(0) >> 1)

	if page-1 > maxInt/pageSize {
		return nil, ErrInvalidPagination
	}

	// 4. 校验项目存在且属于当前用户
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
			"get project: %w", err,
		)
	}

	// 注意：归档项目仍然允许查询任务

	// 5. 查询任务列表
	tasks, err := repository.ListTasks(
		ctx,
		s.db,
		userID,
		projectID,
		page,
		pageSize,
	)

	if err != nil {
		if errors.Is(
			err, repository.ErrInvalidPagination,
		) {
			return nil, ErrInvalidPagination
		}

		return nil, fmt.Errorf(
			"list tasks: %w", err,
		)
	}

	// 6. Model 转 DTO
	result := make([]TaskResult, 0, len(tasks))

	for i := range tasks {
		result = append(
			result,
			*toTaskResult(&tasks[i]),
		)
	}

	return result, nil
}
