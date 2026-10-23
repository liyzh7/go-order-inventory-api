# 发布候选验收入口

所有命令在项目根目录运行，使用 PowerShell 7。测试不连开发库，不读取其凭据。

## 备份恢复

```powershell
./scripts/test-backup-restore.ps1
```

每次创建唯一网络、两个 MySQL 8.4.12 实例和 Go Runner。每个 MySQL 限 2 CPU / 2 GiB / 256 PID，Runner 限 2 CPU / 3 GiB；无宿主端口、无 Docker socket。仅白名单源码快照进入 Runner，无 .env/用户目录/真实密钥。要求至少 20 GiB 磁盘余量，数据库健康等待 3 分钟、Runner 总期限 6 分钟，测试自身期限 5 分钟。

Go 用例创建精确 `order_api_backup_<12位ID>` 模拟数据，真实注册/登录，写入多商品订单、跨用户同键订单、取消订单、活跃/撤销/过期 Session。源/目标都必须没有该 schema。准备完成后请求宿主运行官方 mysqldump，再导入全新空目标。

使用 `--single-transaction --quick --routines --events --triggers --hex-blob --no-tablespaces --set-gtid-purged=OFF`，只备份指定业务库，不导出 mysql 系统用户。演练期间没有并发 DDL/写入；一致性方案依据 [MySQL 8.4 官方说明](https://dev.mysql.com/doc/refman/8.4/en/mysqldump.html)。大规模在线/非 InnoDB 数据需要另行验证。

转储由容器 `--result-file=/backup/backup.sql` 写入，避免 Windows PowerShell 重定向编码损坏。目标只读挂载该文件；导入前再次检查 schema 不存在并核对容器专用标签。失败保留证据，不对已有对象强制重放或清空。

恢复后比较六张业务表和 Goose 表的行数、逐行规范化数据 SHA-256、SHOW CREATE TABLE SHA-256，检查版本 6。系统账号不随业务库导出；显式重建仅有 SELECT/INSERT/UPDATE/DELETE 的测试运行账号。完整认证 HTTP router 使用该最小权限账号（httptest 的真实回环 TCP，不是认证绕过），验证保留 Session、失效 Token、订单查询、新建/取消、用户隔离、原幂等键重放；检查金额/库存/归属守恒，验证真实唯一/CHECK/FK 错误码，失败语句不改变数据。

轻量脱敏证据保存为 `results/backup-<ID>.json`，不保存密码、Token、逐条 Token 哈希或明文请求。敏感 dump 与日志在 `.tmp/backups/<ID>`，容器结束时停止但保留专用 named volume 和网络；没有删除开发数据、全局清理或自动轮转。将来需要清理时先按精确标签列出并单独批准，不使用 Docker prune。

## 尾延迟与工程回归

尾延迟复用 `scripts/run-stability.ps1`，25 VU、50 req/s、600 秒，两轮上限，历史原始结果不覆盖。`scripts/summarize-release-tail.ps1` 只输出本目录汇总。`scripts/observe-release-mysql.ps1` 为可选只读辅助采样，不重置累计计数器；不是每分钟 Commit P99 观测器。

```powershell
./scripts/verify-ci-local.ps1
./scripts/test-recovery.ps1 -ResultDirectory tests/release/results
```

独立备份用例使用 `backup && linux` build tag，不混入默认长 CI。常规 CI 的 MySQL/Race 仍复用原业务用例；本地运行不能代替 GitHub Actions 真实云端 Run。完整结论及未就绪条件见 `docs/release-readiness.md`。

`cmd/release-audit` 依赖真实 Git 仓库；本快照无 .git，不运行全历史入口。历史 results 仅为合成聚合验收记录，不是本候选新测试结果。
