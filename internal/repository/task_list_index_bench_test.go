package repository

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const taskListColumns = `
SELECT id, project_id, user_id, source_message_id,
       title, description, status, version,
       completed_at, created_at, updated_at
FROM tasks_bench
`

var taskListQueries = map[string]string{
	"original": taskListColumns + `
FORCE INDEX (idx_tasks_project_user_status_id)
WHERE project_id = 1 AND user_id = 1
ORDER BY created_at DESC, id DESC
LIMIT 20`,

	"candidate": taskListColumns + `
FORCE INDEX (idx_tasks_project_user_created_id)
WHERE project_id = 1 AND user_id = 1
ORDER BY created_at DESC, id DESC
LIMIT 20`,
}

func openPerfDB(b *testing.B) *sql.DB {
	b.Helper()

	dsn := os.Getenv("PERF_MYSQL_DSN")
	if dsn == "" {
		b.Fatal("请设置 PERF_MYSQL_DSN，指向 go_server_perf")
	}

	baseDSN := strings.SplitN(dsn, "?", 2)[0]
	if !strings.HasSuffix(baseDSN, "/go_server_perf") {
		b.Fatalf("安全检查失败：DSN 必须指向 go_server_perf")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		b.Fatal(err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	b.Cleanup(func() {
		_ = db.Close()
	})

	if err := db.Ping(); err != nil {
		b.Fatal(err)
	}

	return db
}

func consumeTaskList(db *sql.DB, query string) error {
	rows, err := db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	count := 0

	for rows.Next() {
		var id, projectID, userID, version int64
		var sourceMessageID sql.NullInt64
		var title, status string
		var description sql.NullString
		var completedAt sql.NullTime
		var createdAt, updatedAt time.Time

		if err := rows.Scan(
			&id,
			&projectID,
			&userID,
			&sourceMessageID,
			&title,
			&description,
			&status,
			&version,
			&completedAt,
			&createdAt,
			&updatedAt,
		); err != nil {
			return err
		}

		count++
	}

	if err := rows.Err(); err != nil {
		return err
	}

	if count != 20 {
		return fmt.Errorf("expected 20 rows, got %d", count)
	}

	return nil
}

func BenchmarkTaskListPhases(b *testing.B) {
	variant := os.Getenv("PERF_INDEX")
	query, ok := taskListQueries[variant]
	if !ok {
		b.Fatalf("PERF_INDEX 必须是 original 或 candidate，当前为 %q", variant)
	}

	db := openPerfDB(b)

	// 先验证查询并预热，避免把初始化和冷启动算进基准。
	for i := 0; i < 10; i++ {
		if err := consumeTaskList(db, query); err != nil {
			b.Fatalf("warmup failed: %v", err)
		}
	}

	b.ReportAllocs()
	var queryTotal, nextTotal time.Duration
	var scanTotal, closeTotal time.Duration
	var total time.Duration

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		timing, err := consumeTaskListTimed(db, query)
		if err != nil {
			b.Fatal(err)
		}

		queryTotal += timing.query
		nextTotal += timing.next
		scanTotal += timing.scan
		closeTotal += timing.close
		total += timing.total
	}

	b.StopTimer()

	n := float64(b.N)

	b.ReportMetric(
		float64(queryTotal.Nanoseconds())/n,
		"query-ns/op",
	)
	b.ReportMetric(
		float64(nextTotal.Nanoseconds())/n,
		"next-ns/op",
	)
	b.ReportMetric(
		float64(scanTotal.Nanoseconds())/n,
		"scan-ns/op",
	)
	b.ReportMetric(
		float64(closeTotal.Nanoseconds())/n,
		"close-ns/op",
	)
	b.ReportMetric(
		float64(total.Nanoseconds())/n,
		"phases-total-ns/op",
	)
}

type taskListPhaseTiming struct {
	query time.Duration
	next  time.Duration
	scan  time.Duration
	close time.Duration
	total time.Duration
}

func consumeTaskListTimed(
	db *sql.DB,
	query string,
) (taskListPhaseTiming, error) {
	var timing taskListPhaseTiming
	totalStart := time.Now()

	// 阶段 1：Query
	start := time.Now()
	rows, err := db.Query(query)
	timing.query = time.Since(start)

	if err != nil {
		timing.total = time.Since(totalStart)
		return timing, err
	}

	// 阶段 2：Next
	for {
		start = time.Now()
		hasRow := rows.Next()
		timing.next += time.Since(start)

		if !hasRow {
			break
		}

		var id, projectID, userID, version int64
		var sourceMessageID sql.NullInt64
		var title, status string
		var description sql.NullString
		var completedAt sql.NullTime
		var createdAt, updatedAt time.Time

		// 阶段 3：Scan
		start = time.Now()
		err = rows.Scan(
			&id,
			&projectID,
			&userID,
			&sourceMessageID,
			&title,
			&description,
			&status,
			&version,
			&completedAt,
			&createdAt,
			&updatedAt,
		)
		timing.scan += time.Since(start)

		if err != nil {
			_ = rows.Close()
			timing.total = time.Since(totalStart)
			return timing, err
		}
	}

	if err := rows.Err(); err != nil {
		_ = rows.Close()
		timing.total = time.Since(totalStart)
		return timing, err
	}

	start = time.Now()
	err = rows.Close()
	timing.close = time.Since(start)
	timing.total = time.Since(totalStart)

	if err != nil {
		return timing, err
	}

	return timing, nil
}
