package worker

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"GoServer/internal/model"
	"GoServer/internal/repository"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type crashTestJobStore struct {
	targetJobStore
	lease time.Duration
}

func (s crashTestJobStore) ClaimQueuedJob(ctx context.Context, id uint64, worker string, _ time.Duration) ([]byte, bool, error) {
	return s.JobStore.ClaimQueuedJob(ctx, id, worker, s.lease)
}

// This entry point runs inside the process that the parent forcibly terminates.
func TestWorkerCrashChild(t *testing.T) {
	raw := os.Getenv("GO_SERVER_CRASH_CHILD_JOB")
	if raw == "" {
		return
	}
	jobID, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	db := openCrashTestDB(t)
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	base := repository.NewImportJobStore(db)
	var store JobStore = crashTestJobStore{targetJobStore{JobStore: base, jobID: jobID}, 4 * time.Second}
	if os.Getenv("GO_SERVER_CRASH_CHILD_DRAIN") != "" {
		store = base
	}
	inTransaction := os.Getenv("GO_SERVER_CRASH_CHILD_SNAPSHOT_READY")
	if inTransaction != "" {
		err := db.Callback().Create().After("gorm:create").Register("test:pause_snapshot", func(tx *gorm.DB) {
			if tx.Statement.Table == "source_spans" && tx.Error == nil {
				if err := os.WriteFile(inTransaction, nil, 0600); err != nil {
					tx.AddError(err)
					return
				}
				<-tx.Statement.Context.Done()
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	runner, err := NewRunner(store, ExecutorFunc(func(ctx context.Context, _ uint64) (model.ImportJobResult, error) {
		if inTransaction != "" {
			job, err := base.GetByID(ctx, jobID)
			if err != nil {
				return model.ImportJobResult{}, err
			}
			return crashSnapshot(job, "uncommitted\n"), nil
		}
		<-ctx.Done()
		return model.ImportJobResult{}, ctx.Err()
	}), "crash-child")
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestRunner_ForcedProcessExitRecovery(t *testing.T) {
	db := openCrashTestDB(t)
	store := repository.NewImportJobStore(db)
	for _, tc := range []struct {
		name          string
		maxAttempts   uint16
		terminate     bool
		inTransaction bool
	}{
		{"crash with attempts remaining", 3, false, false},
		{"crash with attempts exhausted", 1, false, false},
		{"SIGTERM stops claiming", 3, true, false},
		{"crash before snapshot commit", 3, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.terminate && runtime.GOOS == "windows" {
				t.Skip("Windows os.Process.Signal cannot send SIGTERM to another process")
			}
			maxAttempts := tc.maxAttempts
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
			defer cancel()
			var job, next *model.ImportJob
			if err := db.Transaction(func(tx *gorm.DB) error {
				job, _ = createWorkerIntegrationJob(t, tx)
				if tc.terminate {
					next, _ = createWorkerIntegrationJob(t, tx)
				}
				return tx.Model(job).UpdateColumn("max_attempts", maxAttempts).Error
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				for _, fixture := range []*model.ImportJob{job, next} {
					if fixture == nil {
						continue
					}
					for _, record := range []struct {
						table string
						id    uint64
					}{
						{"import_jobs", fixture.ID}, {"repositories", fixture.RepositoryID},
						{"projects", fixture.ProjectID}, {"users", fixture.UserID},
					} {
						if err := db.Table(record.table).Where("id = ?", record.id).Delete(nil).Error; err != nil {
							t.Errorf("cleanup %s: %v", record.table, err)
						}
					}
				}
			})
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.CommandContext(ctx, executable, "-test.run=^TestWorkerCrashChild$")
			child.Env = append(os.Environ(), "GO_SERVER_CRASH_CHILD_JOB="+strconv.FormatUint(job.ID, 10))
			if tc.terminate {
				child.Env = append(child.Env, "GO_SERVER_CRASH_CHILD_DRAIN=1")
			}
			ready := filepath.Join(t.TempDir(), "source-inserted")
			if tc.inTransaction {
				child.Env = append(child.Env, "GO_SERVER_CRASH_CHILD_SNAPSHOT_READY="+ready)
			}
			child.Stdout = io.Discard
			child.Stderr = os.Stderr
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if child.ProcessState == nil {
					_ = child.Process.Kill()
					_ = child.Wait()
				}
			}()

			var oldToken []byte
			tick := time.NewTicker(25 * time.Millisecond)
			defer tick.Stop()
			for len(oldToken) == 0 {
				got, err := store.GetByID(ctx, job.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status == model.ImportJobRunning {
					oldToken = got.LeaseToken
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("child never claimed job")
				case <-tick.C:
				}
			}
			if tc.terminate {
				if err := child.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				if err := child.Wait(); err != nil {
					t.Fatalf("worker did not exit cleanly after SIGTERM: %v", err)
				}
				active, err := store.GetByID(ctx, job.ID)
				if err != nil || active.Status != model.ImportJobRunning || active.AttemptCount != 1 || active.LastErrorCode != nil {
					t.Fatalf("grace expiry must leave the active lease for recovery: job=%+v err=%v", active, err)
				}
				unclaimed, err := store.GetByID(ctx, next.ID)
				if err != nil || unclaimed.Status != model.ImportJobQueued || unclaimed.AttemptCount != 0 {
					t.Fatalf("new task claimed after SIGTERM: job=%+v err=%v", unclaimed, err)
				}
				return
			}
			if tc.inTransaction {
				for {
					if _, err := os.Stat(ready); err == nil {
						break
					} else if !errors.Is(err, os.ErrNotExist) {
						t.Fatal(err)
					}
					select {
					case <-ctx.Done():
						t.Fatal("child did not write source spans")
					case <-tick.C:
					}
				}
				assertCrashSnapshot(t, db, job.ID, "")
			}
			// Process.Kill uses SIGKILL on Unix and forced termination on Windows.
			if err := child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			var exitErr *exec.ExitError
			if err := child.Wait(); !errors.As(err, &exitErr) {
				t.Fatalf("child did not exit abnormally: %v", err)
			}
			got, err := store.GetByID(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != model.ImportJobRunning || got.AttemptCount != 1 || got.LastErrorCode != nil {
				t.Fatalf("killed process changed failure state: %+v", got)
			}
			if tc.inTransaction {
				assertCrashSnapshot(t, db, job.ID, "")
			}

			wanted := model.ImportJobQueued
			if maxAttempts == 1 {
				wanted = model.ImportJobFailed
			}
			for {
				if _, _, err := store.RecoverExpiredJobs(ctx); err != nil {
					t.Fatal(err)
				}
				got, err = store.GetByID(ctx, job.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status == wanted {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatalf("lease was not recovered: %+v", got)
				case <-tick.C:
				}
			}
			if got.AttemptCount != 1 || len(got.LeaseToken) != 0 {
				t.Fatalf("recovery changed attempts or retained lease: %+v", got)
			}
			if wanted == model.ImportJobFailed {
				if got.FinishedAt == nil || got.LastErrorCode == nil || *got.LastErrorCode != "MAX_ATTEMPTS_EXCEEDED" {
					t.Fatalf("unexpected exhausted state: %+v", got)
				}
				return
			}
			newToken, claimed, err := store.ClaimQueuedJob(ctx, job.ID, "survivor", time.Minute)
			if err != nil || !claimed {
				t.Fatalf("survivor claim=%t err=%v", claimed, err)
			}
			stale := model.ImportJobResult{TreeSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
			valid := model.ImportJobResult{TreeSHA: job.RevisionSHA}
			if tc.inTransaction {
				stale = crashSnapshot(job, "stale\n")
				stale.TreeSHA = strings.Repeat("b", 40)
				valid = crashSnapshot(job, "survivor\n")
			}
			completed, err := store.CompleteJobWithResult(ctx, job.ID, oldToken, stale)
			if err != nil || completed {
				t.Fatalf("old token wrote under the new lease: completed=%t err=%v", completed, err)
			}
			completed, err = store.CompleteJobWithResult(ctx, job.ID, newToken, valid)
			if err != nil || !completed {
				t.Fatalf("survivor result: completed=%t err=%v", completed, err)
			}
			completed, err = store.CompleteJobWithResult(ctx, job.ID, oldToken, stale)
			if err != nil || completed {
				t.Fatalf("old token overwrote result: completed=%t err=%v", completed, err)
			}
			got, err = store.GetByID(ctx, job.ID)
			if err != nil || got.Status != model.ImportJobSucceeded || got.AttemptCount != 2 ||
				got.ResultTreeSHA == nil || *got.ResultTreeSHA != valid.TreeSHA {
				t.Fatalf("unexpected survivor result: job=%+v err=%v", got, err)
			}
			if tc.inTransaction {
				assertCrashSnapshot(t, db, job.ID, "survivor\n")
			}
		})
	}
}

func crashSnapshot(job *model.ImportJob, content string) model.ImportJobResult {
	return model.ImportJobResult{
		TreeSHA: job.RevisionSHA, FileCount: 1, TotalBytes: uint64(len(content)),
		Source: &model.SourceSnapshot{RevisionSHA: job.RevisionSHA, Files: []model.SourceFile{{
			Path: "main.go", BlobSHA: job.RevisionSHA, Language: "go", Content: content,
		}}},
	}
}

func assertCrashSnapshot(t *testing.T, db *gorm.DB, id uint64, want string) {
	t.Helper()
	var files []model.SourceFile
	if err := db.Where("import_job_id = ?", id).Find(&files).Error; err != nil {
		t.Fatal(err)
	}
	if want == "" {
		if len(files) != 0 {
			t.Fatal("uncommitted snapshot became visible")
		}
		return
	}
	if len(files) != 1 || files[0].Content != want {
		t.Fatal("survivor did not publish exactly one correct source file")
	}
	var spans []model.SourceSpan
	if err := db.Where("source_file_id = ?", files[0].ID).Find(&spans).Error; err != nil {
		t.Fatal(err)
	}
	if len(spans) != 1 || spans[0].Content != want {
		t.Fatal("survivor did not publish exactly one correct span")
	}
}

func openCrashTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Fatal("TEST_MYSQL_DSN must point to go_server_test")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	var schema string
	if err := db.Raw("SELECT DATABASE()").Row().Scan(&schema); err != nil || schema != "go_server_test" {
		t.Fatalf("expected go_server_test, got %q: %v", schema, err)
	}
	return db
}
