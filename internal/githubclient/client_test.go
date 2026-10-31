package githubclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 允许使用函数模拟 http.RoundTripper。
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(
	req *http.Request,
) (*http.Response, error) {
	return f(req)
}

// 把客户端访问 api.github.com 的请求转发给测试服务器。
// 这个替换只存在于测试代码，不改变生产代码中的 apiOrigin。
func newMockClient(
	t *testing.T,
	handler http.HandlerFunc,
) *Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	client := NewClient()
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	t.Cleanup(base.CloseIdleConnections)

	client.httpClient.Transport = roundTripFunc(
		func(req *http.Request) (*http.Response, error) {
			// 确保生产代码仍然只构造固定的 GitHub API 域名
			if req.URL.Scheme != "https" ||
				req.URL.Host != "api.github.com" {
				return nil, fmt.Errorf(
					"unexpected origin: %s", req.URL.String(),
				)
			}

			cloned := req.Clone(req.Context())
			u := *req.URL
			u.Scheme = target.Scheme
			u.Host = target.Host

			cloned.URL = &u
			cloned.Host = target.Host

			return base.RoundTrip(cloned)
		},
	)

	return client
}

// 验证输入白名单。
func TestGitHubClient_ValidateInput(t *testing.T) {
	cases := []struct {
		owner   string
		repo    string
		wantErr bool
	}{
		{"golang", "go", false},
		{"my-org", "my_repo", false},
		{"bad/owner", "repo", true},
		{"-bad", "repo", true},
		{"golang", "../secret", true},
		{"golang", ".", true},
		{"golang", "..", true},
		{"golang", "bad?repo", true},
	}

	for _, tc := range cases {
		err := validateOwnerRepo(tc.owner, tc.repo)

		if (err != nil) != tc.wantErr {
			t.Errorf(
				"owner=%q repo=%q: err=%v wantErr=%v",
				tc.owner, tc.repo, err, tc.wantErr,
			)
		}
	}
}

// 验证 HTTP 200 和 JSON 解析。
func TestGitHubClient_OK(t *testing.T) {
	client := newMockClient(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/repos/golang/go" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}

			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"default_branch":"master"}`)
		},
	)

	var result struct {
		DefaultBranch string `json:"default_branch"`
	}

	err := client.doJSON(
		context.Background(),
		"/repos/golang/go",
		maxMetadata,
		&result,
	)

	if err != nil {
		t.Fatal(err)
	}
	if result.DefaultBranch != "master" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

// 验证 HTTP 错误分类。
func TestGitHubClient_HTTPErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		header string
		want   error
	}{
		{"not_found", 404, "", ErrNotFound},
		{"rate_limit_429", 429, "", ErrRateLimited},
		{"rate_limit_403", 403, "0", ErrRateLimited},
		{"forbidden", 403, "", ErrForbidden},
		{"upstream", 500, "", ErrUpstream},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newMockClient(t,
				func(w http.ResponseWriter, r *http.Request) {
					if tc.header != "" {
						w.Header().Set(
							"X-RateLimit-Remaining", tc.header,
						)
					}
					w.WriteHeader(tc.status)
				},
			)

			var result map[string]any

			err := client.doJSON(
				context.Background(),
				"/repos/golang/go",
				maxMetadata,
				&result,
			)

			if !errors.Is(err, tc.want) {
				t.Fatalf(
					"expected %v, got %v", tc.want, err,
				)
			}
		})
	}
}

// 验证重定向被拒绝，且目标没有被访问。
func TestGitHubClient_Redirect(t *testing.T) {
	var followed atomic.Int32

	client := newMockClient(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/external" {
				followed.Add(1)
				w.WriteHeader(http.StatusOK)
				return
			}

			http.Redirect(
				w, r, "/external",
				http.StatusFound,
			)
		},
	)

	var result map[string]any

	err := client.doJSON(
		context.Background(),
		"/repos/golang/go",
		maxMetadata,
		&result,
	)

	if !errors.Is(err, ErrRedirect) {
		t.Fatalf("expected ErrRedirect, got %v", err)
	}

	if followed.Load() != 0 {
		t.Fatal("client followed a forbidden redirect")
	}
}

// 验证响应大小上限。
func TestGitHubClient_TooLarge(t *testing.T) {
	client := newMockClient(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(
				[]byte(strings.Repeat("x", maxMetadata+1)),
			)
		},
	)

	var result map[string]any

	err := client.doJSON(
		context.Background(),
		"/repos/golang/go",
		maxMetadata,
		&result,
	)

	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}

// 验证上游 Context 超时。
func TestGitHubClient_Timeout(t *testing.T) {
	client := newMockClient(t,
		func(w http.ResponseWriter, r *http.Request) {
			// 服务端一直等到客户端取消请求。
			<-r.Context().Done()
		},
	)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		30*time.Millisecond,
	)
	defer cancel()

	var result map[string]any

	err := client.doJSON(
		ctx,
		"/repos/golang/go",
		maxMetadata,
		&result,
	)

	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected ErrTimeout, got %v", err)
	}
}

func TestGitHubClient_GetPublicRepository(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"

	client := newMockClient(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			switch r.URL.Path {
			case "/repos/golang/go":
				fmt.Fprint(w, `{
					"private": false,
					"visibility": "public",
					"default_branch": "master"
				}`)

			case "/repos/golang/go/git/ref/heads/master":
				fmt.Fprintf(w, `{
					"ref": "refs/heads/master",
					"object": {
						"type": "commit",
						"sha": %q
					}
				}`, sha)

			default:
				http.NotFound(w, r)
			}
		},
	)

	info, err := client.GetPublicRepository(
		context.Background(), "golang", "go",
	)
	if err != nil {
		t.Fatal(err)
	}

	if info.Owner != "golang" ||
		info.Repo != "go" ||
		info.DefaultBranch != "master" ||
		info.LastSHA != sha {
		t.Fatalf("unexpected repo info: %+v", info)
	}
}

func TestGitHubClient_RejectPrivate(t *testing.T) {
	client := newMockClient(t,
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{
				"private": true,
				"visibility": "private",
				"default_branch": "main"
			}`)
		},
	)

	_, err := client.GetPublicRepository(
		context.Background(), "golang", "go",
	)

	if !errors.Is(err, ErrPrivate) {
		t.Fatalf("expected ErrPrivate, got %v", err)
	}
}

func TestGitHubClient_GetCommitSHA(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"

	client := newMockClient(t,
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/repos/golang/go":
				fmt.Fprint(w, `{
					"private": false,
					"visibility": "public",
					"default_branch": "master"
				}`)

			case "/repos/golang/go/git/commits/" + sha:
				fmt.Fprintf(w, `{"sha":%q}`, sha)

			default:
				http.NotFound(w, r)
			}
		},
	)

	got, err := client.GetCommitSHA(
		context.Background(), "golang", "go", sha,
	)
	if err != nil {
		t.Fatal(err)
	}

	if got != sha {
		t.Fatalf("want=%s got=%s", sha, got)
	}
}
