package worker

import (
	"context"
	"testing"
	"time"

	"GoServer/internal/model"
	"GoServer/internal/repository"
)

func TestRunner_DrainOnShutdown(t *testing.T) {
	for _, tc := range []struct {
		name  string
		grace time.Duration
		drain bool
	}{
		{"finishes within grace", time.Second, true},
		{"cancels when grace expires", 25 * time.Millisecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := setupWorkerIntegrationTx(t)
			job, _ := createWorkerIntegrationJob(t, tx)
			store := repository.NewImportJobStore(tx)
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			started := make(chan struct{})
			release := make(chan struct{})
			runner, err := NewRunner(targetJobStore{JobStore: store, jobID: job.ID}, ExecutorFunc(
				func(ctx context.Context, _ uint64) (model.ImportJobResult, error) {
					close(started)
					select {
					case <-release:
						return model.ImportJobResult{TreeSHA: job.RevisionSHA}, nil
					case <-ctx.Done():
						return model.ImportJobResult{}, ctx.Err()
					}
				}), "drain-test")
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- runner.Run(ctx, tc.grace) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("worker did not start")
			}
			stop()
			if tc.drain {
				close(release)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("worker did not exit after shutdown")
			}
			got, err := store.GetByID(t.Context(), job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.AttemptCount != 1 || got.LastErrorCode != nil {
				t.Fatalf("unexpected attempts or business failure: %+v", got)
			}
			if tc.drain {
				if got.Status != model.ImportJobSucceeded || got.ResultTreeSHA == nil {
					t.Fatalf("drained work was not persisted: %+v", got)
				}
				return
			}
			if got.Status != model.ImportJobRunning || len(got.LeaseToken) != 16 {
				t.Fatalf("work must remain recoverable: %+v", got)
			}
		})
	}
}
