package service

import (
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"strings"
	"unicode/utf8"

	"GoServer/internal/repository"
)

const (
	askAnswerByteLimit      = 8 << 10
	askModelOutputByteLimit = 64 << 10
)

var (
	ErrInvalidModelOutput       = errors.New("invalid model answer")
	ErrInvalidEvidenceReference = errors.New("invalid evidence reference")
	ErrInsufficientEvidence     = errors.New("insufficient evidence")
)

type ParsedAnswer struct {
	Answer      string
	EvidenceIDs []string
}

type Answer struct {
	Answer    string     `json:"answer"`
	Citations []Citation `json:"citations"`
}

type Citation struct {
	EvidenceID    string `json:"evidence_id"`
	Path          string `json:"path"`
	RevisionSHA   string `json:"revision_sha"`
	LineStart     uint32 `json:"line_start"`
	LineEnd       uint32 `json:"line_end"`
	ContentSHA256 string `json:"content_sha256"`
}

func ParseModelAnswer(content string) (ParsedAnswer, error) {
	// Validate the complete JSON value before decoding fields. The default
	// json.Unmarshal behavior accepts duplicate keys and replaces bad Unicode.
	if len(content) > askModelOutputByteLimit || !jsontext.Value(content).IsValid() {
		return ParsedAnswer{}, ErrInvalidModelOutput
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &fields); err != nil || len(fields) != 2 {
		return ParsedAnswer{}, ErrInvalidModelOutput
	}

	var parsed ParsedAnswer
	if json.Unmarshal(fields["answer"], &parsed.Answer) != nil ||
		json.Unmarshal(fields["evidence_ids"], &parsed.EvidenceIDs) != nil ||
		!validParsedAnswer(parsed) {
		return ParsedAnswer{}, ErrInvalidModelOutput
	}
	return parsed, nil
}

func validParsedAnswer(parsed ParsedAnswer) bool {
	if len(parsed.Answer) > askAnswerByteLimit || !utf8.ValidString(parsed.Answer) ||
		strings.TrimSpace(parsed.Answer) == "" || len(parsed.EvidenceIDs) == 0 || len(parsed.EvidenceIDs) > askEvidenceLimit {
		return false
	}
	seen := make(map[string]bool, len(parsed.EvidenceIDs))
	for _, id := range parsed.EvidenceIDs {
		if strings.TrimSpace(id) == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func GroundAnswer(parsed ParsedAnswer, matchesByID map[string]repository.SourceMatch) (Answer, error) {
	if !validParsedAnswer(parsed) {
		return Answer{}, ErrInvalidModelOutput
	}
	citations := make([]Citation, 0, len(parsed.EvidenceIDs))
	for _, id := range parsed.EvidenceIDs {
		match, ok := matchesByID[id]
		if !ok {
			return Answer{}, ErrInvalidEvidenceReference
		}
		citations = append(citations, Citation{
			EvidenceID: id, Path: match.Path, RevisionSHA: match.RevisionSHA,
			LineStart: match.LineStart, LineEnd: match.LineEnd, ContentSHA256: match.ContentSHA256,
		})
	}
	return Answer{Answer: parsed.Answer, Citations: citations}, nil
}
