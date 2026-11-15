package worker

import (
	"context"
	"fmt"
	"log"

	"GoServer/internal/githubclient"
	"GoServer/internal/model"
	"GoServer/internal/repository"

	"gorm.io/gorm"
)

type RevisionDownloader interface {
	DownloadRevision(ctx context.Context, owner, repo, sha string) (githubclient.RevisionSource, error)
}

type GitHubExecutor struct {
	db    *gorm.DB
	repos *repository.GitHubRepositoryStore
	gh    RevisionDownloader
}

func NewGitHubExecutor(
	db *gorm.DB,
	gh RevisionDownloader,
) *GitHubExecutor {
	return &GitHubExecutor{
		db:    db,
		repos: repository.NewGitHubRepositoryStore(db),
		gh:    gh,
	}
}

// Execute downloads the job's fixed revision. Runner commits the result with
// the lease acquired by ClaimQueuedJob.
func (e *GitHubExecutor) Execute(
	ctx context.Context,
	jobID uint64,
) (model.ImportJobResult, error) {
	if jobID == 0 {
		return model.ImportJobResult{}, fmt.Errorf("invalid job id")
	}

	// 1. 从 MySQL 读取 running Job
	var job model.ImportJob

	err := e.db.WithContext(ctx).
		Where(
			"id = ? AND status = ?",
			jobID,
			model.ImportJobRunning,
		).
		Take(&job).Error

	if err != nil {
		return model.ImportJobResult{},
			fmt.Errorf("load running import job %d: %w", jobID, err)
	}

	// 2. 读取关联的 GitHub 仓库
	binding, err := e.repos.GetByProject(
		ctx,
		job.UserID,
		job.ProjectID,
		job.RepositoryID,
	)
	if err != nil {
		return model.ImportJobResult{},
			fmt.Errorf("load repository for job %d: %w", jobID, err)
	}

	source, err := e.gh.DownloadRevision(
		ctx,
		binding.Owner,
		binding.Repo,
		job.RevisionSHA,
	)
	if err != nil {
		return model.ImportJobResult{},
			fmt.Errorf("download revision for job %d: %w", jobID, err)
	}

	result := model.ImportJobResult{
		TreeSHA:   source.TreeSHA,
		FileCount: uint32(len(source.Files)),
		Source: &model.SourceSnapshot{
			RevisionSHA: source.CommitSHA,
			Files:       make([]model.SourceFile, 0, len(source.Files)),
		},
	}
	for _, file := range source.Files {
		result.TotalBytes += uint64(len(file.Content))
		result.Source.Files = append(result.Source.Files, model.SourceFile{
			Path: file.Path, BlobSHA: file.BlobSHA, Language: file.Language, Content: string(file.Content),
		})
	}

	log.Printf(
		"job=%d revision downloaded commit_sha=%s tree_sha=%s files=%d bytes=%d",
		jobID,
		source.CommitSHA,
		result.TreeSHA,
		result.FileCount,
		result.TotalBytes,
	)

	return result, nil
}
