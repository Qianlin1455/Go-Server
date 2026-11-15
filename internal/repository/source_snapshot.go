package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"GoServer/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var errSourceLeaseLost = errors.New("source snapshot lease lost")

func (r *ImportJobStore) completeSourceSnapshot(
	ctx context.Context, jobID uint64, token []byte, result model.ImportJobResult,
) (bool, error) {
	var size uint64
	for _, file := range result.Source.Files {
		size += uint64(len(file.Content))
	}
	if uint64(result.FileCount) != uint64(len(result.Source.Files)) || size != result.TotalBytes {
		return false, ErrInvalidCompleteJobResult
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.ImportJob
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Scopes(activeImportLease(jobID, token)).Take(&job).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errSourceLeaseLost
		}
		if err != nil {
			return fmt.Errorf("lock import job for source snapshot: %w", err)
		}
		if !strings.EqualFold(job.RevisionSHA, result.Source.RevisionSHA) {
			return ErrInvalidCompleteJobResult
		}

		// Scope and revision come from the locked job. Suppress SQL logging
		// for content inserts, which otherwise include the source on errors.
		contentDB := tx.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
		for _, file := range result.Source.Files {
			file.ID = 0
			file.ImportJobID = job.ID
			file.UserID = job.UserID
			file.ProjectID = job.ProjectID
			file.RepositoryID = job.RepositoryID
			file.RevisionSHA = job.RevisionSHA
			file.SizeBytes = uint32(len(file.Content))
			file.ContentSHA256 = sourceChecksum(file.Content)
			if err := contentDB.Create(&file).Error; err != nil {
				return fmt.Errorf("insert source file: %w", err)
			}
			if spans := splitSourceSpans(file.ID, file.Content); len(spans) != 0 {
				if err := contentDB.CreateInBatches(&spans, 128).Error; err != nil {
					return fmt.Errorf("insert source spans: %w", err)
				}
			}
		}
		// Recheck the lease against DB time after the inserts. A lease that
		// expired during this transaction must roll back the entire snapshot.
		completed, err := NewImportJobStore(tx).completeJobWithResult(ctx, jobID, token, result)
		if err != nil {
			return err
		}
		if !completed {
			return errSourceLeaseLost
		}
		return nil
	})
	if errors.Is(err, errSourceLeaseLost) {
		return false, nil
	}
	return err == nil, err
}

func sourceChecksum(content string) string {
	hash := sha256.Sum256([]byte(content))
	return hex.EncodeToString(hash[:])
}

func splitSourceSpans(fileID uint64, content string) []model.SourceSpan {
	const linesPerSpan = 256
	var spans []model.SourceSpan
	start := 0
	var firstLine, line uint32 = 1, 1
	for end := 0; end < len(content); {
		breakAt := strings.IndexAny(content[end:], "\r\n")
		if breakAt < 0 {
			end = len(content)
		} else {
			end += breakAt + 1
			if content[end-1] == '\r' && end < len(content) && content[end] == '\n' {
				end++
			}
		}
		if line-firstLine+1 == linesPerSpan || end == len(content) {
			part := content[start:end]
			spans = append(spans, model.SourceSpan{
				SourceFileID: fileID, SpanIndex: uint32(len(spans)), LineStart: firstLine, LineEnd: line,
				Content: part, ContentSHA256: sourceChecksum(part),
			})
			start, firstLine = end, line+1
		}
		line++
	}
	return spans
}
