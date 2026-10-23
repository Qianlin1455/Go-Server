package service

import (
	"GoServer/internal/model"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

	"context"
	"fmt"
	"time"

	"GoServer/internal/repository"

	"gorm.io/gorm"
)

var (
	ErrInvalidEvidenceType = errors.New(
		"invalid evidence type",
	)
	ErrInvalidEvidenceStatus = errors.New(
		"invalid evidence status",
	)
	ErrInvalidEvidenceContent = errors.New(
		"invalid evidence content",
	)
)

type CreateEvidenceInput struct {
	Type      string
	Text      *string
	CommitSHA *string
	Status    string
}

type EvidenceService struct {
	db *gorm.DB
}

func NewEvidenceService(db *gorm.DB) *EvidenceService {
	return &EvidenceService{db: db}
}

type EvidenceResult struct {
	ID        uint64    `json:"id"`
	TaskID    uint64    `json:"task_id"`
	Type      string    `json:"type"`
	Text      *string   `json:"text"`
	CommitSHA *string   `json:"commit_sha"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func ValidateEvidenceInput(input CreateEvidenceInput) error {

	// 1. 校验证据状态
	switch input.Status {
	case model.EvidenceStatusDraft,
		model.EvidenceStatusSubmitted:
	default:
		return ErrInvalidEvidenceStatus
	}

	submitted := input.Status == model.EvidenceStatusSubmitted

	// 2. 根据不同 Type 进行校验
	switch input.Type {

	case model.EvidenceTypeExperimentText,
		model.EvidenceTypeTestOutput:

		// 文字类型不能携带 CommitSHA
		if input.CommitSHA != nil {
			return ErrInvalidEvidenceContent
		}

		return validateEvidenceText(input.Text, submitted)

	case model.EvidenceTypeCommitSHA:

		// SHA 类型不能携带 Text
		if input.Text != nil {
			return ErrInvalidEvidenceContent
		}

		return validateEvidenceSHA(input.CommitSHA, submitted)

	default:
		return ErrInvalidEvidenceType
	}
}

func validateEvidenceText(
	text *string,
	submitted bool,
) error {

	// 1. 判断是否提供文本
	if text == nil {
		if submitted {
			return ErrInvalidEvidenceContent
		}
		return nil
	}
	// 2. 校验 UTF-8 编码合法性
	if !utf8.ValidString(*text) {
		return ErrInvalidEvidenceContent
	}
	// 3. 限制最大字符数
	if utf8.RuneCountInString(*text) > 10000 {
		return ErrInvalidEvidenceContent
	}
	// 4. 正式提交不允许空白内容
	if submitted && strings.TrimSpace(*text) == "" {
		return ErrInvalidEvidenceContent
	}
	return nil
}

func validateEvidenceSHA(
	sha *string,
	submitted bool,
) error {
	// 1. 没有提供 SHA
	if sha == nil {
		if submitted {
			return ErrInvalidEvidenceContent
		}
		return nil
	}
	// 2. 草稿允许空字符串
	if *sha == "" {
		if submitted {
			return ErrInvalidEvidenceContent
		}
		return nil
	}
	// 3. 长度必须是 40 或 64
	if len(*sha) != 40 && len(*sha) != 64 {
		return ErrInvalidEvidenceContent
	}
	// 4. 必须为十六进制字符串
	if _, err := hex.DecodeString(*sha); err != nil {
		return ErrInvalidEvidenceContent
	}

	return nil
}

func (s *EvidenceService) CreateEvidence(
	ctx context.Context,
	userID uint64,
	taskID uint64,
	input CreateEvidenceInput,
) (*EvidenceResult, error) {

	// 1. 基础参数校验
	if userID == 0 || taskID == 0 {
		return nil, ErrInvalidTaskInput
	}

	// 2. 证据业务规则校验
	if err := ValidateEvidenceInput(input); err != nil {
		return nil, err
	}

	var created *model.TaskEvidence

	// 3. 开启事务
	err := repository.Transaction(ctx, s.db,
		func(tx *gorm.DB) error {

			// 4. 校验 Task 归属
			task, err := repository.GetTaskByUserID(ctx, tx, userID, taskID)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return ErrTaskNotFound
				}
				return err
			}

			// 5. 锁定并检查所属 Project
			project, err := repository.GetProjectForUpdate(ctx, tx, userID, task.ProjectID)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return ErrTaskNotFound
				}
				return err
			}

			// 6. 归档项目禁止提交证据
			if project.Status != model.ProjectStatusActive {
				return ErrTaskProjectArchived
			}

			// 7. 构造 Evidence
			evidence := &model.TaskEvidence{
				TaskID:    task.ID,
				CreatedBy: userID,
				Type:      input.Type,
				Text:      input.Text,
				CommitSHA: input.CommitSHA,
				Status:    input.Status,
			}

			// 8. 在同一事务内写入证据
			if err := repository.CreateTaskEvidence(
				ctx,
				tx,
				evidence,
			); err != nil {
				return fmt.Errorf(
					"create evidence: %w",
					err,
				)
			}

			created = evidence
			return nil
		},
	)

	// 9. 事务失败不能返回成功结果
	if err != nil {
		return nil, err
	}

	// 10. 事务成功后构造响应
	return &EvidenceResult{
		ID:        created.ID,
		TaskID:    created.TaskID,
		Type:      created.Type,
		Text:      created.Text,
		CommitSHA: created.CommitSHA,
		Status:    created.Status,
		CreatedAt: created.CreatedAt,
	}, nil
}
