# MySQL 逻辑备份与恢复实测

> 历史合成实验记录：下文测试和环境属于原注明的受测版本，不是本候选重新执行结果。原 Git 历史未随快照提供；当前候选资源名/端口及验证状态以 README 和 candidate-validation.md 为准。

日期：2026-10-17；演练 ID `df75838683ce`。证据：`tests/release/results/backup-df75838683ce.json`，执行入口 `scripts/test-backup-restore.ps1`，用例 `tests/release/backup_test.go`。

应用审查起点 `a3e41576be6029f637f45195c1e3dc33ad983ef0`；本阶段新增的验收工具在工作树中测试，其 Go/SQL 白名单快照 SHA-256 另存于 JSON。实际快照已与待提交 Go/SQL 文件逐项校验一致；不能只用旧 HEAD 冒充新增工具的完整版本。

## 环境、数据和方法

使用已固定 digest 的 Oracle MySQL Community Server **8.4.12**；源/目标均为本次新建专用实例，无宿主端口、不同 named volume、同一独立网络。单实例限 2 CPU/2 GiB；Go 1.27.2 Linux Runner 限 2 CPU/3 GiB，仅挂载白名单源码与控制目录，不接触开发 .env/Volume。

备份工具实际输出：`mysqldump Ver 8.4.12 for Linux on x86_64 (MySQL Community Server - GPL)`。UTC 开始时间在 JSON 中保留。使用 [MySQL 官方一致性逻辑备份选项](https://dev.mysql.com/doc/refman/8.4/en/mysqldump.html)：single-transaction、quick、hex-blob、no-tablespaces、GTID purged OFF，以及 routines/events/triggers；只导出指定业务库，不导出 mysql 系统账号。

演练期间没有并发 DDL 或写入。源 fixture 通过真实注册/登录创建两名合成用户、4 个 Session（活跃、撤销、过期）、3 个商品/库存，包含多商品订单、不同用户相同幂等键订单、已取消订单。全表 InnoDB。此用例没有证明大规模在线写入期间的备份表现，也没有创建额外 routine/event/trigger 验证它们的恢复。

转储用容器 result-file 写入忽略的专用路径，避免 PowerShell 文本编码转换；目标导入前检查同名 schema 不存在，核对本次精确容器标签。源和恢复服务分别显式配置只拥有 CRUD 权限的测试账号，不用 Root 运行 HTTP 业务。Root 仅用于迁移、fixture/结构检查和导入。

## 实际结果

| 项目 | 结果 |
| --- | --- |
| 备份大小 | 13,579 字节 |
| 备份命令墙钟耗时 | 534.704 ms |
| 恢复命令墙钟耗时 | 543.846 ms |
| Go 备份验证用例 | PASS，3.108 秒（不是整个容器/编译启动耗时） |
| MySQL / Goose 版本 | 8.4.12 / 6 |

上述毫秒值包含 Docker exec 和本机文件系统往返，样本极小，不能作为生产 RTO 或备份容量指标。

| 表 | 备份前行数 | 恢复后（业务验证前） |
| --- | ---: | ---: |
| users | 2 | 2 |
| auth_sessions | 4 | 4 |
| products | 3 | 3 |
| inventories | 3 | 3 |
| orders | 3 | 3 |
| order_items | 4 | 4 |
| goose_db_version | 7 | 7 |

全部表逐行规范化数据 SHA-256、SHOW CREATE TABLE SHA-256 完全一致。迁移表含初始版本 0 与 1～6，不把 7 行误报成迁移版本 7。报告仅保存整表聚合摘要，不保存个人记录、密码哈希或逐条 Token 哈希。

恢复后的完整 HTTP router 通过真实回环 TCP 验证：

- 保留的活跃 Session：`GET /auth/me` **200**；撤销/过期 Session 均 **401**。
- A 查询原订单 **200**；B 查询/取消 A 的订单均 **404**，不泄漏归属。
- A 使用原 key、倒序商品输入重放 **200**，返回原 ID，不多扣库存。
- 原已取消订单重复取消 **200**；新创建 **201**，新取消及重复取消均 **200**。
- 不使用固定万能 Token、认证绕过或测试关闭权限；接口使用恢复库中真实的用户、Session 哈希和数据。

新业务验证后为 4 个订单、5 条明细，其他表行数未改变。库存符合 `初始库存 - CREATED 订单数量`；金额/明细、无明细订单、孤立明细、重复用户幂等键、无效归属、负库存的违规计数全部 **0**。

恢复后的 SKU 唯一/大小写 CHECK、库存 unsigned、库存 FK、owner FK、用户级幂等唯一约束按真实 MySQL **1062 / 3819 / 1264 / 1452** 错误码验证；失败语句没有改变表数据。失败 INSERT 允许消耗 auto-increment 值，因此只对失败前后行内容做不变断言，不错误要求自增计数不变。

## 数据保护与边界

源/目标及 Runner 已停止；专用 named volume、网络和 `.tmp/backups/df75838683ce` 中的敏感转储保留，均未提交。没有删除 Volume、清空开发库、运行全局 prune 或覆盖已有 schema；再次运行会生成新 ID、新目标，不强制覆盖上次失败资源。

本次只验证逻辑备份/新实例恢复和恢复后的业务正确性。没有实现加密/异地保存、自动备份/轮转、备份权限平台、PITR、断电一致性、故障切主或多节点灾备。系统账号和权限需按部署配置另行重建，业务 dump 不能代替完整运维配置。
