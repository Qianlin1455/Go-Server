package service

import (
	"GoServer/internal/repository"
	"context"
	"errors"
	"time"
)

type TaskAuditResult struct {
	ID          uint64    `json:"id"`
	FromStatus  string    `json:"from_status"`
	ToStatus    string    `json:"to_status"`
	FromVersion uint32    `json:"from_version"`
	ToVersion   uint32    `json:"to_version"`
	CreatedAt   time.Time `json:"created_at"`
}

type TaskRetrospective struct {
	Task     *TaskResult       `json:"task"`
	Source   *MessageResult    `json:"source"`
	Evidence []EvidenceResult  `json:"evidence"`
	Audits   []TaskAuditResult `json:"audits"`
}

// Read-only recap of persisted facts; no Review decision or AI evaluation.
// Separate reads can observe concurrent writes at different snapshots.
func (s *TaskService) Retrospective(ctx context.Context, userID, taskID uint64) (*TaskRetrospective, error) {
	if userID == 0 || taskID == 0 {
		return nil, ErrInvalidTaskInput
	}
	task, err := repository.GetTaskByUserID(ctx, s.db, userID, taskID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrTaskNotFound
		}
		return nil, err
	}
	if _, err := repository.GetProjectByID(ctx, s.db, userID, task.ProjectID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrTaskNotFound
		}
		return nil, err
	}
	result := &TaskRetrospective{Task: toTaskResult(task), Evidence: make([]EvidenceResult, 0), Audits: make([]TaskAuditResult, 0)}
	if task.SourceMessageID != nil {
		source, err := repository.GetTaskSourceByUser(ctx, s.db, userID, task.ProjectID, *task.SourceMessageID)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return nil, err
		}
		if source != nil {
			result.Source = toMessageResult(source)
		}
	}
	evidence, err := repository.ListTaskEvidenceByUser(ctx, s.db, userID, taskID)
	if err != nil {
		return nil, err
	}
	for _, e := range evidence {
		result.Evidence = append(result.Evidence, EvidenceResult{ID: e.ID, TaskID: e.TaskID, Type: e.Type, Text: e.Text, CommitSHA: e.CommitSHA, Status: e.Status, CreatedAt: e.CreatedAt})
	}
	audits, err := repository.ListTaskAuditsByUser(ctx, s.db, userID, taskID)
	if err != nil {
		return nil, err
	}
	for _, a := range audits {
		result.Audits = append(result.Audits, TaskAuditResult{ID: a.ID, FromStatus: a.FromStatus, ToStatus: a.ToStatus, FromVersion: a.FromVersion, ToVersion: a.ToVersion, CreatedAt: a.CreatedAt})
	}
	return result, nil
}
