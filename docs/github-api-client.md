# GitHub API Client 运维与设计

## 认证

服务进程启动前注入 `GITHUB_TOKEN`，`NewClient()` 在构造时读取。更新 Token 后需重启进程。未设置或空值使用匿名请求；非空值统一发送 `Authorization: Bearer ...`，401 映射为 `ErrForbidden`，不会改用匿名重试。不要在命令历史、源码、数据库、日志、截图或提交中保存真实 Token。

PowerShell 7 可交互输入（不会显示输入内容）：

```powershell
$env:GITHUB_TOKEN = Read-Host 'GitHub Token' -MaskInput
go run ./cmd/server
Remove-Item Env:GITHUB_TOKEN
```

生产环境通过部署平台的 secret/environment 注入，容器需要显式传入该环境变量。应用不自动加载 `.env`。只读公开仓库不应申请任何写权限或全库 `repo` 权限；fine-grained PAT 使用公开仓库只读访问范围，classic PAT 可使用无 scope 的公开数据访问。此变量是服务端 PAT 配置，与 GitHub Actions 自动发放的同名 Token 不同。令牌过期/权限错误必须修复配置；缓存命中期间不会主动校验令牌有效性。

## 缓存

`NewClient()` 保持兼容；`NewClientWithCache(Cache)` 可注入现有 `RedisCache` 的 Get/Set 字节接口。server 使用共享 Redis client 创建独立 TTL 的缓存包装，不改变 Project Cache。

实际 key：`go-server:v1:github:v1:repository:<lower-owner>/<lower-repo>`。JSON 快照包含 RepositoryInfo（owner、repo、default branch、最新 commit SHA）、公开性标记、绝对过期时间。只有元数据公开性、默认分支、git ref 类型和 SHA 验证全部成功后才写入。读缓存再次校验身份、公开标记、分支、SHA、过期时间和大小；损坏缓存当作 miss。

逻辑 TTL 为 30 秒：分支头更新较快，短 TTL 同时约束 SHA、默认分支和公开状态的新鲜度。Redis 原有 0–10% 抖动使物理 TTL 为 30–33 秒，但快照绝对过期时间限制可用期为 30 秒。没有负缓存、错误缓存或过期数据兜底。Redis Get/Set 出错不影响 GitHub 查询结果，单次 Redis 操作上限 100ms。

singleflight 在单 Client 内按归一化仓库 key 合并查询，二次检查缓存，返回独立结果副本。等待者可独立取消，共享查询使用 11 秒总期限且保留每次 HTTP 5 秒超时。`GetCommitSHA` 保留每次重新检查公开性的行为，不使用绑定快照缓存。

## 限流、重试和错误映射

`RateLimitError` 实现 Unwrap，兼容 `errors.Is(err, ErrRateLimited)`；可用 errors.As 读取 Kind、Limit、Remaining、Reset、Resource、RetryAt。缺失或非法 quota 数值为 -1。Reset 使用 Unix 秒，Retry-After 支持秒数和 HTTP 日期。Resource 仅保留已知值。

403/429 且 remaining=0 识别为 primary；429、403 带 Retry-After 或有明确 secondary/abuse 消息识别为 secondary。只在必要时读取有大小上限的消息做分类，原始消息不向上返回。重试时间取有效 Retry-After 与 primary reset 的较晚值；没有可用未来时间则建议等待 60 秒。限流请求不自动重试。

只有 HTTP 502/503/504 且没有 Retry-After 时，等待 100ms 后最多重试一次；网络错误、超时、认证错误不重试。退避响应 context 取消。上游错误不会触发本地数据修改。

| 错误 | HTTP / code |
|---|---|
| ErrRateLimited | 503 / GITHUB_RATE_LIMITED，加 Retry-After 秒数 |
| ErrTimeout | 504 / GITHUB_TIMEOUT |
| ErrNotFound、ErrPrivate | 404 / REPOSITORY_NOT_FOUND |
| ErrForbidden（含上游 401）、ErrUpstream、重定向/超大/无效响应 | 502 / GITHUB_UPSTREAM_ERROR |
| ErrInvalidInput | 400 / INVALID_INPUT |

固定 GitHub HTTPS origin、禁止代理/重定向、响应体大小限制和 owner/repo 白名单保持。错误与日志不包含原始传输错误、上游正文或 Token。

## 测试与限制

新增测试使用 httptest 和内存缓存替身，不消耗 GitHub 配额。覆盖 Token/匿名/错误认证、脱敏、限流分类与时间、重试次数、缓存命中/隔离/到期/污染/故障回退/并发合并和 Handler 响应头。原有 Redis/MySQL 集成测试需要 TEST_REDIS_ADDR、TEST_MYSQL_DSN 和 MYSQL_DSN（两者均指向专用 go_server_test 数据库）。

缓存存在最多 30 秒公开性及版本变化窗口；Redis 是可信服务端基础设施，快照结构验证不是密码学防篡改。singleflight 仅进程内生效；多实例共用 Token 仍共用额度，没有分布式限流预算、全局冷却或熔断。本次不缓存额外元数据字段，不更改导入、outbox 或 Kafka。

后续可将凭据获取抽象为 provider，引入 GitHub App 安装级短期令牌和自动刷新；按安装身份隔离配额与缓存命名空间，再加入分布式预算/冷却及配额指标。继续保持公开仓库限制，除非另行设计用户授权体系。

参考：[GitHub REST 限流说明](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api)。
