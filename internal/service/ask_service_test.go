package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"GoServer/internal/llmclient"
	"GoServer/internal/model"
	"GoServer/internal/repository"

	"gorm.io/gorm"
)

type askCompleterFunc func(context.Context, llmclient.Request) (llmclient.Response, error)

func (f askCompleterFunc) Complete(ctx context.Context, request llmclient.Request) (llmclient.Response, error) {
	return f(ctx, request)
}

func TestAskService(t *testing.T) {
	db, input, binding := newAskEvidenceFixture(t)
	job := createAskTestJob(t, db, input, binding.ID, "ask-service",
		[]model.SourceFile{askTestFile("source.go", "Needle\r\n")})
	for _, tc := range []struct {
		name, output string
		modelErr     error
		wantErr      error
		question     string
		wantCalls    int
	}{
		{name: "answer", wantCalls: 1},
		{name: "no evidence", question: "`DoesNotExist`"},
		{name: "model timeout", modelErr: llmclient.ErrModelTimeout, wantErr: llmclient.ErrModelTimeout, wantCalls: 1},
		{name: "model 429", modelErr: &llmclient.HTTPStatusError{StatusCode: 429}, wantErr: llmclient.ErrModelUpstream, wantCalls: 1},
		{name: "model 5xx", modelErr: &llmclient.HTTPStatusError{StatusCode: 503}, wantErr: llmclient.ErrModelUpstream, wantCalls: 1},
		{name: "invalid model JSON", output: "{", wantErr: ErrInvalidModelOutput, wantCalls: 1},
		{name: "forged evidence", output: `{"answer":"forged","evidence_ids":["forged"]}`, wantErr: ErrInvalidEvidenceReference, wantCalls: 1},
		{name: "forged metadata", output: `{"answer":"forged","evidence_ids":["unused"],"path":"fake.go"}`, wantErr: ErrInvalidModelOutput, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := AskInput{AskEvidenceInput: input}
			query.ImportJobID = job.ID
			if tc.question != "" {
				query.Question = tc.question
			}
			calls := 0
			mock := askCompleterFunc(func(ctx context.Context, request llmclient.Request) (llmclient.Response, error) {
				calls++
				if tc.modelErr != nil || tc.output != "" {
					return llmclient.Response{Content: tc.output}, tc.modelErr
				}
				var payload struct {
					Question string              `json:"question"`
					Evidence []askPromptEvidence `json:"untrusted_evidence"`
				}
				if err := json.Unmarshal([]byte(request.UserPrompt), &payload); err != nil ||
					payload.Question != query.Question || len(payload.Evidence) != 1 || payload.Evidence[0].Content != "Needle\r\n" {
					t.Fatalf("retrieved span missing from prompt: %v", err)
				}
				content, err := json.Marshal(map[string]any{
					"answer": "grounded answer", "evidence_ids": []string{payload.Evidence[0].EvidenceID},
				})
				return llmclient.Response{Content: string(content)}, err
			})
			got, err := NewAskService(NewSourceService(db), mock).Ask(t.Context(), query)
			if calls != tc.wantCalls || !errors.Is(err, tc.wantErr) {
				t.Fatalf("model calls=%d, err=%v, want %d, %v", calls, err, tc.wantCalls, tc.wantErr)
			}
			if err != nil {
				if got.Answer.Answer != "" || len(got.Citations) != 0 {
					t.Fatal("failed request exposed a partial answer")
				}
				return
			}
			if got.ImportJobID != job.ID || got.RevisionSHA != strings.ToLower(input.RevisionSHA) {
				t.Fatalf("wrong snapshot in answer: %+v", got)
			}
			if tc.wantCalls == 0 {
				if !got.InsufficientEvidence || got.Citations == nil || len(got.Citations) != 0 || got.Answer.Answer == "" {
					t.Fatal("no-evidence response did not explicitly abstain")
				}
				return
			}
			if got.InsufficientEvidence || got.Answer.Answer != "grounded answer" || len(got.Citations) != 1 ||
				got.Citations[0].Path != "source.go" || got.Citations[0].RevisionSHA != job.RevisionSHA ||
				got.Citations[0].LineStart != 1 || got.Citations[0].LineEnd != 1 || got.Citations[0].ContentSHA256 == "" {
				t.Fatalf("bad grounded answer: %+v", got)
			}
		})
	}
}

func TestAskServiceRetrievalFailureSkipsModel(t *testing.T) {
	db, input, binding := newAskEvidenceFixture(t)
	createAskTestJob(t, db, input, binding.ID, "visible", []model.SourceFile{askTestFile("main.go", "Needle")})
	mock := askCompleterFunc(func(context.Context, llmclient.Request) (llmclient.Response, error) {
		t.Fatal("model called after retrieval failed")
		return llmclient.Response{}, nil
	})
	cause := errors.New("candidate query failure")
	if err := db.Callback().Query().Before("gorm:query").Register("test:ask_query_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "f" {
			tx.AddError(cause)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err := NewAskService(NewSourceService(db), mock).Ask(t.Context(), AskInput{AskEvidenceInput: input})
	if !errors.Is(err, repository.ErrStorage) || !errors.Is(err, cause) {
		t.Fatalf("retrieval error chain was lost: %v", err)
	}
}

func TestAskServicePropagatesParentCancellation(t *testing.T) {
	db, input, binding := newAskEvidenceFixture(t)
	createAskTestJob(t, db, input, binding.ID, "visible", []model.SourceFile{askTestFile("main.go", "Needle")})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	mock := askCompleterFunc(func(modelCtx context.Context, _ llmclient.Request) (llmclient.Response, error) {
		calls++
		cancel()
		if !errors.Is(modelCtx.Err(), context.Canceled) {
			t.Error("completer did not receive request context")
		}
		// Even if the client returned a response concurrently, cancellation wins.
		return llmclient.Response{Content: "should not be parsed"}, nil
	})
	if _, err := NewAskService(NewSourceService(db), mock).Ask(ctx, AskInput{AskEvidenceInput: input}); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("canceled call: calls=%d err=%v", calls, err)
	}
}
