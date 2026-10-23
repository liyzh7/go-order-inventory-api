# Session 与安全事件运维

## Session 生命周期

登录签发 32 字节随机 Token，数据库只保存 SHA-256，绝对有效期 24 小时。每次认证检查到期、撤销和用户 ACTIVE 状态。退出立即撤销当前 Session，但不追溯中止已经通过认证的在途请求。删除失效 Session 不是其失效的前提；删除后认证仍为 401。

本阶段只提供独立 CLI，没有注册周期任务，不在服务启动时自动清理，也没有对现有开发库运行清理。清理对象仅为认证 Session，不包括用户、订单、库存和业务明细。

## 手动清理

先确认进程中的数据库配置指向正确的本地实例，再执行：

```powershell
. .\scripts\load-env.ps1
go run .\cmd\session-cleanup -retention 168h -batch-size 500 -max-batches 100 -timeout 30s
```

默认保留 7 天、每批 500 条、最多 100 批、总超时 30 秒；批量允许 1～1000，批次数允许 1～10000，保留期和超时必须为正数。使用现有数据库连接和查询超时设置。命令支持 Context 取消、Ctrl+C 和 SIGTERM；退出非零代表失败，不输出原始数据库错误或任意旗标内容。

每次运行固定一个微秒精度 UTC 截止点 `now - retention`，所有批次共用该点：

```sql
DELETE FROM auth_sessions
WHERE expires_at <= ? OR (revoked_at IS NOT NULL AND revoked_at <= ?)
ORDER BY id ASC LIMIT ?;
```

两个时间参数使用同一个截止点。因此活跃 Session 不匹配；未过期但撤销超过保留期的记录可删除；刚失效但在保留期内的记录不删除。条件直接写在 DELETE 中，避免先选 ID、后无条件删除造成并发状态检查失效。

每批独立提交，不持有跨批长事务。一个批次失败即停止，不无限重试；此前提交的批次不会撤销，输出累计已删除数。重复运行安全。最后一个批次恰好达到批量且已到批次数上限时，`limit_reached=true` 表示可能仍有任务，不承诺一定有剩余。总超时不包含 `go run` 的编译时间。

标准输出是一条 JSON 摘要，字段为 `deleted_count`、`batches`、`limit_reached`、`cutoff`、`duration_ms`、`outcome`；标准错误输出安全事件及固定失败分类。CLI 在失败且已有部分批次提交时同样报告实际计数。外部调度可在未来显式配置，本阶段不自动启用。

LIMIT 限制删除数量，不保证扫描数量或单批运行时间。现有 expires_at 索引不能保证 OR/ORDER BY 的最优执行计划，也没有专用 revoked_at 索引。当前使用查询和总超时控制，尚无大数据量性能结论；正式规模需先分析执行计划、锁等待、复制影响，再通过新迁移优化。

## 安全事件日志

HTTP 服务在 stdout 使用标准库 `slog` JSON，清理 CLI 在 stderr 使用相同 JSON logger。每个 HTTP 请求生成独立的 128 位随机请求 ID 并返回 `X-Request-ID`，不采信调用方传入的值。CLI 也生成运行 ID。

共同字段：

| 字段 | 内容 |
| --- | --- |
| `time` / `level` / `msg` | UTC 时间、日志级别、固定消息类别 |
| `request_id` | 服务端生成的请求/运行 ID |
| `event` | 允许列表中的事件类型 |
| `outcome` | success、failure、replayed，或限量标记 suppressed |
| `http_status` | HTTP 状态；CLI 为 0 |
| `user_id` | 当前已认证主体（适用时）；未知为 0 |
| `error_category` | 固定、安全的分类，不是原始 error |

事件包括 `auth.register`、`auth.login`、`auth.logout`、`auth.token`、`order.access_denied`、`order.cancel`、订单/商品数据库失败、`http.panic` 及 `session.cleanup`。不逐条记录每次成功 Token 校验。取消记录首次成功或幂等重放。权限失败只记录当前主体，不输出目标订单 ID、拥有者或其业务信息；404 不通过日志进一步区分不存在与越权。

清理摘要另含 `deleted_count` 和 `duration_ms`。不接受任意属性、事件名或错误文本作为日志载荷，使用允许列表防止不可信内容注入。不会记录明文密码、Token、Token 哈希、Authorization、请求正文、完整 URL/查询参数、用户名或完整 DSN。panic 处理不输出 panic 值、请求转储或堆栈，返回通用错误。业务错误不会被伪装为成功。

所有 HTTP failure 事件共享一个进程级固定窗口：每分钟记录前 30 条，第 31 条输出一条 `security.events_suppressed`，其后静默到新窗口。此预算也会抑制数据库和 panic 等失败，不只抑制无效 Token；成功事件不采样。实现使用固定大小计数器，不创建攻击者可控制的无限 IP/事件映射。清理命令摘要不参与此预算。

这只是开发调试和事件追踪，不是完整企业级审计：没有防篡改、持久化、轮转、集中检索、跨进程限量或完整事件保留保证；缺失事件不能用于证明没有发生访问。正式部署前需独立评估日志访问权限和保留政策，仍不得开放当前回环模拟环境。

## 验证入口

```powershell
go test -count=1 ./internal/platform/securitylog ./internal/httpapi ./internal/sessioncleanup ./cmd/session-cleanup
.\scripts\test-integration.ps1
.\scripts\verify-ci-local.ps1
```

日志测试把密码、Token、哈希、原始错误和 CR/LF 放入请求、错误、panic 等输入，断言输出不含这些值，且每行可解码为结构化 JSON；也测试服务端请求 ID、失败限量和 panic 不能被记为业务成功。隔离 MySQL 测试验证保留策略、多批次、重复执行、Trigger 故障、Context 取消及与真实登录/退出并发。详见 [阶段 7 验收记录](stage7-test-report.md)。
