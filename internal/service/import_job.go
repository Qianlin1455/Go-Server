package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"time"

	"GoServer/events"

	"GoServer/internal/model"
	"GoServer/internal/repository"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

const ImportCreateEndpoint = "POST /api/v1/projects/:id/imports"

var (
	ErrImportInvalidInput        = errors.New("invalid import input")
	ErrImportProjectNotFound     = errors.New("import project not found")
	ErrImportRepositoryNotFound  = errors.New("import repository not found")
	ErrImportIdempotencyConflict = errors.New("idempotency key conflict")
	ErrImportProjectArchived     = errors.New("import project archived")
)

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type CreateImportJobInput struct {
	ProjectID      uint64
	RepositoryID   uint64
	IdempotencyKey string
}

type ImportJobService struct {
	db    *gorm.DB
	jobs  *repository.ImportJobStore
	repos *repository.GitHubRepositoryStore
	gate  ImportGate
}

type ImportGate interface {
	TryAcquire(
		ctx context.Context,
		key string,
	) (token string, acquired bool, err error)

	Release(
		ctx context.Context,
		key, token string,
	) error
}

func NewImportJobService(db *gorm.DB) *ImportJobService {
	return &ImportJobService{
		db:    db,
		jobs:  repository.NewImportJobStore(db),
		repos: repository.NewGitHubRepositoryStore(db),
	}
}

func NewImportJobServiceWithGate(
	db *gorm.DB,
	gate ImportGate,
) *ImportJobService {
	svc := NewImportJobService(db)
	svc.gate = gate
	return svc
}

// 请求指纹只包含本次导入请求的稳定业务参数，不使用可能随时间变化的repo.last sha
func importFingerprint(projectID, repositoryID uint64, idempotencyKey string) string {
	canonical := fmt.Sprintf("import:v1:project=%d:repository=%d", projectID, repositoryID)

	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

func importGateKey(userID uint64, key string) string {
	raw := fmt.Sprintf(
		"%d\x00%s\x00%s",
		userID,
		ImportCreateEndpoint,
		key,
	)

	sum := sha256.Sum256([]byte(raw))

	return "go-server:v1:import:gate:" +
		hex.EncodeToString(sum[:])
}

func isImportDuplicateKey(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

// newOutboxEventID 生成 UUID v4。
func newOutboxEventID() (string, error) {
	var id [16]byte

	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("generate event id: %w", err)
	}

	// UUID version 4
	id[6] = (id[6] & 0x0f) | 0x40

	// RFC 4122 variant
	id[8] = (id[8] & 0x3f) | 0x80

	return fmt.Sprintf(
		"%x-%x-%x-%x-%x",
		id[0:4],
		id[4:6],
		id[6:8],
		id[8:10],
		id[10:16],
	), nil
}

// buildImportRequestedOutbox 构造导入任务的 Outbox 事件。
func buildImportRequestedOutbox(
	job *model.ImportJob,
) (*model.OutboxEvent, error) {
	if job == nil || job.ID == 0 {
		return nil, errors.New("invalid import job")
	}

	eventID, err := newOutboxEventID()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	// 1. 构造事件传输结构
	message := events.ImportRequestedV1{
		EventID:       eventID,
		EventType:     events.ImportRequestedType,
		SchemaVersion: events.ImportRequestedVersion,
		OccurredAt:    now,
		JobID:         job.ID,
		UserID:        job.UserID,
		ProjectID:     job.ProjectID,
		RepositoryID:  job.RepositoryID,
		RevisionSHA:   job.RevisionSHA,
	}

	// 2. 验证事件
	if err := message.Validate(); err != nil {
		return nil, fmt.Errorf("validate import event: %w", err)
	}

	// 3. 序列化 JSON
	payload, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("marshal import event: %w", err)
	}

	// 4. 构造数据库持久化模型
	return &model.OutboxEvent{
		EventID:       eventID,
		JobID:         job.ID,
		EventType:     events.ImportRequestedType,
		RevisionSHA:   job.RevisionSHA,
		SchemaVersion: uint32(events.ImportRequestedVersion),
		Payload:       string(payload),
		Status:        model.OutboxPending,
		Attempts:      0,
	}, nil
}

func (s *ImportJobService) CreateImportJob(
	ctx context.Context,
	userID uint64,
	input CreateImportJobInput,
) (*model.ImportJob, bool, error) {
	//校验插入
	if userID == 0 || input.ProjectID == 0 || input.RepositoryID == 0 || !idempotencyKeyPattern.MatchString(input.IdempotencyKey) {
		return nil, false, ErrImportInvalidInput
	}
	//校验项目所属
	project, err := repository.GetProjectByID(ctx, s.db, userID, input.ProjectID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) ||
			errors.Is(err, repository.ErrNotFound) {
			return nil, false, ErrImportProjectNotFound
		}
		return nil, false, err
	}

	fingerprint := importFingerprint(input.ProjectID, input.RepositoryID, input.IdempotencyKey)

	//查询是否有相同幂等键
	existing, err := s.jobs.GetByIdempotency(
		ctx, userID,
		ImportCreateEndpoint,
		input.IdempotencyKey,
	)
	if err == nil {
		if existing.RequestFingerprint != fingerprint {
			return nil, false, ErrImportIdempotencyConflict
		}
		//同一个请求
		return existing, false, nil
	}

	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}

	// Redis 仅作为加速门禁，MySQL UNIQUE 仍然承担最终防重。
	if s.gate != nil {
		gateKey := importGateKey(
			userID,
			input.IdempotencyKey,
		)

		token, acquired, gateErr := s.gate.TryAcquire(
			ctx, gateKey,
		)

		switch {
		case gateErr != nil:
			// Redis 不可用时回退 DB。
			log.Printf("import gate unavailable: %v", gateErr)

		case acquired:
			// 当前请求获得门禁。
			// 使用独立的清理 Context，防止请求取消后无法释放。
			defer func() {
				releaseCtx, cancel := context.WithTimeout(
					context.Background(),
					300*time.Millisecond,
				)
				defer cancel()

				if err := s.gate.Release(
					releaseCtx, gateKey, token,
				); err != nil {
					log.Printf("release import gate failed: %v", err)
				}
			}()

		default:
			// 别的请求正在创建同一个幂等任务。
			// 有界等待其提交，避免立即冲击数据库唯一索引。
			for i := 0; i < 10; i++ {
				existing, readErr := s.jobs.GetByIdempotency(
					ctx,
					userID,
					ImportCreateEndpoint,
					input.IdempotencyKey,
				)

				if readErr == nil {
					if existing.RequestFingerprint != fingerprint {
						return nil, false, ErrImportIdempotencyConflict
					}

					return existing, false, nil
				}

				if !errors.Is(readErr, gorm.ErrRecordNotFound) {
					return nil, false, readErr
				}

				select {
				case <-ctx.Done():
					return nil, false, ctx.Err()
				case <-time.After(20 * time.Millisecond):
				}
			}

			// 等待之后仍然没有任务记录：
			// 继续走 DB 创建逻辑，由 UNIQUE 兜底。
		}
	}

	//新任务需要检查项目状态
	if project.Status != model.ProjectStatusActive {
		return nil, false, ErrImportProjectArchived
	}

	//校验仓库是否属于这个用户
	binding, err := s.repos.GetByProject(
		ctx, userID, input.ProjectID, input.RepositoryID,
	)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrImportRepositoryNotFound
		}
		return nil, false, err
	}

	//构建新任务
	job := &model.ImportJob{
		UserID:             userID,
		ProjectID:          input.ProjectID,
		RepositoryID:       input.RepositoryID,
		Endpoint:           ImportCreateEndpoint,
		IdempotencyKey:     input.IdempotencyKey,
		RequestFingerprint: fingerprint,
		RevisionSHA:        binding.LastSHA,
		Status:             model.ImportJobQueued,
	}

	err = s.jobs.CreateWithOutbox(
		ctx,
		job,
		buildImportRequestedOutbox,
	)

	if err == nil {
		return job, true, nil
	}

	if !isImportDuplicateKey(err) {
		return nil, false, fmt.Errorf("create import job:%w", err)
	}

	//发生并发唯一键冲突,另一个请求创建了任务
	existing, err = s.jobs.GetByIdempotency(
		ctx, userID,
		ImportCreateEndpoint,
		input.IdempotencyKey,
	)
	if err != nil {
		return nil, false, fmt.Errorf("read existing import job:%w", err)
	}

	//再次比较请求指纹
	if existing.RequestFingerprint != fingerprint {
		return nil, false, ErrImportIdempotencyConflict
	}
	return existing, false, nil
}
