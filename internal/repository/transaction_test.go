package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// The embedded pool methods are deliberately unavailable: these tests exercise
// transaction boundaries and must not execute queries or require a database.
type transactionPool struct {
	gorm.ConnPool
	beginErr error
	tx       *transactionConnection
}

func (p *transactionPool) BeginTx(context.Context, *sql.TxOptions) (gorm.ConnPool, error) {
	return p.tx, p.beginErr
}

type transactionConnection struct {
	gorm.ConnPool
	commit                func() error
	committed, rolledBack bool
}

func (t *transactionConnection) Commit() error {
	t.committed = true
	if t.commit != nil {
		return t.commit()
	}
	return nil
}

func (t *transactionConnection) Rollback() error { t.rolledBack = true; return nil }

func openErrorTestDB(t *testing.T, pool gorm.ConnPool) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestTransactionBeginAndCommitErrors(t *testing.T) {
	cause := errors.New("driver transaction failure")
	for _, stage := range []string{"begin", "commit"} {
		t.Run(stage, func(t *testing.T) {
			conn := &transactionConnection{}
			pool := &transactionPool{tx: conn}
			if stage == "begin" {
				pool.beginErr = cause
			} else {
				conn.commit = func() error { return cause }
			}
			called := false
			err := Transaction(context.Background(), openErrorTestDB(t, pool), func(*gorm.DB) error { called = true; return nil })
			if !errors.Is(err, ErrStorage) || !errors.Is(err, cause) {
				t.Fatalf("transaction cause lost: %v", err)
			}
			if called != (stage == "commit") {
				t.Fatalf("callback called = %v", called)
			}
			if stage == "commit" && !conn.rolledBack {
				t.Fatal("failed commit did not roll back")
			}
		})
	}
}

func TestTransactionCancellationDuringCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := &transactionConnection{commit: func() error { cancel(); return sql.ErrTxDone }}
	err := Transaction(ctx, openErrorTestDB(t, &transactionPool{tx: conn}), func(*gorm.DB) error { return nil })
	for _, cause := range []error{context.Canceled, sql.ErrTxDone, ErrStorage} {
		if !errors.Is(err, cause) {
			t.Fatalf("lost %v in %v", cause, err)
		}
	}
}

func TestTransactionBusinessFailureAndPreCommitCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		conn := &transactionConnection{}
		businessErr := errors.New("business rule rejected")
		err := Transaction(ctx, openErrorTestDB(t, &transactionPool{tx: conn}), func(*gorm.DB) error {
			if canceled {
				cancel()
				return nil
			}
			return businessErr
		})
		cancel()
		want := businessErr
		if canceled {
			want = context.Canceled
		}
		if !errors.Is(err, want) || errors.Is(err, ErrStorage) {
			t.Fatalf("business error changed: %v", err)
		}
		if conn.committed || !conn.rolledBack {
			t.Fatal("failed transaction was not rolled back before commit")
		}
	}
}
