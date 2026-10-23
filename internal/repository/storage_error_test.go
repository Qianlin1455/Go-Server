package repository

import (
	"context"
	"errors"
	"testing"

	"GoServer/internal/model"
	driver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// Inject errors at GORM's execution boundary to verify every repository keeps
// both the storage classification and the original typed driver error.
func TestRepositoryStorageCauses(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name, operation string
		call            func(*gorm.DB) error
	}{
		{"create project", "create", func(db *gorm.DB) error { return CreateProject(ctx, db, &model.Project{}) }},
		{"get project", "query", func(db *gorm.DB) error { _, e := GetProjectByID(ctx, db, 1, 2); return e }},
		{"lock project", "query", func(db *gorm.DB) error { _, e := GetProjectForUpdate(ctx, db, 1, 2); return e }},
		{"list projects", "query", func(db *gorm.DB) error { _, e := ListProjects(ctx, db, 1, 1, 10); return e }},
		{"create user", "create", func(db *gorm.DB) error { return CreateUser(ctx, db, &model.Users{}) }},
		{"get user id", "query", func(db *gorm.DB) error { _, e := GetUserByID(ctx, db, 1); return e }},
		{"get user name", "query", func(db *gorm.DB) error { _, e := GetUserByName(ctx, db, "name"); return e }},
		{"get user email", "query", func(db *gorm.DB) error { _, e := GetUserByEmail(ctx, db, "a@b.test"); return e }},
		{"list users", "query", func(db *gorm.DB) error { _, e := ListUser(ctx, db); return e }},
		{"create conversation", "create", func(db *gorm.DB) error { return CreateConversation(ctx, db, &model.Conversation{}) }},
		{"get conversation", "query", func(db *gorm.DB) error { _, e := GetConversationByID(ctx, db, 1, 2); return e }},
		{"list conversations", "query", func(db *gorm.DB) error { _, e := ListConversation(ctx, db, 1); return e }},
		{"list project conversations", "query", func(db *gorm.DB) error { _, e := ListProjectConversations(ctx, db, 1, 2, 1, 10); return e }},
		{"touch conversation", "update", func(db *gorm.DB) error { return TouchConversation(ctx, db, 1) }},
		{"conversation owner", "query", func(db *gorm.DB) error { _, e := GetConversationOwnerID(ctx, db, 1); return e }},
		{"lock conversation owner", "query", func(db *gorm.DB) error { _, e := GetConversationOwnerForUpdate(ctx, db, 1); return e }},
		{"create message", "create", func(db *gorm.DB) error { return CreateMessage(ctx, db, &model.Message{}) }},
		{"get message", "query", func(db *gorm.DB) error { _, e := GetMessageByID(ctx, db, 1, 2, 3); return e }},
		{"list messages", "query", func(db *gorm.DB) error { _, e := ListMessages(ctx, db, 1, 2, 1, 10); return e }},
		{"source message", "query", func(db *gorm.DB) error { _, e := SourceMessageBelongsToProject(ctx, db, 1, 2, 3); return e }},
		{"create task", "create", func(db *gorm.DB) error { return CreateTask(ctx, db, &model.Task{}) }},
		{"get task", "query", func(db *gorm.DB) error { _, e := GetTaskByID(ctx, db, 1, 2, 3); return e }},
		{"get user task", "query", func(db *gorm.DB) error { _, e := GetTaskByUserID(ctx, db, 1, 2); return e }},
		{"list tasks", "query", func(db *gorm.DB) error { _, e := ListTasks(ctx, db, 1, 2, 1, 10); return e }},
		{"update task", "update", func(db *gorm.DB) error {
			_, e := UpdateTaskStatusCAS(ctx, db, 1, 2, 3, "todo", "doing", 1, nil)
			return e
		}},
		{"create evidence", "create", func(db *gorm.DB) error {
			return CreateTaskEvidence(ctx, db, &model.TaskEvidence{TaskID: 1, CreatedBy: 1})
		}},
		{"count evidence", "query", func(db *gorm.DB) error { _, e := CountSubmittedTaskEvidence(ctx, db, 1); return e }},
		{"create audit", "create", func(db *gorm.DB) error { return CreateTaskStatusAudit(ctx, db, &model.TaskStatusAudit{}) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := openErrorTestDB(t, &transactionPool{})
			cause := &driver.MySQLError{Number: 1105, Message: "private storage cause"}
			inject := func(tx *gorm.DB) { tx.AddError(cause) }
			var err error
			switch tt.operation {
			case "create":
				err = db.Callback().Create().Before("gorm:create").Register("test:storage", inject)
			case "query":
				err = db.Callback().Query().Before("gorm:query").Register("test:storage", inject)
			case "update":
				err = db.Callback().Update().Before("gorm:update").Register("test:storage", inject)
			}
			if err != nil {
				t.Fatal(err)
			}
			err = tt.call(db)
			var got *driver.MySQLError
			if !errors.Is(err, ErrStorage) || !errors.As(err, &got) || got != cause {
				t.Fatalf("storage cause lost: %v", err)
			}
		})
	}
}
