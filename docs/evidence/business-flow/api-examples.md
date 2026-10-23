# 完整 HTTP 请求与响应（实际执行记录）

基础地址 `http://localhost:8080`，业务前缀 `/api/v1`。以下来自 2026-10-23 的真实运行；ID 是该次数据库返回值，重放必须换成新请求返回值。密码与 JWT 已脱敏。私有接口均携带 `Authorization: Bearer <JWT>`。状态变更成功返回 204，无 JSON 正文。

## curl / Apifox 使用

在 Apifox 按下面各步骤创建请求：复制 Method、Path 和 JSON request，私有请求配置 Bearer Token。先注册并登录自己的账号；将后续 project/conversation/message/task ID 替换为实际值。

PowerShell curl 示例（仅示例，Token 需来自实际登录；完整自动化用 business-flow.py）：

```powershell
$base = "http://localhost:8080"
$token = "YOUR_JWT"
curl.exe -i "$base/api/v1/projects/318/tasks?page=1&page_size=20&status=done" -H "Authorization: Bearer $token"
curl.exe -i "$base/api/v1/tasks/171/retrospective" -H "Authorization: Bearer $token"
# body.json 保存对应步骤的 JSON（UTF-8），避免 Windows 命令行引号差异
curl.exe -i -X PATCH "$base/api/v1/tasks/171/status" -H "Authorization: Bearer $token" -H "Content-Type: application/json" --data-binary "@body.json"
```

## 实际请求和响应

### GET /healthz => 200

响应体：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "status": "ok"
  }
}
```

### POST /api/v1/auth/register => 201

请求体：

```json
{
  "username": "p0_c9af4b97b753418ca8f0",
  "email": "p0_c9af4b97b753418ca8f0@example.com",
  "password": "<REDACTED>"
}
```

响应体：

```json
{
  "code": "SUCCESS",
  "data": {
    "id": 752,
    "username": "p0_c9af4b97b753418ca8f0",
    "email": "p0_c9af4b97b753418ca8f0@example.com"
  }
}
```

### POST /api/v1/auth/login => 200

请求体：

```json
{
  "email": "p0_c9af4b97b753418ca8f0@example.com",
  "password": "<REDACTED>"
}
```

响应体：

```json
{
  "code": "SUCCESS",
  "data": {
    "access_token": "<REDACTED>",
    "token_type": "Bearer",
    "expires_in": 3600
  }
}
```

### POST /api/v1/projects => 201

请求体：

```json
{
  "name": "P0 business flow 3af825a6"
}
```

响应体：

```json
{
  "code": "SUCCESS",
  "data": {
    "id": 318,
    "name": "P0 business flow 3af825a6",
    "description": null,
    "status": "active",
    "created_at": "0001-01-01T00:00:00Z",
    "updated_at": "0001-01-01T00:00:00Z"
  }
}
```

### POST /api/v1/conversations => 201

请求体：

```json
{
  "title": "Transaction practice",
  "project_id": 318
}
```

响应体：

```json
{
  "code": "SUCCESS",
  "data": {
    "id": 280,
    "title": "Transaction practice",
    "project_id": 318
  }
}
```

### POST /api/v1/conversations/280/messages => 201

请求体：

```json
{
  "content": "Verify atomic task status and audit updates"
}
```

响应体：

```json
{
  "code": "SUCCESS",
  "data": {
    "id": 227,
    "conversation_id": 280,
    "role": "user",
    "content": "Verify atomic task status and audit updates",
    "created_at": "2026-10-23T10:17:34.011+08:00"
  }
}
```

### POST /api/v1/projects/318/tasks => 201

请求体：

```json
{
  "title": "Practice transaction rollback",
  "source_message_id": 227
}
```

响应体：

```json
{
  "code": "SUCCESS",
  "data": {
    "id": 171,
    "project_id": 318,
    "source_message_id": 227,
    "title": "Practice transaction rollback",
    "description": null,
    "status": "todo",
    "version": 1,
    "completed_at": null,
    "created_at": "2026-10-23T10:17:34.026+08:00",
    "updated_at": "2026-10-23T10:17:34.026+08:00"
  }
}
```

### PATCH /api/v1/tasks/171/status => 409

请求体：

```json
{
  "status": "done",
  "version": 1
}
```

响应体：

```json
{
  "code": "INVALID_STATE_TRANSITION",
  "message": "不允许的任务状态转换"
}
```

### PATCH /api/v1/tasks/171/status => 204

请求体：

```json
{
  "status": "doing",
  "version": 1
}
```

响应体：

```json
null
```

### GET /api/v1/projects/318/tasks?status=doing => 200

响应体：

```json
{
  "code": "SUCCESS",
  "data": {
    "items": [
      {
        "id": 171,
        "project_id": 318,
        "source_message_id": 227,
        "title": "Practice transaction rollback",
        "description": null,
        "status": "doing",
        "version": 2,
        "completed_at": null,
        "created_at": "2026-10-23T10:17:34.026+08:00",
        "updated_at": "2026-10-23T10:17:34.043+08:00"
      }
    ],
    "page": 1,
    "page_size": 20,
    "total": 1
  }
}
```

### PATCH /api/v1/tasks/171/status => 409

请求体：

```json
{
  "status": "done",
  "version": 2
}
```

响应体：

```json
{
  "code": "EVIDENCE_REQUIRED",
  "message": "任务完成前必须提交至少一条有效证据"
}
```

### POST /api/v1/tasks/171/evidence => 201

请求体：

```json
{
  "type": "test_output",
  "text": "Draft observation",
  "status": "draft"
}
```

响应体：

```json
{
  "code": "SUCCESS",
  "data": {
    "id": 1,
    "task_id": 171,
    "type": "test_output",
    "text": "Draft observation",
    "commit_sha": null,
    "status": "draft",
    "created_at": "2026-10-23T10:17:34.093+08:00"
  }
}
```

### PATCH /api/v1/tasks/171/status => 409

请求体：

```json
{
  "status": "done",
  "version": 2
}
```

响应体：

```json
{
  "code": "EVIDENCE_REQUIRED",
  "message": "任务完成前必须提交至少一条有效证据"
}
```

### POST /api/v1/tasks/171/evidence => 201

请求体：

```json
{
  "type": "experiment_text",
  "text": "Observed HTTP: todo to done rejected; doing without submitted evidence rejected, including draft-only evidence.",
  "status": "submitted"
}
```

响应体：

```json
{
  "code": "SUCCESS",
  "data": {
    "id": 2,
    "task_id": 171,
    "type": "experiment_text",
    "text": "Observed HTTP: todo to done rejected; doing without submitted evidence rejected, including draft-only evidence.",
    "commit_sha": null,
    "status": "submitted",
    "created_at": "2026-10-23T10:17:34.105+08:00"
  }
}
```

### PATCH /api/v1/tasks/171/status => 409

请求体：

```json
{
  "status": "done",
  "version": 1
}
```

响应体：

```json
{
  "code": "VERSION_CONFLICT",
  "message": "任务版本已发生变化"
}
```

### PATCH /api/v1/tasks/171/status => 204

请求体：

```json
{
  "status": "done",
  "version": 2
}
```

响应体：

```json
null
```

### GET /api/v1/projects/318/tasks?page=1&page_size=20&status=done => 200

响应体：

```json
{
  "code": "SUCCESS",
  "data": {
    "items": [
      {
        "id": 171,
        "project_id": 318,
        "source_message_id": 227,
        "title": "Practice transaction rollback",
        "description": null,
        "status": "done",
        "version": 3,
        "completed_at": "2026-10-23T10:17:34.119+08:00",
        "created_at": "2026-10-23T10:17:34.026+08:00",
        "updated_at": "2026-10-23T10:17:34.12+08:00"
      }
    ],
    "page": 1,
    "page_size": 20,
    "total": 1
  }
}
```

### GET /api/v1/projects/318/tasks?page=2&page_size=20&status=done => 200

响应体：

```json
{
  "code": "SUCCESS",
  "data": {
    "items": [],
    "page": 2,
    "page_size": 20,
    "total": 1
  }
}
```

### GET /api/v1/projects/318/tasks?status=invalid => 400

响应体：

```json
{
  "code": "INVALID_ARGUMENT",
  "message": "无效的 status 参数"
}
```

### GET /api/v1/tasks/171/retrospective => 200

响应体：

```json
{
  "code": "SUCCESS",
  "data": {
    "task": {
      "id": 171,
      "project_id": 318,
      "source_message_id": 227,
      "title": "Practice transaction rollback",
      "description": null,
      "status": "done",
      "version": 3,
      "completed_at": "2026-10-23T10:17:34.119+08:00",
      "created_at": "2026-10-23T10:17:34.026+08:00",
      "updated_at": "2026-10-23T10:17:34.12+08:00"
    },
    "source": {
      "id": 227,
      "conversation_id": 280,
      "role": "user",
      "content": "Verify atomic task status and audit updates",
      "created_at": "2026-10-23T10:17:34.011+08:00"
    },
    "evidence": [
      {
        "id": 1,
        "task_id": 171,
        "type": "test_output",
        "text": "Draft observation",
        "commit_sha": null,
        "status": "draft",
        "created_at": "2026-10-23T10:17:34.093+08:00"
      },
      {
        "id": 2,
        "task_id": 171,
        "type": "experiment_text",
        "text": "Observed HTTP: todo to done rejected; doing without submitted evidence rejected, including draft-only evidence.",
        "commit_sha": null,
        "status": "submitted",
        "created_at": "2026-10-23T10:17:34.105+08:00"
      }
    ],
    "audits": [
      {
        "id": 1,
        "from_status": "todo",
        "to_status": "doing",
        "from_version": 1,
        "to_version": 2,
        "created_at": "2026-10-23T10:17:34.045+08:00"
      },
      {
        "id": 2,
        "from_status": "doing",
        "to_status": "done",
        "from_version": 2,
        "to_version": 3,
        "created_at": "2026-10-23T10:17:34.122+08:00"
      }
    ]
  }
}
```

### POST /api/v1/auth/register => 201

请求体：

```json
{
  "username": "p0_f9f416f4543a46c5b2a9",
  "email": "p0_f9f416f4543a46c5b2a9@example.com",
  "password": "<REDACTED>"
}
```

响应体：

```json
{
  "code": "SUCCESS",
  "data": {
    "id": 753,
    "username": "p0_f9f416f4543a46c5b2a9",
    "email": "p0_f9f416f4543a46c5b2a9@example.com"
  }
}
```

### POST /api/v1/auth/login => 200

请求体：

```json
{
  "email": "p0_f9f416f4543a46c5b2a9@example.com",
  "password": "<REDACTED>"
}
```

响应体：

```json
{
  "code": "SUCCESS",
  "data": {
    "access_token": "<REDACTED>",
    "token_type": "Bearer",
    "expires_in": 3600
  }
}
```

### GET /api/v1/tasks/171/retrospective => 404

响应体：

```json
{
  "code": "NOT_FOUND",
  "message": "任务或所属项目不存在"
}
```

### GET /api/v1/projects/318/tasks => 404

响应体：

```json
{
  "code": "NOT_FOUND",
  "message": "项目不存在"
}
```

## 补充分页与错误语义

`status` 可选，空值/不传返回全部；仅接受 todo、doing、done。page 默认 1，page_size 默认 20，page>=1，1<=page_size<=100，并拒绝 OFFSET 溢出。总数是过滤后全部记录数；空页 items=[] 而 total 仍可能非零。参数错误 400，未认证 401；任务越权和不存在均为 404；非法状态迁移、缺少 submitted 证据及旧版本分别为 409 INVALID_STATE_TRANSITION / EVIDENCE_REQUIRED / VERSION_CONFLICT。

列表和 Count 为独立 SQL，并发写入不保证同一快照。复盘也使用多条独立只读语句。
