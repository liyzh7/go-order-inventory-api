# 候选本地验证记录

文档状态更新：2026-10-24（P7b）。[新展示仓库](https://github.com/liyzh7/go-order-inventory-api)仍为 **Private，未切换 Public**。本页将 P1 初审、P3 本地补验与 P6 云端证据分开；本轮仅本地文档提交，未重跑测试或 Push，新文档提交尚无自身 SHA 的 CI 结果。

## P1 初审历史记录

以下表格记录 2026-10-23 初审时点。当时尚无新仓库或云端 CI，Race 与 MySQL 尚未验证；这些是当时的环境限制，后续 P3/P6 结果见下文，不能当作当前仍未验证。

| 检查 | 实际结果 |
| --- | --- |
| Go | go1.27.2 windows/amd64，与 go.mod 一致 |
| gofmt | PASS，无剩余格式输出 |
| go mod verify | PASS，all modules verified |
| go vet ./... | PASS |
| 普通 Go 测试 | PASS，67 个顶层测试、140 个含子测试通过事件，15 个有测试的包通过，测试用例无 skip |
| integration 标签 vet | PASS |
| integration 测试编译 | PASS，仅编译；没有运行数据库测试 |
| PowerShell 静态解析 | PASS，30 个脚本，0 语法错误 |
| actionlint 1.7.12 | PASS，既有官方源码离线构建；内置工作流静态检查退出 0，未运行 shellcheck/pyflakes |
| Docker Compose 配置 | PASS，开发、性能、性能资源限制、恢复共 4 种组合；仅 config --quiet，未启动容器 |
| Race | NOT VERIFIED，实际尝试返回 -race requires cgo；当前 CGO 关闭且未找到 C 编译器 |
| MySQL 集成运行 | NOT VERIFIED，Docker Linux 引擎未运行，未接触原数据库或 Volume |
| 长压测、恢复、备份 | NOT VERIFIED，本候选未重新执行；保留报告为历史合成实验 |
| 云端 CI | NOT VERIFIED，新仓库尚未创建 |

Go 检查禁用网络依赖下载和自动工具链下载，使用本机已有模块缓存；构建缓存、临时测试文件和测试可执行程序均在本目录之外。DB/MYSQL 环境未继承真实开发配置，未读取原 .env 进行认证。

唯一新增 Go 防护使历史审查工具在没有自身 .git 时拒绝运行，避免误读父目录仓库；拒绝路径和回归测试均已验证。普通测试不能替代真实数据库锁竞争、提交恢复或 Linux Race。

## P3 候选自身的本地补验

2026-10-23，候选在全新隔离 MySQL 8.4 与 Linux/CGO 条件下完成补验，并未连接原开发库或复用其历史测试结果：

| 测试 | 顶层 passed | 含子测试 PASS 事件 | failed / skipped |
| --- | --- | --- | --- |
| 普通单元 | 67 | 140 | 0 / 0 |
| 单元 Race | 67 | 140 | 0 / 0 |
| MySQL 集成 | 22 | 37 | 0 / 0 |
| MySQL 集成 Race | 22 | 37 | 0 / 0 |

迁移及危险 Down/回滚保护、库存并发一致性、用户级幂等、取消/重复取消、事务回滚、认证隔离和独立库清理检查均由候选自身用例验证。上述计数是 P3 本地结果，不是 P7b 重新执行，也不直接充作云端用例计数。

## P6 新仓库云端证据

根提交 `9e40815202220dd919e3db485e38c1e53c0bdb99` 的 [Run 39962402403](https://github.com/liyzh7/go-order-inventory-api/actions/runs/39962402403)两个 Job 均 SUCCESS：普通单元与 Race 各 15 个包成功，失败 0；非 verbose 单元日志没有逐项用例/跳过计数。MySQL 普通集成与集成 Race 各 22 个顶层测试（各 37 个含子测试 PASS 事件），各失败 0、跳过 0，Artifact 为 0。

本轮只改文档，P6 证据仍仅覆盖上述旧 SHA。新文档提交尚未 Push、无新 SHA 对应 CI；长压测、备份和进程恢复仍未在本轮重跑，不宣称生产上线。完整云端口径和复现方式见 [ci.md](ci.md)。文件哈希、逐文件差异及本地完整证据保存在维护者的独立验收报告目录，不随源码携带原始日志、身份记录或安全发现。
