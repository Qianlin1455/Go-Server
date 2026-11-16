# G-2.3-C 模型客户端配置

从服务进程环境读取以下配置。由部署环境注入 `AI_API_KEY`。

| 变量 | 示例或用途 |
|---|---|
| `AI_BASE_URL` | `https://api.example.com/v1`，客户端附加 `/chat/completions` |
| `AI_MODEL` | `your-model`，作为请求的 `model` |
| `AI_API_KEY` | 兼容端点颁发的 API Key，用于 Bearer 认证 |

去除首尾空白后，三项都为空时 `llmclient.LoadConfig` 返回禁用配置，`NewClient` 返回 `ErrDisabled`。部分配置或配置非法时，`LoadConfig` 返回 `ErrInvalidConfig`。地址须为绝对 URL，禁用 userinfo、查询字符串和 fragment。明文 HTTP 仅限 `localhost` 与 loopback IP，其余地址使用 HTTPS。

`Completer.Complete(ctx, Request{SystemPrompt, UserPrompt})` 发送两条消息，固定 `stream: false`。JSON 请求上限为 32 KiB，成功响应上限为 64 KiB。首条 choice 需要 `finish_reason: "stop"` 和非空白 `message.content`。内容按原字符串返回，包含 `tool_calls` 或 `function_call` 字段的响应会被拒绝。

客户端 8 秒超时覆盖连接、响应头和响应体。较早到期的父 Context 优先，客户端自己的超时返回 `ErrModelTimeout`。非 2xx 响应可用 `errors.Is(err, ErrModelUpstream)` 分类，用 `errors.As` 取得只携带状态码的 `HTTPStatusError`。客户端禁止重试和重定向，错误消息不会包含 Key、提示词或上游正文。

`cmd/server` 在启动时显式加载配置，创建该进程共用的客户端，并注入 [Ask API](ask-api.md)。
