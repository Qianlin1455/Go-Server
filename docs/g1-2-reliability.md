# G1.2 可靠性验证

## 部署

在 Kafka 上预建 `imports.dlq`，并给 Consumer 账号授予写入权限。原消费 Topic 为 `imports.requested`。本地单 Broker 可使用如下命令，集群按实际副本策略配置。

```powershell
docker exec go-kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --create --if-not-exists --topic imports.dlq --partitions 1 --replication-factor 1
```

Worker 接收 SIGINT 和 SIGTERM。`WORKER_SHUTDOWN_GRACE` 默认 `15s`，接受 Go duration。信号取消扫描和领取，已领取的任务可以在宽限时间及原有任务时限内继续执行。任务 Context 超时仍按原有错误分类处理。若排空期限先到，Execution Context 取消，尚未完成的任务留给租约恢复。结果写入使用独立的短时 Finish Context，受排空期限控制。生产执行器和数据库驱动需要响应 Context 取消。

Consumer 主动退出时取消 Kafka Session，等待 Sarama 释放 Session，再关闭 DLQ Producer 和数据库连接池。Rebalance 也会取消 Session，处理中断后由租约恢复接管。Consumer 沿用 eager Range 分配策略。取消后保留原 Offset，下一次投递重新读取 MySQL。如果 DB 已经写入终态，重投只确认 Offset。已开始的同步 Kafka 调用受 Sarama 网络超时和内部重试限制，可能延长退出。

## 消费和死信

成功或业务失败以 MySQL 持久化终态为准。格式、字段、版本、类型、已确认不存在的任务和事件任务不匹配可进入死信。连接失败、SQL 查询错误及 DLQ 发送失败会结束当前 Claim，原 Offset 留待重投。

DLQ Envelope v1 包含 `schema_version`、`message_id`、`original_topic`、`original_partition`、`original_offset`、`original_key`、`original_value`、可选 `event_id`、`failure_code`、`failure_message` 和 UTC `failed_at`。JSON 中原始字节使用 Base64，空引用编码为 `null`。稳定的 `message_id` 由原 Topic、Partition 和 Offset 构成，同时用作 Kafka Key。

同步 Producer 等待 ISR 确认。得到 ACK 后，仍有效的 Session 才调用 `MarkMessage` 和 `Commit`。Sarama 的 `Commit` 接口不返回错误，Kafka 提交失败时依靠原消息重投。DLQ 至少一次投递，ACK 与 Offset 提交之间崩溃可能重复发信，下游可用 `message_id` 去重。DLQ 包含原始负载，需设置相应的 ACL、保留期和消息体大小上限。

## 可重复故障注入

1. 创建专用 `go_server_test`，应用仓库当前所有 up migrations，设置 `TEST_MYSQL_DSN` 指向该库并启用 `parseTime=true`。在测试环境运行全仓测试时，将 `MYSQL_DSN` 也指向该专用库。
2. 从仓库根目录运行下面的定向验证。子进程用真实 MySQL 领取任务，父进程确认 `running` 后强制终止子进程，并等待数据库租约自然到期。Windows 使用强制进程终止，Linux 使用 SIGKILL，并附加 SIGTERM 停止领取的验证。HTTP 用例使用本地 TLS 测试服务和生产 GitHub 客户端，确认尚未到期不能领取后再将数据库调度时间推进，缩短测试运行时间。

```powershell
go test -p 1 ./internal/worker -run 'TestRunner_(DrainOnShutdown|ForcedProcessExitRecovery)$' -count=1 -v
go test -p 1 ./internal/consumer -run 'TestHandler_(GitHubHTTPFaults|DeadLetterBeforeOffset|NoDeadLetterWhenMySQLUnavailable|RebalanceCancelsWorkWithoutOffset|DuplicateEvent10Times_OneResult)$' -count=1 -v
```

3. 在独立 Kafka Broker 上运行下面的用例。该用例创建需要的 Topic，向 `imports.requested` 发送坏消息，从 `imports.dlq` 读取原始内容，再通过 Broker 查询消费组的已提交 Offset。每次运行使用空 Topic，只在一次性环境中设置该变量。

```powershell
$env:TEST_KAFKA_BROKER = '127.0.0.1:19094'
go test -p 1 ./internal/consumer -run '^TestConsumer_RealKafkaDeadLetter$' -count=1 -v
```

4. 手动观察生产 Worker 的退出时，可在专用环境中先启动 `WORKER_MODE=revision-check` 的 Worker，再创建两条导入任务。看到第一条被领取后向 Worker 发送 SIGTERM（Linux 用 `kill -TERM <pid>`，Windows 控制台用 Ctrl+C）。观察第一条的结果或租约状态，以及第二条的 `attempt_count`。另开一次实验，在看到 `running` 后用 Linux 的 `kill -KILL <pid>` 或 PowerShell 的 `Stop-Process -Id <pid> -Force`，待 `lease_expires_at` 过去，再启动另一 Worker 验证回收。
