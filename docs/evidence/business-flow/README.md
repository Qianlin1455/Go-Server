# Go-Server v6.1 P0 验收证据（2026-10-23）

已实际执行：真实 MySQL 三层测试、全量测试、独立事务错误注入、编译后启动 `cmd/server` 的真实网络 HTTP 流程，以及开发库只读核对。没有录屏或截图，也没有执行 commit、push、创建 PR 或破坏性数据库操作。

## 业务闭环

注册 → 登录 → Project → 关联 Conversation → Message → 来源 Task → doing → submitted Evidence → done → 查询复盘。

本次开发库样例：user=752、project=318、conversation=280、message=227、task=171。最终状态 `done`，version=3，完成时间非空，证据包含 draft 和 submitted，审计为 todo→doing、doing→done。另一个新用户访问任务复盘及任务列表均返回 404。数据保留在 `go_db`，服务验证完成后已停止；重新演示会新建随机账号和业务数据，不删除历史数据。

新增 `GET /api/v1/tasks/:id/retrospective` 仅聚合事实：`task`、`source`、`evidence`、`audits`。依据 PRD 2.2、8.4 的复盘目的，采用一个最小只读接口，不实现 task_reviews 的人工/AI 评审流程。任务与项目先校验归属；来源 JOIN 同时限定 user/project；证据与审计 JOIN 限定任务所有者；身份只来自 JWT。无来源时 `source=null`，没有证据或审计时返回 `[]`。归档项目仍可复盘。

## 工程与一致性取舍

- 保留已实现的 status 校验与传递、分页默认值、OFFSET 溢出保护，以及 `created_at DESC, id DESC` 排序。
- Count/List 共用 user_id、project_id、status 条件。total 是过滤后全部数量，空页也保留 total。Service 保留原 ListTasks 方法，新增 ListTasksPage 供 HTTP 使用。
- 列表与 Count 是两条独立 SQL，并发写入时不保证同一快照。复盘的多条只读查询同样不提供跨语句快照保证；完成后的静态演示可完整读取事实。今天不增加快照事务或新索引。
- 复盘返回该任务全部证据及审计，P0 未做独立分页；大量历史记录时可后续扩展。
- 原有状态 CAS、submitted 门槛及状态/审计事务保持不变。原有 Project/Conversation/Message 查询和来源校验已审阅；少数仅以 ID 查询 owner 的内部方法由 Service 比较当前用户后才访问数据。

## 复现

在仓库根目录的 PowerShell 中配置自己的合法凭据（示例不是本次真实凭据）：

```powershell
$env:MYSQL_DSN = 'go_user:YOUR_PASSWORD@tcp(127.0.0.1:3307)/go_db?charset=utf8mb4&parseTime=True&loc=Local'
$env:JWT_SECRET = 'REPLACE_WITH_A_LONG_RANDOM_SECRET'
go run ./cmd/server
```

另开 PowerShell，在仓库根目录执行（Python 3，仅标准库）：

```powershell
python -X utf8 docs/evidence/business-flow/business-flow.py http://localhost:8080
```

脚本自动注册随机账号、获取 JWT、断言完整成功链路和负例，输出脱敏请求与响应。密码和 Token 仅在内存使用，不输出。想在 Apifox 手工演示时，使用自己的可登录账号，并按 api-examples.md 的请求体操作；脚本生成的随机密码不会保存。

测试前确认 `go_server_test` 已应用迁移 1～9，设置具有测试库权限的 TEST_MYSQL_DSN，执行：

```powershell
$env:TEST_MYSQL_DSN = 'TEST_USER:YOUR_PASSWORD@tcp(127.0.0.1:3307)/go_server_test?charset=utf8mb4&parseTime=True&loc=Local'
./docs/evidence/business-flow/run-tests.ps1
```

脚本同时把进程内 MYSQL_DSN 临时切到测试库，因为现有中间件测试读取该变量；结束后恢复。不会自动创建数据库、授权或迁移。

## 实际结果与环境说明

- 初次复用开发账号访问测试库失败：MySQL 1044，账号没有测试库权限。随后使用本机既有 Docker 容器管理员配置，仅在测试进程内使用，未修改授权，未保存密码。测试库已存在且结构可用。
- 三层和全量测试 PASS，独立错误注入 PASS，新增 HTTP 集成测试 PASS，见 test-results.txt 和 transaction-rollback.txt。没有把编译结果当作业务通过依据。
- HTTP 实际记录见 http-results.txt；数据库 SELECT 结果见 database-state.txt。注入仅发生于 go_server_test，fixture 在测试后清理。
- 文档示例使用脱敏凭据；api-examples.md 的请求与响应摘自本次真实运行，重放需使用新账号和实际返回的 ID/版本。
- 已知既有非阻塞问题：Project 创建响应的 created_at/updated_at 为零时间（数据库默认时间没有回填到创建 DTO），GET Project 可读取持久化时间；本次保留在真实 HTTP 记录中，未将其包装成正确时间。可后续单独修复，不影响本次任务闭环。
- 本次未执行 Migration up/down 循环、性能基准或 EXPLAIN；没有修改 schema 和索引。没有外部阻塞项。

## 人工录屏步骤（尚未录制）

启动服务，在 Apifox 展示登录及关键接口；依次展示 Task v1/todo、v2/doing、draft 不能完成、submitted 后 v3/done；最后打开复盘响应展示来源、证据和审计。终端展示独立回滚测试及数据库后置断言即可，注意隐藏 Authorization 和本地环境变量。

## 修改文件清单

| 文件 | 修改目的 |
| --- | --- |
| `internal/repository/TaskRepository.go` | CountTasks 与 List 共用过滤条件 |
| `internal/repository/task_retrospective.go` | 带用户归属限制的来源、证据及审计读取 |
| `internal/service/task_service.go` | 新增 ListTasksPage，返回过滤总数 |
| `internal/service/task_retrospective.go` | 最小只读复盘编排与公开 DTO |
| `internal/handler/task_handler.go` | 列表输出 total、复盘 Handler |
| `cmd/server/main.go` | 注册 JWT 保护的复盘路由 |
| `internal/handler/task_handler_test.go` | 测试路由接入复盘 Handler |
| `internal/handler/task_p0_test.go` | 过滤、total、排序、分页、隔离、完成门槛、CAS 及复盘测试 |
| `internal/service/task_service_test.go` | 输出注入失败后的数据库断言状态 |
| `README.md` / `PRD.md` | API 契约、实现状态和设计取舍 |
| `docs/evidence/business-flow/README.md` | 验收、复现和限制说明 |
| `docs/evidence/business-flow/api-examples.md` | 完整真实 HTTP 请求、响应、状态码及 curl 示例 |
| `docs/evidence/business-flow/test-results.txt` | 实际测试命令与输出 |
| `docs/evidence/business-flow/transaction-rollback.txt` | 真实事务错误注入与数据库后置断言 |
| `docs/evidence/business-flow/http-results.txt` | 真实服务 HTTP 演示脱敏记录 |
| `docs/evidence/business-flow/database-state.txt` | 演示数据的只读 SQL 与实际查询结果 |
| `docs/evidence/business-flow/business-flow.py` | 一条命令复现 HTTP 成功链路及负例 |
| `docs/evidence/business-flow/run-tests.ps1` | 测试库保护、运行测试并恢复进程配置 |

执行期间另出现未跟踪的 `compose.yml`，不是本次工具创建或修改，已原样保留，不纳入本次修改清单。
