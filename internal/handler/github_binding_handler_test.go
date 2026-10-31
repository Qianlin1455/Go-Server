package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"GoServer/internal/githubclient"
	"GoServer/internal/model"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

type fakeRepositoryBinder struct {
	calls int
	err   error
}

func (f *fakeRepositoryBinder) BindRepository(
	ctx context.Context,
	userID, projectID uint64,
	owner, repo string,
) (*model.Repository, error) {
	f.calls++

	if f.err != nil {
		return nil, f.err
	}

	return &model.Repository{
		ID:            1,
		ProjectID:     projectID,
		Owner:         owner,
		Repo:          repo,
		DefaultBranch: "master",
		LastSHA:       strings.Repeat("a", 40),
	}, nil
}

// 构造独立的测试路由。
// 这里只模拟 JWT 已认证后的 Context，不测试真实 JWT。
func runGitHubBindingRequest(
	t *testing.T,
	fake *fakeRepositoryBinder,
	userID uint64,
	projectID string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	r := gin.New()

	h := NewGitHubBindingHandler(fake)

	r.POST("/projects/:pid/repositories",
		func(c *gin.Context) {
			if userID != 0 {
				c.Set("user_id", userID)
			}
			c.Next()
		},
		h.Bind,
	)

	req := httptest.NewRequest(
		http.MethodPost,
		"/projects/"+projectID+"/repositories",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	return w
}

func TestGitHubBindingHandler_Success(t *testing.T) {
	fake := &fakeRepositoryBinder{}

	w := runGitHubBindingRequest(
		t, fake,
		100, "10",
		`{"owner":"golang","repo":"go"}`,
	)

	if w.Code != http.StatusCreated {
		t.Fatalf(
			"expected 201, got %d: %s",
			w.Code, w.Body.String(),
		)
	}

	if fake.calls != 1 {
		t.Fatalf("expected 1 service call, got %d", fake.calls)
	}
}

func TestGitHubBindingHandler_Errors(t *testing.T) {
	cases := []struct {
		name      string
		userID    uint64
		projectID string
		body      string
		svcErr    error
		wantCode  int
		wantCalls int
	}{
		{
			name:   "unauthorized",
			userID: 0, projectID: "10",
			body:     `{"owner":"golang","repo":"go"}`,
			wantCode: 401,
		},
		{
			name:   "invalid_project_id",
			userID: 100, projectID: "abc",
			body:     `{"owner":"golang","repo":"go"}`,
			wantCode: 400,
		},
		{
			name:   "invalid_json",
			userID: 100, projectID: "10",
			body:     `{"owner":`,
			wantCode: 400,
		},
		{
			name:   "invalid_input",
			userID: 100, projectID: "10",
			body:     `{"owner":"bad/owner","repo":"go"}`,
			svcErr:   githubclient.ErrInvalidInput,
			wantCode: 400, wantCalls: 1,
		},
		{
			name:   "foreign_project",
			userID: 100, projectID: "10",
			body:     `{"owner":"golang","repo":"go"}`,
			svcErr:   service.ErrGitHubBindingProjectNotFound,
			wantCode: 404, wantCalls: 1,
		},
		{
			name:   "duplicate",
			userID: 100, projectID: "10",
			body:     `{"owner":"golang","repo":"go"}`,
			svcErr:   service.ErrGitHubRepositoryAlreadyBound,
			wantCode: 409, wantCalls: 1,
		},
		{
			name:   "rate_limited",
			userID: 100, projectID: "10",
			body:     `{"owner":"golang","repo":"go"}`,
			svcErr:   githubclient.ErrRateLimited,
			wantCode: 503, wantCalls: 1,
		},
		{
			name:   "timeout",
			userID: 100, projectID: "10",
			body:     `{"owner":"golang","repo":"go"}`,
			svcErr:   githubclient.ErrTimeout,
			wantCode: 504, wantCalls: 1,
		},
		{
			name:   "upstream",
			userID: 100, projectID: "10",
			body:     `{"owner":"golang","repo":"go"}`,
			svcErr:   githubclient.ErrUpstream,
			wantCode: 502, wantCalls: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeRepositoryBinder{
				err: tc.svcErr,
			}

			w := runGitHubBindingRequest(
				t, fake,
				tc.userID,
				tc.projectID,
				tc.body,
			)

			if w.Code != tc.wantCode {
				t.Fatalf(
					"want HTTP %d, got %d: %s",
					tc.wantCode,
					w.Code,
					w.Body.String(),
				)
			}

			if fake.calls != tc.wantCalls {
				t.Fatalf(
					"want %d service calls, got %d",
					tc.wantCalls,
					fake.calls,
				)
			}
		})
	}
}

// 确保 Fake Service 实现了所需接口。
var _ RepositoryBinder = (*fakeRepositoryBinder)(nil)

// 避免在错误映射测试中误以为任意非 nil 错误
// 都属于 GitHub 上游错误。
var _ = errors.Is
