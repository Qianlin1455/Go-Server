package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"GoServer/internal/model"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

func TestEvidenceHandlerAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)

	f := newTaskAPIFixture(t)

	// 创建用户 A 的正常任务和归档项目任务
	task := &model.Task{
		UserID:    f.userA.ID,
		ProjectID: f.projectA.ID,
		Title:     "Evidence HTTP 测试任务",
		Status:    model.TaskStatusTodo,
		Version:   1,
	}

	archivedTask := &model.Task{
		UserID:    f.userA.ID,
		ProjectID: f.archived.ID,
		Title:     "Evidence 归档测试任务",
		Status:    model.TaskStatusTodo,
		Version:   1,
	}

	for _, item := range []*model.Task{task, archivedTask} {
		if err := f.db.Create(item).Error; err != nil {
			t.Fatalf("准备任务失败: %v", err)
		}
	}

	path := fmt.Sprintf(
		"/api/v1/tasks/%d/evidence", task.ID,
	)

	archivedPath := fmt.Sprintf(
		"/api/v1/tasks/%d/evidence", archivedTask.ID,
	)

	countEvidence := func(taskID uint64) int64 {
		t.Helper()

		var count int64
		err := f.db.Model(&model.TaskEvidence{}).
			Where("task_id = ?", taskID).
			Count(&count).Error
		if err != nil {
			t.Fatalf("统计证据失败: %v", err)
		}
		return count
	}

	// ============================
	// 1. 正常创建三种证据与空草稿
	// ============================

	sha := strings.Repeat("a", 40)

	validCases := []struct {
		name   string
		body   string
		typ    string
		status string
	}{
		{
			name: "实验文字提交",
			body: `{
				"type":"experiment_text",
				"text":"CAS并发实验通过",
				"status":"submitted"
			}`,
			typ:    model.EvidenceTypeExperimentText,
			status: model.EvidenceStatusSubmitted,
		},
		{
			name: "测试输出提交",
			body: `{
				"type":"test_output",
				"text":"go test ./... PASS",
				"status":"submitted"
			}`,
			typ:    model.EvidenceTypeTestOutput,
			status: model.EvidenceStatusSubmitted,
		},
		{
			name: "Commit SHA提交",
			body: fmt.Sprintf(
				`{"type":"commit_sha","commit_sha":"%s","status":"submitted"}`,
				sha,
			),
			typ:    model.EvidenceTypeCommitSHA,
			status: model.EvidenceStatusSubmitted,
		},
		{
			name: "空草稿",
			body: `{
				"type":"commit_sha",
				"status":"draft"
			}`,
			typ:    model.EvidenceTypeCommitSHA,
			status: model.EvidenceStatusDraft,
		},
	}

	for _, tc := range validCases {
		t.Run(tc.name, func(t *testing.T) {
			w := f.request(
				http.MethodPost,
				path,
				f.tokenA,
				tc.body,
			)

			expectTaskHTTP(t, w, 201, "SUCCESS")

			var out struct {
				Code string                 `json:"code"`
				Data service.EvidenceResult `json:"data"`
			}

			if err := json.Unmarshal(
				w.Body.Bytes(), &out,
			); err != nil {
				t.Fatalf("解析响应失败: %v", err)
			}

			if out.Data.ID == 0 ||
				out.Data.TaskID != task.ID ||
				out.Data.Type != tc.typ ||
				out.Data.Status != tc.status {
				t.Fatalf("响应内容异常: %+v", out.Data)
			}

			var stored model.TaskEvidence

			if err := f.db.
				Where("id = ?", out.Data.ID).
				First(&stored).Error; err != nil {
				t.Fatalf("证据未持久化: %v", err)
			}

			if stored.CreatedBy != f.userA.ID ||
				stored.Type != tc.typ ||
				stored.Status != tc.status {
				t.Fatalf("数据库证据异常: %+v", stored)
			}

			if tc.typ == model.EvidenceTypeCommitSHA &&
				tc.status == model.EvidenceStatusSubmitted {
				if stored.CommitSHA == nil ||
					*stored.CommitSHA != sha {
					t.Fatal("Commit SHA 持久化错误")
				}
			}
		})
	}

	if got := countEvidence(task.ID); got != 4 {
		t.Fatalf("预期4条证据，实际%d条", got)
	}

	// ============================
	// 2. 认证、越权和非法输入
	// ============================

	normalBody := `{
		"type":"experiment_text",
		"text":"正常证据",
		"status":"submitted"
	}`

	invalidCases := []struct {
		name   string
		path   string
		token  string
		body   string
		status int
		code   string
	}{
		{
			name:   "缺少JWT",
			path:   path,
			token:  "",
			body:   normalBody,
			status: 401,
			code:   "MISSING_TOKEN",
		},
		{
			name:   "非法JWT",
			path:   path,
			token:  "wrong.token.value",
			body:   normalBody,
			status: 401,
			code:   "INVALID_TOKEN",
		},
		{
			name:   "跨用户提交",
			path:   path,
			token:  f.tokenB,
			body:   normalBody,
			status: 404,
			code:   "NOT_FOUND",
		},
		{
			name: "任务不存在",
			path: fmt.Sprintf(
				"/api/v1/tasks/%d/evidence",
				^uint64(0),
			),
			token:  f.tokenA,
			body:   normalBody,
			status: 404,
			code:   "NOT_FOUND",
		},
		{
			name:   "项目已归档",
			path:   archivedPath,
			token:  f.tokenA,
			body:   normalBody,
			status: 409,
			code:   "PROJECT_ARCHIVED",
		},
		{
			name:   "非法任务ID",
			path:   "/api/v1/tasks/abc/evidence",
			token:  f.tokenA,
			body:   normalBody,
			status: 400,
			code:   "INVALID_ARGUMENT",
		},
		{
			name:   "JSON格式错误",
			path:   path,
			token:  f.tokenA,
			body:   `{"type":`,
			status: 400,
			code:   "INVALID_ARGUMENT",
		},
		{
			name:   "缺少Type",
			path:   path,
			token:  f.tokenA,
			body:   `{"status":"draft"}`,
			status: 400,
			code:   "INVALID_ARGUMENT",
		},
		{
			name:   "非法Type",
			path:   path,
			token:  f.tokenA,
			body:   `{"type":"unknown","status":"draft"}`,
			status: 400,
			code:   "INVALID_EVIDENCE_TYPE",
		},
		{
			name:   "非法Status",
			path:   path,
			token:  f.tokenA,
			body:   `{"type":"test_output","status":"unknown"}`,
			status: 400,
			code:   "INVALID_EVIDENCE_CONTENT",
		},
		{
			name:  "正式提交空内容",
			path:  path,
			token: f.tokenA,
			body: `{
				"type":"test_output",
				"status":"submitted"
			}`,
			status: 400,
			code:   "INVALID_EVIDENCE_CONTENT",
		},
		{
			name:  "非法Commit SHA",
			path:  path,
			token: f.tokenA,
			body: fmt.Sprintf(
				`{"type":"commit_sha","commit_sha":"%s","status":"submitted"}`,
				strings.Repeat("a", 39)+"z",
			),
			status: 400,
			code:   "INVALID_EVIDENCE_CONTENT",
		},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			before := countEvidence(task.ID)
			archivedBefore := countEvidence(archivedTask.ID)

			w := f.request(
				http.MethodPost,
				tc.path,
				tc.token,
				tc.body,
			)

			expectTaskHTTP(
				t, w, tc.status, tc.code,
			)

			if got := countEvidence(task.ID); got != before {
				t.Fatalf(
					"失败请求意外写入证据: before=%d after=%d",
					before, got,
				)
			}

			if got := countEvidence(archivedTask.ID); got != archivedBefore {
				t.Fatal("失败请求向归档任务写入证据")
			}
		})
	}
}
