package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"GoServer/internal/llmclient"
	"GoServer/internal/midware"
	"GoServer/internal/model"
	"GoServer/internal/security"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
)

type httpAskCompleter func(context.Context, llmclient.Request) (llmclient.Response, error)

func (f httpAskCompleter) Complete(ctx context.Context, request llmclient.Request) (llmclient.Response, error) {
	return f(ctx, request)
}

func fixtureAnswer(prompt string) (string, error) {
	var data struct {
		Evidence []struct {
			ID string `json:"evidence_id"`
		} `json:"untrusted_evidence"`
	}
	if err := json.Unmarshal([]byte(prompt), &data); err != nil || len(data.Evidence) != 1 || data.Evidence[0].ID == "" {
		return "", fmt.Errorf("model fixture expected one evidence ID")
	}
	payload, err := json.Marshal(map[string]any{
		"answer": "grounded answer", "evidence_ids": []string{data.Evidence[0].ID},
	})
	return string(payload), err
}

func TestAskHandler(t *testing.T) {
	dsn, err := mysql.ParseDSN(os.Getenv("TEST_MYSQL_DSN"))
	if err != nil || dsn.DBName != "go_server_test" {
		t.Fatal("TEST_MYSQL_DSN must point to go_server_test")
	}
	gin.SetMode(gin.TestMode)
	db := setupMessageAPITestDB(t)
	owner := createSourceTestUser(t, db, "ask_owner")
	other := createSourceTestUser(t, db, "ask_other")
	project := createSourceTestProject(t, db, owner.ID, "ask")
	otherProject := createSourceTestProject(t, db, other.ID, "ask")
	conversation := model.Conversation{UserID: owner.ID, ProjectID: &project.ID, Title: "owned"}
	foreign := model.Conversation{UserID: other.ID, ProjectID: &otherProject.ID, Title: "foreign"}
	if err := db.Create(&[]*model.Conversation{&conversation, &foreign}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&conversation, conversation.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("a", 40)
	binding := model.Repository{ProjectID: project.ID, Owner: "org", Repo: "repo", DefaultBranch: "main", LastSHA: revision}
	if err := db.Create(&binding).Error; err != nil {
		t.Fatal(err)
	}
	job := publishSourceTestFiles(t, db, project, binding.ID, revision, "ask", model.SourceFile{
		Path: "main.go", Content: "Needle\nsensitive-source-code", Language: "go", BlobSHA: strings.Repeat("b", 40),
	})
	manager, err := security.NewJWTManager(strings.Repeat("k", 32), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token := func(userID uint64) string {
		signed, err := manager.Generate(userID)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	ownerJWT, otherJWT := token(owner.ID), token(other.ID)
	path := fmt.Sprintf("/api/v1/projects/%d/ai/ask", project.ID)
	valid := fmt.Sprintf(`{"question":%q,"revision_sha":%q,"import_job_id":%d,"conversation_id":%d}`,
		"`Needle`", revision, job.ID, conversation.ID)
	run := func(ctx context.Context, token, body string, client llmclient.Completer) *httptest.ResponseRecorder {
		r := gin.New()
		r.Use(midware.Timeout(12 * time.Second))
		group := r.Group("/api/v1", midware.JWTAuth(manager))
		group.GET("/projects/:id", func(*gin.Context) {})
		NewAskHandler(service.NewAskService(service.NewSourceService(db), client)).RegisterRoutes(group)
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out
	}
	assertResponse := func(t *testing.T, out *httptest.ResponseRecorder, status int, code string) service.AnswerResponse {
		t.Helper()
		var body struct {
			Code string                 `json:"code"`
			Data service.AnswerResponse `json:"data"`
		}
		if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil || out.Code != status || body.Code != code {
			t.Fatalf("status=%d code=%q decode=%v, want %d %q", out.Code, body.Code, err, status, code)
		}
		for _, secret := range []string{"sensitive-source-code", "fixture-secret", "vendor-private-error", "SystemPrompt", "UserPrompt", "untrusted_evidence", "evidence_ids", "model-private-output"} {
			if strings.Contains(out.Body.String(), secret) {
				t.Fatalf("response disclosed internal data: %q", secret)
			}
		}
		return body.Data
	}
	for _, tc := range []struct {
		name, body, token, code string
		status, wantCalls       int
		modelErr                error
		modelOutput             string
		disabled                bool
	}{
		{name: "normal", body: valid, token: ownerJWT, code: "SUCCESS", status: 200, wantCalls: 1},
		{name: "missing question", body: fmt.Sprintf(`{"revision_sha":%q}`, revision), token: ownerJWT, code: "INVALID_ARGUMENT", status: 400},
		{name: "missing SHA", body: `{"question":"Needle"}`, token: ownerJWT, code: "INVALID_ARGUMENT", status: 400},
		{name: "bad SHA", body: `{"question":"Needle","revision_sha":"wrong"}`, token: ownerJWT, code: "INVALID_ARGUMENT", status: 400},
		{name: "cannot pass user ID", body: valid[:len(valid)-1] + `,"user_id":1}`, token: ownerJWT, code: "INVALID_ARGUMENT", status: 400},
		{name: "trailing JSON", body: valid + "{}", token: ownerJWT, code: "INVALID_ARGUMENT", status: 400},
		{name: "request size", body: valid + strings.Repeat(" ", askRequestByteLimit), token: ownerJWT, code: "REQUEST_TOO_LARGE", status: 413},
		{name: "anonymous", body: valid, code: "MISSING_TOKEN", status: 401},
		{name: "foreign project", body: valid, token: otherJWT, code: "NOT_FOUND", status: 404},
		{name: "foreign conversation", body: strings.Replace(valid, fmt.Sprintf(`"conversation_id":%d`, conversation.ID), fmt.Sprintf(`"conversation_id":%d`, foreign.ID), 1), token: ownerJWT, code: "NOT_FOUND", status: 404},
		{name: "disabled", body: valid, token: ownerJWT, code: "AI_DISABLED", status: 503, disabled: true},
		{name: "model timeout", body: valid, token: ownerJWT, code: "MODEL_TIMEOUT", status: 504, wantCalls: 1, modelErr: llmclient.ErrModelTimeout},
		{name: "no evidence", body: strings.Replace(valid, "Needle", "NoSuchSymbol", 1), token: ownerJWT, code: "INSUFFICIENT_EVIDENCE", status: 200},
		{name: "invalid provider envelope", body: valid, token: ownerJWT, code: "MODEL_INVALID_RESPONSE", status: 502, wantCalls: 1, modelErr: llmclient.ErrModelInvalidResponse},
		{name: "untrusted JSON", body: valid, token: ownerJWT, code: "MODEL_INVALID_RESPONSE", status: 502, wantCalls: 1, modelOutput: `{"model-private-output":true}`},
		{name: "untrusted evidence", body: valid, token: ownerJWT, code: "MODEL_INVALID_EVIDENCE", status: 502, wantCalls: 1, modelOutput: `{"answer":"model-private-output","evidence_ids":["forged"]}`},
		{name: "private upstream failure", body: valid, token: ownerJWT, code: "MODEL_UPSTREAM_ERROR", status: 502, wantCalls: 1, modelErr: fmt.Errorf("vendor-private-error fixture-secret: %w", llmclient.ErrModelUpstream)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var client llmclient.Completer
			if !tc.disabled {
				client = httpAskCompleter(func(_ context.Context, input llmclient.Request) (llmclient.Response, error) {
					calls++
					if tc.modelOutput != "" || tc.modelErr != nil {
						return llmclient.Response{Content: tc.modelOutput}, tc.modelErr
					}
					content, err := fixtureAnswer(input.UserPrompt)
					return llmclient.Response{Content: content}, err
				})
			}
			got := assertResponse(t, run(t.Context(), tc.token, tc.body, client), tc.status, tc.code)
			if calls != tc.wantCalls {
				t.Fatalf("model called %d times, want %d", calls, tc.wantCalls)
			}
			switch tc.code {
			case "SUCCESS":
				if got.ImportJobID != job.ID || got.RevisionSHA != revision ||
					len(got.Citations) != 1 || got.Citations[0].Path != "main.go" || got.Citations[0].LineEnd != 2 {
					t.Fatalf("wrong snapshot or grounded citation: %+v", got)
				}
			case "INSUFFICIENT_EVIDENCE":
				if got.ImportJobID != job.ID || got.RevisionSHA != revision || got.Answer.Answer == "" ||
					got.Citations == nil || len(got.Citations) != 0 {
					t.Fatalf("unsafe abstention response: %+v", got)
				}
			}
		})
	}

	t.Run("real HTTP client", func(t *testing.T) {
		var calls, upstreamStatus atomic.Int32
		modelStarted, modelCanceled := make(chan struct{}), make(chan struct{})
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-secret" {
				t.Error("incorrect real model HTTP request")
			}
			if status := upstreamStatus.Load(); status < 0 {
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Error(err)
				}
				close(modelStarted)
				select {
				case <-r.Context().Done():
					close(modelCanceled)
				case <-time.After(5 * time.Second):
					t.Error("upstream did not observe request context cancellation")
				}
				return
			} else if status != 0 {
				w.WriteHeader(int(status))
				fmt.Fprint(w, "vendor-private-error fixture-secret")
				return
			}
			var req struct {
				Messages []struct{ Content string }
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) != 2 {
				t.Error("invalid messages in HTTP request")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			content, err := fixtureAnswer(req.Messages[1].Content)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, `{"choices":[{"message":{"content":%q},"finish_reason":"stop"}]}`, content)
		}))
		defer upstream.Close()
		t.Setenv("AI_BASE_URL", upstream.URL+"/v1")
		t.Setenv("AI_MODEL", "fixture-model")
		t.Setenv("AI_API_KEY", "fixture-secret")
		config, err := llmclient.LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		client, err := llmclient.NewClient(config)
		if err != nil {
			t.Fatal(err)
		}
		got := assertResponse(t, run(t.Context(), ownerJWT, valid, client), 200, "SUCCESS")
		if len(got.Citations) != 1 || got.Citations[0].Path != "main.go" {
			t.Fatal("real HTTP completion was not grounded")
		}
		for _, status := range []int32{429, 500} {
			upstreamStatus.Store(status)
			assertResponse(t, run(t.Context(), ownerJWT, valid, client), 502, "MODEL_UPSTREAM_ERROR")
		}
		upstreamStatus.Store(-1)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() {
			select {
			case <-modelStarted:
				cancel()
			case <-ctx.Done():
			}
		}()
		assertResponse(t, run(ctx, ownerJWT, valid, client), 408, "REQUEST_CANCELED")
		select {
		case <-modelCanceled:
		case <-time.After(5 * time.Second):
			t.Fatal("request cancellation did not stop the model HTTP request")
		}
		if calls.Load() != 4 {
			t.Fatalf("want exactly four model calls, got %d", calls.Load())
		}
	})

	t.Run("request context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		client := httpAskCompleter(func(modelCtx context.Context, _ llmclient.Request) (llmclient.Response, error) {
			deadline, ok := modelCtx.Deadline()
			if !ok || time.Until(deadline) > 12*time.Second {
				t.Error("global request deadline did not reach completer")
			}
			cancel()
			return llmclient.Response{}, modelCtx.Err()
		})
		assertResponse(t, run(ctx, ownerJWT, valid, client), 408, "REQUEST_CANCELED")
		ctx, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		defer stop()
		assertResponse(t, run(ctx, ownerJWT, valid, client), 504, "DEADLINE_EXCEEDED")
	})

	var count int64
	if err := db.Model(&model.Message{}).Where("conversation_id = ?", conversation.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("ask wrote conversation messages: count=%d err=%v", count, err)
	}
	var stored model.Conversation
	if err := db.First(&stored, conversation.ID).Error; err != nil || !stored.UpdatedAt.Equal(conversation.UpdatedAt) {
		t.Fatalf("ask updated the conversation timestamp: %v", err)
	}
}
