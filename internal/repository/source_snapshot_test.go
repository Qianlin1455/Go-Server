package repository

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"GoServer/internal/model"

	"gorm.io/gorm"
)

func snapshotResult(revision string) model.ImportJobResult {
	content := strings.Repeat("α\r\n", 256) + "last"
	return model.ImportJobResult{
		TreeSHA: strings.Repeat("a", 40), FileCount: 1, TotalBytes: uint64(len(content)),
		Source: &model.SourceSnapshot{
			RevisionSHA: revision,
			Files: []model.SourceFile{{
				Path: "目录/main.go", BlobSHA: strings.Repeat("b", 40), Language: "go", Content: content,
			}},
		},
	}
}

func TestCompleteSourceSnapshot_ScopeAndIdempotency(t *testing.T) {
	ctx := t.Context()
	tx, store, job, token := prepareRecoveryJob(t, 3, false)
	result := snapshotResult(job.RevisionSHA)
	if ok, err := store.CompleteJobWithResult(ctx, job.ID, token, snapshotResult(strings.Repeat("f", 40))); ok ||
		!errors.Is(err, ErrInvalidCompleteJobResult) {
		t.Fatalf("wrong revision completion: %v, %v", ok, err)
	}
	if ok, err := store.CompleteJobWithResult(ctx, job.ID, token, result); !ok || err != nil {
		t.Fatalf("complete snapshot: %v, %v", ok, err)
	}
	if ok, err := store.CompleteJobWithResult(ctx, job.ID, token, result); ok || err != nil {
		t.Fatalf("repeated completion: %v, %v", ok, err)
	}
	var files []model.SourceFile
	if err := tx.Where("import_job_id = ?", job.ID).Find(&files).Error; err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != result.Source.Files[0].Path || files[0].Content != result.Source.Files[0].Content ||
		files[0].UserID != job.UserID || files[0].ProjectID != job.ProjectID || files[0].RepositoryID != job.RepositoryID {
		t.Fatal("persisted file or ownership differs")
	}
	file := files[0]
	var spans []model.SourceSpan
	if err := tx.Where("source_file_id = ?", file.ID).Order("span_index").Find(&spans).Error; err != nil {
		t.Fatal(err)
	}
	if len(spans) != 2 || spans[0].SpanIndex != 0 || spans[1].SpanIndex != 1 ||
		spans[0].LineStart != 1 || spans[0].LineEnd != 256 || spans[1].LineStart != 257 || spans[1].LineEnd != 257 ||
		spans[0].Content+spans[1].Content != file.Content ||
		spans[1].ContentSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte("last"))) {
		t.Fatalf("unexpected spans: count=%d", len(spans))
	}
	for _, edit := range []func(*model.SourceFile){
		func(f *model.SourceFile) { f.UserID++ },
		func(f *model.SourceFile) { f.RevisionSHA = strings.Repeat("f", 40) },
	} {
		other := file
		other.ID, other.Path = 0, "new.go"
		edit(&other)
		if err := tx.Create(&other).Error; !errors.Is(err, gorm.ErrForeignKeyViolated) {
			t.Fatalf("expected job scope FK error, got %v", err)
		}
	}
	next := model.ImportJob{
		UserID: job.UserID, ProjectID: job.ProjectID, RepositoryID: job.RepositoryID,
		Endpoint: job.Endpoint, IdempotencyKey: job.IdempotencyKey + "-next",
		RequestFingerprint: job.RequestFingerprint, RevisionSHA: strings.Repeat("f", 40), Status: model.ImportJobQueued,
	}
	if err := store.Create(ctx, &next); err != nil {
		t.Fatal(err)
	}
	nextToken, claimed, err := store.ClaimQueuedJob(ctx, next.ID, "source-test", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim next revision: %v, %v", claimed, err)
	}
	if ok, err := store.CompleteJobWithResult(ctx, next.ID, nextToken, snapshotResult(next.RevisionSHA)); !ok || err != nil {
		t.Fatalf("persist same path for next revision: %v, %v", ok, err)
	}
	assertSnapshotCounts(t, tx, next.ID, 1, 2)
}

func TestCompleteSourceSnapshot_Rollback(t *testing.T) {
	for _, table := range []string{"source_files", "source_spans", "import_jobs"} {
		t.Run(table, func(t *testing.T) {
			tx, store, job, token := prepareRecoveryJob(t, 3, false)
			injected := errors.New("injected snapshot write failure")
			callback := func(db *gorm.DB) {
				if db.Statement.Table == table {
					db.AddError(injected)
				}
			}
			const name = "test:snapshot_after_write"
			if table == "import_jobs" {
				if err := tx.Callback().Update().After("gorm:update").Register(name, callback); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = tx.Callback().Update().Remove(name) })
			} else {
				if err := tx.Callback().Create().After("gorm:create").Register(name, callback); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = tx.Callback().Create().Remove(name) })
			}
			if ok, err := store.CompleteJobWithResult(t.Context(), job.ID, token, snapshotResult(job.RevisionSHA)); ok ||
				!errors.Is(err, injected) {
				t.Fatalf("injected failure: %v, %v", ok, err)
			}
			assertSnapshotCounts(t, tx, job.ID, 0, 0)
			if got := loadRecoveryJob(t, tx, job.ID); got.Status != model.ImportJobRunning || got.ResultTreeSHA != nil {
				t.Fatal("failed transaction published a terminal job")
			}
		})
	}
}

func TestCompleteSourceSnapshot_LostLease(t *testing.T) {
	tx, store, job, old := prepareRecoveryJob(t, 3, true)
	result := snapshotResult(job.RevisionSHA)
	ctx := t.Context()
	if ok, err := store.CompleteJobWithResult(ctx, job.ID, old, result); ok || err != nil {
		t.Fatalf("expired lease completion: %v, %v", ok, err)
	}
	if _, _, err := store.RecoverExpiredJobs(ctx); err != nil {
		t.Fatal(err)
	}
	token, claimed, err := store.ClaimQueuedJob(ctx, job.ID, "new-worker", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("reclaim: %v, %v", claimed, err)
	}
	if ok, err := store.CompleteJobWithResult(ctx, job.ID, old, result); ok || err != nil {
		t.Fatalf("stale lease completion: %v, %v", ok, err)
	}
	assertSnapshotCounts(t, tx, job.ID, 0, 0)
	if ok, err := store.CompleteJobWithResult(ctx, job.ID, token, result); !ok || err != nil {
		t.Fatalf("valid lease completion: %v, %v", ok, err)
	}
	assertSnapshotCounts(t, tx, job.ID, 1, 2)
}

func TestCompleteSourceSnapshot_LeaseExpiresDuringWrite(t *testing.T) {
	tx, store, job, token := prepareRecoveryJob(t, 3, false)
	const callback = "test:expire_source_lease"
	if err := tx.Callback().Create().After("gorm:create").Register(callback, func(db *gorm.DB) {
		if db.Statement.Table == "source_spans" && db.Error == nil {
			// Use the same transaction to put the deadline in the past before
			// the final CAS, without sleeping against a real clock.
			err := db.Session(&gorm.Session{NewDB: true}).Exec(
				"UPDATE import_jobs SET lease_expires_at = DATE_SUB(NOW(3), INTERVAL 1 SECOND) WHERE id = ?", job.ID,
			).Error
			if err != nil {
				db.AddError(err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Callback().Create().Remove(callback) })
	if ok, err := store.CompleteJobWithResult(t.Context(), job.ID, token, snapshotResult(job.RevisionSHA)); ok || err != nil {
		t.Fatalf("lease expired during snapshot: %v, %v", ok, err)
	}
	assertSnapshotCounts(t, tx, job.ID, 0, 0)
	if got := loadRecoveryJob(t, tx, job.ID); got.Status != model.ImportJobRunning || got.ResultTreeSHA != nil {
		t.Fatal("expired lease published a terminal job")
	}
}

func assertSnapshotCounts(t *testing.T, db *gorm.DB, jobID uint64, files, spans int64) {
	t.Helper()
	var gotFiles, gotSpans int64
	if err := db.Model(&model.SourceFile{}).Where("import_job_id = ?", jobID).Count(&gotFiles).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT COUNT(*) FROM source_spans s JOIN source_files f
		ON f.id = s.source_file_id WHERE f.import_job_id = ?`, jobID).Scan(&gotSpans).Error; err != nil {
		t.Fatal(err)
	}
	if gotFiles != files || gotSpans != spans {
		t.Fatalf("files/spans=%d/%d, want %d/%d", gotFiles, gotSpans, files, spans)
	}
}
