package repository

import (
	"errors"
	"strings"
	"testing"
	"time"

	"GoServer/internal/model"
)

func TestSourceStore_Visibility(t *testing.T) {
	ctx := t.Context()
	tx, jobs, job, token := prepareRecoveryJob(t, 3, false)
	store := NewSourceStore(tx)
	scope := SourceScope{UserID: job.UserID, ProjectID: job.ProjectID}

	// Add rows explicitly to prove visibility depends on SQL constraints
	// even if a nonterminal job has source rows.
	content := "internal source"
	file := model.SourceFile{
		ImportJobID: job.ID, UserID: job.UserID, ProjectID: job.ProjectID, RepositoryID: job.RepositoryID,
		RevisionSHA: job.RevisionSHA, Path: "main.go", BlobSHA: strings.Repeat("a", 40),
		ContentSHA256: sourceChecksum(content), Language: "go", SizeBytes: uint32(len(content)), Content: content,
	}
	if err := tx.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	span := model.SourceSpan{
		SourceFileID: file.ID, LineStart: 1, LineEnd: 1, Content: content, ContentSHA256: sourceChecksum(content),
	}
	if err := tx.Create(&span).Error; err != nil {
		t.Fatal(err)
	}

	assertInvisible := func(scope SourceScope) {
		t.Helper()
		if _, err := store.ResolveSnapshot(ctx, scope, job.RevisionSHA, job.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("resolve invisible snapshot: %v", err)
		}
		if _, err := store.GetFile(ctx, scope, file.ID, 512<<10); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get invisible file: %v", err)
		}
		if _, _, err := store.ListSpans(ctx, scope, file.ID, 1, 20); !errors.Is(err, ErrNotFound) {
			t.Fatalf("list invisible spans: %v", err)
		}
		if files, total, err := store.ListFiles(ctx, scope, job.ID, "", "", 1, 20); err != nil || len(files) != 0 || total != 0 {
			t.Fatalf("list invisible snapshot: files=%d total=%d err=%v", len(files), total, err)
		}
		if matches, total, err := store.Search(ctx, scope, job.ID, "internal", "", 1, 20); err != nil || len(matches) != 0 || total != 0 {
			t.Fatalf("search invisible snapshot: matches=%d total=%d err=%v", len(matches), total, err)
		}
	}

	assertInvisible(scope)
	if ok, err := jobs.FailJobPermanently(ctx, job.ID, token, "TEST_FAILURE"); err != nil || !ok {
		t.Fatalf("fail job: %t, %v", ok, err)
	}
	assertInvisible(scope)

	// Test stale rows on a queued job and verify the same SQL when exposed
	// by a successful job. The fixture already supplies valid timestamps.
	if err := tx.Model(job).Updates(map[string]any{"status": model.ImportJobQueued, "finished_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	assertInvisible(scope)
	token, claimed, err := jobs.ClaimQueuedJob(ctx, job.ID, "retrieval-test", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim: %t, %v", claimed, err)
	}
	if ok, err := jobs.CompleteJob(ctx, job.ID, token); err != nil || !ok {
		t.Fatalf("complete: %t, %v", ok, err)
	}
	assertInvisible(SourceScope{UserID: job.UserID + 1, ProjectID: job.ProjectID})
	assertInvisible(SourceScope{UserID: job.UserID, ProjectID: job.ProjectID + 1})
	if got, err := store.GetFile(ctx, scope, file.ID, 512<<10); err != nil || got.Content != content {
		t.Fatalf("read succeeded file: %v", err)
	}
}

func TestSourceStore_PropagatesStorageFailure(t *testing.T) {
	db := setupTestDB(t)
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	store := NewSourceStore(db)
	if _, _, err := store.Search(t.Context(), SourceScope{UserID: 1, ProjectID: 1}, 1, "source", "", 1, 20); !errors.Is(err, ErrStorage) || errors.Is(err, ErrNotFound) {
		t.Fatalf("expected storage failure, got %v", err)
	}
}
