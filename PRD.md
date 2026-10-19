# Go-Server 项目与任务模块 PRD

-   **文档版本：** v1.0
-   **日期：** 2026-10-19
-   **状态：** P0 设计稿；Project
    创建接口已实现并通过当前测试，其他接口按本文规划逐步实现
-   **项目：** Go-Server
-   **目标读者：** 开发者、代码审查者与后续维护者

------------------------------------------------------------------------

## 1. 背景与目标

Go-Server
的目标不只是提供代码问答接口，还要帮助个人开发者围绕自己的代码仓库形成可追踪的学习与实践闭环：

1.  创建并管理一个开发项目；
2.  在项目上下文中进行代码提问和讨论；
3.  从讨论中建立可执行的实践任务；
4.  推进任务状态，并在完成后查看任务与来源信息，支持复盘。

本阶段优先完善 `projects`、`conversations`、`tasks` 的业务关系、API
契约、权限规则和测试标准。仓库快照、源文件索引及导入任务暂不阻塞本阶段。

### 1.1 P0 目标

-   为项目提供创建、查询、编辑、归档和恢复能力。
-   允许会话可选关联项目，并兼容历史上没有项目归属的会话。
-   为任务提供创建、查询、编辑和状态更新能力。
-   强制执行用户资源隔离。
-   定义任务状态机、并发更新规则与统一错误语义。
-   通过自动化测试覆盖越权、重复、非法状态、来源引用和并发冲突等反例。

### 1.2 非目标

本阶段暂不实现：

-   仓库克隆、仓库快照与源文件解析；
-   `repo_snapshots`、`source_files` 的完整业务流程；
-   独立 `import_jobs` 的执行、重试与调度；
-   多人协作、组织/团队权限；
-   项目物理删除；
-   任务评论、复杂工作流、自定义状态和任务依赖；
-   将所有业务拆分成独立部署的微服务。

------------------------------------------------------------------------

## 2. 用户与核心场景

### 2.1 目标用户

个人开发者，希望围绕自己的项目阅读代码、提出问题、实践改进并回顾完成情况。

### 2.2 核心业务闭环

1.  用户通过 JWT 登录；
2.  用户创建项目；
3.  用户在项目下创建或关联会话；
4.  用户在会话中提出代码问题；
5.  用户将某条消息作为来源，创建一个实践任务；
6.  用户把任务从待处理推进到进行中，再推进到已完成；
7.  用户查看任务状态、完成时间及来源消息，进行复盘。

### 2.3 核心模块职责

-   **ProjectService：**
    负责项目输入校验、项目归属、项目状态规则、创建/编辑/归档/恢复等项目业务行为。
-   **TaskService：**
    负责任务输入校验、项目和来源消息归属校验、状态转换、版本冲突处理、完成时间维护。
-   **Repository：** 只负责数据持久化和数据库错误分类；不取代 Service
    的业务规则。
-   **Handler：** 解析 HTTP 请求、从已认证上下文读取用户 ID、调用
    Service，并映射 HTTP 状态码和统一响应。
-   **JWT 中间件：** 验证 Token 并向请求上下文注入用户身份。

------------------------------------------------------------------------

## 3. 实体关系与数据边界

### 3.1 ER 关系

``` text
users
  └── projects
        ├── conversations (project_id 可空，兼容历史会话)
        └── tasks
              └── source_message_id → messages.id（可空）

users
  └── conversations
        └── messages

import_jobs（独立实体，本阶段不实现）
repo_snapshots（后续阶段）
  └── source_files（后续阶段）
```

### 3.2 关系约束

-   一个用户可以拥有多个项目。
-   一个项目属于且只属于一个用户。
-   一个会话属于一个用户；会话可以不关联项目，也可以关联一个项目。
-   一个项目可以拥有多个任务。
-   一个任务属于一个用户和一个项目。
-   一个任务可以选择关联一条来源消息。
-   如果任务设置 `source_message_id`，Service
    必须验证该消息所属会话属于当前用户，并且该会话关联到当前任务的项目。
-   项目与会话/任务的复合外键用于防止 `project_id` 与 `user_id` 不匹配。
-   历史会话的 `project_id` 保持可空；不自动将历史会话分配到任意项目。
-   `import_jobs` 与项目/任务主链路解耦，后续再定义其业务关系。

------------------------------------------------------------------------

## 4. 数据模型与 Migration 草案

> 下列设计以本地已创建并验证的 `000004`--`000006` SQL
> 为基础。正式合并前仍需检查 SQL 文件、up/down
> 顺序和目标数据库现状。项目当前使用版本化 SQL
> Migration；应用初始化逻辑不会自动执行 `migrations/` 目录中的 SQL。

### 4.1 `projects`

  -----------------------------------------------------------------------
  字段                    类型                    约束/含义
  ----------------------- ----------------------- -----------------------
  `id`                    `BIGINT UNSIGNED`       主键，自增

  `user_id`               `BIGINT UNSIGNED`       必填，归属用户

  `name`                  `VARCHAR(128)`          必填

  `description`           `TEXT NULL`             可选描述

  `status`                `VARCHAR(16)`           默认 `active`，只允许
                                                  `active` / `archived`

  `created_at`            `DATETIME(3)`           创建时间

  `updated_at`            `DATETIME(3)`           更新时间
  -----------------------------------------------------------------------

关键约束：

-   `UNIQUE (user_id, name)`：同一用户不能创建同名项目，不同用户可以同名。
-   `UNIQUE (id, user_id)`：供复合外键引用。
-   `INDEX (user_id, id)`：支持按用户查询项目。
-   `FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT`。
-   `CHECK (status IN ('active', 'archived'))`。

### 4.2 `conversations` 的变更

新增字段：

  字段           类型                     约束/含义
  -------------- ------------------------ --------------
  `project_id`   `BIGINT UNSIGNED NULL`   可选项目归属

关键约束：

-   索引 `(project_id, user_id)`。
-   复合外键
    `(project_id, user_id) REFERENCES projects(id, user_id) ON DELETE RESTRICT`。
-   `project_id` 允许 `NULL`，用于兼容既有会话。

### 4.3 `tasks`

  字段                  类型                     约束/含义
  --------------------- ------------------------ ----------------------
  `id`                  `BIGINT UNSIGNED`        主键，自增
  `user_id`             `BIGINT UNSIGNED`        必填，任务所属用户
  `project_id`          `BIGINT UNSIGNED`        必填，任务所属项目
  `source_message_id`   `BIGINT UNSIGNED NULL`   可选来源消息
  `title`               `VARCHAR(200)`           必填
  `description`         `TEXT NULL`              可选描述
  `status`              `VARCHAR(16)`            默认 `todo`
  `version`             `INT UNSIGNED`           默认 `1`，乐观锁版本
  `completed_at`        `DATETIME(3) NULL`       完成时间
  `created_at`          `DATETIME(3)`            创建时间
  `updated_at`          `DATETIME(3)`            更新时间

关键约束：

-   复合外键
    `(project_id, user_id) REFERENCES projects(id, user_id) ON DELETE RESTRICT`。
-   `source_message_id` 外键引用 `messages(id)`，删除来源消息时设为
    `NULL`。
-   索引
    `(project_id, user_id, status, id)`，支持项目任务列表与状态筛选。
-   `CHECK (status IN ('todo', 'doing', 'done'))`。
-   `version` 用于并发更新检测。

### 4.4 Migration 文件

  ---------------------------------------------------------------------------
  文件                                    作用
  --------------------------------------- -----------------------------------
  `000004_create_projects.up.sql`         创建 `projects`

  `000004_create_projects.down.sql`       删除 `projects`

  `000005_alter_conversations.up.sql`     为 `conversations` 增加
                                          `project_id`、索引和复合外键

  `000005_alter_conversations.down.sql`   删除复合外键、索引和 `project_id`

  `000006_create_tasks.up.sql`            创建 `tasks`

  `000006_create_tasks.down.sql`          删除 `tasks`
  ---------------------------------------------------------------------------

回滚顺序必须与依赖关系相反：先回滚 `000006`，再回滚 `000005`，最后回滚
`000004`。在生产或含重要数据的环境中执行 down
前必须先备份，并确认数据删除影响。

------------------------------------------------------------------------

## 5. 通用 API 约定

### 5.1 基础路径与认证

-   基础路径：`/api/v1`
-   所有 Project、Task 和项目关联 Conversation 的接口均要求有效
    JWT，除非后续明确标注为公开接口。
-   当前用户 ID 必须来自已认证的请求上下文；不信任客户端传入的
    `user_id`。
-   所有数据库查询和更新都必须在服务端施加用户归属约束。

### 5.2 统一响应

成功响应示例：

``` json
{
  "code": "SUCCESS",
  "data": {}
}
```

分页响应示例：

``` json
{
  "code": "SUCCESS",
  "data": {
    "items": [],
    "page": 1,
    "page_size": 20,
    "total": 0
  }
}
```

错误响应沿用现有统一错误响应机制，禁止把数据库原始错误直接返回给客户端。

### 5.3 错误码约定

  ----------------------------------------------------------------------------------------------
  场景                                                  HTTP 状态码 业务码建议
  ------------------------------------ ---------------------------- ----------------------------
  请求 JSON                                                     400 `INVALID_ARGUMENT`
  不合法、字段校验失败、分页参数错误                                

  缺少/无效 JWT                                                 401 `UNAUTHORIZED`

  访问其他用户资源                                              403 `FORBIDDEN`

  当前用户下资源不存在                                          404 `NOT_FOUND`

  项目名称重复                                                  409 `PROJECT_ALREADY_EXISTS`

  非法状态转换                                                  409 `INVALID_STATE_TRANSITION`

  乐观锁冲突                                                    409 `VERSION_CONFLICT`

  数据库内部故障                                                500 `STORAGE_FAILURE`
  ----------------------------------------------------------------------------------------------

对于越权资源的具体响应策略，需要保持项目内一致。若现有接口采用
`403`，本模块沿用；若产品后续选择隐藏资源存在性，则需统一调整为
`404`，不能不同接口随意混用。

### 5.4 分页约定

-   `page` 默认 `1`，最小值 `1`。
-   `page_size` 默认 `20`，范围 `1–100`。
-   参数不合法返回 `400`。
-   使用稳定排序（建议 `id ASC` 或明确的更新时间排序加 ID
    作为次级排序）。
-   列表结果必须先限定当前用户和父资源，再应用状态过滤与分页。
-   总数统计必须使用相同的用户、项目和状态过滤条件。
-   复用现有 `Paginate(page, pageSize)`，并防止 offset 整数溢出。

------------------------------------------------------------------------

## 6. Project API 契约

### 6.1 接口清单

  方法      路径                             功能              成功码
  --------- -------------------------------- --------------- --------
  `POST`    `/api/v1/projects`               创建项目             201
  `GET`     `/api/v1/projects`               项目列表             200
  `GET`     `/api/v1/projects/:id`           项目详情             200
  `PATCH`   `/api/v1/projects/:id`           修改名称/描述        200
  `POST`    `/api/v1/projects/:id/archive`   归档项目             200
  `POST`    `/api/v1/projects/:id/restore`   恢复项目             200

本阶段不提供项目物理删除 API。

### 6.2 创建项目

`POST /api/v1/projects`

请求：

``` json
{
  "name": "Go-Server 源码研读",
  "description": "学习 Go 微服务架构与项目实践"
}
```

规则：

-   `name` 必填，去除首尾空白后不得为空，长度不超过 128 个 Unicode
    字符。
-   `description` 可选。
-   `user_id` 由 JWT 上下文提供。
-   初始状态为 `active`。
-   同一用户同名项目返回 `409`；不同用户可以创建同名项目。
-   成功返回 `201 Created`，响应包含
    `id`、`name`、`description`、`status`、`created_at`、`updated_at`。

### 6.3 项目列表

`GET /api/v1/projects?page=1&page_size=20&status=active`

-   `status` 可选，只允许 `active` 或 `archived`。
-   只返回当前 JWT 用户的项目。
-   返回 `items`、`page`、`page_size`、`total`。

### 6.4 项目详情与编辑

-   `GET /api/v1/projects/:id`：只能查询当前用户拥有的项目。
-   `PATCH /api/v1/projects/:id`：只允许修改 `name`、`description`。
-   修改名称时仍需遵守同一用户名称唯一约束。
-   不允许通过普通编辑接口直接修改 `user_id`、`id` 或内部状态字段。

### 6.5 归档与恢复

-   `POST /api/v1/projects/:id/archive`：`active → archived`。
-   `POST /api/v1/projects/:id/restore`：`archived → active`。
-   归档不删除项目、会话或任务。
-   归档项目仍可读取。
-   归档项目不允许创建新会话或新任务，也不允许修改现有任务；用户恢复项目后才可继续操作。
-   由于 `(user_id, name)` 唯一约束仍生效，归档项目名称仍被保留。

------------------------------------------------------------------------

## 7. Conversation 与 Project 关联 API

### 7.1 创建会话时关联项目

`POST /api/v1/conversations`

请求示例：

``` json
{
  "title": "研究 SendMessage 的事务边界",
  "project_id": 12
}
```

-   `project_id` 可选，省略时创建未关联项目的会话，以兼容既有客户端。
-   如果传入 `project_id`，必须验证项目属于当前用户且状态为 `active`。
-   `user_id` 仍然来自 JWT。

### 7.2 修改会话项目关联

`PATCH /api/v1/conversations/:id/project`

关联项目：

``` json
{
  "project_id": 12
}
```

解除关联：

``` json
{
  "project_id": null
}
```

-   会话必须属于当前用户。
-   目标项目必须属于当前用户且处于 `active`。
-   必须区分字段缺失与显式 `null`：字段缺失应返回
    `400`，不能被解释为解除关联。
-   数据库复合外键作为最终一致性保护，但 Service
    仍须执行清晰的业务校验。

------------------------------------------------------------------------

## 8. Task API 契约

### 8.1 接口清单

  方法      路径                           功能
  --------- ------------------------------ -------------------
  `POST`    `/api/v1/projects/:id/tasks`   在项目下创建任务
  `GET`     `/api/v1/projects/:id/tasks`   分页查询项目任务
  `GET`     `/api/v1/tasks/:id`            查询任务详情
  `PATCH`   `/api/v1/tasks/:id`            修改任务标题/描述
  `PATCH`   `/api/v1/tasks/:id/status`     更新任务状态

### 8.2 创建任务

`POST /api/v1/projects/:id/tasks`

请求：

``` json
{
  "title": "为 SendMessage 补充事务回滚测试",
  "description": "验证消息插入失败时事务不会部分提交",
  "source_message_id": 58
}
```

规则：

-   `project_id` 从路由参数读取。
-   `user_id` 从 JWT 上下文读取。
-   `title` 必填，去除首尾空白后不得为空，最多 200 个 Unicode 字符。
-   `description` 可选。
-   `source_message_id` 可选。
-   项目必须属于当前用户且处于 `active` 状态。
-   来源消息存在时，必须验证其会话属于当前用户且关联到该项目。
-   初始状态为 `todo`，`version = 1`，`completed_at = NULL`。
-   成功返回 `201 Created`。

### 8.3 任务列表

`GET /api/v1/projects/:id/tasks?page=1&page_size=20&status=doing`

-   项目必须属于当前用户。
-   `status` 可选，只允许 `todo`、`doing`、`done`。
-   结果稳定排序并返回分页元数据。

### 8.4 任务详情与内容编辑

-   `GET /api/v1/tasks/:id`：只能读取当前用户拥有的任务。
-   `PATCH /api/v1/tasks/:id`：只允许修改 `title`、`description`。
-   更新必须带上客户端读取时的 `version`。
-   更新时使用 `WHERE id = ? AND user_id = ? AND version = ?`
    等价条件进行原子乐观锁校验。
-   成功更新后 `version = version + 1`；版本不匹配返回
    `409 VERSION_CONFLICT`。

### 8.5 状态更新

`PATCH /api/v1/tasks/:id/status`

请求：

``` json
{
  "status": "doing",
  "version": 1
}
```

-   `status` 必填且只能是 `todo`、`doing`、`done`。
-   `version` 必须是客户端读取到的当前版本。
-   先验证任务归属、项目状态、转换合法性和版本。
-   更新应是原子操作，防止并发请求绕过状态机。
-   进入 `done` 时设置 `completed_at`。
-   P0 将 `done` 定义为终态，不允许重新打开。
-   从 `todo` 进入 `doing` 或从 `doing` 退回 `todo` 时，`completed_at`
    应为 `NULL`。
-   同状态更新不作为有效状态转换，返回 `409 INVALID_STATE_TRANSITION`。

------------------------------------------------------------------------

## 9. Task 状态机

### 9.1 状态定义

  状态      含义
  --------- -----------------
  `todo`    待处理
  `doing`   进行中
  `done`    已完成，P0 终态

### 9.2 合法转换

``` text
todo  ─────→ doing ─────→ done
               │
               └──────→ todo
```

  当前状态   目标状态         是否允许
  ---------- ---------------- ----------------------
  `todo`     `doing`          允许
  `doing`    `todo`           允许
  `doing`    `done`           允许
  `todo`     `done`           拒绝
  `done`     `todo`           拒绝
  `done`     `doing`          拒绝
  任意状态   同一状态         拒绝，不视为有效变更
  任意状态   未知状态字符串   拒绝

### 9.3 数据库约束与业务状态机的边界

-   MySQL `CHECK` 只限制状态值集合。
-   Service 负责合法状态转换。
-   Repository 负责原子条件更新和错误分类。
-   `version` 用于识别并发更新。
-   测试必须验证失败转换不会改变 `status`、`version` 和 `completed_at`。

------------------------------------------------------------------------

## 10. 反例与自动化测试矩阵

至少覆盖以下场景。表中"预期"是接口契约；实现完成后必须以自动化测试验证，而不能仅以设计存在作为通过证据。

  -------------------------------------------------------------------------------------------------------------
                     \# 场景                         预期结果                  重点验证
  --------------------- ---------------------------- ------------------------- --------------------------------
                      1 未携带 JWT 创建项目          401                       请求在认证层被拒绝

                      2 用户 A 查询用户 B 的项目     403（沿用当前项目策略）   不泄露或返回他人项目数据

                      3 用户 A 重复创建同名项目      409                       唯一约束与错误映射

                      4 用户 A、B 创建同名项目       均成功                    唯一约束包含 `user_id`

                      5 在归档项目下创建任务         409                       Service 检查项目状态

                      6 创建任务时提交               400                       输入校验和数据库 CHECK
                        `status=finished`                                      

                      7 `todo → done`                409                       非法状态转换被拒绝

                      8 `done → doing`               409                       已完成任务是终态

                      9 使用旧 `version` 更新任务    409                       乐观锁冲突

                     10 关联其他用户的来源消息       拒绝请求                  来源消息、会话、项目的归属校验

                     11 将会话关联到其他用户的项目   拒绝请求                  项目归属校验和复合外键

                     12 `page_size=101`              400                       分页边界校验
  -------------------------------------------------------------------------------------------------------------

每个失败场景应尽可能检查数据库后置状态，确认没有产生非法写入或部分更新。并发冲突测试应覆盖两个请求读取同一版本、只有一个更新成功的情况。

------------------------------------------------------------------------

## 11. 测试与验收标准

### 11.1 Repository 层

-   `CreateProject`：正常创建、同用户重名、不同用户同名。
-   `GetProject`：存在、不存在、用户隔离。
-   `ListProjects`：空列表、第一页、后续页、最后一页、空页、状态筛选、用户隔离。
-   Task Repository：创建、查询、状态条件更新、版本冲突、事务一致性。
-   数据库测试必须连接测试数据库，不允许意外写入开发库 `go_db`。

### 11.2 Service 层

-   项目名称与任务标题校验。
-   项目归属和归档状态校验。
-   来源消息归属校验。
-   状态转换规则。
-   `completed_at` 的正确维护。
-   版本冲突和数据库错误传播。

### 11.3 Handler 层

-   JWT 缺失或无效返回 401。
-   非法 JSON、非法参数返回 400。
-   正常创建返回 201。
-   重名、非法状态转换和版本冲突返回 409。
-   越权访问返回与项目约定一致的错误码。
-   响应 JSON 结构保持一致。

### 11.4 全量验收

-   `go test ./... -count=1` 全部通过。
-   Migration 在空测试库和已有旧数据的测试库中均能按计划执行。
-   up/down 顺序经过验证；down 执行前确认会删除哪些数据。
-   旧的用户、会话、消息功能没有回归。
-   旧会话允许 `project_id = NULL`。
-   API 契约和状态机规则写入仓库文档。
-   可以在 3 分钟内说明从一次代码提问到任务复盘的业务闭环，以及
    ProjectService 和 TaskService 的职责边界。

------------------------------------------------------------------------

## 12. 当前实现状态与后续计划

### 12.1 已完成（截至 2026-10-19 的开发记录）

-   本地创建了 `000004`--`000006` 的 up/down SQL 文件。
-   在 MySQL 中验证了：
    -   跨用户项目关联被复合外键拒绝；
    -   同一用户创建重名项目被唯一约束拒绝；
    -   非法任务状态被 CHECK 约束拒绝。
-   已添加 `internal/model/Project.go`。
-   已实现 `repository.CreateProject`，并测试同用户重名与不同用户同名。
-   已实现 `ProjectService.CreateProject` 的基本输入校验。
-   已实现 `POST /api/v1/projects` Handler 与路由。
-   `TestCreateProjectAPI` 的 7 个集成测试通过：缺少 JWT、非法
    JSON、空白名称、正常创建、重复名称、不同用户同名、伪造 `user_id`。
-   已运行 `go test ./...`，当时结果全部通过。
-   `go_db` 的当前备份文件为
    `go_db_after_projects_20261019.sql`；该文件是迁移后备份。

### 12.2 尚待完成

-   `ProjectRepository.GetProject` 与 `ListProjects`。
-   Project 列表、详情、编辑、归档和恢复 API。
-   Conversation 与 Project 的显式关联 API。
-   Task Model/Repository/Service/Handler 与路由。
-   Task 状态机、乐观锁和完成时间的自动化测试。
-   12 个反例全部转化为自动化测试。
-   Migration 的完整 up/down 循环与迁移版本管理机制。
-   将当前本地新增文件纳入 Git 并提交、推送。
-   3 分钟业务闭环口头验收。

### 12.3 建议实施顺序

1.  完成 Migration 审核与版本管理方案；
2.  完成 Project Repository 的 Create/Get/List；
3.  完成 Project Service 和列表/详情 API；
4.  完成项目修改、归档、恢复；
5.  完成 Conversation 项目关联；
6.  完成 Task Model、Repository、Service；
7.  实现状态机与乐观锁；
8.  完成 Handler、路由和反例测试；
9.  执行全量回归并准备业务演示。

------------------------------------------------------------------------

## 13. 设计决策记录

-   **项目不物理删除：** P0 使用归档与恢复，减少误删风险。
-   **历史会话可不关联项目：** `project_id` 可空，兼容既有数据。
-   **用户身份来自 JWT：** 不接受请求体中的 `user_id` 作为授权依据。
-   **数据库约束与 Service 双重防护：**
    数据库约束保护数据一致性，Service 负责业务语义。
-   **任务使用乐观锁：** 避免并发更新时静默覆盖。
-   **`done` 是 P0 终态：** 不在第一版引入 reopen 流程。
-   **Repository 不承担业务规则：** 保持数据访问与业务编排的职责边界。
-   **版本化 SQL 是 Schema 的事实来源：** 不使用 GORM `AutoMigrate`
    替代现有迁移文件。
