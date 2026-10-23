package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"GoServer/internal/model"
	"GoServer/internal/repository"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func TestServiceQueryStorageErrors(t *testing.T) {
	for _, tt := range []struct{ name, table, operation string }{
		{"change task query", "tasks", "change"},
		{"change project query", "projects", "change"},
		{"submitted evidence count", "task_evidence", "change"},
		{"evidence task query", "tasks", "evidence"},
		{"evidence project lock", "projects", "evidence"},
		{"create task project lock", "projects", "create"},
		{"source message query", "messages", "create"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, user, task := newTaskStatusTestFixture(t)
			if err := db.Model(task).Update("status", model.TaskStatusDoing).Error; err != nil {
				t.Fatal(err)
			}
			cause := &mysql.MySQLError{Number: 1105, Message: "private query failure"}
			if err := db.Callback().Query().Before("gorm:query").Register("test:query_failure", func(tx *gorm.DB) {
				if tx.Statement.Table == tt.table {
					tx.AddError(cause)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Query().Remove("test:query_failure") })
			var err error
			switch tt.operation {
			case "change":
				err = NewTaskService(db).ChangeStatus(t.Context(), user.ID, task.ID, model.TaskStatusDone, 1)
			case "evidence":
				_, err = NewEvidenceService(db).CreateEvidence(t.Context(), user.ID, task.ID, CreateEvidenceInput{Type: model.EvidenceTypeTestOutput, Status: model.EvidenceStatusDraft})
			case "create":
				messageID := uint64(1)
				_, err = NewTaskService(db).CreateTask(t.Context(), user.ID, CreateTaskInput{ProjectID: task.ProjectID, Title: "query error", SourceMessageID: &messageID})
			}
			var got *mysql.MySQLError
			if !errors.Is(err, repository.ErrStorage) || !errors.As(err, &got) || got != cause {
				t.Fatalf("service lost repository cause: %v", err)
			}
		})
	}
}

func TestServiceQueryDeadlinePropagation(t *testing.T) {
	for _, operation := range []string{"change", "evidence", "create"} {
		t.Run(operation, func(t *testing.T) {
			db, user, task := newTaskStatusTestFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			if err := db.Callback().Query().Before("gorm:query").Register("test:query_timeout", func(tx *gorm.DB) {
				<-ctx.Done()
				tx.AddError(errors.New("driver stopped after deadline"))
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Query().Remove("test:query_timeout") })
			var err error
			switch operation {
			case "change":
				err = NewTaskService(db).ChangeStatus(ctx, user.ID, task.ID, model.TaskStatusDoing, 1)
			case "evidence":
				_, err = NewEvidenceService(db).CreateEvidence(ctx, user.ID, task.ID, CreateEvidenceInput{Type: model.EvidenceTypeTestOutput, Status: model.EvidenceStatusDraft})
			case "create":
				_, err = NewTaskService(db).CreateTask(ctx, user.ID, CreateTaskInput{ProjectID: task.ProjectID, Title: "deadline"})
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("service lost deadline: %v", err)
			}
		})
	}
}
