package githubclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	ErrInvalidInput = errors.New("invalid github input")
	ErrNotFound     = errors.New("github resource not found")
	ErrPrivate      = errors.New("repository is not public")
	ErrRateLimited  = errors.New("github rate limited")
	ErrForbidden    = errors.New("github access forbidden")
	ErrTimeout      = errors.New("github request timeout")
	ErrRedirect     = errors.New("github redirect rejected")
	ErrTooLarge     = errors.New("github response too large")
	ErrBadResponse  = errors.New("invalid github response")
	ErrUpstream     = errors.New("github upstream error")
)
var (
	ownerPattern = regexp.MustCompile(
		`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$`,
	)
	repoPattern = regexp.MustCompile(
		`^[a-zA-Z0-9_.-]{1,100}$`,
	)
	shaPattern = regexp.MustCompile(
		`^[0-9a-fA-F]{40}$`,
	)
)

const (
	apiOrigin   = "https://api.github.com"
	maxMetadata = 1 << 20
	maxSHA      = 4096
)

type Client struct {
	httpClient *http.Client
}

type RepositoryInfo struct {
	Owner         string
	Repo          string
	DefaultBranch string
	LastSHA       string
}

type repositoryMetadata struct {
	Name  string `json:"name"`
	Owner struct {
		Login string `json:"login"`
	} `json:"owner"`

	Private       bool   `json:"private"`
	Visibility    string `json:"visibility"`
	DefaultBranch string `json:"default_branch"`
}

func NewClient() *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()

	//不通过环境变量配置的http代理转发请求
	transport.Proxy = nil

	return &Client{
		httpClient: &http.Client{
			Timeout:   5 * time.Second,
			Transport: transport,
			// 禁止自动跟随所有重定向
			CheckRedirect: func(
				req *http.Request,
				via []*http.Request,
			) error {
				return ErrRedirect
			},
		},
	}
}

func validateOwnerRepo(owner, repo string) error {
	if !ownerPattern.MatchString(owner) {
		return fmt.Errorf("%w:invalid owner", ErrInvalidInput)
	}

	if !repoPattern.MatchString(repo) || repo == "." || repo == ".." {
		return fmt.Errorf("%w: invalid repo", ErrInvalidInput)
	}

	return nil
}

func classifyRequestError(err error) error {
	if errors.Is(err, ErrRedirect) {
		return ErrRedirect
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ErrTimeout
	}
	return fmt.Errorf("%w: %v", ErrUpstream, err)
}

func (c *Client) doJSON(
	ctx context.Context,
	path string,
	maxByte int64,
	dst any,
) error {
	if !strings.HasPrefix(path, "/repos/") || maxByte <= 0 {
		return ErrInvalidInput
	}

	target, err := url.Parse(apiOrigin + path)
	if err != nil || target.Scheme != "https" ||
		target.Host != "api.github.com" ||
		target.User != nil {
		return ErrInvalidInput
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)

	if err != nil {
		return fmt.Errorf("%w:%v", ErrInvalidInput, err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")

	start := time.Now()

	resp, err := c.httpClient.Do(req)

	if err != nil {
		log.Printf(
			"GitHub HTTP FAILED path=%s elapsed=%s ctx_err=%v err=%v",
			path,
			time.Since(start),
			ctx.Err(),
			err,
		)
		return classifyRequestError(err)
	}

	log.Printf(
		"GitHub HTTP HEADERS path=%s status=%d elapsed=%s",
		path,
		resp.StatusCode,
		time.Since(start),
	)

	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// 继续读取响应

	case http.StatusNotFound:
		return ErrNotFound

	case http.StatusTooManyRequests:
		return ErrRateLimited

	case http.StatusForbidden:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" ||
			resp.Header.Get("Retry-After") != "" {
			return ErrRateLimited
		}
		return ErrForbidden

	default:
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return ErrRedirect
		}
		if resp.StatusCode >= 500 {
			return ErrUpstream
		}
		return fmt.Errorf(
			"%w: HTTP %d",
			ErrBadResponse,
			resp.StatusCode,
		)
	}

	// 多读一个字节以判断是否超过限制
	body, err := io.ReadAll(
		io.LimitReader(resp.Body, maxByte+1),
	)
	if err != nil {
		return classifyRequestError(err)
	}

	if int64(len(body)) > maxByte {
		return ErrTooLarge
	}

	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("%w: %v", ErrBadResponse, err)
	}

	return nil
}

type gitReference struct {
	Ref    string `json:"ref"`
	Object struct {
		Type string `json:"type"`
		SHA  string `json:"sha"`
	} `json:"object"`
}

// 验证仓库公开可访问并读取元信息
func (c *Client) getPublicMetadata(
	ctx context.Context,
	owner, repo string,
) (*repositoryMetadata, error) {
	if err := validateOwnerRepo(owner, repo); err != nil {
		return nil, err
	}
	path := "/repos/" + owner + "/" + repo

	var metaData repositoryMetadata
	if err := c.doJSON(ctx, path, maxMetadata, &metaData); err != nil {
		return nil, err
	}

	if metaData.Private || metaData.Visibility != "public" {
		return nil, ErrPrivate
	}

	if metaData.DefaultBranch == "" || len(metaData.DefaultBranch) > 255 {
		return nil, fmt.Errorf("%w:invalid default branch", ErrBadResponse)
	}
	return &metaData, nil
}

func (c *Client) GetPublicRepository(
	ctx context.Context,
	owner, repo string,
) (*RepositoryInfo, error) {
	metadata, err := c.getPublicMetadata(ctx, owner, repo)
	if err != nil {
		return nil, err
	}

	path := "/repos/" + owner + "/" + repo +
		"/git/ref/heads/" + url.PathEscape(metadata.DefaultBranch)

	var ref gitReference
	if err := c.doJSON(ctx, path, maxSHA, &ref); err != nil {
		return nil, err
	}

	if ref.Ref != "refs/heads/"+metadata.DefaultBranch ||
		ref.Object.Type != "commit" ||
		!shaPattern.MatchString(ref.Object.SHA) {
		return nil, fmt.Errorf("%w:invalid git reference", ErrBadResponse)
	}

	return &RepositoryInfo{
		Owner:         owner,
		Repo:          repo,
		DefaultBranch: metadata.DefaultBranch,
		LastSHA:       strings.ToLower(ref.Object.SHA),
	}, nil
}

func (c *Client) GetCommitSHA(
	ctx context.Context,
	owner, repo, sha string,
) (string, error) {
	if !shaPattern.MatchString(sha) {
		return "", fmt.Errorf(
			"%w: invalid commit SHA",
			ErrInvalidInput,
		)
	}

	// 每次独立调用都必须检查仓库公开性。
	if _, err := c.getPublicMetadata(ctx, owner, repo); err != nil {
		return "", err
	}

	path := "/repos/" + owner + "/" + repo +
		"/git/commits/" + strings.ToLower(sha)

	var commit struct {
		SHA string `json:"sha"`
	}

	if err := c.doJSON(
		ctx, path, maxMetadata, &commit,
	); err != nil {
		return "", err
	}

	// 确保返回结果与指定 revision 一致。
	if !shaPattern.MatchString(commit.SHA) ||
		!strings.EqualFold(commit.SHA, sha) {
		return "", fmt.Errorf(
			"%w: commit SHA mismatch",
			ErrBadResponse,
		)
	}

	return strings.ToLower(commit.SHA), nil
}
