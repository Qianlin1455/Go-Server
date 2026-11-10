package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"GoServer/internal/midware"
	"GoServer/internal/model"
	"GoServer/internal/repository"
	"GoServer/internal/security"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
	mysqlDriver "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestImportHandler_HTTPIdempotency(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 1. 连接独立测试数据库
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Fatal("TEST_MYSQL_DSN 未设置")
	}

	cfg, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBName != "go_server_test" {
		t.Fatal("禁止在非测试数据库运行")
	}

	db, err := gorm.Open(
		gormmysql.Open(dsn),
		&gorm.Config{TranslateError: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}

	// 使用单个事务，测试结束后自动回滚
	tx := db.Begin()
	if tx.Error != nil {
		_ = sqlDB.Close()
		t.Fatal(tx.Error)
	}

	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil {
			t.Errorf("rollback failed: %v", err)
		}
		_ = sqlDB.Close()
	})

	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	// 2. 创建两个测试用户
	userA := &model.Users{
		UserName: "import_a_" + suffix,
		Email:    "import_a_" + suffix + "@example.com",
		Password: "test-hash",
	}

	userB := &model.Users{
		UserName: "import_b_" + suffix,
		Email:    "import_b_" + suffix + "@example.com",
		Password: "test-hash",
	}

	for _, u := range []*model.Users{userA, userB} {
		if err := repository.CreateUser(ctx, tx, u); err != nil {
			t.Fatal(err)
		}
	}

	// 3. 创建用户 A 的项目
	project := &model.Project{
		UserID: userA.ID,
		Name:   "import_http_" + suffix,
		Status: model.ProjectStatusActive,
	}

	if err := repository.CreateProject(
		ctx, tx, project,
	); err != nil {
		t.Fatal(err)
	}

	// 4. 创建已绑定 GitHub 仓库
	revisionSHA := strings.Repeat("a", 40)

	repo := &model.Repository{
		ProjectID:     project.ID,
		Owner:         "golang",
		Repo:          "go",
		DefaultBranch: "master",
		LastSHA:       revisionSHA,
	}

	if err := repository.NewGitHubRepositoryStore(tx).
		Create(ctx, repo); err != nil {
		t.Fatal(err)
	}

	// 5. 创建 JWT
	jwtManager, err := security.NewJWTManager(
		"01234567890123456789012345678901",
		time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}

	tokenA, err := jwtManager.Generate(userA.ID)
	if err != nil {
		t.Fatal(err)
	}

	tokenB, err := jwtManager.Generate(userB.ID)
	if err != nil {
		t.Fatal(err)
	}

	// 6. 注册真实 Handler
	svc := service.NewImportJobService(tx)
	h := NewImportHandler(svc)

	router := gin.New()
	router.POST(
		"/api/v1/projects/:id/imports",
		midware.JWTAuth(jwtManager),
		h.Create,
	)

	path := fmt.Sprintf(
		"/api/v1/projects/%d/imports",
		project.ID,
	)

	// 7. 封装 HTTP 请求
	send := func(
		token string,
		key string,
		body string,
	) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			path,
			strings.NewReader(body),
		)

		req.Header.Set(
			"Content-Type", "application/json",
		)

		if token != "" {
			req.Header.Set(
				"Authorization", "Bearer "+token,
			)
		}

		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// 定义新版 HTTP 响应结构
	type importResponse struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    struct {
			JobID        uint64 `json:"job_id"`
			ProjectID    uint64 `json:"project_id"`
			RepositoryID uint64 `json:"repository_id"`
			RevisionSHA  string `json:"revision_sha"`
			Status       string `json:"status"`
		} `json:"data"`
	}

	body := fmt.Sprintf(
		`{"repository_id":%d}`,
		repo.ID,
	)

	// ====================================================
	// 场景一：首次创建 -> 202 Accepted
	// ====================================================

	first := send(tokenA, "http-test-key-001", body)

	if first.Code != http.StatusAccepted {
		t.Fatalf(
			"first: want 202, got %d: %s",
			first.Code,
			first.Body.String(),
		)
	}

	var firstResponse importResponse

	if err := json.Unmarshal(
		first.Body.Bytes(), &firstResponse,
	); err != nil {
		t.Fatal(err)
	}

	if firstResponse.Code != "SUCCESS" {
		t.Fatalf(
			"unexpected response code: %s",
			firstResponse.Code,
		)
	}

	firstID := firstResponse.Data.JobID

	if firstID == 0 {
		t.Fatal("first job_id is zero")
	}

	// 新任务必须处于 queued 状态
	if firstResponse.Data.Status != model.ImportJobQueued {
		t.Fatalf(
			"expected queued, got %s",
			firstResponse.Data.Status,
		)
	}

	// 验证项目、仓库和版本关联
	if firstResponse.Data.ProjectID != project.ID {
		t.Fatal("project_id mismatch")
	}

	if firstResponse.Data.RepositoryID != repo.ID {
		t.Fatal("repository_id mismatch")
	}

	if firstResponse.Data.RevisionSHA != revisionSHA {
		t.Fatal("revision_sha mismatch")
	}

	t.Logf(
		"PASS: first request 202, job_id=%d, status=queued",
		firstID,
	)

	// ====================================================
	// 场景二：重复请求 -> 200 + 相同 job_id
	// ====================================================

	retry := send(tokenA, "http-test-key-001", body)

	if retry.Code != http.StatusOK {
		t.Fatalf(
			"retry: want 200, got %d: %s",
			retry.Code,
			retry.Body.String(),
		)
	}

	var retryResponse importResponse

	if err := json.Unmarshal(
		retry.Body.Bytes(), &retryResponse,
	); err != nil {
		t.Fatal(err)
	}

	if retryResponse.Data.JobID != firstID {
		t.Fatalf(
			"retry returned job_id=%d, want %d",
			retryResponse.Data.JobID,
			firstID,
		)
	}

	if retryResponse.Data.Status != model.ImportJobQueued {
		t.Fatalf(
			"retry status = %s, want queued",
			retryResponse.Data.Status,
		)
	}

	t.Log("PASS: retry 200, same job_id")

	// ====================================================
	// 场景三：同 Key 不同 Payload -> 409
	// ====================================================

	differentBody := fmt.Sprintf(
		`{"repository_id":%d}`,
		repo.ID+999999,
	)

	conflict := send(
		tokenA,
		"http-test-key-001",
		differentBody,
	)

	if conflict.Code != http.StatusConflict {
		t.Fatalf(
			"conflict: want 409, got %d: %s",
			conflict.Code,
			conflict.Body.String(),
		)
	}

	var conflictResponse struct {
		Code string `json:"code"`
	}

	if err := json.Unmarshal(
		conflict.Body.Bytes(),
		&conflictResponse,
	); err != nil {
		t.Fatal(err)
	}

	if conflictResponse.Code != "IDEMPOTENCY_KEY_CONFLICT" {
		t.Fatalf(
			"unexpected error code: %s",
			conflictResponse.Code,
		)
	}

	t.Log("PASS: different payload returns 409")

	// ====================================================
	// 场景四：没有 JWT -> 401
	// ====================================================

	unauthorized := send(
		"",
		"http-test-key-001",
		body,
	)

	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf(
			"unauthorized: want 401, got %d",
			unauthorized.Code,
		)
	}

	t.Log("PASS: missing JWT returns 401")

	// ====================================================
	// 场景五：用户 B 访问用户 A 的项目 -> 404
	// ====================================================

	foreign := send(
		tokenB,
		"http-test-key-001",
		body,
	)

	if foreign.Code != http.StatusNotFound {
		t.Fatalf(
			"foreign project: want 404, got %d: %s",
			foreign.Code,
			foreign.Body.String(),
		)
	}

	t.Log("PASS: foreign project returns 404")

	// ====================================================
	// 场景六：缺少 Idempotency-Key -> 400
	// ====================================================

	missingKey := send(tokenA, "", body)

	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf(
			"missing key: want 400, got %d",
			missingKey.Code,
		)
	}

	t.Log("PASS: missing key returns 400")

	// ====================================================
	// 场景七：确认数据库真正持久化了 queued 任务
	// ====================================================

	var persistedJob model.ImportJob

	if err := tx.
		Where("id = ? AND user_id = ?", firstID, userA.ID).
		Take(&persistedJob).Error; err != nil {
		t.Fatalf("query persisted job: %v", err)
	}

	if persistedJob.Status != model.ImportJobQueued {
		t.Fatalf(
			"persisted status = %s, want queued",
			persistedJob.Status,
		)
	}

	if persistedJob.AttemptCount != 0 {
		t.Fatalf(
			"attempt_count = %d, want 0",
			persistedJob.AttemptCount,
		)
	}

	if persistedJob.RevisionSHA != revisionSHA {
		t.Fatal("persisted revision_sha mismatch")
	}

	t.Log("PASS: queued job persisted correctly")

	// ====================================================
	// 最终验证：数据库只有一条导入任务
	// ====================================================

	var count int64

	if err := tx.Model(&model.ImportJob{}).
		Where(
			"user_id = ? AND endpoint = ? AND idempotency_key = ?",
			userA.ID,
			service.ImportCreateEndpoint,
			"http-test-key-001",
		).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}

	if count != 1 {
		t.Fatalf(
			"expected 1 import job, got %d",
			count,
		)
	}

	t.Log("PASS: database contains exactly 1 import job")
}
