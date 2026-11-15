package service

import (
	"context"
	"errors"
	"io/fs"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"GoServer/internal/model"
	"GoServer/internal/repository"

	"gorm.io/gorm"
)

const (
	SourceDefaultPageSize = 20
	sourceMaxPage         = 1000
	sourceListMaxPageSize = 100
	sourceTextMaxPageSize = 20
	sourceMaxKeywordBytes = 256
	sourceMaxFileBytes    = 512 << 10
	sourceSearchTimeout   = 2 * time.Second
)

var (
	ErrInvalidSourceInput = errors.New("invalid source query input")
	ErrSourceNotFound     = errors.New("source snapshot or file not found")
	ErrSourceFileTooLarge = errors.New("source file exceeds response size limit")
	sourceRevisionPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
)

type SourceService struct {
	db    *gorm.DB
	store *repository.SourceStore
}

func NewSourceService(db *gorm.DB) *SourceService {
	return &SourceService{db: db, store: repository.NewSourceStore(db)}
}

type SourceQuery struct {
	RevisionSHA string
	ImportJobID uint64
	Path        string
	PathPrefix  string
	Page        int
	PageSize    int
}

func (s *SourceService) resolveSnapshot(ctx context.Context, scope repository.SourceScope, input SourceQuery, maxPageSize int) (uint64, error) {
	if !sourceRevisionPattern.MatchString(input.RevisionSHA) ||
		!validSourcePathFilter(input.Path, false) || !validSourcePathFilter(input.PathPrefix, true) {
		return 0, ErrInvalidSourceInput
	}
	if !validSourcePagination(input.Page, input.PageSize, maxPageSize) {
		return 0, ErrInvalidPagination
	}
	if err := s.checkProject(ctx, scope); err != nil {
		return 0, err
	}
	jobID, err := s.store.ResolveSnapshot(ctx, scope, strings.ToLower(input.RevisionSHA), input.ImportJobID)
	return jobID, sourceLookupError(err)
}

func (s *SourceService) ListFiles(ctx context.Context, userID, projectID uint64, input SourceQuery) ([]model.SourceFile, int64, uint64, error) {
	scope := repository.SourceScope{UserID: userID, ProjectID: projectID}
	jobID, err := s.resolveSnapshot(ctx, scope, input, sourceListMaxPageSize)
	if err != nil {
		return nil, 0, 0, err
	}
	files, total, err := s.store.ListFiles(ctx, scope, jobID, input.Path, input.PathPrefix, input.Page, input.PageSize)
	return files, total, jobID, err
}

func (s *SourceService) GetFile(ctx context.Context, userID, projectID, fileID uint64) (model.SourceFile, error) {
	scope := repository.SourceScope{UserID: userID, ProjectID: projectID}
	if err := s.checkProject(ctx, scope); err != nil {
		return model.SourceFile{}, err
	}
	file, err := s.store.GetFile(ctx, scope, fileID, sourceMaxFileBytes)
	if err != nil {
		return model.SourceFile{}, sourceLookupError(err)
	}
	if file.SizeBytes > sourceMaxFileBytes {
		return model.SourceFile{}, ErrSourceFileTooLarge
	}
	return file, nil
}

func (s *SourceService) ListSpans(ctx context.Context, userID, projectID, fileID uint64, page, pageSize int) ([]model.SourceSpan, int64, error) {
	if !validSourcePagination(page, pageSize, sourceTextMaxPageSize) {
		return nil, 0, ErrInvalidPagination
	}
	scope := repository.SourceScope{UserID: userID, ProjectID: projectID}
	if err := s.checkProject(ctx, scope); err != nil {
		return nil, 0, err
	}
	spans, total, err := s.store.ListSpans(ctx, scope, fileID, page, pageSize)
	return spans, total, sourceLookupError(err)
}

func (s *SourceService) Search(ctx context.Context, userID, projectID uint64, input SourceQuery, keyword string) ([]repository.SourceMatch, int64, uint64, error) {
	if len(keyword) > sourceMaxKeywordBytes || strings.TrimSpace(keyword) == "" ||
		!utf8.ValidString(keyword) || strings.ContainsRune(keyword, 0) {
		return nil, 0, 0, ErrInvalidSourceInput
	}
	ctx, cancel := context.WithTimeout(ctx, sourceSearchTimeout)
	defer cancel()
	scope := repository.SourceScope{UserID: userID, ProjectID: projectID}
	jobID, err := s.resolveSnapshot(ctx, scope, input, sourceTextMaxPageSize)
	if err != nil {
		return nil, 0, 0, err
	}
	matches, total, err := s.store.Search(ctx, scope, jobID, keyword, input.PathPrefix, input.Page, input.PageSize)
	return matches, total, jobID, err
}

func (s *SourceService) checkProject(ctx context.Context, scope repository.SourceScope) error {
	_, err := repository.GetProjectByID(ctx, s.db, scope.UserID, scope.ProjectID)
	if errors.Is(err, repository.ErrNotFound) {
		return ErrProjectNotFound
	}
	return err
}

func sourceLookupError(err error) error {
	if errors.Is(err, repository.ErrNotFound) {
		return ErrSourceNotFound
	}
	return err
}

func validSourcePagination(page, pageSize, maxPageSize int) bool {
	return page >= 1 && page <= sourceMaxPage && pageSize >= 1 && pageSize <= maxPageSize
}

func validSourcePathFilter(value string, prefix bool) bool {
	if value == "" {
		return true
	}
	if len(value) > 768 || strings.ContainsAny(value, `\:<>"|?*`) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	if prefix {
		value = strings.TrimSuffix(value, "/")
	}
	return value != "." && fs.ValidPath(value)
}
