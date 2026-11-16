# Go-Server

Go-Server 是一个基于 Go 的异步源码导入与 AI 源码问答后端。它把 GitHub 公开仓库的固定 Revision 导入为可检索快照，让客户端能按文件和行号核验 AI 回答。项目、会话、消息、任务与实践证据构成另一条业务闭环，支持从讨论创建任务，再查看完成证据和状态审计。

v1.0 采用**单仓库、单体分层架构**。HTTP API、Publisher、Consumer 和独立 Worker 是共享领域代码与 MySQL Schema 的不同进程入口。仓库提供 HTTP API，使用 Apifox、curl 等客户端即可调用。

## 核心能力

| 模块 | 当前能力 |
| --- | --- |
| 认证与资源归属 | 注册、登录、bcrypt 密码哈希、JWT 鉴权和当前用户查询。私有接口从认证上下文获取用户 ID。 |
| 项目、会话与消息 | 创建项目、分页列表和详情。会话可在创建时关联项目，支持项目内会话列表、发送消息和历史分页。消息写入与会话更新时间在同一事务提交。 |
| 实践任务与证据 | 创建任务、按状态筛选列表、CAS 更新状态。`todo → doing`、`doing → todo`、`doing → done` 合法，`done` 要求至少一条 `submitted` 证据，且为终态。成功的状态变更与审计记录一起提交。只读复盘返回任务、仍可访问的来源消息、证据和审计。 |
| Redis | 项目详情按用户分键，校验缓存归属，使用 TTL 抖动、进程内 Singleflight 和 DB 回退。项目改名 Service 有缓存失效逻辑和并发测试，目前未注册改名 API。仓库绑定使用 Redis 用户级限流，10 次/分钟；Redis 限流不可用时接口返回 `503`。 |
| GitHub 与导入入口 | 仅绑定公开仓库，读取默认分支的 Commit SHA；导入使用创建时的绑定记录固定 Revision。客户端支持可选 Token、仓库元数据缓存和限流识别。导入 API 使用 `Idempotency-Key`，MySQL 唯一键兜底，Redis 门禁加速并发请求。 |
| 异步任务可靠性 | Import Job 和 Outbox 同事务写入。Publisher 用 Kafka 同步发送，Consumer 驱动 Runner，任务持久化到终态后确认。领取使用 CAS 和 Lease Token，支持租约过期恢复、带 Equal Jitter 的有界指数退避和 GitHub 重试时间、有限尝试、永久消息 DLQ。Consumer 和独立 Worker 支持信号取消，独立 Worker 支持限时排空。 |
| 固定 Revision 快照与检索 | 导入支持的文本源码并存储 `source_files`、`source_spans`。文件内容、行号和 SHA-256 可核验；源码 API 仅从当前用户项目下成功导入的快照读取，支持列表、详情、片段分页和字面关键词搜索。 |
| 基于证据的 AI 问答 | 从选定快照构建有限上下文，调用 OpenAI-compatible Chat Completions，严格解析结果并校验 Evidence ID，引用元数据由服务端组装。没有证据时直接返回，问答结构和资源归属校验独立于会话消息。 |

## 架构和关键链路

```mermaid
flowchart LR
    Client["HTTP 客户端"] --> API["Gin Handler 与 JWT"]
    API --> Domain["业务 Service"]
    Domain --> Repo["Repository"]
    Repo --> DB[("MySQL")]
    Domain --> Redis[("Redis 缓存 / 门禁 / 限流")]
    Domain --> GH["GitHub 公开 REST API"]
    Repo --> Tx["创建事务: Import Job + Outbox"]
    Tx --> DB
    DB --> Pub["Publisher 扫描 Outbox"]
    Pub --> Kafka["Kafka: imports.requested"]
    Kafka --> Con["Consumer Group"]
    Con --> Runner["共享 Runner 逻辑: CAS 领取与租约"]
    DB --> Standalone["独立 Worker: 轮询与恢复"]
    Standalone --> Runner
    Runner --> GH
    Runner --> Snapshot["快照与终态事务"]
    Snapshot --> DB
    Con --> DLQ["Kafka: imports.dlq"]
```

数据库是任务和快照的事实来源。Kafka 发送和 Outbox 标记之间存在故障窗口，消息可能重投。Consumer 会重新读取任务状态，已成功或已失败的导入直接确认，过期 Lease Token 无法回写。DLQ 用于永久消息错误；GitHub 执行失败按任务状态机处理。

```mermaid
flowchart LR
    Ask["Ask HTTP API"] --> Scope["鉴权、项目与 Revision 校验"]
    Scope --> Retrieval["读取成功快照、字面检索"]
    Retrieval --> Context["挑选片段并生成 Evidence ID"]
    Context -->|有证据| LLM["LLM Client: Chat Completions"]
    Context -->|证据不足| Empty["HTTP 200: INSUFFICIENT_EVIDENCE"]
    LLM --> Parse["严格解析 answer 与 evidence_ids"]
    Parse --> Ground["校验本次引用并组装路径、行号与哈希"]
    Ground --> Response["HTTP 响应"]
```

Ask 最多选取 4 个片段，每段不超过 4 KiB，源码总量最多 16 KiB。用户问题和源码作为数据传入。模型只返回答案和引用 ID，服务端验证引用是否属于当次检索；这项校验约束引用来源，答案语义仍需结合源码核验。

## 技术栈与目录

- Go **1.27.1**，以 `go.mod` 为准；Gin **1.12.0**、GORM **1.31.2** 与 MySQL Driver。
- MySQL 8.0 与版本化 SQL Migration；Redis 7 Alpine 与 go-redis/v9；Kafka 4.4.0 Compose 镜像与 IBM Sarama。
- golang-jwt/jwt/v5、bcrypt、`golang.org/x/sync/singleflight`；LLM 客户端基于标准库 `net/http` 和 JSON。
- Go `testing`、`httptest`、Race Detector，另有真实 MySQL/Redis/Kafka 集成测试和 PowerShell E2E。

```text
cmd/
  server/           Gin API 与组件组装
  publisher/        MySQL Outbox → Kafka
  consumer/         Kafka 消费、Runner 调用与 DLQ
  worker/           可选的 MySQL 轮询导入 Worker
  poolbaseline/     独立的 MySQL 连接池诊断程序
internal/
  handler/ service/ repository/ model/   HTTP、业务、持久化与模型
  midware/ response/ security/          请求边界、响应与认证
  database/ cache/ ratelimit/ idempotency/
  githubclient/ llmclient/              GitHub 与模型 HTTP 客户端
  worker/ publisher/ consumer/         异步执行和交付
events/             ImportRequested 与 DLQ 消息定义
migrations/         000001–000017，配套 up/down SQL
scripts/e2e/        导入主链路与故障注入
docs/               API、可靠性、实验与验证材料
```

## 本地运行

### 1. MySQL、迁移与环境

准备 Go 1.27.1、MySQL 8.0、Docker Compose 以及带 MySQL 驱动的 `migrate` CLI。从仓库根目录执行。Compose 提供 Redis 和 Kafka，MySQL 需另外启动。示例使用本地 MySQL 端口 `3307`，请按实际端口和授权账号替换占位符。

```powershell
mysql -h 127.0.0.1 -P 3307 -u root -p -e "CREATE DATABASE IF NOT EXISTS go_db CHARACTER SET utf8mb4"
migrate -path migrations -database "mysql://root:YOUR_PASSWORD@tcp(127.0.0.1:3307)/go_db?multiStatements=true" up
migrate -path migrations -database "mysql://root:YOUR_PASSWORD@tcp(127.0.0.1:3307)/go_db?multiStatements=true" version

$env:MYSQL_DSN = 'root:YOUR_PASSWORD@tcp(127.0.0.1:3307)/go_db?charset=utf8mb4&parseTime=True&loc=UTC'
$env:JWT_SECRET = Read-Host 'JWT signing secret' -MaskInput
```

运行时从进程环境读取配置。每个独立终端都需要继承或设置相应变量。PowerShell 下也可运行 `./set-dsn.ps1` 交互设置业务和测试 DSN。

| 变量 | 用途与默认值 |
| --- | --- |
| `MYSQL_DSN`、`JWT_SECRET` | 数据库驱动 DSN；JWT 签发密钥由 HTTP 进程使用。 |
| `REDIS_ADDR`、`REDIS_PASSWORD` | HTTP 使用的 Redis，默认 `127.0.0.1:6379`，密码按实例配置，仓库 Compose 默认无密码。 |
| `KAFKA_BROKERS`、`KAFKA_GROUP_ID` | Publisher 与 Consumer 使用逗号分隔的 Broker，默认 `127.0.0.1:9092`。Consumer 默认组 `go-server-import-workers-v1`。 |
| `PUBLISHER_BATCH_SIZE`、`PUBLISHER_INTERVAL` | 默认 `100` 条、`1s`。 |
| `WORKER_MODE`、`WORKER_SHUTDOWN_GRACE` | 独立 Worker 必须设置 `source-import`，排空时间默认 `15s`。 |
| `GITHUB_TOKEN` | 可选，HTTP 绑定和源码导入进程构造 GitHub 客户端时读取。 |
| `AI_BASE_URL`、`AI_MODEL`、`AI_API_KEY` | 三项均空时关闭 AI，部分配置或配置非法时 HTTP 进程启动失败。Base URL 示例为 `https://api.example.com/v1`，客户端追加 `/chat/completions`。本地 loopback 可使用 HTTP。 |
| `DEBUG_POOL` | 非空时 HTTP 进程每 5 秒记录 MySQL 连接池统计。 |

### 2. Redis、Kafka 和 Topic

```powershell
docker compose up -d --wait redis kafka
docker compose ps
docker compose exec -T kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --create --if-not-exists --topic imports.requested --partitions 1 --replication-factor 1
docker compose exec -T kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --create --if-not-exists --topic imports.dlq --partitions 1 --replication-factor 1
```

如果首次启动 Kafka 时日志出现 `AccessDeniedException` 指向 `/tmp/kraft-combined-logs`，可在本地开发 Compose 中按现有 E2E 脚本的方式修正该卷目录属主，再启动 Kafka。

```powershell
docker compose stop kafka
docker compose run --rm --no-deps --user 0 --entrypoint chown kafka 1000:1000 /tmp/kraft-combined-logs
docker compose up -d --wait kafka
```

### 3. 启动进程并调用

按需在不同终端启动。启用 Ask 时，在 HTTP 进程启动前设置以下模型配置。使用 GitHub 认证额度时，在绑定和导入进程启动前设置 `GITHUB_TOKEN`。

```powershell
$env:AI_BASE_URL = 'https://api.example.com/v1'
$env:AI_MODEL = 'YOUR_MODEL_ID'
$env:AI_API_KEY = Read-Host 'Model API key' -MaskInput
```

```powershell
go run ./cmd/server
go run ./cmd/publisher
go run ./cmd/consumer
```

HTTP 监听 `:8080`，`GET /healthz` 可探测 HTTP 服务。Kafka 导入链路使用 Publisher 和 Consumer，Consumer 已包含 Runner。独立 Worker 可直接在 MySQL 轮询和恢复任务；演示 Kafka 链路时只启动上面的三个进程，便于观测领取路径。

```powershell
$env:WORKER_MODE = 'source-import'
go run ./cmd/worker
```

调用顺序为注册、登录、创建项目、绑定 `owner/repo`、创建导入。创建导入时提交 `{"repository_id":1}` 和 `Idempotency-Key`，保存返回的 `job_id` 与 `revision_sha`。用同一 JWT 查 `GET /api/v1/imports/:id`，成功后使用 `revision_sha` 和可选 `import_job_id` 检索或提问。Ask 示例和错误语义见 [Ask API](docs/ask-api.md)。

实践任务演示可先关联一条同项目会话消息，再提交正式证据，例如 `{"type":"test_output","status":"submitted","text":"TestTaskState: PASS"}`。任务处于 `doing` 时，以当前版本提交 `{"status":"done","version":2}`，完成后读取复盘核对来源、证据和审计。

## HTTP API

| 模块 | Method | 路径 | 功能 | JWT |
| --- | --- | --- | --- | --- |
| 探针 | GET | `/healthz` | HTTP 健康探针 | 否 |
| 认证 | POST | `/api/v1/auth/register` | 注册 | 否 |
| 认证 | POST | `/api/v1/auth/login` | 登录、获取 JWT | 否 |
| 认证 | GET | `/api/v1/users/me` | 当前用户身份 | 是 |
| 项目 | POST | `/api/v1/projects` | 创建项目 | 是 |
| 项目 | GET | `/api/v1/projects` | 分页列出项目 | 是 |
| 项目 | GET | `/api/v1/projects/:id` | 项目详情 | 是 |
| 会话 | POST | `/api/v1/conversations` | 创建会话，可指定项目 | 是 |
| 会话 | GET | `/api/v1/conversations/:id` | 会话详情 | 是 |
| 会话 | GET | `/api/v1/projects/:id/conversations` | 项目内会话分页 | 是 |
| 消息 | POST | `/api/v1/conversations/:id/messages` | 发送消息 | 是 |
| 消息 | GET | `/api/v1/conversations/:id/messages` | 历史消息分页 | 是 |
| 任务 | POST | `/api/v1/projects/:id/tasks` | 创建实践任务 | 是 |
| 任务 | GET | `/api/v1/projects/:id/tasks` | 列表、状态筛选和分页 | 是 |
| 任务 | PATCH | `/api/v1/tasks/:id/status` | 按版本变更状态 | 是 |
| 任务证据 | POST | `/api/v1/tasks/:id/evidence` | 创建稿件或正式证据 | 是 |
| 任务复盘 | GET | `/api/v1/tasks/:id/retrospective` | 读取任务、来源、证据和审计 | 是 |
| 仓库 | POST | `/api/v1/projects/:id/repositories` | 绑定 GitHub 公开仓库 | 是 |
| 导入 | POST | `/api/v1/projects/:id/imports` | 发起或重用幂等导入 | 是 |
| 导入 | GET | `/api/v1/imports/:id` | 任务所有者查询导入状态 | 是 |
| 源码 | GET | `/api/v1/projects/:id/source-files` | 快照文件列表 | 是 |
| 源码 | GET | `/api/v1/projects/:id/source-files/:file_id` | 源文件详情与内容 | 是 |
| 源码 | GET | `/api/v1/projects/:id/source-files/:file_id/spans` | 源文件片段分页 | 是 |
| 源码 | GET | `/api/v1/projects/:id/source-search` | 字面关键词搜索 | 是 |
| AI | POST | `/api/v1/projects/:id/ai/ask` | 对固定快照提问并获得可核验引用 | 是 |

## 测试、演示与边界

| 验证范围 | 仓库中的测试依据 |
| --- | --- |
| 单元与 HTTP | `internal/security/jwt_test.go`、`internal/service/task_state_test.go`、`internal/llmclient/client_test.go`，覆盖签发验签、状态转换和模型协议、超时、异常响应。 |
| MySQL 与并发 | `internal/service/task_service_test.go`、`internal/service/import_job_concurrency_test.go`、`internal/repository/source_snapshot_test.go`，覆盖 CAS、并发幂等、结果回滚及过期租约。 |
| Redis 与一致性 | `internal/cache/redis_test.go`、`internal/service/project_cache_race_test.go`、`internal/ratelimit/redis_test.go`，覆盖缓存、失效并发、限流隔离和 Lua 计数。 |
| Kafka、故障和恢复 | `internal/publisher/publisher_test.go`、`internal/consumer/kafka_dlq_integration_test.go`、`internal/consumer/handler_integration_test.go`、`internal/worker/process_integration_test.go`，覆盖发送失败、真实 Broker DLQ、重复消费、落库与提交 Offset 的故障窗口及进程强杀后的租约恢复。 |
| AI 端到端 | `internal/handler/ask_e2e_test.go` 使用 Gin、JWT、隔离 MySQL 和本地 HTTP 模型替身；`internal/service/ask_live_test.go` 提供显式启用的真实模型冒烟。 |

先为隔离的 `go_server_test` 创建数据库并应用同一套迁移。全仓测试中部分测试需要 `TEST_MYSQL_DSN`，另有用例直接读取 `MYSQL_DSN`，因此在**单独测试终端**中将两者指向测试库。使用隔离 Redis；不同测试读取 `TEST_REDIS_ADDR`/`TEST_REDIS_PASSWORD` 或 `REDIS_ADDR`。真实 Kafka DLQ 测试使用 `TEST_KAFKA_BROKER`，只在空的独立 Broker 上运行。未配置外部依赖的测试有的会跳过，有的会报错。

```powershell
$env:TEST_MYSQL_DSN = 'root:YOUR_PASSWORD@tcp(127.0.0.1:3307)/go_server_test?charset=utf8mb4&parseTime=True&loc=UTC'
$env:MYSQL_DSN = $env:TEST_MYSQL_DSN
$env:TEST_REDIS_ADDR = '127.0.0.1:6379'  # 确认该端口是隔离测试实例
$env:REDIS_ADDR = $env:TEST_REDIS_ADDR
go build ./...
go test -p 1 ./... -count=1
go test -race -p 1 ./internal/service ./internal/handler ./internal/llmclient ./internal/worker ./internal/consumer -count=1
```

在 PowerShell 7 的隔离环境可依次运行 `./scripts/e2e/01-up.ps1`、`./scripts/e2e/02-happy.ps1` 和 `./scripts/e2e/99-down.ps1`。脚本使用 Compose 服务名及宿主端口，启动前应预留这些资源。故障脚本覆盖强杀 Consumer 内的 Worker、Kafka 停机补发、并发幂等和成功事件重投。真实模型冒烟需额外配置 AI 三项并显式设置 `AI_LIVE_TEST=1`。详见 [实验记录](docs/experiments/kafka-outbox.md)、[可靠性验证](docs/g1-2-reliability.md)和 [LLM Client](docs/llm-client.md)。

v1.0 提供单轮源码问答和任务证据复盘。项目改名 HTTP 接口、归档恢复、独立评审、自动 DLQ 重放、多轮/Agent/工具调用、前端与生产级多机高可用属于后续边界。业务约束和逐项交付状态见 [PRD](PRD.md)，GitHub 配额与客户端行为见 [GitHub API Client](docs/github-api-client.md)。
