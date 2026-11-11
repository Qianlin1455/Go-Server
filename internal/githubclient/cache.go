package githubclient

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

const RepositoryCacheTTL = 30 * time.Second

// Cache is implemented by internal/cache.RedisCache. Failures are cache misses.
type Cache interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte) error
}

func NewClientWithCache(cache Cache) *Client {
	c := NewClient()
	c.cache = cache
	return c
}

type repositorySnapshot struct {
	Info      RepositoryInfo
	Public    bool
	ExpiresAt time.Time
}

func repositoryKey(owner, repo string) string {
	return "github:v1:repository:" + strings.ToLower(owner) + "/" + strings.ToLower(repo)
}
func (c *Client) cached(ctx context.Context, key, owner, repo string) *RepositoryInfo {
	if c.cache == nil {
		return nil
	}
	data, hit, err := c.cache.Get(ctx, key)
	if err != nil || !hit || len(data) > maxMetadata {
		return nil
	}
	var snapshot repositorySnapshot
	if json.Unmarshal(data, &snapshot) != nil {
		return nil
	}
	info := snapshot.Info
	now := time.Now()
	if !snapshot.Public || !snapshot.ExpiresAt.After(now) || snapshot.ExpiresAt.After(now.Add(RepositoryCacheTTL)) ||
		!strings.EqualFold(info.Owner, owner) || !strings.EqualFold(info.Repo, repo) ||
		info.DefaultBranch == "" || len(info.DefaultBranch) > 255 || !shaPattern.MatchString(info.LastSHA) {
		return nil
	}
	info.Owner, info.Repo, info.LastSHA = owner, repo, strings.ToLower(info.LastSHA)
	return &info
}
func (c *Client) GetPublicRepository(ctx context.Context, owner, repo string) (*RepositoryInfo, error) {
	if err := validateOwnerRepo(owner, repo); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, classifyRequestError(err)
	}
	key := repositoryKey(owner, repo)
	if info := c.cached(ctx, key, owner, repo); info != nil {
		return info, nil
	}
	result := c.flights.DoChan(key, func() (any, error) {
		// The shared lookup survives an individual waiter's cancellation, but is bounded.
		shared, cancel := context.WithTimeout(context.WithoutCancel(ctx), 11*time.Second)
		defer cancel()
		if info := c.cached(shared, key, owner, repo); info != nil {
			return *info, nil
		}
		info, err := c.fetchPublicRepository(shared, owner, repo)
		if err != nil {
			return nil, err
		}
		if c.cache != nil {
			data, err := json.Marshal(repositorySnapshot{Info: *info, Public: true, ExpiresAt: time.Now().Add(RepositoryCacheTTL)})
			if err == nil {
				_ = c.cache.Set(shared, key, data)
			}
		}
		return *info, nil
	})
	select {
	case <-ctx.Done():
		return nil, classifyRequestError(ctx.Err())
	case r := <-result:
		if r.Err != nil {
			return nil, r.Err
		}
		info := r.Val.(RepositoryInfo)
		info.Owner, info.Repo = owner, repo
		return &info, nil
	}
}
