# 有界稳定性与恢复验证

这些入口只针对本机合成数据。稳态负载使用现有 `order_api_perf_baseline` 隔离库；故障实验另用 `order-api-showcase-recovery-mysql` 专用实例、专用网络和持久化卷。开发 MySQL 不作为故障目标。

候选已移除稳定性批处理脚本中的原机器模块缓存路径，使用当前 Go 配置。性能和故障资源使用候选专属名称，详见 README；本次未重新执行稳定性/恢复实验。

```powershell
# 默认四轮各 300 秒，5 秒预热及 30 秒负载后观察；总计约 23 分钟。
.\scripts\run-stability-suite.ps1
.\scripts\summarize-stability.ps1

# 已存在或中断的原始证据不会被覆盖；新一轮指定不同前缀。
.\scripts\run-stability-suite.ps1 -RunPrefix s10-repeat
.\scripts\summarize-stability.ps1 -RunPrefix s10-repeat

# 手动提前停止当前负载：创建此标记文件。清理标记后方可重新启动。
New-Item .tmp/stability/STOP -ItemType File
# 也可中断前台 PowerShell；finally 会终止本次 k6 子进程。

# 停止隔离性能服务和数据库，保留 Volume。
.\scripts\performance-stop.ps1 -StopDatabase

# 四轮正式采样结束后，运行独立故障容器中的恢复测试。
.\scripts\test-recovery.ps1

# Linux 常规测试、单元 race、MySQL 集成测试与集成 race。
.\scripts\verify-ci-local.ps1
```

首次默认每轮 5 分钟。`-Seconds 600` 是显式选择的单轮上限，不自动延长。每轮最大 25 VU、固定到达率读 100/s、写 50/s、请求超时 3 秒。四轮写入总量上限约 45,750 个订单（含预热）；一轮每个商品初始 1,000,000 库存。每轮只重置精确的性能库合成数据。

最终版本另执行一轮600秒单热点，单独汇总，不覆盖四轮结果：

```powershell
$env:GOMAXPROCS='2'; $env:GOMEMLIMIT='256MiB' # 仅当前PowerShell进程
.\scripts\performance-start.ps1 -ResetData -CompetitionStock 1000000 -CancellationOrders 0 -StabilityLimits
.\scripts\run-stability.ps1 -Scenario order-create-hotspot -Rate 50 -Seconds 600 -RunID s10-hotspot-600 -CooldownSeconds 30
.\scripts\summarize-stability-extension.ps1
.\scripts\performance-stop.ps1 -StopDatabase
```

重现已完成的600秒实验时同样应选择新RunID，并用 `summarize-stability-extension.ps1 -RunID 新值` 汇总；默认输入保留本次交付的实验。故障或配额中断后不要将旧成功文件当作新一轮通过证据。

MySQL 通过 Compose override 限制为 2 CPU、2 GiB 内存、256 PID、256 MiB buffer pool、256 MiB redo。Go 进程使用 `GOMAXPROCS=2` 和 `GOMEMLIMIT=256MiB`；后者是软限制，监测到 working set 超过 512 MiB 时停止负载。可用磁盘低于 20 GiB、性能库表/索引估计超过 1 GiB、诊断端点失败或超出硬截止时间时同样停止。binlog 1 小时过期，日志轮转为 2×10 MiB；禁止将这些本地限制当作生产配置建议。

k6 使用每分钟动态 VU tag 和子指标保存窗口吞吐、错误率与延迟，不输出逐请求数据流。实现依据 [Grafana k6 execution API](https://grafana.com/docs/k6/latest/javascript-api/k6-execution/) 和 [tag/threshold 文档](https://grafana.com/docs/k6/latest/using-k6/tags-and-groups/)。启动预热不计入正式窗口；数据库初始/结束验证也位于窗口外。每 5 秒尽力采样 runtime、连接池、进程 CPU/内存、MySQL 状态；Docker 命令开销可能使实际采样间隔偏离 5 秒，CSV 保留实际 UTC 时间。

`HTTP 错误率>=1%` 或 `P95>=1000ms` 在至少 15 秒后触发 k6 提前停止；任何检查失败、非预期业务响应或 dropped iteration 使该轮失败。失败运行不会生成通过结果。原始输出与本地 Session manifest 保留在忽略的 `.tmp`；提交的 JSON/CSV 只含聚合计数和资源样本。

故障测试在 Linux 容器内编译并启动真实服务进程，使用真实注册/登录所得 Session。宿主脚本只接受专用 Compose project/name 的 `pause/resume/restart` 三种操作；暂停有 12 秒自动解除保护。每次运行创建新的 `order_api_recovery_<随机后缀>` 库，历史实验库与命名卷保留。测试服务器、故障实例和 runner 最终停止。容器、Volume 和数据库均不进行全局清理。

Commit 回执丢失另由 `tests/integration/commit_recovery_test.go` 注入：真实 MySQL Commit/Rollback 完成后返回受控 driver 错误，覆盖已提交、未提交和核验连接不可用；这些结果与真实容器暂停/重启明确区分。该注入没有生产开关。

每轮后验证订单/明细增量、库存减少量、金额一致性、无负库存、无孤立数据及用户级幂等唯一性。故障库再逐商品核对 `初始库存 - CREATED 明细数量 = 当前库存`，并验证授权和重复取消。报告只支持本机有界实验的结论。
