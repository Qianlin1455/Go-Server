package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"GoServer/events"
	"GoServer/internal/model"
	"GoServer/internal/repository"
	"GoServer/internal/worker"

	"github.com/IBM/sarama"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func setupConsumerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Fatal("TEST_MYSQL_DSN 未设置")
	}

	db, err := gorm.Open(
		mysql.Open(dsn),
		&gorm.Config{TranslateError: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	var dbName string
	if err := db.Raw("SELECT DATABASE()").
		Row().Scan(&dbName); err != nil {
		t.Fatal(err)
	}

	if dbName != "go_server_test" {
		t.Fatalf("拒绝测试，当前数据库：%s", dbName)
	}

	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}

	t.Cleanup(func() {
		_ = tx.Rollback().Error
	})

	return tx
}

// Fake Executor：不调用真实 GitHub API。
type consumerTestExecutor struct {
	calls  int
	result model.ImportJobResult
}

func (e *consumerTestExecutor) Execute(
	ctx context.Context,
	jobID uint64,
) (model.ImportJobResult, error) {
	if err := ctx.Err(); err != nil {
		return model.ImportJobResult{}, err
	}

	e.calls++

	return e.result, nil
}

// 创建符合项目外键约束的测试任务。
func createConsumerTestJob(
	t *testing.T,
	tx *gorm.DB,
) *model.ImportJob {
	t.Helper()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	user := model.Users{
		UserName: "consumer_it_" + suffix,
		Email:    "consumer_it_" + suffix + "@example.com",
		Password: "test-hash",
	}

	if err := tx.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	project := model.Project{
		UserID: user.ID,
		Name:   "consumer_it_" + suffix,
	}

	if err := tx.Create(&project).Error; err != nil {
		t.Fatal(err)
	}

	repo := model.Repository{
		ProjectID:     project.ID,
		Owner:         "golang",
		Repo:          "go",
		DefaultBranch: "master",
		LastSHA:       strings.Repeat("2", 40),
	}

	if err := tx.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}

	job := &model.ImportJob{
		UserID:             user.ID,
		ProjectID:          project.ID,
		RepositoryID:       repo.ID,
		Endpoint:           "POST /api/v1/projects/:id/imports",
		IdempotencyKey:     "consumer-it-" + suffix,
		RequestFingerprint: strings.Repeat("a", 64),
		RevisionSHA:        strings.Repeat("1", 40),
		Status:             model.ImportJobQueued,
		MaxAttempts:        3,
	}

	if err := tx.Create(job).Error; err != nil {
		t.Fatal(err)
	}

	return job
}

func TestHandler_DuplicateEvent10Times_OneResult(
	t *testing.T,
) {
	ctx := context.Background()

	// 1. 准备真实 MySQL 测试事务。
	db := setupConsumerTestDB(t)

	// 2. 创建一个 queued Job。
	job := createConsumerTestJob(t, db)

	// 3. 创建真实 Repository。
	store := repository.NewImportJobStore(db)

	// 4. 创建 Fake Executor。
	executor := &consumerTestExecutor{
		result: model.ImportJobResult{
			TreeSHA:    strings.Repeat("a", 40),
			FileCount:  10,
			TotalBytes: 1024,
		},
	}

	// 5. 创建真实 Runner。
	runner, err := worker.NewRunner(
		store,
		executor,
		"consumer-integration-test",
	)
	if err != nil {
		t.Fatal(err)
	}

	// 6. 创建真实 Handler。
	handler, err := NewHandler(store, runner)
	if err != nil {
		t.Fatal(err)
	}

	// 7. 构造一条合法 Kafka 事件。
	event := events.ImportRequestedV1{
		EventID:       "duplicate-event-" + fmt.Sprint(job.ID),
		EventType:     events.ImportRequestedType,
		SchemaVersion: events.ImportRequestedVersion,
		OccurredAt:    time.Now().UTC(),

		JobID:        job.ID,
		UserID:       job.UserID,
		ProjectID:    job.ProjectID,
		RepositoryID: job.RepositoryID,
		RevisionSHA:  job.RevisionSHA,
	}

	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}

	// 8. 模拟同一条消息重复投递 10 次。
	messages := make(chan *sarama.ConsumerMessage, 10)

	for i := 0; i < 10; i++ {
		messages <- &sarama.ConsumerMessage{
			Topic:     ImportsRequestedTopic,
			Partition: 0,
			Offset:    int64(i),
			Value:     payload,
		}
	}

	close(messages)

	session := newTestConsumerSession()
	claim := &testConsumerClaim{
		messages: messages,
	}

	// 9. 执行真实 Consumer Handler。
	if err := handler.ConsumeClaim(
		session,
		claim,
	); err != nil {
		t.Fatalf("ConsumeClaim failed: %v", err)
	}

	// 10. 验证 Executor 只执行一次。
	if executor.calls != 1 {
		t.Fatalf(
			"executor calls=%d, want 1",
			executor.calls,
		)
	}

	// 11. 验证 10 条消息均被标记并提交。
	if len(session.marked) != 10 {
		t.Fatalf(
			"marked=%d, want 10",
			len(session.marked),
		)
	}

	if session.commitCount != 10 {
		t.Fatalf(
			"commits=%d, want 10",
			session.commitCount,
		)
	}

	// 12. 查询真实 MySQL。
	got, err := store.GetByID(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}

	if got.Status != model.ImportJobSucceeded {
		t.Fatalf(
			"status=%s, want succeeded",
			got.Status,
		)
	}

	if got.AttemptCount != 1 {
		t.Fatalf(
			"attempt_count=%d, want 1",
			got.AttemptCount,
		)
	}

	if got.ResultTreeSHA == nil ||
		*got.ResultTreeSHA != executor.result.TreeSHA {
		t.Fatal("tree SHA persisted incorrectly")
	}

	if got.ResultFileCount == nil ||
		*got.ResultFileCount != 10 {
		t.Fatal("file count persisted incorrectly")
	}

	if got.ResultTotalBytes == nil ||
		*got.ResultTotalBytes != 1024 {
		t.Fatal("total bytes persisted incorrectly")
	}

	t.Log("PASS: 同一消息投递 10 次")
	t.Log("PASS: GitHub Executor 实际执行 1 次")
	t.Log("PASS: ImportJob 只产生一份持久化结果")
	t.Log("PASS: 10 条消息均完成 Offset 提交")
}

type testConsumerSession struct {
	ctx         context.Context
	marked      []*sarama.ConsumerMessage
	commitCount int
}

func newTestConsumerSession() *testConsumerSession {
	return &testConsumerSession{
		ctx: context.Background(),
	}
}

func (s *testConsumerSession) Claims() map[string][]int32 {
	return map[string][]int32{
		ImportsRequestedTopic: []int32{0},
	}
}

func (s *testConsumerSession) MemberID() string {
	return "consumer-test"
}

func (s *testConsumerSession) GenerationID() int32 {
	return 1
}

func (s *testConsumerSession) MarkOffset(
	topic string,
	partition int32,
	offset int64,
	metadata string,
) {
}

func (s *testConsumerSession) Commit() {
	s.commitCount++
}

func (s *testConsumerSession) ResetOffset(
	topic string,
	partition int32,
	offset int64,
	metadata string,
) {
}

func (s *testConsumerSession) MarkMessage(
	msg *sarama.ConsumerMessage,
	metadata string,
) {
	s.marked = append(s.marked, msg)
}

func (s *testConsumerSession) Context() context.Context {
	return s.ctx
}

type testConsumerClaim struct {
	messages chan *sarama.ConsumerMessage
}

func (c *testConsumerClaim) Topic() string {
	return ImportsRequestedTopic
}

func (c *testConsumerClaim) Partition() int32 {
	return 0
}

func (c *testConsumerClaim) InitialOffset() int64 {
	return 0
}

func (c *testConsumerClaim) HighWaterMarkOffset() int64 {
	return 100
}

func (c *testConsumerClaim) Messages() <-chan *sarama.ConsumerMessage {
	return c.messages
}

type failingHandlerStore struct {
	job model.ImportJob
}

func (s *failingHandlerStore) GetByID(
	ctx context.Context,
	jobID uint64,
) (*model.ImportJob, error) {
	if jobID != s.job.ID {
		return nil, fmt.Errorf("job not found")
	}

	job := s.job
	return &job, nil
}

func (s *failingHandlerStore) RecoverExpiredJobs(
	ctx context.Context,
) (int64, int64, error) {
	return 0, 0, nil
}

type failingJobProcessor struct {
	calls int
}

func (p *failingJobProcessor) ProcessJob(
	ctx context.Context,
	jobID uint64,
) (bool, error) {
	p.calls++

	// 模拟 Runner 中 CompleteJobWithResult
	// 返回 MySQL 持久化失败。
	return true, fmt.Errorf(
		"complete job %d with result: simulated MySQL failure",
		jobID,
	)
}

func TestHandler_NoOffsetCommitWhenPersistenceFails(
	t *testing.T,
) {
	// 1. 准备 queued Job。
	job := model.ImportJob{
		ID:           2024,
		UserID:       100,
		ProjectID:    200,
		RepositoryID: 300,
		RevisionSHA:  strings.Repeat("a", 40),
		Status:       model.ImportJobQueued,
	}

	store := &failingHandlerStore{
		job: job,
	}

	runner := &failingJobProcessor{}

	handler, err := NewHandler(store, runner)
	if err != nil {
		t.Fatal(err)
	}

	// 2. 构造合法事件。
	event := events.ImportRequestedV1{
		EventID:       "mysql-failure-test",
		EventType:     events.ImportRequestedType,
		SchemaVersion: events.ImportRequestedVersion,
		OccurredAt:    time.Now().UTC(),

		JobID:        job.ID,
		UserID:       job.UserID,
		ProjectID:    job.ProjectID,
		RepositoryID: job.RepositoryID,
		RevisionSHA:  job.RevisionSHA,
	}

	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}

	messages := make(chan *sarama.ConsumerMessage, 1)

	messages <- &sarama.ConsumerMessage{
		Topic:     ImportsRequestedTopic,
		Partition: 0,
		Offset:    10,
		Value:     payload,
	}

	close(messages)

	session := newTestConsumerSession()

	claim := &testConsumerClaim{
		messages: messages,
	}

	// 3. 执行 Consumer。
	err = handler.ConsumeClaim(session, claim)

	// 4. 持久化失败必须向上传播错误。
	if err == nil {
		t.Fatal("expected persistence error")
	}

	// 5. 验证没有标记消息。
	if len(session.marked) != 0 {
		t.Fatalf(
			"message marked %d times, want 0",
			len(session.marked),
		)
	}

	// 6. 验证没有提交 Offset。
	if session.commitCount != 0 {
		t.Fatalf(
			"commit count=%d, want 0",
			session.commitCount,
		)
	}

	if runner.calls != 1 {
		t.Fatalf(
			"runner calls=%d, want 1",
			runner.calls,
		)
	}

	t.Log("PASS: MySQL 持久化失败")
	t.Log("PASS: Handler 返回错误")
	t.Log("PASS: 没有调用 MarkMessage")
	t.Log("PASS: 没有调用 Commit")
}

func setupCommittedConsumerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Fatal("TEST_MYSQL_DSN 未设置")
	}

	db, err := gorm.Open(
		mysql.Open(dsn),
		&gorm.Config{TranslateError: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	var dbName string
	if err := db.Raw("SELECT DATABASE()").
		Row().Scan(&dbName); err != nil {
		t.Fatal(err)
	}

	if dbName != "go_server_test" {
		t.Fatalf("拒绝测试，当前数据库：%s", dbName)
	}

	return db
}

type cancelAfterProcessJob struct {
	next interface {
		ProcessJob(
			ctx context.Context,
			jobID uint64,
		) (bool, error)
	}

	cancel context.CancelFunc
}

func (p *cancelAfterProcessJob) ProcessJob(
	ctx context.Context,
	jobID uint64,
) (bool, error) {
	claimed, err := p.next.ProcessJob(ctx, jobID)

	// Runner 已经执行完毕，且没有返回错误。
	// 此时取消第一次 Consumer Session，
	// 模拟 MySQL 已经提交，但 Kafka Offset 尚未提交。
	if err == nil && claimed {
		p.cancel()
	}

	return claimed, err
}

func TestHandler_CrashAfterDBCommitBeforeOffsetCommit(
	t *testing.T,
) {
	ctx := context.Background()

	// 1. 使用真实数据库连接。
	db := setupCommittedConsumerTestDB(t)

	// 2. 创建测试任务，并提交创建事务。
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()

	job := createConsumerTestJob(t, tx)

	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}

	// 3. 清理真实测试数据。
	t.Cleanup(func() {
		for _, item := range []struct {
			table string
			id    uint64
		}{
			{"import_jobs", job.ID},
			{"repositories", job.RepositoryID},
			{"projects", job.ProjectID},
			{"users", job.UserID},
		} {
			if err := db.Exec(
				"DELETE FROM "+item.table+" WHERE id = ?",
				item.id,
			).Error; err != nil {
				t.Errorf("cleanup %s: %v", item.table, err)
			}
		}
	})

	// 4. 真实 Store。
	store := repository.NewImportJobStore(db)

	// 5. Fake Executor。
	executor := &consumerTestExecutor{
		result: model.ImportJobResult{
			TreeSHA:    strings.Repeat("a", 40),
			FileCount:  10,
			TotalBytes: 1024,
		},
	}

	// 6. 真实 Runner。
	runner, err := worker.NewRunner(
		store,
		executor,
		"crash-integration-test",
	)
	if err != nil {
		t.Fatal(err)
	}

	// 7. 创建第一次 Consumer Session。
	firstCtx, cancelFirst := context.WithCancel(
		context.Background(),
	)
	defer cancelFirst()

	crashRunner := &cancelAfterProcessJob{
		next:   runner,
		cancel: cancelFirst,
	}

	handler, err := NewHandler(store, crashRunner)
	if err != nil {
		t.Fatal(err)
	}

	// 8. 构造 Kafka 消息。
	event := events.ImportRequestedV1{
		EventID:       fmt.Sprintf("crash-test-%d", job.ID),
		EventType:     events.ImportRequestedType,
		SchemaVersion: events.ImportRequestedVersion,
		OccurredAt:    time.Now().UTC(),

		JobID:        job.ID,
		UserID:       job.UserID,
		ProjectID:    job.ProjectID,
		RepositoryID: job.RepositoryID,
		RevisionSHA:  job.RevisionSHA,
	}

	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}

	message := &sarama.ConsumerMessage{
		Topic:     ImportsRequestedTopic,
		Partition: 0,
		Offset:    100,
		Value:     payload,
	}

	// ==========================================
	// 阶段 A：第一次消费，模拟崩溃
	// ==========================================

	firstMessages := make(chan *sarama.ConsumerMessage, 1)
	firstMessages <- message
	close(firstMessages)

	firstSession := &testConsumerSession{
		ctx: firstCtx,
	}

	err = handler.ConsumeClaim(
		firstSession,
		&testConsumerClaim{
			messages: firstMessages,
		},
	)

	// 9. 第一次消费必须因 Session 取消而退出。
	if err == nil {
		t.Fatal("expected first consumer session to stop")
	}

	if firstSession.Context().Err() != context.Canceled {
		t.Fatalf(
			"first session error=%v, want context.Canceled",
			firstSession.Context().Err(),
		)
	}

	// 10. 第一次不能提交 Offset。
	if len(firstSession.marked) != 0 {
		t.Fatalf(
			"first session marked=%d, want 0",
			len(firstSession.marked),
		)
	}

	if firstSession.commitCount != 0 {
		t.Fatalf(
			"first session commits=%d, want 0",
			firstSession.commitCount,
		)
	}

	// 11. 验证 MySQL 已经真正持久化成功。
	got, err := store.GetByID(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}

	if got.Status != model.ImportJobSucceeded {
		t.Fatalf(
			"first session status=%s, want succeeded",
			got.Status,
		)
	}

	if got.AttemptCount != 1 {
		t.Fatalf(
			"attempt_count=%d, want 1",
			got.AttemptCount,
		)
	}

	t.Log("PASS: MySQL 已提交 succeeded")
	t.Log("PASS: 第一次 Consumer 未提交 Offset")

	// ==========================================
	// 阶段 B：模拟 Consumer 重启并重新消费
	// ==========================================

	secondSession := newTestConsumerSession()

	secondMessages := make(chan *sarama.ConsumerMessage, 1)

	// 同一条 Kafka 消息重新投递。
	secondMessages <- message
	close(secondMessages)

	err = handler.ConsumeClaim(
		secondSession,
		&testConsumerClaim{
			messages: secondMessages,
		},
	)
	if err != nil {
		t.Fatalf("second consumer failed: %v", err)
	}

	// 12. 第二次应该发现任务已经成功。
	if executor.calls != 1 {
		t.Fatalf(
			"executor calls=%d, want 1",
			executor.calls,
		)
	}

	// 13. 第二次应该正常提交 Offset。
	if len(secondSession.marked) != 1 {
		t.Fatalf(
			"second session marked=%d, want 1",
			len(secondSession.marked),
		)
	}

	if secondSession.commitCount != 1 {
		t.Fatalf(
			"second session commits=%d, want 1",
			secondSession.commitCount,
		)
	}

	// 14. 最终状态仍然只有一份业务结果。
	got, err = store.GetByID(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}

	if got.Status != model.ImportJobSucceeded ||
		got.AttemptCount != 1 {
		t.Fatalf(
			"unexpected final state: status=%s attempts=%d",
			got.Status,
			got.AttemptCount,
		)
	}

	if got.ResultTreeSHA == nil ||
		*got.ResultTreeSHA != executor.result.TreeSHA {
		t.Fatal("result tree SHA changed")
	}

	if got.ResultFileCount == nil ||
		*got.ResultFileCount != executor.result.FileCount {
		t.Fatal("result file count changed")
	}

	if got.ResultTotalBytes == nil ||
		*got.ResultTotalBytes != executor.result.TotalBytes {
		t.Fatal("result total bytes changed")
	}

	t.Log("PASS: 第二次消费识别已成功任务")
	t.Log("PASS: Executor 没有重复执行")
	t.Log("PASS: 第二次 Consumer 正常提交 Offset")
	t.Log("PASS: MySQL 只有一份导入结果")
}
