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

// 依赖接口:方便测试时候用Fake Github Client
type RevisionInspector interface {
	InspectRevision(ctx context.Context,
		owner, repo, sha string) (githubclient.RevisionSummary, error)
}

type GitHubExecutor struct {
	db    *gorm.DB
	repos *repository.GitHubRepositoryStore
	gh    RevisionInspector
}

func NewGitHubExecutor(
	db *gorm.DB,
	gh RevisionInspector,
) *GitHubExecutor {
	return &GitHubExecutor{
		db:    db,
		repos: repository.NewGitHubRepositoryStore(db),
		gh:    gh,
	}
}

// Execute执行V1固定Revision只读校验任务
// 返回nil表示成功
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

	// 3. 检查任务创建时固定的 Revision
	summary, err := e.gh.InspectRevision(
		ctx,
		binding.Owner,
		binding.Repo,
		job.RevisionSHA,
	)
	if err != nil {
		return model.ImportJobResult{},
			fmt.Errorf("inspect revision for job %d: %w", jobID, err)
	}

	// 4. 校验数值，避免非法类型转换
	if summary.FileCount < 0 ||
		uint64(summary.FileCount) > uint64(^uint32(0)) ||
		summary.TotalBytes < 0 {
		return model.ImportJobResult{},
			fmt.Errorf("invalid revision summary for job %d", jobID)
	}

	// 5. 转换为业务结果
	result := model.ImportJobResult{
		TreeSHA:    summary.TreeSHA,
		FileCount:  uint32(summary.FileCount),
		TotalBytes: uint64(summary.TotalBytes),
	}

	log.Printf(
		"job=%d revision verified commit_sha=%s tree_sha=%s files=%d bytes=%d",
		jobID,
		summary.CommitSHA,
		summary.TreeSHA,
		summary.FileCount,
		summary.TotalBytes,
	)

	return result, nil
}
