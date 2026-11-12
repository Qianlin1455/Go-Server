package repository

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"GoServer/internal/model"
)

// 测试1：正常完成任务，结果与状态同时持久化。
func TestCompleteJobWithResult_Success(t *testing.T) {
	tx, store, job, token := prepareRecoveryJob(t, 3, false)

	result := model.ImportJobResult{
		TreeSHA:    "ABCDEF0123456789ABCDEF0123456789ABCDEF01",
		FileCount:  12,
		TotalBytes: 3456,
	}

	completed, err := store.CompleteJobWithResult(
		context.Background(),
		job.ID,
		token,
		result,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !completed {
		t.Fatal("expected job to complete successfully")
	}

	// 查询数据库实际持久化结果。
	got := loadRecoveryJob(t, tx, job.ID)

	if got.Status != model.ImportJobSucceeded {
		t.Fatalf("status=%s, want succeeded", got.Status)
	}

	if got.FinishedAt == nil {
		t.Fatal("finished_at should not be nil")
	}

	if got.ResultTreeSHA == nil {
		t.Fatal("result_tree_sha should not be nil")
	}
	if *got.ResultTreeSHA != strings.ToLower(result.TreeSHA) {
		t.Fatalf(
			"tree_sha=%s, want %s",
			*got.ResultTreeSHA,
			strings.ToLower(result.TreeSHA),
		)
	}

	if got.ResultFileCount == nil ||
		*got.ResultFileCount != result.FileCount {
		t.Fatalf("unexpected result_file_count: %v", got.ResultFileCount)
	}

	if got.ResultTotalBytes == nil ||
		*got.ResultTotalBytes != result.TotalBytes {
		t.Fatalf("unexpected result_total_bytes: %v", got.ResultTotalBytes)
	}

	assertLeaseCleared(t, got)

	// 重复调用不能再次完成，也不能覆盖原结果。
	duplicateResult := model.ImportJobResult{
		TreeSHA:    "0123456789abcdef0123456789abcdef01234567",
		FileCount:  99,
		TotalBytes: 9999,
	}

	completed, err = store.CompleteJobWithResult(
		context.Background(),
		job.ID,
		token,
		duplicateResult,
	)

	if err != nil || completed {
		t.Fatalf(
			"duplicate completion should be rejected: completed=%v err=%v",
			completed,
			err,
		)
	}

	got = loadRecoveryJob(t, tx, job.ID)

	if *got.ResultFileCount != 12 ||
		*got.ResultTotalBytes != 3456 {
		t.Fatal("duplicate call overwrote the original result")
	}

	t.Log("PASS: result persisted, job succeeded, duplicate rejected")
}

// 测试2：零值必须保存为 0，而不是 NULL。
func TestCompleteJobWithResult_ZeroValues(t *testing.T) {
	tx, store, job, token := prepareRecoveryJob(t, 3, false)

	result := model.ImportJobResult{
		TreeSHA:    "0123456789abcdef0123456789abcdef01234567",
		FileCount:  0,
		TotalBytes: 0,
	}

	completed, err := store.CompleteJobWithResult(
		context.Background(),
		job.ID,
		token,
		result,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !completed {
		t.Fatal("expected job to complete")
	}

	got := loadRecoveryJob(t, tx, job.ID)

	if got.ResultTreeSHA == nil {
		t.Fatal("tree_sha should not be nil")
	}

	if got.ResultFileCount == nil {
		t.Fatal("file_count should not be nil")
	}

	if got.ResultTotalBytes == nil {
		t.Fatal("total_bytes should not be nil")
	}

	if *got.ResultFileCount != 0 ||
		*got.ResultTotalBytes != 0 {
		t.Fatalf(
			"expected zero values, got files=%d bytes=%d",
			*got.ResultFileCount,
			*got.ResultTotalBytes,
		)
	}

	t.Log("PASS: zero values persisted as 0, not NULL")
}

// 测试3：非法参数应该被拒绝。
func TestCompleteJobWithResult_InvalidArgs(t *testing.T) {
	validToken := make([]byte, 16)

	validResult := model.ImportJobResult{
		TreeSHA:    "0123456789abcdef0123456789abcdef01234567",
		FileCount:  10,
		TotalBytes: 100,
	}

	tests := []struct {
		name    string
		jobID   uint64
		token   []byte
		result  model.ImportJobResult
		wantErr error
	}{
		{
			name:    "zero job id",
			jobID:   0,
			token:   validToken,
			result:  validResult,
			wantErr: ErrInvalidCompleteJobArgs,
		},
		{
			name:    "invalid lease token length",
			jobID:   1,
			token:   []byte{1, 2, 3},
			result:  validResult,
			wantErr: ErrInvalidCompleteJobArgs,
		},
		{
			name:  "invalid tree sha",
			jobID: 1,
			token: validToken,
			result: model.ImportJobResult{
				TreeSHA: "invalid-sha",
			},
			wantErr: ErrInvalidCompleteJobResult,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewImportJobStore(setupTestTx(t))

			_, err := store.CompleteJobWithResult(
				context.Background(),
				tt.jobID,
				tt.token,
				tt.result,
			)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf(
					"error=%v, want %v",
					err,
					tt.wantErr,
				)
			}
		})
	}

	t.Log("PASS: invalid arguments rejected")
}

// 测试4：错误 Token 不能完成任务。
func TestCompleteJobWithResult_WrongToken(t *testing.T) {
	tx, store, job, token := prepareRecoveryJob(t, 3, false)

	wrongToken := append([]byte(nil), token...)
	wrongToken[0] ^= 0xff

	result := model.ImportJobResult{
		TreeSHA: "0123456789abcdef0123456789abcdef01234567",
	}

	completed, err := store.CompleteJobWithResult(
		context.Background(),
		job.ID,
		wrongToken,
		result,
	)

	if err != nil {
		t.Fatal(err)
	}

	if completed {
		t.Fatal("wrong token must not complete the job")
	}

	got := loadRecoveryJob(t, tx, job.ID)

	if got.Status != model.ImportJobRunning {
		t.Fatalf("status=%s, want running", got.Status)
	}

	if got.ResultTreeSHA != nil ||
		got.ResultFileCount != nil ||
		got.ResultTotalBytes != nil {
		t.Fatal("wrong token must not persist results")
	}

	if !bytes.Equal(got.LeaseToken, token) {
		t.Fatal("original lease token should remain unchanged")
	}

	t.Log("PASS: wrong token rejected")
}

// 测试5：租约过期后不能完成任务。
func TestCompleteJobWithResult_ExpiredLease(t *testing.T) {
	tx, store, job, token := prepareRecoveryJob(t, 3, true)

	result := model.ImportJobResult{
		TreeSHA: "0123456789abcdef0123456789abcdef01234567",
	}

	completed, err := store.CompleteJobWithResult(
		context.Background(),
		job.ID,
		token,
		result,
	)

	if err != nil {
		t.Fatal(err)
	}

	if completed {
		t.Fatal("expired lease must not complete the job")
	}

	got := loadRecoveryJob(t, tx, job.ID)

	if got.Status != model.ImportJobRunning {
		t.Fatalf("status=%s, want running", got.Status)
	}

	if got.ResultTreeSHA != nil ||
		got.ResultFileCount != nil ||
		got.ResultTotalBytes != nil {
		t.Fatal("expired lease must not persist results")
	}

	t.Log("PASS: expired lease rejected")
}

// 测试6：数据库操作失败时不能产生部分更新。
func TestCompleteJobWithResult_DatabaseFailure(t *testing.T) {
	tx, store, job, token := prepareRecoveryJob(t, 3, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := model.ImportJobResult{
		TreeSHA:    "0123456789abcdef0123456789abcdef01234567",
		FileCount:  10,
		TotalBytes: 100,
	}

	completed, err := store.CompleteJobWithResult(
		ctx,
		job.ID,
		token,
		result,
	)

	if err == nil {
		t.Fatal("expected database operation to fail")
	}

	if completed {
		t.Fatal("failed database operation must not complete the job")
	}

	// 使用新的有效 Context 查询，确认数据库没有发生更新。
	got := loadRecoveryJob(t, tx, job.ID)

	if got.Status != model.ImportJobRunning {
		t.Fatalf("status=%s, want running", got.Status)
	}

	if got.ResultTreeSHA != nil ||
		got.ResultFileCount != nil ||
		got.ResultTotalBytes != nil {
		t.Fatal("failed operation must not persist partial results")
	}

	if !bytes.Equal(got.LeaseToken, token) {
		t.Fatal("lease token should remain unchanged")
	}

	t.Log("PASS: failed database operation left job unchanged")
}
