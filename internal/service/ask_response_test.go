package service

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"GoServer/internal/model"
	"GoServer/internal/repository"
)

func TestParseModelAnswer(t *testing.T) {
	got, err := ParseModelAnswer(" \n" + `{"evidence_ids":["ev_2","ev_1"],"answer":"  原始回答\n"}` + "\r\n")
	if err != nil || got.Answer != "  原始回答\n" || !slices.Equal(got.EvidenceIDs, []string{"ev_2", "ev_1"}) {
		t.Fatalf("answer did not preserve content and citation order: %+v, %v", got, err)
	}
	escaped, err := ParseModelAnswer(`{"answer":"\ud83d\ude00\ufffd","evidence_ids":["ev_1"]}`)
	if err != nil || escaped.Answer != "😀�" {
		t.Fatalf("valid Unicode escape sequences were rejected: %v", err)
	}
	atLimit := strings.Repeat("字", 2730) + "ab"
	for _, tc := range []struct {
		name, answer string
		valid        bool
	}{
		{"8 KiB UTF-8", atLimit, true},
		{"8 KiB plus 1 byte", atLimit + "c", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{"answer": tc.answer, "evidence_ids": []string{"ev_1"}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := ParseModelAnswer(string(payload))
			if tc.valid {
				if err != nil || got.Answer != tc.answer {
					t.Fatalf("valid byte boundary rejected: %v", err)
				}
			} else if !errors.Is(err, ErrInvalidModelOutput) {
				t.Fatalf("oversized UTF-8 answer: %v", err)
			}
		})
	}
}

func TestParseModelAnswerRejectsInvalidOutput(t *testing.T) {
	const valid = `{"answer":"ok","evidence_ids":["ev_1"]}`
	for _, tc := range []struct{ name, payload string }{
		{"invalid JSON", `{"answer":!`},
		{"truncated JSON", `{"answer":"ok","evidence_ids":["ev_1"]`},
		{"invalid UTF-8", `{"answer":"` + "\xff" + `","evidence_ids":["ev_1"]}`},
		{"unpaired surrogate", `{"answer":"\ud800","evidence_ids":["ev_1"]}`},
		{"trailing object", valid + `{}`},
		{"trailing scalar", valid + ` null`},
		{"trailing invalid content", valid + " garbage"},
		{"Markdown fence", "```json\n" + valid + "\n```"},
		{"non object", "[" + valid + "]"},
		{"unknown field", `{"answer":"ok","evidence_ids":["ev_1"],"extra":true}`},
		{"case variant key", `{"Answer":"ok","evidence_ids":["ev_1"]}`},
		{"duplicate answer", `{"answer":"original","answer":"override","evidence_ids":["ev_1"]}`},
		{"escaped duplicate key", `{"answer":"original","ans\u0077er":"override","evidence_ids":["ev_1"]}`},
		{"duplicate evidence_ids", `{"answer":"ok","evidence_ids":["ev_1"],"evidence_ids":["ev_2"]}`},
		{"empty answer", `{"answer":"","evidence_ids":["ev_1"]}`},
		{"whitespace answer", `{"answer":" \r\n\u3000","evidence_ids":["ev_1"]}`},
		{"missing answer", `{"evidence_ids":["ev_1"]}`},
		{"null answer", `{"answer":null,"evidence_ids":["ev_1"]}`},
		{"empty references", `{"answer":"ok","evidence_ids":[]}`},
		{"missing references", `{"answer":"ok"}`},
		{"null references", `{"answer":"ok","evidence_ids":null}`},
		{"too many references", `{"answer":"ok","evidence_ids":["1","2","3","4","5"]}`},
		{"duplicate reference", `{"answer":"ok","evidence_ids":["ev_1","ev_\u0031"]}`},
		{"empty ID", `{"answer":"ok","evidence_ids":[""]}`},
		{"whitespace ID", `{"answer":"ok","evidence_ids":[" \t"]}`},
		{"null ID", `{"answer":"ok","evidence_ids":[null]}`},
		{"path override", `{"answer":"ok","evidence_ids":["ev_1"],"path":"forged.go"}`},
		{"line number override", `{"answer":"ok","evidence_ids":["ev_1"],"line_start":999}`},
		{"revision override", `{"answer":"ok","evidence_ids":["ev_1"],"revision_sha":"forged"}`},
		{"embedded citation metadata", `{"answer":"ok","evidence_ids":[{"evidence_id":"ev_1","path":"forged.go"}]}`},
		{"oversized raw JSON", valid + strings.Repeat(" ", askModelOutputByteLimit)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseModelAnswer(tc.payload)
			if !errors.Is(err, ErrInvalidModelOutput) || got.Answer != "" || len(got.EvidenceIDs) != 0 {
				t.Fatalf("invalid model output produced usable data: %v", err)
			}
		})
	}
}

func TestGroundAnswerUsesOnlyTrustedMatches(t *testing.T) {
	matches := map[string]repository.SourceMatch{
		"ev_1": {
			Path: "src/one.go", RevisionSHA: "first-revision",
			SourceSpan: model.SourceSpan{LineStart: 10, LineEnd: 20, ContentSHA256: "first-hash"},
		},
		"ev_2": {
			Path: "src/two.go", RevisionSHA: "second-revision",
			SourceSpan: model.SourceSpan{LineStart: 30, LineEnd: 40, ContentSHA256: "second-hash"},
		},
	}
	parsed, err := ParseModelAnswer(`{"answer":"用源码解释","evidence_ids":["ev_2","ev_1"]}`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := GroundAnswer(parsed, matches)
	want := []Citation{
		{EvidenceID: "ev_2", Path: "src/two.go", RevisionSHA: "second-revision", LineStart: 30, LineEnd: 40, ContentSHA256: "second-hash"},
		{EvidenceID: "ev_1", Path: "src/one.go", RevisionSHA: "first-revision", LineStart: 10, LineEnd: 20, ContentSHA256: "first-hash"},
	}
	if err != nil || got.Answer != "用源码解释" || !slices.Equal(got.Citations, want) {
		t.Fatalf("citations did not come from trusted matches in reference order: %+v, %v", got, err)
	}
	for _, tc := range []struct {
		name string
		ids  []string
	}{
		{"forged only", []string{"unknown"}},
		{"mixed reference list", []string{"ev_1", "unknown"}},
		{"near match", []string{" ev_1 "}},
		{"other request", []string{"previous_request_id"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GroundAnswer(ParsedAnswer{Answer: "text", EvidenceIDs: tc.ids}, matches)
			if !errors.Is(err, ErrInvalidEvidenceReference) || got.Answer != "" || len(got.Citations) != 0 {
				t.Fatalf("invalid reference leaked a partial answer: %+v, %v", got, err)
			}
		})
	}
}
