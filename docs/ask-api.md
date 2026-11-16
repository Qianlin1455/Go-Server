# G-2.3-E 源码问答 API

`POST /api/v1/projects/:id/ai/ask` 使用 `Authorization: Bearer {{token}}` 和 `Content-Type: application/json`。

```json
{
  "question": "为什么 SendMessage 使用事务？",
  "revision_sha": "0123456789abcdef0123456789abcdef01234567",
  "import_job_id": 42
}
```

`question` 必填，上限 2 KiB。`revision_sha` 必须为 40 位十六进制 SHA。省略 `import_job_id` 时选取该项目与 Revision 下 ID 最大的成功任务。可选的 `conversation_id` 必须属于当前用户和路由项目，只用于权限校验。请求体上限为 16 KiB，字段 `user_id` 会被拒绝，用户身份来自 JWT。

```json
{
  "code": "SUCCESS",
  "data": {
    "answer": "事务让消息写入和会话更新时间一起成功或回滚。",
    "revision_sha": "0123456789abcdef0123456789abcdef01234567",
    "import_job_id": 42,
    "citations": [
      {
        "evidence_id": "EXAMPLE_EVIDENCE_ID",
        "path": "internal/service/message_service.go",
        "revision_sha": "0123456789abcdef0123456789abcdef01234567",
        "line_start": 236,
        "line_end": 305,
        "content_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
      }
    ]
  }
}
```

证据不足时响应为 HTTP 200，`code` 为 `INSUFFICIENT_EVIDENCE`，`data` 仍包含服务端说明、Revision、快照 ID 和 `citations: []`。这条路径直接结束于检索阶段。

| HTTP | code | 场景 |
|---|---|---|
| 400 | `INVALID_ARGUMENT` | 请求或字段校验失败 |
| 401 | `MISSING_TOKEN` / `INVALID_TOKEN` | JWT 缺失或无效 |
| 404 | `NOT_FOUND` | 项目、会话或成功快照不可访问 |
| 408 | `REQUEST_CANCELED` | 请求 Context 已取消。客户端已经断开时可能收不到响应 |
| 413 | `REQUEST_TOO_LARGE` / `AI_CONTEXT_TOO_LARGE` | Ask 请求体或发送给模型的 JSON 超限 |
| 500 | `INTERNAL_ERROR` | 数据库或其他内部故障 |
| 502 | `MODEL_UPSTREAM_ERROR` | 模型网络故障或返回非 2xx |
| 502 | `MODEL_INVALID_RESPONSE` | 模型响应格式无效或超限 |
| 502 | `MODEL_INVALID_EVIDENCE` | 模型引用了本次未提供的证据 |
| 503 | `AI_DISABLED` | 服务端未配置 AI |
| 504 | `DEADLINE_EXCEEDED` / `MODEL_TIMEOUT` | 请求或数据库阶段到期，或模型客户端超时 |

服务器启动时按 [LLM Client 配置](llm-client.md) 加载环境变量。三项配置齐全时创建一个共用客户端。三项都为空时 Ask 返回 `AI_DISABLED`，部分配置会使启动失败。

## Apifox 验收

1. 启动服务，通过 `POST /api/v1/auth/login` 获取 token。选用本用户一个已成功导入源码的项目，从文件列表获取 Revision 和 `import_job_id`。
2. 创建上述 POST 请求，设置 `{{base_url}}`、项目 ID 和 Bearer token。将请求里的问题改为包含快照中真实符号的问句，例如用反引号包住方法名。检查 `SUCCESS`，对照文件片段接口核验引用的路径、行号和哈希。
3. 替换为当前快照不存在的符号，检查 `200 INSUFFICIENT_EVIDENCE` 和空引用数组。删除 token 应得到 401，换为不拥有项目的用户 token 应得到 404。
4. 在专用环境中用三项 AI 变量全空的配置重启服务，同一条合法且有权限的请求应得到 `503 AI_DISABLED`。
