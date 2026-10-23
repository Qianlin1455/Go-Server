package midware_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"GoServer/internal/database"
	"GoServer/internal/handler"
	"GoServer/internal/midware"

	"github.com/gin-gonic/gin"
)

func TestTimeoutCancelsMySQL(t *testing.T) {
	if os.Getenv("MYSQL_DSN") == "" {
		t.Fatal("请先设置 MYSQL_DSN")
	}

	db, err := database.InitDB()
	if err != nil {
		t.Fatal(err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	gin.SetMode(gin.TestMode)

	r := gin.New()

	// 为实验设置 150ms 超时，不影响正式服务器的 3 秒配置。
	r.Use(midware.Timeout(150 * time.Millisecond))

	var dbErr error
	var ctxErr error

	r.GET("/test/slow-db", func(c *gin.Context) {
		var result int

		// MySQL 正常执行需要约 1 秒。
		dbErr = db.WithContext(c.Request.Context()).
			Raw("SELECT SLEEP(1)").
			Scan(&result).Error

		ctxErr = c.Request.Context().Err()

		if dbErr != nil {
			handler.WriteServiceerror(c, dbErr)
			return
		}

		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(
		http.MethodGet,
		"/test/slow-db",
		nil,
	)
	recorder := httptest.NewRecorder()

	start := time.Now()
	r.ServeHTTP(recorder, req)
	elapsed := time.Since(start)

	t.Logf("HTTP status: %d", recorder.Code)
	t.Logf("Elapsed: %s", elapsed)
	t.Logf("DB error: %v", dbErr)
	t.Logf("Context error: %v", ctxErr)

	if ctxErr != context.DeadlineExceeded {
		t.Fatalf("预期 Context 超时，实际: %v", ctxErr)
	}

	if dbErr == nil {
		t.Fatal("数据库查询没有被取消")
	}

	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("HTTP status = %d, want 504", recorder.Code)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("超时响应不是单个 JSON: %v", err)
	}
	if body.Code != "DEADLINE_EXCEEDED" {
		t.Fatalf("超时错误码 = %q", body.Code)
	}

	if elapsed >= 900*time.Millisecond {
		t.Fatalf("查询未及时取消: %s", elapsed)
	}
}
