package consumer

import (
	"encoding/json"
	"log"

	"GoServer/events"

	"github.com/IBM/sarama"
)

type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
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
		"consumer session cleanup: member=%s",
		session.MemberID(),
	)

	return nil
}

// ConsumeClaim 真正接收 Kafka 消息。
func (h *Handler) ConsumeClaim(
	session sarama.ConsumerGroupSession,
	claim sarama.ConsumerGroupClaim,
) error {

	for {
		select {

		// Session 结束或者发生 Rebalance
		case <-session.Context().Done():
			return nil

		// 接收 Partition 消息
		case msg, ok := <-claim.Messages():

			if !ok {
				return nil
			}

			// 1. 解析 JSON
			var event events.ImportRequestedV1

			if err := json.Unmarshal(
				msg.Value,
				&event,
			); err != nil {

				log.Printf(
					"invalid kafka JSON: topic=%s partition=%d offset=%d err=%v",
					msg.Topic,
					msg.Partition,
					msg.Offset,
					err,
				)

				// 暂时不提交 Offset
				continue
			}

			// 2. 校验事件
			if err := event.Validate(); err != nil {

				log.Printf(
					"invalid import event: event_id=%s err=%v",
					event.EventID,
					err,
				)

				// 暂时不提交 Offset
				continue
			}

			// 3. 打印消费信息
			log.Printf(
				"received import event: event_id=%s job_id=%d revision=%s partition=%d offset=%d",
				event.EventID,
				event.JobID,
				event.RevisionSHA,
				msg.Partition,
				msg.Offset,
			)

			// 第一阶段只完成消息接收和解析。
			// 不执行 Worker。
			// 不调用 session.MarkMessage()。
			// 不提交 Offset。
		}
	}
}
