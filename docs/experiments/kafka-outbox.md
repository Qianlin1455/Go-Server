# Kafka Outbox 导入链路实测

实测环境为 Windows、PowerShell 7、Docker Compose v5.5.1、Apache Kafka 4.4.0、Redis 7 Alpine 和 MySQL 8.0。实测仓库为 `octocat/Spoon-Knife`，导入 Revision 为 `d0dd1f61b33d64e29d8bc1372a94ef6a2fee76a9`。

## 拓扑

```text
PowerShell E2E --> Gin HTTP API (:8080) --> MySQL (独立 go_server_e2e)
                        |                import_jobs + outbox_events
                        +--> Redis (:6379)         |
                        +--> GitHub API            v
                                                Publisher
                                                   |
                                                   v
                                          Kafka (:9092)
                                          imports.requested
                                                   |
                                                   v
                                     Consumer 内的 Worker Runner
                                         |                 |
                                         v                 v
                                    GitHub API        MySQL
                                               import_jobs / 源码快照
```

## 正常时序

1. API 校验 JWT 和 `Idempotency-Key`，在同一 MySQL 事务中写入 `queued` 的任务和 `pending` 的 Outbox 事件。HTTP 返回 `202`。
2. Publisher 扫描 `pending` 记录，发送至 `imports.requested`，收到 Kafka 确认后将 Outbox 标记为 `sent`。
3. Consumer 收到事件后，Worker Runner 领取该任务并更新为 `running`，在该租约下从 GitHub 下载固定 Revision 的源码。
4. Worker 持久化源码快照和任务结果。成功终态在数据库中是 `succeeded`。Consumer 观察到持久化终态后才确认消息。
5. 正常链路脚本使用任务所有者的 JWT 轮询 `GET /api/v1/imports/:id`，并调用 `GET /api/v1/projects/:id/source-files` 校验 Job ID、Revision 和文件。故障注入脚本继续用隔离 MySQL 验证 Outbox 和快照行数。

## 运行方法

在 Windows PowerShell 7 中使用以下命令。需要 Docker、Go、带 MySQL 驱动的 `migrate` CLI，以及可访问 GitHub 的网络。

```powershell
./scripts/e2e/01-up.ps1
./scripts/e2e/02-happy.ps1
./scripts/e2e/99-down.ps1

./scripts/e2e/01-up.ps1
./scripts/e2e/fault-01-worker-kill.ps1
./scripts/e2e/fault-02-kafka-down.ps1
./scripts/e2e/fault-03-duplicate.ps1
./scripts/e2e/fault-04-duplicate-event.ps1
./scripts/e2e/99-down.ps1
```

四个故障脚本可以复用已启动的 E2E 环境，也可以独立启动并关闭各自的环境。每个脚本创建自己的用户、项目、绑定和幂等键。故障测试通过 `finally` 尝试恢复共享环境中停止的 Consumer 或 Kafka。脚本使用 Compose 声明的服务名和宿主端口，运行期间会占用这些服务。每轮生成独立 MySQL 容器、Redis/Kafka 卷和 Kafka 消费组。关闭时删除本轮容器及本轮 Compose 卷，应用进程日志留在启动时打印的系统临时目录。

## Compose 验收

| 命令 | 实测 |
| --- | --- |
| `docker compose -f compose.yml config -q` | 退出码 `0` |
| `docker compose -f compose.yml up -d`，随后执行 `docker compose ps` | 修正首次启动的数据卷权限后，`go-server-redis`、`go-kafka-compose` 均为 `Up (healthy)` |
| `docker exec go-kafka-compose /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --list` | 退出码 `0`，此时还没有导入主题，标准输出为空 |

## 正常链路耗时

2026-11-16 GitHub 匿名额度恢复后，使用公开仓库 `octocat/Spoon-Knife` 完成 `01-up → 02-happy → 99-down`。迁移版本为 `17`，服务的 `GET /healthz` 返回 `200`。以下时间为 UTC+08:00。任务状态和耗时取自 HTTP 查询与脚本计时，任务 API 未暴露 Outbox 状态，故此列用 `-` 表示。

| 阶段 | 时间戳 | job status | outbox status | 备注 |
| --- | --- | --- | --- | --- |
| register | 2026-11-16T15:56:20.4458093+08:00 | - | - | HTTP 201，user=1 |
| login | 2026-11-16T15:56:20.6002256+08:00 | - | - | HTTP 200，收到 JWT |
| create project | 2026-11-16T15:56:20.6262471+08:00 | - | - | HTTP 201，project=1 |
| bind repository | 2026-11-16T15:56:22.1306422+08:00 | - | - | HTTP 201，`octocat/Spoon-Knife`，repository=1 |
| create import | 2026-11-16T15:56:22.1847324+08:00 | queued | - | HTTP 202，job=1 |
| GET import probe | 2026-11-16T15:56:22.2046508+08:00 | queued | - | HTTP 200，attempts=0 |
| GET import poll | 2026-11-16T15:56:22.7224024+08:00 | running | - | HTTP 200，attempts=1 |
| GET import poll | 2026-11-16T15:56:26.8315347+08:00 | succeeded | - | HTTP 200，attempts=1 |
| import terminal | 2026-11-16T15:56:26.8580193+08:00 | succeeded | - | API 时间戳差 4,493 ms，创建请求至首次观测终态 4,727 ms |
| GET source-files | 2026-11-16T15:56:26.8926812+08:00 | succeeded | - | HTTP 200，共 3 个文件，首个为 `README.md`，场景总耗时 6,656 ms |

关停后三个受管进程均已退出，Compose 服务和本轮 MySQL 容器均已移除。

## 故障注入

各场景从独立环境开始，表中 Job ID 只在该场景内有意义。`Outbox rows` 来自数据库行数和事件 ID 的校验，Kafka 物理投递次数不在此项的观测范围内。

| 场景 | 预期 | 实际 | 结果 |
| --- | --- | --- | --- |
| 强制终止 Consumer 内的 Worker | 租约保留到过期，新 Consumer 重新领取并完成，Outbox 保留一条 | PID 12672 被强制结束，任务仍为 `running`。租约到期 17 ms 后重新领取，强制退出到重新领取 59,034 ms，退出到观测成功 62,609 ms。尝试次数 1→2，Outbox 为 `sent`、1 行且 event ID 相同 | PASS |
| 停 Kafka 后创建 3 个任务 | API 返回 202，Outbox 保留 `pending`，Kafka 恢复后自动补发 | HTTP 202×3。三条记录均为 `queued/pending`，各记录 2 次发布失败，最后错误是 `circuit breaker is open`。从请求重启 Kafka 到 `sent` 分别为 10,295 / 10,505 / 10,512 ms。三个任务均为 `succeeded`，各有一条 Outbox | PASS |
| 相同幂等键并发 10 次 | 新建 1 个 Job，其余返回同一 Job 或 409 | HTTP 202×1、200×9，10 个响应均为 `SUCCESS` 且指向 Job 1，请求批次耗时 49 ms。MySQL 中只有 1 个 Job、1 条 `sent` Outbox，执行 1 次，最终 `succeeded` | PASS |
| 已成功事件原样重投 Kafka | Consumer 跳过同一 event ID，任务、领取次数和快照均不增加 | 重投 277 字节 Outbox JSON 后，Consumer 记录新收件及 `already succeeded, skip duplicate event`。Job 1→1，attempts 1→1，文件 1→1，Span 1→1，领取及源码下载日志均 1→1。夹具共收到 6 次 HTTP 请求，其中 blob 下载 1 次 | PASS（本地 GitHub 夹具） |
| GitHub 匿名限流 | 任务持久化为 `queued` 并记录 `GITHUB_RATE_LIMITED`，额度恢复且到达 `available_at` 后可继续领取 | 2026-11-16 15:30:34（UTC+08:00）Consumer 记录 `job=1 retryable failure recorded: code=GITHUB_RATE_LIMITED`。查询时该行仍在，`status=queued`、`attempt_count=1`，`available_at=2026-11-16 07:56:14.880 UTC`。该隔离环境在额度恢复前清理，额度恢复后另起环境的新任务导入成功 | 保留及调度已实测，原任务复领未观测 |

第四项的真实 GitHub 试跑达到匿名限流，`X-RateLimit-Remaining: 0`。前序任务记录 `GITHUB_RATE_LIMITED` 并保留在 `queued`，后续任务在 150 秒内未进入终态，因此该轮未到达重投步骤。复跑通过临时构建 overlay 将 GitHub HTTP transport 指到本地响应夹具，实际运行 MySQL、Kafka、Publisher、Consumer 和 Worker。仓库中的 GitHub 客户端与 Worker 源码保持原样。

## 我踩的坑

1. **Kafka 首次格式化数据卷时进程不断重启。** Docker 日志显示 `uid=1000(appuser)` 和 `java.nio.file.AccessDeniedException: /tmp/kraft-combined-logs/__cluster_metadata-0`。启动脚本在识别到该日志时停止 Kafka，仅调整该挂载目录的属主为 `1000:1000`，然后重新启动。正常 Kafka 容器仍以镜像默认用户运行。
2. **Windows 绝对路径被迁移工具解析成 URL。** 首次执行 `migrate up` 返回 `invalid port ... after host`，路径原样出现在 `file://C:\...` 中。脚本切到仓库根目录后传入相对目录 `-path migrations`，随后迁移版本为 `17`。
3. **首次实验时导入接口只有创建路由。** `GET /api/v1/projects/1/imports/1` 实际返回 HTTP `404`。该轮脚本从测试 MySQL 读取状态，并通过源码 API 验证产物。当日已新增 `GET /api/v1/imports/:id`，需任务所有者的 JWT。
4. **只重建 MySQL 会重放 Kafka 旧事件。** 使用原 Kafka 卷配合新 MySQL 和新消费组时，Consumer 日志先收到上一轮的 `job_id=1`，随后记录 `dead letter acknowledged ... code=IMPORT_JOB_NOT_FOUND`。每次实验使用独立 Kafka 卷，使历史消息与新数据库隔离。

## 代价与取舍

1. **为什么租约是 60 秒？** Worker 单次执行预算为 30 秒，执行后另有最多 3 秒写回。60 秒给下载、调度和数据库写回留出余量。固定租约缩得过短时，仍在执行的任务可能被回收并被另一 Worker 再次调用 GitHub。Lease token 仍会挡住旧 Worker 的过期写回，但重复计算和请求已经发生。若要缩短接管等待，同时保留当前执行预算，需要设计基于 lease token 的心跳续租。
2. **任务可能不可用多久？** 单次崩溃若发生在刚领取时，任务可能要等将近 60 秒才有资格被重新领取。实际还要计入 Kafka 检测掉线与再均衡、DB 恢复检查以及新一轮执行。实测从强制退出到重新领取用了 59.034 秒，到观测成功为 62.609 秒。默认最多 3 次领取，连续崩溃可用掉约 180 秒的租约窗口。Kafka 或 MySQL 长时间不可用、队列积压以及 GitHub 的重试时间都可能继续拉长延迟，因此当前没有端到端最坏时间上限。这段等待对秒级交互、同步发布门禁和严格截止时间的任务不可接受。
3. **DLQ 谁来处理？** 当前 Consumer 将永久消息错误发到 `imports.dlq`，收到 Broker 确认后才标记并提交原 Offset；DLQ 发布失败则保留原消息待重投。仓库当前没有 DLQ 消费程序、自动重放或已指定的值班负责人。有 Kafka 权限的项目维护者需要人工检查 `failure_code` 和原 Topic/Partition/Offset，用 `message_id` 去重，修复数据或生产端后决定是否新建任务或受控重投。

## 运行与演示注意

- 匿名 GitHub API 额度为每小时 60 次。一次导入会请求仓库元数据、提交、目录树以及所需的各个 blob。
- 演示前在启动 `01-up.ps1` 的 PowerShell 中设置 `GITHUB_TOKEN`，例如输入 `$env:GITHUB_TOKEN = Read-Host 'GitHub Token' -MaskInput`。GitHub 客户端在 `internal/githubclient/client.go` 的 `NewClient` 构造时读取该变量，更新后需重启相关进程。
- 正式演示前先跑一次完整导入预热，确认任务查询返回 `200` 且状态达到 `succeeded`，再通过源码 API 核对快照。
