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
) error {
	if jobID == 0 {
		return fmt.Errorf("invalid job id")
	}

	//从MySQL读取running Job
	var job model.ImportJob

	err := e.db.WithContext(ctx).
		Where("id = ? AND status = ?", jobID, model.ImportJobRunning).Take(&job).Error
	if err != nil {
		return fmt.Errorf("load running import job %d:%w", jobID, err)
	}

	//读取关联的github仓库，复用已用用户、项目、仓库归属查询
	binding, err := e.repos.GetByProject(
		ctx,
		job.UserID,
		job.ProjectID,
		job.RepositoryID,
	)
	if err != nil {
		return fmt.Errorf("load repository for job %d:%w", jobID, err)
	}

	//必须使用任务创建时固定的RevisionSHA，不能使用可能变化的binding.LastSHA
	summary, err := e.gh.InspectRevision(
		ctx,
		binding.Owner,
		binding.Repo,
		job.RevisionSHA,
	)
	if err != nil {
		return fmt.Errorf("inspect revision for job %d:%w", jobID, err)
	}

	// 4. 记录核验结果
	log.Printf(
		"job=%d revision verified sha=%s files=%d bytes=%d",
		jobID,
		summary.CommitSHA,
		summary.FileCount,
		summary.TotalBytes,
	)

	return nil
}
