package service

import (
	"GoServer/internal/model"
	"GoServer/internal/repository"
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	return s.db.WithContext(ctx).Transaction(
		func(tx *gorm.DB) error {
			var task model.Task
			err := tx.Model(&model.Task{}).
				Select("id", "user_id", "project_id", "status", "version", "completed_at").
				Where("id = ? AND user_id = ?", taskID, userID).
				First(&task).Error
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrTaskNotFound
				}
				return fmt.Errorf("query task: %w", err)
			}

			var proj model.Project
			err = tx.Model(&model.Project{}).
				Select("id", "user_id", "status").
				Where("id = ? AND user_id = ?", task.ProjectID, userID).
				First(&proj).Error
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrTaskNotFound
				}
				return fmt.Errorf("query project: %w", err)
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

	err := s.db.WithContext(ctx).Transaction(
		func(tx *gorm.DB) error {

			// 2. 查询 Project，校验用户归属
			var project model.Project

			err := tx.
				Clauses(clause.Locking{
					Strength: "UPDATE",
				}).
				Where(
					"id = ? AND user_id = ?",
					input.ProjectID,
					userID,
				).
				First(&project).Error

			if err != nil {
				if errors.Is(
					err, gorm.ErrRecordNotFound,
				) {
					return ErrTaskNotFound
				}
				return fmt.Errorf(
					"query project: %w", err,
				)
			}

			// 3. 归档项目禁止创建任务
			if project.Status != model.ProjectStatusActive {
				return ErrTaskProjectArchived
			}

			// 4. 可选：验证来源消息的归属
			if input.SourceMessageID != nil {
				var count int64

				err := tx.Model(&model.Message{}).
					Joins(
						"INNER JOIN conversations "+
							"ON conversations.id = "+
							"messages.conversation_id",
					).
					Where(
						"messages.id = ? AND "+
							"conversations.user_id = ? AND "+
							"conversations.project_id = ?",
						*input.SourceMessageID,
						userID,
						input.ProjectID,
					).
					Count(&count).Error

				if err != nil {
					return fmt.Errorf(
						"verify source message: %w",
						err,
					)
				}

				if count != 1 {
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
