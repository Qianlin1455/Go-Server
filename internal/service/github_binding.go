package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"GoServer/internal/githubclient"
	"GoServer/internal/model"
	"GoServer/internal/repository"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

var (
	ErrGitHubBindingInvalidInput = errors.New(
		"invalid github binding input",
	)
	ErrGitHubBindingProjectNotFound = errors.New(
		"github binding project not found",
	)
	ErrGitHubRepositoryAlreadyBound = errors.New(
		"github repository already bound",
	)
)

type PublicRepositoryFetcher interface {
	GetPublicRepository(ctx context.Context, owner, repo string) (*githubclient.RepositoryInfo, error)
}

type GitHubBingService struct {
	db    *gorm.DB
	store *repository.GitHubRepositoryStore
	gh    PublicRepositoryFetcher
}

func NewGitHubBindingService(db *gorm.DB, gh PublicRepositoryFetcher) *GitHubBingService {
	return &GitHubBingService{
		db:    db,
		store: repository.NewGitHubRepositoryStore(db),
		gh:    gh,
	}
}

// BindRepository将公开Github 仓库绑定到用户项目
func (s *GitHubBingService) BindRepository(
	ctx context.Context,
	userID, projectID uint64,
	owner, repo string,
) (*model.Repository, error) {
	if userID == 0 || projectID == 0 {
		return nil, ErrGitHubBindingInvalidInput
	}

	_, err := repository.GetProjectByID(
		ctx, s.db, userID, projectID,
	)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrGitHubBindingProjectNotFound
		}
		return nil, fmt.Errorf("check project:%w", err)
	}

	//通过githubclient验证公开仓库，client负责owner/repo白名单和Http安全边界
	info, err := s.gh.GetPublicRepository(
		ctx, owner, repo,
	)
	if err != nil {
		return nil, err
	}

	//将github数据转为数据库模型
	binding := &model.Repository{
		ProjectID:     projectID,
		Owner:         strings.ToLower(info.Owner),
		Repo:          strings.ToLower(info.Repo),
		DefaultBranch: info.DefaultBranch,
		LastSHA:       info.LastSHA,
	}

	err = s.store.Create(ctx, binding)
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrGitHubRepositoryAlreadyBound
		}

		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) &&
			mysqlErr.Number == 1062 {
			return nil, ErrGitHubRepositoryAlreadyBound
		}

		return nil, fmt.Errorf(
			"create github repository binding: %w",
			err,
		)
	}
	return binding, nil
}
