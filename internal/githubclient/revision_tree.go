package githubclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	maxTreeResponseBytes int64 = 2 << 20
	maxRevisionFiles           = 200
	maxRevisionFileBytes int64 = 8 << 20
)

var ErrTreeBudgetExceeded = errors.New("github revision tree exceeds scan budget")

type RevisionSummary struct {
	CommitSHA  string
	TreeSHA    string
	FileCount  int
	TotalBytes int64
}

// 读取固定commit的文件树元数据，不下载文件、代码
func (c *Client) InspectRevision(
	ctx context.Context,
	owner, repo, revisionSHA string,
) (RevisionSummary, error) {
	var result RevisionSummary

	//验证输入
	if err := validateOwnerRepo(owner, repo); err != nil {
		return result, err
	}
	if !shaPattern.MatchString(revisionSHA) {
		return result, ErrInvalidInput
	}

	//验证仓库公开性
	if _, err := c.getPublicMetadata(ctx, owner, repo); err != nil {
		return result, err
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
	err := c.doJSON(ctx, base+"git/commits/"+revisionSHA, maxMetadata, &commit)
	if err != nil {
		return result, err
	}

	if !strings.EqualFold(commit.SHA, revisionSHA) ||
		!shaPattern.MatchString(commit.Tree.SHA) {
		return result, fmt.Errorf("%w:invalid commit/tree sha", ErrBadResponse)
	}
	// 4. 获取递归 Git Tree
	var tree struct {
		SHA       string `json:"sha"`
		Truncated bool   `json:"truncated"`
		Entries   []struct {
			Type string `json:"type"`
			Size *int64 `json:"size"`
		} `json:"tree"`
	}

	treeSHA := strings.ToLower(commit.Tree.SHA)
	err = c.doJSON(
		ctx,
		base+"/git/trees/"+treeSHA+"?recursive=1",
		maxTreeResponseBytes,
		&tree,
	)
	if err != nil {
		return result, err
	}

	//不接受截断结果
	if tree.Truncated {
		return result, ErrTreeBudgetExceeded
	}
	if !strings.EqualFold(tree.SHA, treeSHA) {
		return result, fmt.Errorf("%w:tree sha mismatch", ErrBadResponse)
	}

	result.CommitSHA = revisionSHA
	result.TreeSHA = treeSHA

	for _, entry := range tree.Entries {
		switch entry.Type {
		case "tree":
			continue
		case "blob":
			if entry.Size == nil || *entry.Size < 0 {
				return RevisionSummary{}, fmt.Errorf("%w:invalid blob size", ErrBadResponse)
			}
			if result.FileCount >= maxRevisionFiles ||
				*entry.Size > maxRevisionFileBytes-result.TotalBytes {
				return RevisionSummary{}, ErrTreeBudgetExceeded
			}
			result.FileCount++
			result.TotalBytes += *entry.Size

		default:
			return RevisionSummary{}, fmt.Errorf("%w:unsupported tree entry %q", ErrBadResponse, entry.Type)
		}
	}
	return result, nil
}
