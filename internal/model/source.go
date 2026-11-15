package model

import "time"

type SourceFile struct {
	ID            uint64
	ImportJobID   uint64
	UserID        uint64
	ProjectID     uint64
	RepositoryID  uint64
	RevisionSHA   string
	Path          string
	BlobSHA       string
	ContentSHA256 string
	Language      string
	SizeBytes     uint32
	Content       string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (SourceFile) TableName() string { return "source_files" }

type SourceSpan struct {
	ID            uint64
	SourceFileID  uint64
	SpanIndex     uint32
	LineStart     uint32
	LineEnd       uint32
	Content       string
	ContentSHA256 string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (SourceSpan) TableName() string { return "source_spans" }

// SourceSnapshot holds downloaded content before it is committed with the job.
type SourceSnapshot struct {
	RevisionSHA string
	Files       []SourceFile
}
