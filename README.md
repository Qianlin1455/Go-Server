# Go-Server

> 面向个人开发者的项目实践与任务证据管理后端：将项目讨论、实践任务、执行证据和状态追踪关联起来。

**项目状态：持续开发中** · **当前形式：Go 单体 HTTP API（分层架构）**

Go-Server 的目标是帮助开发者把「围绕项目的讨论」转化为「可追踪、可验证的实践任务」。用户可以创建项目与会话、保存消息、基于来源消息建立任务，并提交测试输出、实验说明或 Git Commit SHA 作为完成证据。

当前仓库聚焦于可靠的后端业务基础设施；仓库自动解析、AI 评审、异步任务调度等能力仍在规划或开发中，不应视为已上线功能。

## 核心功能

| 模块       | 已实现能力                                                   |
| ---------- | ------------------------------------------------------------ |
| 身份认证   | 用户注册、登录、JWT 鉴权，私有资源按用户隔离                 |
| 项目管理   | 项目创建、详情及分页列表；区分 `active` / `archived` 状态    |
| 会话与消息 | 会话可选关联项目；发送消息、查询会话与历史消息、项目内会话列表 |
| 任务管理   | 在项目下创建与查询任务，可关联来源消息；支持状态机与版本校验 |
| 实践证据   | 为任务创建草稿或正式证据，类型包括实验文本、测试输出、Commit SHA |
| 状态审计   | 成功变更任务状态时，在同一数据库事务内记录审计信息           |

### 任务状态与完成条件

```
todo ──> doing ──> done
          │
          └──────> todo
```

- 禁止非法或重复状态迁移；当前 `done` 不支持直接重新打开。
- 从 `doing` 进入 `done` 前，任务必须至少有一条 `submitted` 证据。
- 状态更新使用 `status`、`version` 等条件进行 CAS 更新，并检查影响行数；并发请求中只有满足预期版本与状态的更新可以成功。
- 状态变更与审计写入位于同一个事务中，失败时整体回滚。
- 已归档项目不允许创建任务、修改任务状态或提交证据；查询能力保留。

## 技术栈

- **语言与框架**：Go 1.27.1、Gin
- **数据访问**：GORM、MySQL 8（InnoDB）
- **认证**：JWT（HS256）
- **数据库变更**：版本化 SQL Migration（`golang-migrate` CLI）
- **测试**：Go `testing`、`httptest`、真实 MySQL 集成测试

> Go 版本以仓库 `go.mod` 为准。当前代码以单体服务部署，尚未拆分成独立微服务。

## 架构与目录

```
Go-Server/
├── cmd/
│   ├── server/          # HTTP 服务入口与路由注册
│   └── poolbaseline/    # 数据库连接池基线实验
├── internal/
│   ├── database/        # MySQL 初始化与连接池配置
│   ├── handler/         # HTTP 参数解析、响应与路由处理
│   ├── midware/         # JWT、Request ID、Timeout 等中间件
│   ├── model/           # 数据模型
│   ├── repository/      # 数据持久化与条件更新
│   ├── response/        # 响应结构
│   ├── security/        # JWT 相关实现
│   └── service/         # 业务规则与事务边界
├── migrations/          # 000001～000009 数据库迁移文件
├── PRD.md               # 业务需求与设计记录（部分为规划内容）
├── go.mod
└── README.md
```

典型调用链：`HTTP Request → Gin Middleware → Handler → Service → Repository/GORM → MySQL`。

分层边界上，Handler 负责协议和参数处理；Service 负责权限、状态机及事务；Repository 负责数据访问。数据库约束（外键、唯一键、CHECK）用于兜底保证数据一致性。

Repository 统一分类数据库错误，并保留底层错误链，支持通过 `errors.Is` / `errors.As` 排查；事务开始和提交失败同样经过该边界。Handler 只返回固定的公开错误码和消息，内部数据库细节不会写入客户端响应。

`Timeout` 默认设置 3 秒的请求 Context 截止时间，采用协作式取消。下游数据库操作必须传播该 Context；超时错误统一映射为 `504 DEADLINE_EXCEEDED`，中间件也会在处理链返回且响应尚未写出时补充 504。已经发送的响应（含已 Flush 的响应头）不会被覆盖或追加错误 JSON。忽略 Context 的处理代码无法被该中间件强制中断，因此它不保证每个请求都在 3 秒时返回。

## 快速开始

以下示例以 **Windows CMD + Docker MySQL（本机端口 3307）** 为例。请替换示例密码，**不要将真实密码、JWT 密钥或个人配置提交到仓库**。

### 1. 环境准备

- Go（版本参考 `go.mod`）
- MySQL 8（可使用 Docker）
- `golang-migrate` 命令行工具（带 MySQL 驱动）

如尚未运行 MySQL，可创建一个本地测试容器：

```
docker run --name go-server-mysql -e MYSQL_ROOT_PASSWORD=YOUR_PASSWORD -e MYSQL_DATABASE=go_db -p 3307:3306 -d mysql:8.0
```

如已有 MySQL 容器，只需确保能连接 `127.0.0.1:3307` 且已创建 `go_db`，不必重复创建容器。

### 2. 获取代码

```
git clone https://github.com/Qianlin1455/Go-Server.git
cd Go-Server
go mod download
```

### 3. 配置数据库和 JWT

在当前 **CMD** 会话中设置环境变量：

```
set "MYSQL_DSN=root:YOUR_PASSWORD@tcp(127.0.0.1:3307)/go_db?charset=utf8mb4&parseTime=True&loc=Local"
set "JWT_SECRET=REPLACE_WITH_A_LONG_RANDOM_SECRET"
```

`MYSQL_DSN` 供 GORM 使用；`JWT_SECRET` 用于签发与验证令牌。

### 4. 执行数据库迁移

请先确保已安装支持 MySQL 的 `migrate` CLI，并已创建 `go_db`：

```
migrate -path migrations -database "mysql://root:YOUR_PASSWORD@tcp(127.0.0.1:3307)/go_db?multiStatements=true" up
migrate -path migrations -database "mysql://root:YOUR_PASSWORD@tcp(127.0.0.1:3307)/go_db?multiStatements=true" version
```

当前迁移范围为 **000001～000009**。`up` 仅运行尚未应用的版本；已有数据库**不要**在未核对表结构和版本记录前使用 `force`。

### 5. 启动服务

```
go run ./cmd/server
```

默认监听 `http://localhost:8080`，可访问：

```
GET http://localhost:8080/healthz
```

## API 概览

基础路径：`/api/v1`。除健康检查和注册、登录外，其余接口均需要携带 `Authorization: Bearer <JWT>`。

| Method | 路径                                 | 说明                            |
| ------ | ------------------------------------ | ------------------------------- |
| GET    | `/healthz`                           | 健康检查（不带 `/api/v1` 前缀） |
| POST   | `/api/v1/auth/register`              | 注册                            |
| POST   | `/api/v1/auth/login`                 | 登录                            |
| GET    | `/api/v1/users/me`                   | 当前用户信息                    |
| POST   | `/api/v1/projects`                   | 创建项目                        |
| GET    | `/api/v1/projects`                   | 项目分页列表                    |
| GET    | `/api/v1/projects/:id`               | 项目详情                        |
| GET    | `/api/v1/projects/:id/conversations` | 项目内会话列表                  |
| POST   | `/api/v1/conversations`              | 创建会话（可选 `project_id`）   |
| GET    | `/api/v1/conversations/:id`          | 会话详情                        |
| POST   | `/api/v1/conversations/:id/messages` | 发送消息                        |
| GET    | `/api/v1/conversations/:id/messages` | 历史消息分页                    |
| POST   | `/api/v1/projects/:id/tasks`         | 创建任务                        |
| GET    | `/api/v1/projects/:id/tasks`         | 任务分页列表                    |
| PATCH  | `/api/v1/tasks/:id/status`           | 修改状态（需要 `version`）      |
| POST   | `/api/v1/tasks/:id/evidence`         | 创建任务证据                    |

### 示例：提交实验结果

```
POST /api/v1/tasks/1/evidence
Authorization: Bearer <JWT>
Content-Type: application/json

{
  "type": "test_output",
  "text": "go test -p 1 ./... -count=1：全部相关测试通过",
  "status": "submitted"
}
```

证据支持 `experiment_text`、`test_output` 和 `commit_sha` 三类；状态分为 `draft`、`submitted`。上述 Task ID 与 JWT 需替换为真实值。

### 示例：更新任务状态

```
PATCH /api/v1/tasks/1/status
Authorization: Bearer <JWT>
Content-Type: application/json

{
  "status": "doing",
  "version": 1
}
```

成功返回 HTTP `204 No Content`。任务最新版本需要从查询结果获取；使用过期版本可能收到冲突响应。

## 测试与验证

部分集成测试使用真实 MySQL，并要求独立测试库 `go_server_test`。**不要将集成测试 DSN 指向开发库 `go_db`**。

```
set "TEST_MYSQL_DSN=root:YOUR_PASSWORD@tcp(127.0.0.1:3307)/go_server_test?charset=utf8mb4&parseTime=True&loc=Local"
go test -p 1 ./... -count=1
```

运行前请创建测试库并将相应迁移应用到测试库；同时保留运行时所需的 `MYSQL_DSN` 和 `JWT_SECRET`。部分测试在未设置测试 DSN 时会跳过，检查测试输出中的 `SKIP` 才能确认覆盖是否完整。

已有测试重点覆盖：

- JWT 认证、跨用户资源隔离与非法参数；
- 项目、会话、消息、任务的 CRUD 相关接口及分页边界；
- 任务状态转换、旧版本更新冲突、并发 CAS；
- 状态变更与审计的事务原子性、故障注入回滚；
- 证据格式校验、归档项目禁止写入、跨用户拒绝与持久化回滚。

## 数据库演进

| 版本          | 主要变化                                          |
| ------------- | ------------------------------------------------- |
| 000001–000003 | 用户、会话、消息                                  |
| 000004–000006 | 项目、会话关联项目、任务                          |
| 000007        | 任务状态审计 `task_status_audits`                 |
| 000008        | 任务证据 `task_evidence`                          |
| 000009        | 任务评审记录 `task_reviews`（当前仅有数据表结构） |

## 后续计划

以下均为**计划中**的能力，非当前已实现功能：

- 基于 `task_reviews` 实现评审 Service、HTTP API 与业务测试；
- Redis 幂等键与并发控制；
- Kafka Outbox / Worker、重试与异常任务恢复；
- gRPC 调用治理：超时预算、重试与幂等；
- 进一步完善可观测性、指标与性能基线。

## 设计文档

更多背景和业务约束参见 [`PRD.md`](./PRD.md)。其中包含阶段性设计和未完成需求，实际接口能力以本 README 的「API 概览」和当前路由代码为准。

## 说明

本项目目前以个人开发和工程实践为主，持续完善真实业务约束、事务一致性、并发正确性与测试覆盖。

