package repository

import (
	"context"
	"strings"

	"GoServer/internal/model"

	"gorm.io/gorm"
)

type SourceStore struct {
	db *gorm.DB
}

func NewSourceStore(db *gorm.DB) *SourceStore { return &SourceStore{db: db} }

type SourceScope struct {
	UserID    uint64
	ProjectID uint64
}

type SourceMatch struct {
	model.SourceSpan
	ImportJobID uint64
	RevisionSHA string
	Path        string
	Language    string
}

func (r *SourceStore) ResolveSnapshot(
	ctx context.Context, scope SourceScope, revision string, jobID uint64,
) (uint64, error) {
	query := r.db.WithContext(ctx).Table("import_jobs AS j").
		Joins("JOIN projects AS p ON p.id = j.project_id AND p.user_id = j.user_id").
		Where("j.project_id = ? AND j.user_id = ? AND j.status = ? AND LOWER(j.revision_sha) = ?",
			scope.ProjectID, scope.UserID, model.ImportJobSucceeded, revision)
	if jobID != 0 {
		query = query.Where("j.id = ?", jobID)
	}
	var job model.ImportJob
	err := query.Select("j.id").Order("j.id DESC").Take(&job).Error
	return job.ID, databaseError(ctx, "resolve source snapshot", err)
}

func (r *SourceStore) visibleFiles(ctx context.Context, scope SourceScope) *gorm.DB {
	return r.db.WithContext(ctx).Table("source_files AS f").
		Joins(`JOIN import_jobs AS j ON j.id = f.import_job_id AND j.user_id = f.user_id
			AND j.project_id = f.project_id AND j.repository_id = f.repository_id AND j.revision_sha = f.revision_sha`).
		Joins("JOIN projects AS p ON p.id = j.project_id AND p.user_id = j.user_id").
		Where("f.project_id = ? AND f.user_id = ? AND j.status = ?", scope.ProjectID, scope.UserID, model.ImportJobSucceeded)
}

// literalLike escapes the chosen escape byte as well as both LIKE wildcards.
var literalLike = strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")

func withSourcePrefix(query *gorm.DB, prefix string) *gorm.DB {
	if prefix == "" {
		return query
	}
	return query.Where("f.path LIKE ? ESCAPE '!'", literalLike.Replace(prefix)+"%")
}

func (r *SourceStore) ListFiles(ctx context.Context, scope SourceScope, jobID uint64, path, prefix string, page, pageSize int) ([]model.SourceFile, int64, error) {
	query := withSourcePrefix(r.visibleFiles(ctx, scope).Where("f.import_job_id = ?", jobID), prefix)
	if path != "" {
		query = query.Where("f.path = ?", path)
	}
	return readSourcePage[model.SourceFile](ctx, "list source files", query, page, pageSize,
		"f.id, f.import_job_id, f.revision_sha, f.path, f.language, f.size_bytes, f.content_sha256, f.created_at",
		"f.path ASC, f.id ASC")
}

func (r *SourceStore) GetFile(ctx context.Context, scope SourceScope, fileID uint64, maxBytes int) (model.SourceFile, error) {
	var file model.SourceFile
	err := r.visibleFiles(ctx, scope).Where("f.id = ?", fileID).
		Select(`f.id, f.import_job_id, f.revision_sha, f.path, f.blob_sha, f.content_sha256, f.language, f.size_bytes,
			CASE WHEN f.size_bytes <= ? THEN f.content ELSE '' END AS content`, maxBytes).
		Take(&file).Error
	return file, databaseError(ctx, "get source file", err)
}

func (r *SourceStore) ListSpans(ctx context.Context, scope SourceScope, fileID uint64, page, pageSize int) ([]model.SourceSpan, int64, error) {
	query := r.visibleFiles(ctx, scope).Where("f.id = ?", fileID)
	// Distinguish a visible empty file from a hidden or nonexistent file.
	var file model.SourceFile
	if err := query.Session(&gorm.Session{}).Select("f.id").Take(&file).Error; err != nil {
		return nil, 0, databaseError(ctx, "resolve source file for spans", err)
	}
	return readSourcePage[model.SourceSpan](ctx, "list source spans", query.Joins("JOIN source_spans AS s ON s.source_file_id = f.id"),
		page, pageSize, "s.id, s.span_index, s.line_start, s.line_end, s.content, s.content_sha256",
		"s.span_index ASC, s.id ASC")
}

func (r *SourceStore) Search(ctx context.Context, scope SourceScope, jobID uint64, keyword, prefix string, page, pageSize int) ([]SourceMatch, int64, error) {
	query := withSourcePrefix(r.visibleFiles(ctx, scope).Where("f.import_job_id = ?", jobID), prefix).
		Joins("JOIN source_spans AS s ON s.source_file_id = f.id").
		Where("s.content COLLATE utf8mb4_bin LIKE ? ESCAPE '!'", "%"+literalLike.Replace(keyword)+"%")
	return readSourcePage[SourceMatch](ctx, "search source spans", query, page, pageSize,
		"s.id, s.source_file_id, s.line_start, s.line_end, s.content, s.content_sha256, f.import_job_id, f.revision_sha, f.path, f.language",
		"f.path ASC, s.span_index ASC, s.id ASC")
}

func readSourcePage[T any](ctx context.Context, operation string, query *gorm.DB, page, pageSize int, columns, order string) ([]T, int64, error) {
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, databaseError(ctx, operation+" count", err)
	}
	items := make([]T, 0)
	if int64((page-1)*pageSize) >= total {
		return items, total, nil
	}
	err := query.Select(columns).Order(order).Scopes(Paginate(page, pageSize)).Find(&items).Error
	return items, total, databaseError(ctx, operation, err)
}
