# 独立仓库 CI 设计与运行

状态记录：2026-10-24（P7b）。[新展示仓库](https://github.com/liyzh7/go-order-inventory-api)已建立，仍为 **Private，尚未切换 Public**。P6 根提交 `9e40815202220dd919e3db485e38c1e53c0bdb99` 的[首次 CI Run 39962402403](https://github.com/liyzh7/go-order-inventory-api/actions/runs/39962402403)由 main push 触发，attempt=1，两个 Job 均 SUCCESS，Artifact=0。

上述结果只属于该根提交。本轮 P7b 仅本地 Markdown 文档提交、尚未 Push，**新文档提交尚无自身 SHA 的云端 CI 结果**；后续获授权 Push 后必须核验新 SHA 的自动运行，不能沿用旧 Run 作为新提交的通过证明。

## P6 实际云端结果

| Job | 普通测试 | Race | failed / skipped |
| --- | --- | --- | --- |
| [Go checks and unit race tests](https://github.com/liyzh7/go-order-inventory-api/actions/runs/39962402403/job/120443427471) | 15 个测试包 OK | 15 个测试包 OK | 失败 0；用例级 skipped 未逐项报告 |
| [MySQL integration and integration race tests](https://github.com/liyzh7/go-order-inventory-api/actions/runs/39962402403/job/120443427750) | 22 顶层 / 37 含子测试 PASS 事件 | 22 顶层 / 37 含子测试 PASS 事件 | 各失败 0、跳过 0；无 Race 报告 |

15 个包不是 15 个用例。单元入口未用 -v/-json，不能由云端日志声称精确 67 个单元用例或用例级 skipped=0。集成入口为 -v，顶层与子测试不重复相加；EXIT trap 另有非 verbose 的残留库校验包成功，不当作另一套完整集成计数。

实际工具链为 Go 1.27.2 linux/amd64、CGO/C 编译器；MySQL 8.4.12 健康初始化、Goose 迁移至 6、事务/库存守恒、幂等、并发取消、授权隔离及回滚保护通过。两个 Runner 为标准 GitHub-hosted Ubuntu（workflow 标签 ubuntu-latest），非本机开发数据库。先前候选本地 P3 的 67 单元/67 单元 Race/22 集成/22 集成 Race 单独记录于 [本地验证](candidate-validation.md)，不是云端逐用例统计。

## 运行与安全边界

工作流在 push / pull_request / workflow_dispatch 时运行，权限仅 `contents: read`；官方 checkout/setup-go 固定完整 SHA，checkout 不持久化凭据，MySQL 固定镜像 digest，无部署、发布、Artifact 上传或特权 PR 事件。

`unit` Job 按 go.mod 安装 Go，执行 `bash scripts/test-ci-linux.sh unit`：检查工具链、Linux/CGO/C 编译器、gofmt、模块下载/校验、vet、普通单元测试、单元 Race。

`integration` Job 启动无持久化 Volume 的临时 MySQL，随机映射宿主端口，使用明确公开的 `*-not-a-secret` 一次性测试值。入口不读取 .env；只迁移 `_ci` 基库，逐测试建立随机 `order_api_it_*` schema，执行集成 vet、普通集成与集成 Race。清理只处理各测试自行创建的精确 schema，退出 trap 另做只读残留断言。

Artifact=0 不代表没有缓存：setup-go 使用标准 Go 模块/编译缓存。P7 API 列出一个缓存，归档字节未直接审计；不得把凭据、.env 或用户目录写入缓存。外部 Fork 审批策略与分支保护尚未验证为已启用；Private 下的套餐/API 限制与公开后维护设置要另行处理，不能从这两个成功 Job 推断规则已配置。

## 本地复现入口

Windows 上启动 Docker 后，可运行：

```powershell
./scripts/verify-ci-local.ps1
```

该脚本创建唯一名称的测试网络、无宿主端口的 MySQL 和非 root Go runner，只挂载 Go/SQL/Bash/module 白名单源码，不挂载 .env、Git、用户目录或 Docker socket。Linux 具备匹配工具链和 C 编译器时，可运行：

```bash
CGO_ENABLED=1 bash scripts/test-ci-linux.sh unit
```

集成模式还需单独提供全新临时实例的 DB_*、TEST_MYSQL_* 与 MYSQL_ROOT_PASSWORD；不得复用开发库凭据。CI 凭据仅用于一次性实例，不是生产配置建议。

P6 已实测该根提交的事件触发、Action 获取、镜像拉取、两组测试与缓存步骤；P7b 没有重跑这些步骤。长压测、备份和进程级恢复不进入默认 CI，本轮未重跑。CI 成功不代表生产上线或容量保证。
