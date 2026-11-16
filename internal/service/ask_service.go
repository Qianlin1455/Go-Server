package service

import (
	"context"
	"errors"

	"GoServer/internal/llmclient"
	"GoServer/internal/repository"
)

type AskService struct {
	source   *SourceService
	complete llmclient.Completer
}

// A nil completer indicates that AI is disabled in the server configuration.
func NewAskService(source *SourceService, complete llmclient.Completer) *AskService {
	return &AskService{source: source, complete: complete}
}

type AskInput struct {
	AskEvidenceInput
	ConversationID uint64
}

type AnswerResponse struct {
	Answer
	RevisionSHA          string `json:"revision_sha"`
	ImportJobID          uint64 `json:"import_job_id"`
	InsufficientEvidence bool   `json:"-"`
}

func (s *AskService) Ask(ctx context.Context, input AskInput) (AnswerResponse, error) {
	if err := validateAskEvidenceInput(input.AskEvidenceInput); err != nil {
		return AnswerResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return AnswerResponse{}, err
	}
	evidence, err := s.retrieveEvidence(ctx, input)
	if parentErr := ctx.Err(); parentErr != nil {
		return AnswerResponse{}, parentErr
	}
	if err != nil && !errors.Is(err, ErrInsufficientEvidence) {
		return AnswerResponse{}, err
	}
	result := AnswerResponse{ImportJobID: evidence.ImportJobID, RevisionSHA: evidence.RevisionSHA}
	if errors.Is(err, ErrInsufficientEvidence) || evidence.InsufficientEvidence {
		return insufficientAnswer(result), nil
	}
	prompt, err := BuildAskPrompt(input.Question, evidence)
	if errors.Is(err, ErrInsufficientEvidence) {
		return insufficientAnswer(result), nil
	}
	if err != nil {
		// A mismatch while assembling trusted retrieval data is an internal
		// error. At this point no model response has been received.
		return AnswerResponse{}, errors.New("could not build prompt from retrieved evidence")
	}
	if err := ctx.Err(); err != nil {
		return AnswerResponse{}, err
	}
	modelResponse, err := s.complete.Complete(ctx, prompt)
	if parentErr := ctx.Err(); parentErr != nil {
		return AnswerResponse{}, parentErr
	}
	if err != nil {
		return AnswerResponse{}, err
	}
	parsed, err := ParseModelAnswer(modelResponse.Content)
	if err != nil {
		return AnswerResponse{}, err
	}
	result.Answer, err = GroundAnswer(parsed, evidence.MatchesByID)
	if err != nil {
		return AnswerResponse{}, err
	}
	return result, nil
}

func (s *AskService) retrieveEvidence(ctx context.Context, input AskInput) (AskEvidenceResult, error) {
	ctx, cancel := context.WithTimeout(ctx, sourceSearchTimeout)
	defer cancel()
	// BuildAskEvidence checks the project itself. Check it here as well
	// when there is an earlier authorization step or an early exit.
	if input.ConversationID != 0 || s.complete == nil {
		scope := repository.SourceScope{UserID: input.UserID, ProjectID: input.ProjectID}
		if err := s.source.checkProject(ctx, scope); err != nil {
			return AskEvidenceResult{}, err
		}
	}
	if input.ConversationID != 0 {
		conversation, err := repository.GetConversationByID(ctx, s.source.db, input.UserID, input.ConversationID)
		if errors.Is(err, repository.ErrNotFound) {
			return AskEvidenceResult{}, ErrConversationNotFound
		}
		if err != nil {
			return AskEvidenceResult{}, err
		}
		if conversation.ProjectID == nil || *conversation.ProjectID != input.ProjectID {
			return AskEvidenceResult{}, ErrConversationNotFound
		}
	}
	if s.complete == nil {
		return AskEvidenceResult{}, llmclient.ErrDisabled
	}
	return s.source.BuildAskEvidence(ctx, input.AskEvidenceInput)
}

func insufficientAnswer(result AnswerResponse) AnswerResponse {
	result.Answer = Answer{Answer: "当前源码快照中的证据不足，无法核实这个问题。", Citations: []Citation{}}
	result.InsufficientEvidence = true
	return result
}
