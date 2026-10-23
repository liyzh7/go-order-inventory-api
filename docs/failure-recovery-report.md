# 阶段 10：故障恢复验证

> 历史合成实验记录：下文测试和环境属于原注明的受测版本，不是本候选重新执行结果。原 Git 历史未随快照提供；当前候选资源名/端口及验证状态以 README 和 candidate-validation.md 为准。

日期：2026-10-17。使用真实本地容器与测试进程执行有界实验；本报告不代表生产事故、故障切换或高可用认证。

## 隔离环境与版本

故障实例为 `order-api-recovery-mysql`，Compose project 为 `order-api-recovery`，专用网络不发布 MySQL 或 API 端口。MySQL 8.4.12 固定 digest，限制 2 CPU/2 GiB，命名卷 `order-api-recovery-mysql-data` 保留。每次运行新建精确匹配 `order_api_recovery_<12位后缀>` 的数据库，不删除旧实验库。Go runner 限 2 CPU/3 GiB、非 root、只读根文件系统、256 PID、2 GiB 临时区；仅挂载源码快照和脱敏证据控制目录，没有宿主 `.env`、Docker socket 或用户目录。

最终严格套件对应 `047bd10a15e049954ba0afa78716bcdea73e5887`，故障库为 `order_api_recovery_cf5a36d0783c`。Go 子进程使用真实 `cmd/server` 构建，在 Linux 回环 `127.0.0.1:18082` 上监听。用户通过实际注册与登录取得随机 Session，无认证绕过。没有修改迁移 1～6。

宿主控制器只处理经 project/name 核对的专用容器；暂停有 12 秒强制解除保护，套件有 8 分钟总截止。实验结束停止 runner 与故障 MySQL，命名卷和实验数据保留。开发容器不属于控制目标。

## 真实故障与恢复结果

下表“接口耗时”来自单次 HTTP 计时；“完整流程”包含锁控制、重启、健康等待、重试与状态核验，二者不能混用。

| 实验 | 注入方式 | API/客户端行为 | 接口耗时 | 完整流程 | 数据结果 |
|---|---|---|---:|---:|---|
| 数据库短暂不可用 | `docker pause` 后 `unpause` | 503 `temporarily_unavailable` | 3004.15ms | 4.45s | 原进程自动恢复，同键重试只创建一单 |
| SQL 中断 | 第二商品库存 UPDATE 的专用 Trigger `SIGNAL` | 安全的500 `internal_error` | 11.94ms | 44.08ms | 订单头、两条明细和第一商品扣减全部回滚；移除 Trigger 后重试成功 |
| 客户端取消 | 等待真实库存锁后取消 request context | 客户端 transport error，无伪造HTTP成功 | 4.22ms | 18.73ms | 无部分提交，同键安全重试 |
| 客户端200ms超时 | 等待库存锁，context deadline | `context.DeadlineExceeded` | 200.27ms | 215.27ms | 无部分提交，同键安全重试 |
| 正常优雅关闭 | 有真实请求等锁时发 SIGTERM，然后释放锁 | 处理中请求201；进程exit 0；重启后重放200 | 重放2.74ms | 257.10ms | 原Session有效，同键同ID，无第二次扣减 |
| 非优雅退出 | 有真实请求等锁时 SIGKILL 子进程 | 原请求连接断开；重启后可认证 | 原请求29.69ms | 128.72ms | 未提交订单不存在，同键重试创建一次 |
| MySQL容器重启 | 阻塞真实库存事务后重启专用实例 | 503 `temporarily_unavailable` | 1007.39ms | 9.31s | 已提交订单/Session存在，未提交头/明细/库存全部回滚，恢复后查询和创建成功 |
| 恢复后权限与取消 | 用户B访问/取消A订单，A取消及重复取消 | B均404；A首次及重复均200 | 重复3.65ms | 22.19ms | B无副作用；重复取消不二次回补 |

最终库为 **8个订单、9条明细、商品库存93/199**。初始库存100/200；一单取消，逐商品 `初始库存 - CREATED明细数量 = 当前库存` 成立。负库存、金额不一致、无明细订单、孤立明细、用户级重复幂等、库存守恒差异六项检查均为0。Trigger 已显式移除，不遗留到开发库或实验结束状态。

UTC控制时间：

| 操作 | 开始 | 控制操作完成 |
|---|---|---|
| pause | 03:33:40.8352612Z | 03:33:41.0210987Z |
| resume | 03:33:44.5430650Z | 03:33:44.7182946Z |
| restart至容器健康 | 03:33:45.9649921Z | 03:33:54.6805485Z |

恢复后的业务请求与最终一致性检查在03:33:54.777Z前完成。实际控制器每200ms轮询，毫秒值仅用于复核本次事件，不是恢复SLA。

## Commit 结果未知：明确标注的注入

`tests/integration/commit_recovery_test.go` 包装真实 MySQL Connector，所有业务 SQL、唯一约束、事务及核验读取仍使用 MySQL。测试仅在真实 Commit/Rollback 完成后返回一次受控 driver 错误；**没有声称稳定复现了真实网络中恰好丢失 Commit 回包**。不向生产程序加入故障开关。

| 注入 | 初次响应 | 原用户+原key重试 | 最终验证 |
|---|---|---|---|
| 数据已提交但回执报错 | 200并标识重放 | 200，原订单 | A仅一单、库存仅扣一次 |
| 数据未提交且结果报错 | 503 | 201 | 先无订单/扣减，重试只创建一次 |
| 已提交但核验连接不可用 | 503，不假定成功 | 核验恢复后200 | 不重复下单或扣库存 |

每种场景另让用户B使用相同key：B新建自己的订单，不能读取A订单。各独立库最终2个订单、2条明细、库存16（初始20，各用户购买2）；注入池InUse为0。首次与最终专用实例均验证过，最终套件本部分耗时3.033秒；完整Linux集成回归也重复执行了这些用例并通过Race。

## 发现与修复

首次实验 `d70d0cb` 的旧断言接受任意安全5xx，因此数据检查虽通过，数据库重启仍返回500，不能作为最终错误响应验收。首次结果保存为 `recovery-initial-summary.json`。

补充 typed transport/invalid connection 分类后的 `5daa998`，严格要求503的重跑仍失败：`interrupted transaction returned 500`。该轮记录为失败，原始证据保留于忽略的 `.tmp/recovery-64e0ae45c175`，不被汇总为通过。

本机驱动源码显示，某些失败写入使用私有哨兵，回滚时可返回公开的连接错误。旧代码忽略 Rollback 返回值，丢失了分类证据。最终修复保留原操作错误与回滚错误，使用 `errors.Is/As` 识别连接错误；正常 Commit 之后的 `sql.ErrTxDone` 被正确忽略。取消事务也复用同一处理。另按 [MySQL 8.4错误参考](https://dev.mysql.com/doc/mysql-errors/8.4/en/server-error-reference.html) 补充1053服务器关闭分类；没有把1053宣称为现场已捕获的错误码。

真实重跑随后返回503并通过全套断言。重试白名单仍仅为1205/1213，连接异常没有因此自动重放整个事务；Commit失败仍先按原用户与原key核验。没有通过匹配错误文本、放松事务或删除约束实现修复。

新增单元测试覆盖 wrapped connection错误、EOF、关闭连接、1053、context取消、普通1644 Trigger错误及保留回滚失败；普通SQL错误仍为500，context语义保留。故障先后结果见 `tests/stability/results/recovery-attempts.json`。

## 执行入口、证据与限制

运行 `scripts/test-recovery.ps1`；该命令不读取开发 `.env`。完整回归使用 `scripts/verify-ci-local.ps1`，包含Linux普通及Race的单元和MySQL集成测试。Windows全量单元、依赖和vet已在最终修复版本实际通过；沙箱对外部Go缓存的访问限制通过批准后的同命令重跑解决，没有修改全局GOPATH或安装编译器。

修复后另有15秒、50/s单热点冒烟，751次成功并通过逐商品一致性检查，见 `postfix-smoke.json`；随后在相同最终版本完成600秒、50/s单热点，30,000次成功、错误与dropped均0，逐商品库存差异0。四轮5分钟及这轮10分钟的版本、窗口与资源结果分别保存在 [稳定性报告](stability-test-report.md)。

提交的JSON/CSV只含脱敏计数、事件分类、UTC时间和资源数据。Token、密码、请求正文和大体积原始日志不提交。最终回归结果记录在 `tests/stability/results/verification-summary.json`。

最终执行结果：Windows `gofmt`、`go mod verify`、`go vet ./...`、`go test -count=1 ./...`通过；Linux同类检查、单元Race、MySQL普通集成测试（19.757秒）及集成Race（27.494秒）全部通过。隔离实例为MySQL8.4.12、迁移版本6，残留测试库0。Linux使用Go1.27.2、GCC12.2.0；真实进程/容器恢复套件本身没有Race插桩，不能将其与集成Race混称。云端GitHub Actions未运行。

没有验证真实生产网络中未知Commit的发生概率、多节点自动故障切换、宿主断电/磁盘损坏、跨机房容灾、24小时泄漏或备份恢复。数据库重启是专用本地实例实验，不等于生产高可用。下一步宜验证备份恢复和更长独占稳态窗口，仍无证据要求引入Redis。
