# MySQL 执行计划、索引取舍、连接池与超时回滚证据

> 项目：Go-Server  
> 实验日期：2026-10-23  
> 证据状态：已完成 SQL 执行计划对照、连接池基线、HTTP 超时与事务回滚验证；正式索引变更待决策。  
> 原则：只记录本次实际观测；单次 `EXPLAIN ANALYZE` 不当作稳定性能基准，不声称确定的性能提升倍数。

## 1. 环境与隔离

| 场景 | 数据库 | 用途 |
| --- | --- | --- |
| 消息分页 SQL 的小数据验证 | `go_db` | 开发库，仅读取现有业务数据 |
| 任务分页索引对照 | `go_server_perf` | 独立性能实验库，包含合成任务数据与候选索引 |
| `cmd/poolbaseline` 连接池基线 | `go_db` | 本地 MySQL 实例上的 `SELECT SLEEP(0.02)`；经 `SELECT DATABASE()` 确认 |
| 事务超时回滚集成测试 | `go_server_test` | 独立测试库；测试代码会校验数据库名称 |

- 系统：Windows 11；MySQL 8，Docker 本地映射端口 `3307`（MySQL 精确小版本未在本次日志中记录）。
- `go_server_perf`：`schema_migrations.version = 9`、`dirty = 0`；已迁移业务表。
- MySQL Server 版本、机器配置、Git Commit SHA：本次尚未记录。可补充 `SELECT VERSION();` 和本地 `git rev-parse HEAD` 输出。
- **敏感信息**：不要在本文档提交密码、完整 DSN、JWT_SECRET、Cookie、Token 或真实用户数据。

## 2. 业务 SQL 与数据规模

### 2.1 任务分页：`repository.ListTasks`

真实查询形态（参数为本次性能测试用户和项目）：

```sql
SELECT tasks.*
FROM tasks
WHERE tasks.user_id = @perf_user_id
  AND tasks.project_id = @perf_project_id
ORDER BY tasks.created_at DESC, tasks.id DESC
LIMIT 20 OFFSET 0;
```

测试库中创建 1 个测试用户、1 个测试项目及 10,000 条任务，任务 `created_at` 随生成序号单调增加；只检验这一数据分布与第一页分页场景。

原有索引：`idx_tasks_project_user_status_id (project_id, user_id, status, id)`。  
候选索引：`idx_tasks_project_user_created_id (project_id, user_id, created_at DESC, id DESC)`。

#### 2.1.1 候选索引创建前（自然选择原索引）

```text
-> Limit: 20 row(s) (cost=570 rows=20)
   (actual time=6.56..6.56 rows=20 loops=1)
    -> Sort: tasks.created_at DESC, tasks.id DESC,
       limit input to 20 row(s) per chunk (cost=570 rows=4969)
       (actual time=6.56..6.56 rows=20 loops=1)
        -> Index lookup on tasks using idx_tasks_project_user_status_id
           (project_id=(@perf_project_id), user_id=(@perf_user_id))
           (cost=570 rows=4969)
           (actual time=0.0369..5.49 rows=10000 loops=1)
```

**观察**：原索引可以使用 `(project_id, user_id)` 等值前缀过滤，却不能直接提供 `created_at DESC, id DESC` 的顺序。下层索引查找向排序节点返回 10,000 行，上层排序取前 20 行。

#### 2.1.2 创建候选索引后（不加索引提示，自然选择）

```text
-> Limit: 20 row(s) (cost=583 rows=20)
   (actual time=0.0179..0.0516 rows=20 loops=1)
    -> Index lookup on tasks using idx_tasks_project_user_created_id
       (project_id=(@perf_project_id), user_id=(@perf_user_id))
       (cost=583 rows=5098)
       (actual time=0.0174..0.0504 rows=20 loops=1)
```

**观察**：优化器主动选择候选索引，独立 `Sort` 节点消失；受 `LIMIT 20` 限制，索引节点只向上层返回 20 行。

#### 2.1.3 `FORCE INDEX` 受控对照（单次）

| 访问路径 | 是否独立 Sort | Index lookup 实际返回行数 | Limit 实际返回行数 | Limit 完成耗时 |
| --- | --- | ---: | ---: | ---: |
| 原索引 `idx_tasks_project_user_status_id` | 是 | 10,000 | 20 | 6.26 ms |
| 候选索引 `idx_tasks_project_user_created_id` | 否 | 20 | 20 | 0.036 ms |

对应输出：

```text
# FORCE INDEX (idx_tasks_project_user_status_id)
-> Limit: 20 row(s) (cost=583 rows=20) (actual time=6.26..6.26 rows=20 loops=1)
    -> Sort: tasks.created_at DESC, tasks.id DESC, limit input to 20 row(s) per chunk
       (cost=583 rows=5098) (actual time=6.26..6.26 rows=20 loops=1)
        -> Index lookup on tasks using idx_tasks_project_user_status_id
           (project_id=(@perf_project_id), user_id=(@perf_user_id))
           (cost=583 rows=5098) (actual time=0.0558..5.15 rows=10000 loops=1)

# FORCE INDEX (idx_tasks_project_user_created_id)
-> Limit: 20 row(s) (cost=1092 rows=20) (actual time=0.0216..0.036 rows=20 loops=1)
    -> Index lookup on tasks using idx_tasks_project_user_created_id
       (project_id=(@perf_project_id), user_id=(@perf_user_id))
       (cost=1092 rows=10197) (actual time=0.0212..0.0349 rows=20 loops=1)
```

**解释与限制**：`cost` 及其 `rows` 为优化器估算，不是耗时或实际扫描量；`actual rows` 是节点每轮向上层输出的行数，不等于精确的物理磁盘 I/O。`actual time` 为执行树节点的计时，父子节点时间不可简单相加。这里的时间都是**单次** `EXPLAIN ANALYZE` 观测，不能用于宣称稳定的性能提升倍数。

**索引决策**：候选索引对 `WHERE project_id=? AND user_id=? ORDER BY created_at DESC, id DESC LIMIT 20` 显示出明显的执行路径优势；**目前只保留在 `go_server_perf` 中，不直接加入正式迁移**。上线前仍应补充重复测量（中位数 / p95）、数据分布变化、深分页、写入成本，以及按 `status` 筛选的其他查询。原索引可能服务不同查询，不因本次实验删除。

### 2.2 消息分页：`repository.ListMessages`

```sql
SELECT messages.*
FROM messages
INNER JOIN conversations
  ON messages.conversation_id = conversations.id
WHERE conversations.user_id = 165
  AND messages.conversation_id = 73
ORDER BY messages.id ASC
LIMIT 20 OFFSET 0;
```

在 `go_db` 验证时：`messages` 共 5 行，`conversations` 共 4 行；会话 73 属于用户 165，匹配 4 条消息。

- 带 JOIN 的 `EXPLAIN ANALYZE`：`Limit` 完成耗时 `0.0303 ms`，下层使用 `idx_messages_conversation_id_id`，输出 4 行；无独立 Sort。
- 不带 JOIN 的对照：`Limit` 完成耗时 `0.0287 ms`，访问路径相同；两次微小耗时差异不证明 JOIN 无成本。
- 传统 `EXPLAIN`：`conversations` 为 `type=const`、`key=PRIMARY`、`rows=1`；`messages` 为 `type=ref`、`key=idx_messages_conversation_id_id`、`rows=4`。主键等值查找会话可作为常量表处理，不代表归属校验失效。
- 把 `conversations.user_id` 改为不匹配的 `0` 后，SQL 返回空结果，验证了本次查询的用户归属隔离条件。
- 早期的一次小数据执行计划曾选择 `messages.PRIMARY` 扫描 5 行并过滤；说明访问路径可受代价估算和数据规模影响，不代表组合索引无效。

**索引决策**：已有 `(conversation_id, id)` 索引能够覆盖过滤与排序的访问路径；当前没有新增索引的证据。5 行数据不足以支持稳定性能评价。

## 3. 连接池基线：`cmd/poolbaseline`

脚本：`go run ./cmd/poolbaseline`。  
负载：每种场景 100 次 `SELECT SLEEP(0.02)`，每次查询 Context 上限 3 秒；客户端通过并发 gate 控制同时进入的查询。  
注意：脚本报告的单次延迟包括**获取数据库连接等待 + SQL 操作**，**不包括进入客户端并发 gate 的排队等待**。`WaitDurationDelta` 是多次连接等待的累计时长，不能和 wall time 直接比较。

### 3.1 第一轮（`go_db`，本地 MySQL）

| MaxOpen | 并发 | failures | wall | QPS | p50 | p95 | WaitCountDelta | WaitDurationDelta | avgWaitPerEvent |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 2 | 1 | 0 | 2.116s | 47.3 | 21ms | 23ms | 0 | 0s | 0ms |
| 2 | 10 | 0 | 1.047s | 95.5 | 83ms | 254ms | 98 | 7.958s | 81ms |
| 2 | 50 | 0 | 1.050s | 95.3 | 316ms | 987ms | 98 | 37.770s | 385ms |
| 10 | 1 | 0 | 2.128s | 47.0 | 21ms | 24ms | 0 | 0s | 0ms |
| 10 | 10 | 0 | 218ms | 459.0 | 21ms | 26ms | 0 | 0s | 0ms |
| 10 | 50 | 0 | 221ms | 452.8 | 85ms | 171ms | 90 | 6.645s | 74ms |

### 3.2 第二轮（`Current Database: go_db`，已明确确认）

| MaxOpen | 并发 | failures | wall | QPS | p50 | p95 | WaitCountDelta | WaitDurationDelta | avgWaitPerEvent |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 2 | 1 | 0 | 2.234s | 44.8 | 21ms | 27ms | 0 | 0s | 0ms |
| 2 | 10 | 0 | 1.059s | 94.4 | 85ms | 273ms | 98 | 8.037s | 82ms |
| 2 | 50 | 0 | 1.054s | 94.8 | 358ms | 887ms | 98 | 37.945s | 387ms |
| 10 | 1 | 0 | 2.113s | 47.3 | 21ms | 22ms | 0 | 0s | 0ms |
| 10 | 10 | 0 | 219ms | 456.0 | 21ms | 26ms | 0 | 0s | 0ms |
| 10 | 50 | 0 | 216ms | 462.3 | 67ms | 193ms | 90 | 6.441s | 72ms |

**观察结论**：

1. MaxOpen=2 时，并发从 10 增至 50，QPS 仍约 95，而 p95 及累计等待上升；连接池等待是主要可见限制。
2. MaxOpen=10、并发 10 时，两轮均未发生连接池等待；吞吐量明显高于 MaxOpen=2 场景。
3. MaxOpen=10、并发 50 仍有等待事件，说明不是无条件消除排队。
4. `Open=2 InUse=0 Idle=2` 是场景结束时的连接池快照；由于 `MaxIdleConns=2`，不能据此认为测试期间最多只创建过两条连接。
5. 脚本模拟固定约 20ms 的数据库操作，不是业务 HTTP 端到端基准；因此不直接推导生产环境应使用的 MaxOpenConns 值，也不以此调优正式实例。

## 4. Context 超时、HTTP 返回与数据库事务原子性

### 4.1 中间件 / DB 取消

命令：

```bash
go test -p 1 ./internal/midware -run '^TestTimeout' -v -count=1
```

本次完整测试组最终 `PASS`，覆盖普通 Timeout、响应边界、父 Context 取消、MySQL 查询取消、事务超时回滚。

`TestTimeoutCancelsMySQL` 观察：

```text
# 原 SQL：SELECT SLEEP(1)；中间件超时配置 150ms
HTTP status: 504
Elapsed: 169.742ms
DB error: context deadline exceeded
Context error: context deadline exceeded
--- PASS: TestTimeoutCancelsMySQL
```

**结论**：HTTP Deadline 经请求 Context 向数据库调用传播，使数据库调用提前返回错误，并按该测试中的响应逻辑映射为 HTTP 504。Context 取消是协作式机制，不能保证预empt 服务端所有工作。

### 4.2 事务超时回滚

命令：

```bash
go test -p 1 ./internal/midware -run '^TestTimeoutRollsBackTransaction$' -v -count=1
```

被测流程：`BEGIN → INSERT users（事务内）→ SELECT SLEEP(2) → Context 300ms Deadline → 事务未提交 → 使用新的 Context 独立查询 users`。

| 观测 | 第一轮 | 第二轮 |
| --- | --- | --- |
| `Transaction elapsed` | 300.2328ms | 318.6677ms |
| `Transaction error` | `context deadline exceeded` | `context deadline exceeded` |
| `Context error` | `context deadline exceeded` | `context deadline exceeded` |
| `Rows remaining after rollback` | **0** | **0** |
| 结果 | PASS | PASS |

**结论**：本次测试验证了事务中已成功执行但尚未提交的用户 INSERT，在后续查询超时后没有持久化；仅检查错误不足以证明回滚，使用新的 Context 验证数据库最终状态是关键证据。

**注意**：之前有一次运行因误连接 `go_db` 被测试内的数据库名保护条件拒绝；切换为 `go_server_test` 后通过。这是环境保护生效，不是回滚失败。

**待排查**：两次测试结束时，清理用的 `DELETE FROM users WHERE username = ...` 尽管 `rows=0`，分别记录约 `1707.760ms`、`1684.919ms`。可能与此前超时数据库请求的服务端结束过程或事务资源释放有关，但现有证据不足以确认根因。建议未来在严格隔离的测试库里采集 `performance_schema`、InnoDB 锁等待及连接状态以验证，不要在无证据时声称具体锁原因。

## 5. 索引取舍与后续动作

| 事项 | 结论 | 动作 |
| --- | --- | --- |
| 项目任务列表 | 候选索引可避免独立 Sort，减少节点输出的无关行数 | 候选索引仅在 `go_server_perf` 保留，暂不进入 migration |
| 会话消息列表 | 已有 `(conversation_id, id)` 满足当前查询 | 暂不新增索引 |
| 连接池 | 小 MaxOpen 限制当前固定等待查询负载的并发吞吐 | 记录 WaitCount/WaitDuration；不以模拟 SQL 直接配置生产环境 |
| Context 超时 | HTTP 504、DB 超时传播均已验证 | 保留回归测试 |
| 事务回滚 | 两轮验证超时后 `users` 残留 0 行 | 保留独立 DB 保护与最终状态断言 |
| 清理 DELETE 耗时 | 两次观察到约 1.7s 慢查询 | 作为待调查问题，暂不臆测根因 |

### 继续补证据（非本次已完成）

- 重复基准：分别在不同项目规模、分布、不同页数及有无 `status` 过滤时，多次测量候选索引与原索引，报告中位数、p95、失败数；避免把 `EXPLAIN ANALYZE` 单次时间当稳定倍数。
- 候选索引成本：通过插入 / 更新 / 删除负载与索引大小验证维护成本，再决定正式 schema migration。
- 复查完整测试组：`go test -p 1 ./... -count=1`；此命令的本次执行结果**尚未提供，不能记为 PASS**。
- 记录环境指纹：`SELECT VERSION();`、本地 `git rev-parse HEAD`、相关 MySQL 配置和实际提交 commit SHA。

---

> 证据解释：上表、执行计划和测试值均来自 2026-10-23 的本地输出记录；尚未执行的命令、正式库上线收益和错误根因都没有被写成既成事实。
