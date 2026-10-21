package service

import (
	"GoServer/internal/model"
	"GoServer/internal/repository"
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

var (
	ErrTaskNotFound          = errors.New("Task Not Fouond")
	ErrTaskVersionConflict   = errors.New("Task Version conflict")
	ErrInvalidTaskTransition = errors.New("Task Transition invalid")
	ErrInvalidTaskInput      = errors.New("invalid task input")
	ErrTaskProjectArchived   = errors.New("Task project has been archived")
)

type TaskService struct {
	db *gorm.DB
}

func NewTaskService(db *gorm.DB) *TaskService {
	return &TaskService{db: db}
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
