package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"GoServer/events"
	"GoServer/internal/model"

	"github.com/IBM/sarama"
	"gorm.io/gorm"
)

const jobStatePollInterval = 500 * time.Millisecond

type JobStore interface {
	GetByID(
		ctx context.Context,
		jobID uint64,
	) (*model.ImportJob, error)

	RecoverExpiredJobs(
		ctx context.Context,
	) (int64, int64, error)
}

type JobProcessor interface {
	ProcessJob(
		ctx context.Context,
		jobID uint64,
	) (bool, error)
}

type Handler struct {
	jobs   JobStore
	runner JobProcessor
	dlq    DeadLetterProducer

	// 第一版保证同一个进程内串行处理消息。
	processSem chan struct{}

	// 多个 Partition 的 Claim 可能并发执行。
	// 串行提交，避免同时调用 Commit。
	commitMu sync.Mutex
}

func NewHandler(
	jobs JobStore,
	runner JobProcessor,
	dlq ...DeadLetterProducer,
) (*Handler, error) {
	if jobs == nil || runner == nil || len(dlq) > 1 || (len(dlq) == 1 && dlq[0] == nil) {
		return nil, errors.New("invalid consumer handler configuration")
	}

	h := &Handler{
		jobs:       jobs,
		runner:     runner,
		processSem: make(chan struct{}, 1),
	}
	if len(dlq) == 1 {
		h.dlq = dlq[0]
	}
	return h, nil
}

// Setup 在 Consumer Session 建立时调用。
func (h *Handler) Setup(
	session sarama.ConsumerGroupSession,
) error {
	log.Printf(
		"consumer session setup: member=%s generation=%d claims=%v",
		session.MemberID(),
		session.GenerationID(),
		session.Claims(),
	)

	return nil
}

// Cleanup 在 Consumer Session 结束时调用。
func (h *Handler) Cleanup(
	session sarama.ConsumerGroupSession,
) error {
	log.Printf(
		"consumer session cleanup: member=%s generation=%d cause=%v",
		session.MemberID(),
		session.GenerationID(),
		context.Cause(session.Context()),
	)

	return nil
}

// ConsumeClaim 接收消息、执行任务，最后提交 Offset。
func (h *Handler) ConsumeClaim(
	session sarama.ConsumerGroupSession,
	claim sarama.ConsumerGroupClaim,
) error {
	for {
		select {
		case <-session.Context().Done():
			return nil

		case msg, ok := <-claim.Messages():
			if !ok {
				return nil
			}
			started := time.Now()

			// 控制同一个 Consumer 进程内的任务并发。
			select {
			case h.processSem <- struct{}{}:
			case <-session.Context().Done():
				return nil
			}

			err := func() error {
				defer func() {
					<-h.processSem
				}()

				if err := session.Context().Err(); err != nil {
					return err
				}
				err := h.handleMessage(session.Context(), msg)
				var permanent *permanentMessageError
				if errors.As(err, &permanent) {
					return h.sendDeadLetter(session.Context(), msg, permanent)
				}
				return err
			}()

			// 业务处理失败，不能标记消息，
			// 也不能提交 Offset。
			if err != nil {
				return fmt.Errorf(
					"process kafka message topic=%s partition=%d offset=%d event_id=%q job_id=%d elapsed=%s: %w",
					msg.Topic,
					msg.Partition,
					msg.Offset,
					readEventID(msg.Value),
					readJobID(msg.Value),
					time.Since(started),
					err,
				)
			}

			// A persisted terminal state or a confirmed dead letter permits the commit.
			h.commitMu.Lock()
			if session.Context().Err() != nil {
				h.commitMu.Unlock()
				return nil
			}

			session.MarkMessage(msg, "")
			session.Commit()

			h.commitMu.Unlock()

			log.Printf(
				"offset commit requested: event_id=%q job_id=%d partition=%d offset=%d elapsed=%s",
				readEventID(msg.Value),
				readJobID(msg.Value),
				msg.Partition,
				msg.Offset,
				time.Since(started),
			)
		}
	}
}

// handleMessage 解析事件并执行对应业务任务。
func (h *Handler) handleMessage(
	ctx context.Context,
	msg *sarama.ConsumerMessage,
) error {
	var event events.ImportRequestedV1

	// 1. JSON 解析
	if err := json.Unmarshal(msg.Value, &event); err != nil {
		return &permanentMessageError{"INVALID_EVENT_JSON", "cannot decode import event JSON"}
	}

	// 2. 事件结构校验
	if err := event.Validate(); err != nil {
		code := "INVALID_EVENT_FIELDS"
		switch {
		case errors.Is(err, events.ErrInvalidImportEventType):
			code = "INVALID_EVENT_TYPE"
		case errors.Is(err, events.ErrUnsupportedImportEventVersion):
			code = "UNSUPPORTED_EVENT_VERSION"
		}
		return &permanentMessageError{code, err.Error()}
	}

	log.Printf(
		"received import event: event_id=%q job_id=%d revision=%s",
		event.EventID,
		event.JobID,
		event.RevisionSHA,
	)

	// 3. 执行任务，直到进入持久化终态。
	return h.processUntilTerminal(ctx, event)
}

// processUntilTerminal 直到任务进入 succeeded 或 failed 才返回成功。
func (h *Handler) processUntilTerminal(
	ctx context.Context,
	event events.ImportRequestedV1,
) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		// 1. 查询 MySQL 中的真实任务状态。
		job, err := h.jobs.GetByID(ctx, event.JobID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return &permanentMessageError{"IMPORT_JOB_NOT_FOUND", "referenced import job does not exist"}
			}
			return fmt.Errorf(
				"load import job %d: %w",
				event.JobID,
				err,
			)
		}

		if job == nil {
			return fmt.Errorf(
				"import job %d not found",
				event.JobID,
			)
		}

		// 2. 验证 Kafka 事件与 MySQL 任务一致。
		if err := validateEventJob(event, job); err != nil {
			return &permanentMessageError{"IMPORT_JOB_MISMATCH", "event and import job fields do not match"}
		}

		switch job.Status {

		case model.ImportJobSucceeded:
			// 重复投递：之前已经成功。
			// 不再执行 GitHub 导入。
			log.Printf(
				"job=%d already succeeded, skip duplicate event=%q",
				job.ID,
				event.EventID,
			)
			return nil

		case model.ImportJobFailed:
			// 永久失败或重试次数耗尽。
			// 失败状态已经持久化，可以确认消息。
			log.Printf(
				"job=%d already failed, skip duplicate event=%q",
				job.ID,
				event.EventID,
			)
			return nil

		case model.ImportJobQueued:
			// 3. 尝试领取指定任务。
			// ClaimQueuedJob 内部使用 MySQL NOW(3)
			// 判断 available_at，避免应用时钟偏差。
			claimed, err := h.runner.ProcessJob(
				ctx,
				job.ID,
			)
			if err != nil {
				return fmt.Errorf(
					"process import job %d: %w",
					job.ID,
					err,
				)
			}

			if !claimed {
				// 可能还没到重试时间，
				// 也可能被其他 Worker 抢先领取。
				if err := waitContext(
					ctx,
					jobStatePollInterval,
				); err != nil {
					return err
				}
			}

			// 无论是否领取成功，都重新查询数据库。
			// 如果执行失败后重新排队，则等待下一次重试。
			continue

		case model.ImportJobRunning:
			// 4. 任务正在被其他 Worker 执行。
			// 不能把 running 误认为处理成功。
			if job.LeaseExpiresAt == nil {
				return fmt.Errorf(
					"job %d is running without lease expiration",
					job.ID,
				)
			}

			remaining := time.Until(*job.LeaseExpiresAt)

			if remaining <= 0 {
				// Lease 已过期，尝试恢复任务。
				// RecoverExpiredJobs 内部使用数据库时间。
				_, _, err := h.jobs.RecoverExpiredJobs(ctx)
				if err != nil {
					return fmt.Errorf(
						"recover expired job %d: %w",
						job.ID,
						err,
					)
				}

				continue
			}

			if remaining > jobStatePollInterval {
				remaining = jobStatePollInterval
			}

			if err := waitContext(ctx, remaining); err != nil {
				return err
			}

			continue

		default:
			return fmt.Errorf(
				"job %d has unsupported status: %s",
				job.ID,
				job.Status,
			)
		}
	}
}

// 验证事件中的资源归属和 Revision 与 MySQL 任务一致。
func validateEventJob(
	event events.ImportRequestedV1,
	job *model.ImportJob,
) error {
	if job.ID != event.JobID ||
		job.UserID != event.UserID ||
		job.ProjectID != event.ProjectID ||
		job.RepositoryID != event.RepositoryID ||
		!strings.EqualFold(
			job.RevisionSHA,
			event.RevisionSHA,
		) {
		return fmt.Errorf(
			"event/job mismatch: event_id=%s job_id=%d",
			event.EventID,
			event.JobID,
		)
	}

	return nil
}

func waitContext(
	ctx context.Context,
	duration time.Duration,
) error {
	if duration <= 0 {
		return nil
	}

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()

	case <-timer.C:
		return nil
	}
}

func readEventID(value []byte) string {
	var event events.ImportRequestedV1
	if json.Unmarshal(value, &event) != nil {
		return ""
	}
	return event.EventID
}

func readJobID(value []byte) uint64 {
	var event events.ImportRequestedV1
	if json.Unmarshal(value, &event) != nil {
		return 0
	}
	return event.JobID
}
