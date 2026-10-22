package handler

import (
	"net/http"

	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

type EvidenceHandler struct {
	svc *service.EvidenceService
}

func NewEvidenceHandler(
	svc *service.EvidenceService,
) *EvidenceHandler {
	return &EvidenceHandler{svc: svc}
}

type CreateEvidenceRequest struct {
	Type      string  `json:"type" binding:"required"`
	Text      *string `json:"text"`
	CommitSHA *string `json:"commit_sha"`
	Status    string  `json:"status" binding:"required"`
}

func (h *EvidenceHandler) CreateEvidence(
	c *gin.Context,
) {
	// 1. 从 JWT 获取当前用户
	userID, ok := taskUserID(c)
	if !ok {
		return
	}

	// 2. 解析 Task ID
	taskID, ok := parseTaskPathID(c, "id")
	if !ok {
		return
	}

	// 3. JSON 请求绑定
	var req CreateEvidenceRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "证据提交参数不合法",
		})
		return
	}

	// 4. 构造 Service 输入
	input := service.CreateEvidenceInput{
		Type:      req.Type,
		Text:      req.Text,
		CommitSHA: req.CommitSHA,
		Status:    req.Status,
	}

	// 5. 调用 Service
	result, err := h.svc.CreateEvidence(
		c.Request.Context(),
		userID,
		taskID,
		input,
	)

	if err != nil {
		WriteServiceerror(c, err)
		return
	}

	// 6. 创建成功
	c.JSON(http.StatusCreated, gin.H{
		"code": "SUCCESS",
		"data": result,
	})
}
