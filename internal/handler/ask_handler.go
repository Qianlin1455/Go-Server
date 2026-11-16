package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

const askRequestByteLimit = 16 << 10

type AskHandler struct {
	svc *service.AskService
}

func NewAskHandler(svc *service.AskService) *AskHandler { return &AskHandler{svc: svc} }

func (h *AskHandler) RegisterRoutes(private *gin.RouterGroup) {
	private.POST("/projects/:id/ai/ask", h.Ask)
}

type askRequest struct {
	Question       string  `json:"question"`
	RevisionSHA    string  `json:"revision_sha"`
	ImportJobID    *uint64 `json:"import_job_id"`
	ConversationID *uint64 `json:"conversation_id"`
}

func (h *AskHandler) Ask(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	userID, projectID, _, ok := sourceRequestIDs(c, false)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, askRequestByteLimit)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var request askRequest
	err := decoder.Decode(&request)
	if err == nil {
		if tailErr := decoder.Decode(&struct{}{}); tailErr != io.EOF {
			err = tailErr
			if err == nil {
				err = service.ErrInvalidSourceInput
			}
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case ctx.Err() != nil:
			writeAskError(c, ctx.Err())
		case errors.As(err, &tooLarge):
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"code": "REQUEST_TOO_LARGE", "message": "请求体超过大小限制",
			})
		default:
			WriteServiceerror(c, service.ErrInvalidSourceInput)
		}
		return
	}

	input := service.AskInput{AskEvidenceInput: service.AskEvidenceInput{
		UserID: userID, ProjectID: projectID, Question: request.Question, RevisionSHA: request.RevisionSHA,
	}}
	if request.ImportJobID != nil {
		if *request.ImportJobID == 0 {
			WriteServiceerror(c, service.ErrInvalidSourceInput)
			return
		}
		input.ImportJobID = *request.ImportJobID
	}
	if request.ConversationID != nil {
		if *request.ConversationID == 0 {
			WriteServiceerror(c, service.ErrInvalidSourceInput)
			return
		}
		input.ConversationID = *request.ConversationID
	}
	result, err := h.svc.Ask(ctx, input)
	if parentErr := ctx.Err(); parentErr != nil {
		err = parentErr
	}
	if err != nil {
		writeAskError(c, err)
		return
	}
	code := "SUCCESS"
	if result.InsufficientEvidence {
		code = "INSUFFICIENT_EVIDENCE"
	}
	c.JSON(http.StatusOK, gin.H{"code": code, "data": result})
}

func writeAskError(c *gin.Context, err error) {
	if errors.Is(err, context.Canceled) {
		c.AbortWithStatusJSON(http.StatusRequestTimeout, gin.H{
			"code": "REQUEST_CANCELED", "message": "请求已取消",
		})
		return
	}
	WriteServiceerror(c, err)
}
