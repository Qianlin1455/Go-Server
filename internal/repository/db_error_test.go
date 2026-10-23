package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func TestDatabaseErrorClassificationAndCause(t *testing.T) {
	for _, tt := range []struct {
		name        string
		cause, kind error
	}{
		{"not found", gorm.ErrRecordNotFound, ErrNotFound},
		{"duplicate", gorm.ErrDuplicatedKey, ErrAlreadyExists},
		{"foreign key", gorm.ErrForeignKeyViolated, ErrInvalidReference},
		{"driver failure", &mysql.MySQLError{Number: 1105, Message: "private diagnostic"}, ErrStorage},
		{"deadline", context.DeadlineExceeded, ErrStorage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := databaseError(context.Background(), "test query", fmt.Errorf("driver: %w", tt.cause))
			if !errors.Is(err, tt.kind) || !errors.Is(err, tt.cause) {
				t.Fatalf("classification or cause lost: %v", err)
			}
			if original, ok := tt.cause.(*mysql.MySQLError); ok {
				var got *mysql.MySQLError
				if !errors.As(err, &got) || got != original {
					t.Fatal("typed MySQL error lost")
				}
			}
		})
	}
	if err := databaseError(context.Background(), "success", nil); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseErrorPreservesDeadlineAndDriverFailure(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	err := databaseError(ctx, "commit", sql.ErrTxDone)
	for _, cause := range []error{context.DeadlineExceeded, sql.ErrTxDone, ErrStorage} {
		if !errors.Is(err, cause) {
			t.Fatalf("lost %v in %v", cause, err)
		}
	}
}
