package service

import (
	"context"
	"crypto/rand"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"GoServer/internal/repository"
)

const (
	askEvidenceLimit    = 4
	askSpanByteLimit    = 4 << 10
	askContextByteLimit = 16 << 10
	askQuestionLimit    = 2 << 10
)

type AskEvidenceInput struct {
	// UserID comes from the authenticated caller's context.
	UserID      uint64
	ProjectID   uint64
	RevisionSHA string
	ImportJobID uint64
	Question    string
}

type AskEvidenceResult struct {
	ImportJobID          uint64                            `json:"import_job_id"`
	RevisionSHA          string                            `json:"revision_sha"`
	Evidence             []Evidence                        `json:"evidence"`
	MatchesByID          map[string]repository.SourceMatch `json:"-"`
	InsufficientEvidence bool                              `json:"insufficient_evidence"`
}

type Evidence struct {
	ID            string `json:"evidence_id"`
	SourceFileID  uint64 `json:"source_file_id"`
	SourceSpanID  uint64 `json:"source_span_id"`
	ImportJobID   uint64 `json:"import_job_id"`
	RevisionSHA   string `json:"revision_sha"`
	Path          string `json:"path"`
	LineStart     uint32 `json:"line_start"`
	LineEnd       uint32 `json:"line_end"`
	ContentSHA256 string `json:"content_sha256"`
	Content       string `json:"content"`
}

func validateAskEvidenceInput(input AskEvidenceInput) error {
	if input.UserID == 0 || input.ProjectID == 0 || !sourceRevisionPattern.MatchString(input.RevisionSHA) ||
		len(input.Question) > askQuestionLimit || !utf8.ValidString(input.Question) ||
		strings.TrimSpace(input.Question) == "" || strings.ContainsRune(input.Question, 0) {
		return ErrInvalidSourceInput
	}
	return nil
}

func (s *SourceService) BuildAskEvidence(ctx context.Context, input AskEvidenceInput) (AskEvidenceResult, error) {
	if err := validateAskEvidenceInput(input); err != nil {
		return AskEvidenceResult{}, err
	}
	keywords := askKeywords(input.Question)
	scope := repository.SourceScope{UserID: input.UserID, ProjectID: input.ProjectID}
	revision := strings.ToLower(input.RevisionSHA)

	queryCtx, cancel := context.WithTimeout(ctx, sourceSearchTimeout)
	defer cancel()
	jobID, err := s.resolveVisibleSnapshot(queryCtx, scope, revision, input.ImportJobID)
	if err != nil {
		return AskEvidenceResult{}, err
	}

	var matches []repository.SourceMatch
	if len(keywords) != 0 {
		matches, err = s.store.SearchEvidenceCandidates(queryCtx, scope, jobID, revision, keywords, askSpanByteLimit)
		if err != nil {
			return AskEvidenceResult{}, err
		}
	}
	if err := queryCtx.Err(); err != nil {
		return AskEvidenceResult{}, err
	}
	evidence, matchesByID := buildEvidence(matches)
	return AskEvidenceResult{
		ImportJobID: jobID, RevisionSHA: revision, Evidence: evidence, MatchesByID: matchesByID,
		InsufficientEvidence: len(evidence) == 0,
	}, nil
}

func buildEvidence(matches []repository.SourceMatch) ([]Evidence, map[string]repository.SourceMatch) {
	evidence := make([]Evidence, 0, askEvidenceLimit)
	matchesByID := make(map[string]repository.SourceMatch, askEvidenceLimit)
	var totalBytes int
	for _, match := range matches {
		if len(evidence) == askEvidenceLimit {
			break
		}
		size := len(match.Content)
		if size == 0 || size > askSpanByteLimit || size > askContextByteLimit-totalBytes {
			continue
		}
		id := rand.Text()
		for {
			if _, exists := matchesByID[id]; !exists {
				break
			}
			id = rand.Text()
		}
		matchesByID[id] = match
		evidence = append(evidence, Evidence{
			ID: id, SourceFileID: match.SourceFileID, SourceSpanID: match.ID,
			ImportJobID: match.ImportJobID, RevisionSHA: match.RevisionSHA, Path: match.Path,
			LineStart: match.LineStart, LineEnd: match.LineEnd, ContentSHA256: match.ContentSHA256, Content: match.Content,
		})
		totalBytes += size
	}
	return evidence, matchesByID
}

var (
	askQuotedTerm = regexp.MustCompile("`([^`]+)`")
	askCodeTerm   = regexp.MustCompile(`[A-Za-z0-9_./-]+`)
	askWordTerm   = regexp.MustCompile(`[\p{L}\p{M}\p{N}_]+`)
	askStopWords  = strings.Fields("a an the and or but of for to in on at by with as is are be was were it this that " +
		"can could does do did how what which where when why who would should please explain show tell me find about")
)

func askKeywords(question string) []string {
	keywords := make([]string, 0, repository.EvidenceKeywordLimit)
	seen := make(map[string]bool)
	add := func(term string) {
		if len(keywords) == repository.EvidenceKeywordLimit || seen[term] || len(term) > sourceMaxKeywordBytes ||
			strings.IndexFunc(term, unicode.IsControl) >= 0 || strings.IndexFunc(term, unicode.IsLetter) < 0 {
			return
		}
		keywords = append(keywords, term)
		seen[term] = true
	}

	for _, quoted := range askQuotedTerm.FindAllStringSubmatch(question, -1) {
		add(strings.TrimSpace(quoted[1]))
	}
	prose := askQuotedTerm.ReplaceAllString(question, " ")
	codeTerms := askCodeTerm.FindAllString(prose, -1)
	// Paths and qualified names take priority over ordinary words.
	for _, raw := range codeTerms {
		term := strings.Trim(raw, ".-")
		if strings.ContainsAny(term, "./") {
			add(term)
		}
	}
	for _, term := range codeTerms {
		term = strings.Trim(term, ".-/")
		if len(term) > 1 && !strings.ContainsAny(term, "./") &&
			(strings.ContainsAny(term, "_0123456789") || strings.IndexFunc(term[1:], unicode.IsUpper) >= 0) {
			add(term)
		}
	}
	if len(keywords) != 0 {
		return keywords
	}
	for _, term := range askWordTerm.FindAllString(prose, -1) {
		if utf8.RuneCountInString(term) < 2 || isAskStopWord(term) {
			continue
		}
		add(term)
	}
	return keywords
}

func isAskStopWord(term string) bool {
	for _, stop := range askStopWords {
		if strings.EqualFold(term, stop) {
			return true
		}
	}
	return false
}
