package handler

import (
	"GoServer/internal/model"
	"GoServer/internal/repository"
	"GoServer/internal/service"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestTaskP0ListAndRetrospective(t *testing.T) {
	f := newTaskAPIFixture(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	var tasks []model.Task
	for _, status := range []string{"todo", "doing", "done", "doing"} {
		task := model.Task{UserID: f.userA.ID, ProjectID: f.projectA.ID, Title: status, Status: status, CreatedAt: base}
		if err := f.db.Create(&task).Error; err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	for _, project := range []model.Project{f.other, f.projectB} {
		if err := f.db.Create(&model.Task{UserID: project.UserID, ProjectID: project.ID, Title: "isolated", Status: "doing"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		query string
		total int64
		ids   []uint64
	}{
		{"", 4, []uint64{tasks[3].ID, tasks[2].ID, tasks[1].ID, tasks[0].ID}},
		{"?status=todo", 1, []uint64{tasks[0].ID}},
		{"?status=done", 1, []uint64{tasks[2].ID}},
		{"?status=doing&page_size=1", 2, []uint64{tasks[3].ID}},
		{"?status=doing&page_size=1&page=2", 2, []uint64{tasks[1].ID}},
		{"?status=doing&page_size=1&page=3", 2, []uint64{}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			w := f.request("GET", fmt.Sprintf("/api/v1/projects/%d/tasks%s", f.projectA.ID, tc.query), f.tokenA, "")
			expectTaskHTTP(t, w, 200, "SUCCESS")
			var out struct {
				Data struct {
					Items    []service.TaskResult `json:"items"`
					Total    int64                `json:"total"`
					Page     int                  `json:"page"`
					PageSize int                  `json:"page_size"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out.Data.Total != tc.total || out.Data.Items == nil || len(out.Data.Items) != len(tc.ids) {
				t.Fatalf("unexpected list: %s", w.Body.String())
			}
			for i, id := range tc.ids {
				if out.Data.Items[i].ID != id {
					t.Fatalf("unstable order: %s", w.Body.String())
				}
			}
			if tc.query == "" && (out.Data.Page != 1 || out.Data.PageSize != 20) {
				t.Fatal("default pagination changed")
			}
		})
	}
	for _, query := range []string{"status=invalid", "page=0", "page=-1", "page=x", "page_size=0", "page_size=101", "page_size=-1", "page_size=x", "page=9223372036854775807&page_size=100"} {
		expectTaskHTTP(t, f.request("GET", fmt.Sprintf("/api/v1/projects/%d/tasks?%s", f.projectA.ID, query), f.tokenA, ""), 400, "INVALID_ARGUMENT")
	}
	expectTaskHTTP(t, f.request("GET", fmt.Sprintf("/api/v1/projects/%d/tasks", f.projectB.ID), f.tokenA, ""), 404, "NOT_FOUND")
	count, err := repository.CountTasks(ctx, f.db, f.userA.ID, f.projectB.ID, "doing")
	if err != nil || count != 0 {
		t.Fatalf("cross-user count leaked: %d %v", count, err)
	}
	emptyRecap := f.request("GET", fmt.Sprintf("/api/v1/tasks/%d/retrospective", tasks[0].ID), f.tokenA, "")
	expectTaskHTTP(t, emptyRecap, 200, "SUCCESS")
	var emptyResult struct {
		Data service.TaskRetrospective `json:"data"`
	}
	if err := json.Unmarshal(emptyRecap.Body.Bytes(), &emptyResult); err != nil {
		t.Fatal(err)
	}
	if emptyResult.Data.Source != nil || emptyResult.Data.Evidence == nil || len(emptyResult.Data.Evidence) != 0 || emptyResult.Data.Audits == nil || len(emptyResult.Data.Audits) != 0 {
		t.Fatalf("unexpected empty recap: %s", emptyRecap.Body.String())
	}
	if source, err := repository.GetTaskSourceByUser(ctx, f.db, f.userB.ID, f.projectA.ID, f.messageA.ID); err == nil || source != nil {
		t.Fatal("source ownership missing")
	}
	if source, err := repository.GetTaskSourceByUser(ctx, f.db, f.userA.ID, f.other.ID, f.messageA.ID); err == nil || source != nil {
		t.Fatal("source project isolation missing")
	}
	svc := service.NewTaskService(f.db)
	if _, _, err := svc.ListTasksPage(ctx, f.userA.ID, f.projectA.ID, 1, 20, "invalid"); err != service.ErrInvalidTaskInput {
		t.Fatalf("service status validation: %v", err)
	}

	task, err := svc.CreateTask(ctx, f.userA.ID, service.CreateTaskInput{ProjectID: f.projectA.ID, Title: "recap", SourceMessageID: &f.messageA.ID})
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/tasks/%d", task.ID)
	expectTaskHTTP(t, f.request("PATCH", path+"/status", f.tokenA, `{"status":"done","version":1}`), 409, "INVALID_STATE_TRANSITION")
	expectTaskHTTP(t, f.request("PATCH", path+"/status", f.tokenA, `{"status":"doing","version":1}`), 204, "")
	expectTaskHTTP(t, f.request("POST", path+"/evidence", f.tokenA, `{"type":"test_output","text":"draft only","status":"draft"}`), 201, "SUCCESS")
	expectTaskHTTP(t, f.request("PATCH", path+"/status", f.tokenA, `{"status":"done","version":2}`), 409, "")
	expectTaskHTTP(t, f.request("POST", path+"/evidence", f.tokenA, `{"type":"test_output","text":"verified HTTP assertions","status":"submitted"}`), 201, "SUCCESS")
	expectTaskHTTP(t, f.request("PATCH", path+"/status", f.tokenA, `{"status":"done","version":1}`), 409, "VERSION_CONFLICT")
	expectTaskHTTP(t, f.request("PATCH", path+"/status", f.tokenA, `{"status":"done","version":2}`), 204, "")
	w := f.request("GET", path+"/retrospective", f.tokenA, "")
	expectTaskHTTP(t, w, 200, "SUCCESS")
	var out struct {
		Data service.TaskRetrospective `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	r := out.Data
	if r.Task.Status != "done" || r.Task.Version != 3 || r.Task.CompletedAt == nil || r.Source == nil || r.Source.ID != f.messageA.ID || len(r.Evidence) != 2 || len(r.Audits) != 2 || r.Audits[1].ToVersion != 3 {
		t.Fatalf("incomplete recap: %s", w.Body.String())
	}
	expectTaskHTTP(t, f.request("GET", path+"/retrospective", f.tokenB, ""), 404, "NOT_FOUND")
	expectTaskHTTP(t, f.request("GET", path+"/retrospective", "", ""), 401, "")
	expectTaskHTTP(t, f.request("GET", "/api/v1/tasks/0/retrospective", f.tokenA, ""), 400, "INVALID_ARGUMENT")
	evidence, err := repository.ListTaskEvidenceByUser(ctx, f.db, f.userB.ID, task.ID)
	if err != nil || len(evidence) != 0 {
		t.Fatalf("cross-user evidence leaked: %+v %v", evidence, err)
	}
	audits, err := repository.ListTaskAuditsByUser(ctx, f.db, f.userB.ID, task.ID)
	if err != nil || len(audits) != 0 {
		t.Fatalf("cross-user audits leaked: %+v %v", audits, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repository.CountTasks(canceled, f.db, f.userA.ID, f.projectA.ID, ""); err == nil {
		t.Fatal("count ignored context cancellation")
	}
}
