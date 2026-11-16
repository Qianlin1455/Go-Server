package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
	"time"

	"GoServer/internal/llmclient"
	"GoServer/internal/model"
	"GoServer/internal/repository"
)

func TestAskLiveModelSmoke(t *testing.T) {
	if os.Getenv("AI_LIVE_TEST") != "1" {
		t.Skip("NOT RUN: set AI_LIVE_TEST=1 to enable the live model smoke test")
	}
	cfg, err := llmclient.LoadConfig()
	if err != nil {
		t.Fatal("live model configuration is invalid")
	}
	if !cfg.Enabled() {
		t.Skip("NOT RUN: AI_BASE_URL, AI_MODEL and AI_API_KEY are unset")
	}
	client, err := llmclient.NewClient(cfg)
	if err != nil {
		t.Fatal("live model client could not be initialized")
	}

	const source = "func Add(a, b int) int { return a + b }\n"
	match := repository.SourceMatch{
		Path: "example.go", RevisionSHA: testCommitSHA, ImportJobID: 1,
		SourceSpan: model.SourceSpan{
			ID: 1, SourceFileID: 1, LineStart: 1, LineEnd: 1, Content: source,
			ContentSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(source))),
		},
	}
	evidence, matches := buildEvidence([]repository.SourceMatch{match})
	prompt, err := BuildAskPrompt("What value does the Add function return? Cite the provided evidence.",
		AskEvidenceResult{Evidence: evidence, MatchesByID: matches})
	if err != nil {
		t.Fatal("synthetic evidence could not be prepared")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	response, err := client.Complete(ctx, prompt)
	if err != nil {
		// Client errors carry only a classification and optional HTTP status.
		t.Fatalf("live model completion failed: %v", err)
	}
	parsed, err := ParseModelAnswer(response.Content)
	if err != nil {
		t.Fatal("live model output did not satisfy the answer JSON protocol")
	}
	grounded, err := GroundAnswer(parsed, matches)
	if err != nil || len(grounded.Citations) != 1 ||
		grounded.Citations[0].EvidenceID != evidence[0].ID ||
		grounded.Citations[0].ContentSHA256 != match.ContentSHA256 {
		t.Fatal("live model answer did not cite the supplied evidence")
	}
}
