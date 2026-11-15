package githubclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	maxTreeResponseBytes  int64 = 2 << 20
	maxRevisionFiles            = 200
	maxRevisionTotalBytes int64 = 8 << 20
)

var ErrTreeBudgetExceeded = errors.New("github revision tree exceeds scan budget")

type RevisionSummary struct {
	CommitSHA  string
	TreeSHA    string
	FileCount  int
	TotalBytes int64
}

type revisionTreeEntry struct {
	Path string `json:"path"`
	SHA  string `json:"sha"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	Size *int64 `json:"size"`
}

// 读取固定commit的文件树元数据，不下载文件、代码
func (c *Client) InspectRevision(
	ctx context.Context,
	owner, repo, revisionSHA string,
) (RevisionSummary, error) {
	result, _, err := c.loadRevisionTree(ctx, owner, repo, revisionSHA)
	return result, err
}

func (c *Client) loadRevisionTree(ctx context.Context, owner, repo, revisionSHA string) (RevisionSummary, []revisionTreeEntry, error) {
	var result RevisionSummary

	//验证输入
	if err := validateOwnerRepo(owner, repo); err != nil {
		return result, nil, err
	}
	if !shaPattern.MatchString(revisionSHA) {
		return result, nil, ErrInvalidInput
	}

	//验证仓库公开性
	if _, err := c.getPublicMetadata(ctx, owner, repo); err != nil {
		return result, nil, err
	}

	base := "/repos/" + owner + "/" + repo
	revisionSHA = strings.ToLower(revisionSHA)

	//获取固定commit的tree sha
	var commit struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	err := c.doJSON(ctx, base+"/git/commits/"+revisionSHA, maxMetadata, &commit)
	if err != nil {
		return result, nil, err
	}

	if !strings.EqualFold(commit.SHA, revisionSHA) ||
		!shaPattern.MatchString(commit.Tree.SHA) {
		return result, nil, fmt.Errorf("%w: invalid commit/tree sha", ErrBadResponse)
	}
	// 4. 获取递归 Git Tree
	var tree struct {
		SHA       string              `json:"sha"`
		Truncated bool                `json:"truncated"`
		Entries   []revisionTreeEntry `json:"tree"`
	}

	treeSHA := strings.ToLower(commit.Tree.SHA)
	err = c.doJSON(
		ctx,
		base+"/git/trees/"+treeSHA+"?recursive=1",
		maxTreeResponseBytes,
		&tree,
	)
	if err != nil {
		return result, nil, err
	}

	//不接受截断结果
	if tree.Truncated {
		return result, nil, ErrTreeBudgetExceeded
	}
	if !strings.EqualFold(tree.SHA, treeSHA) || tree.Entries == nil {
		return result, nil, fmt.Errorf("%w: invalid tree or tree SHA mismatch", ErrBadResponse)
	}

	result.CommitSHA = revisionSHA
	result.TreeSHA = treeSHA

	for _, entry := range tree.Entries {
		switch entry.Type {
		case "tree", "commit":
			continue
		case "blob":
			if entry.Size == nil || *entry.Size < 0 {
				return RevisionSummary{}, nil, fmt.Errorf("%w: invalid blob size", ErrBadResponse)
			}
			if result.FileCount >= maxRevisionFiles ||
				*entry.Size > maxRevisionTotalBytes-result.TotalBytes {
				return RevisionSummary{}, nil, ErrTreeBudgetExceeded
			}
			result.FileCount++
			result.TotalBytes += *entry.Size

		default:
			return RevisionSummary{}, nil, fmt.Errorf("%w: unsupported tree entry", ErrBadResponse)
		}
	}
	return result, tree.Entries, nil
}
