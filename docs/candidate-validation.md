# 候选本地验证记录

验收完成日期：2026-10-23（Asia/Shanghai）。这是本地源码候选，尚无新仓库或云端 CI 结果。

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

工作流设计和后续运行方式见 [ci.md](ci.md)。文件哈希、逐文件差异及本地完整证据保存在维护者的独立验收报告目录，不随源码携带原始日志、身份记录或安全发现。
