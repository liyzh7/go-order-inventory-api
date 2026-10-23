# 阶段 7 验收记录

> 历史合成实验记录：下文测试和环境属于原注明的受测版本，不是本候选重新执行结果。原 Git 历史未随快照提供；当前候选资源名/端口及验证状态以 README 和 candidate-validation.md 为准。

日期：2026-10-16（Asia/Shanghai）。全部结果来自本地实际执行，未创建远程仓库、未 push，也没有 GitHub Actions 云端运行记录。

## 交付范围

- GitHub Actions：独立 unit/integration Job，最小只读权限，已核实 SHA 的官方 checkout/setup-go，从 go.mod 读取并严格匹配 Go，固定 digest 的官方 MySQL 8.4，健康检查和 CI 临时凭据。
- 跨平台共用 Bash 测试入口与 Windows 隔离容器编排脚本；普通单元、单元 Race、普通 MySQL 集成与集成 Race 分开执行。
- 手动 Session 清理 CLI，默认保留 7 天、分批提交、有界批次数、Context 超时/取消、固定 UTC 截止点和安全摘要。
- 标准库 slog 安全事件：服务端请求 ID、认证、越权失败、取消、清理、固定错误分类、秘密值保护及失败事件限量。
- 未新增业务接口，没有修改 go.mod/go.sum 或迁移 1～6。

## 执行环境

Windows Go 1.27.2；本地开发 MySQL 8.4.12，迁移 6，Docker 容器 healthy。

Linux 验证使用官方 Go 1.27.2 Debian Bookworm 镜像，CGO_ENABLED=1、GCC 12.2.0。Go 镜像固定 digest 为 `sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61`；官方 Oracle MySQL Community 镜像固定 digest 为 `sha256:7dcc4add9183664de3a214daf85a50c3ba6cccfd7534f700b6561bf5b41885be`，实际 SQL 返回 8.4.12。

编排脚本创建独立网络、无宿主端口的一次性 MySQL 和非 root Go runner；仅挂载 Go/SQL/Bash 源码白名单快照，不挂载 `.env`、Git、宿主用户目录或 Docker socket。只向其传入公开 CI 临时值。最终已移除本次容器和网络；只读核对没有遗留 `order-api-ci-*` 网络/容器。最终 Go、SQL、Bash 文件与已验证快照逐文件 SHA-256 相同，之后仅补充文档。

## 工程与 CI 静态检查

以下实际通过：

```text
gofmt 检查（无未格式化文件）
go mod verify
go vet ./...
go vet -tags=integration ./...
go test -count=1 ./...
git diff --check
```

Windows 普通单元测试通过，包括新增日志、清理 CLI/service 与现有业务模块。PowerShell 容器编排脚本解析无语法错误。

实际使用 actionlint v1.7.12：

```text
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -shellcheck= -pyflakes= .github/workflows/ci.yml
exit 0
```

关闭的是未安装的外部 ShellCheck/Pyflakes 调用，不是工作流/YAML/表达式校验。Bash 实际执行由后述 Linux 验证覆盖。工具下载在忽略的临时缓存，没有加入应用依赖或全局安装。

## 本地 Linux 与真实 MySQL

实际执行：

```powershell
.\scripts\verify-ci-local.ps1
```

该命令在隔离 Linux runner 内执行同一 CI 入口：

```bash
bash scripts/test-ci-linux.sh all
```

| 检查 | 实际结果 |
| --- | --- |
| Go 版本、CGO、C 编译器检查 | 通过：Go 1.27.2 / CGO 1 / GCC 12.2.0 |
| 模块下载、verify、gofmt、vet | 通过 |
| 普通单元测试 | 通过 |
| `go test -race -count=1 ./...` | 通过，未报告 DATA RACE |
| 全新 CI 基库 Goose Up/Status | 迁移 1～6 成功，最终版本 6 |
| 普通 MySQL integration 测试 | 通过 |
| `go test -race -tags=integration -count=1 -v ./tests/integration` | 通过，`ok ... 20.790s`，未报告 DATA RACE |
| EXIT trap 测试库残留断言 | 通过，`ok ... 0.011s` |
| 最终 SQL 状态 | MySQL 8.4.12 / Goose 6 / `order_api_it_*` 残留 0 |

集成测试复用了原测试而非复制第二套实现。迁移测试覆盖全新安装、隔离库回滚后再迁移、认证升级和旧数据/唯一索引兼容；开发库没有执行 Down。TestMain 检查新增残留，CI EXIT trap 额外检查一次性服务全部测试库残留，只查询不删除陌生数据库。

Linux runner 为 Debian 容器，不等同于 `ubuntu-latest`；本次没有实际执行 GitHub 的 checkout/setup-go、Runner 服务生命周期、事件触发或缓存。上述步骤仍需获授权推送后首次云端 Run 验证，不得将本地通过写成 CI 云端成功。

## Session 清理验证

真实隔离 MySQL 测试的结果：

- `TestSessionCleanupRetentionAndBatches`：删除 7 条符合期限的记录，批量 2，共 4 批；保留 2 条活跃及 2 条保留期内失效记录。重复运行删除 0；已失效 Token 清理前后均 401，活跃 Token 仍为 200。
- `TestSessionCleanupFailureAndCancellation`：已取消和超时 Context 不删除；测试专用 DELETE Trigger 故障不删除；解除 Trigger 后重试删除 1 条；关闭数据库后立即报错停止。
- `TestSessionCleanupConcurrentLoginLogout`：与真实注册账号登录/退出并发清理 20 条旧记录；最终只有 1 条新活跃 Session 和 1 条近期撤销 Session，新 Token 为 200，退出 Token 为 401。
- 单元测试覆盖固定 UTC 截止点、多批、达到上限、非法选项、部分批次已提交后失败及取消。故障不无限重试。

完整 CLI 也在空的 CI 基库实际执行（未对开发库执行）：

```text
go run ./cmd/session-cleanup -retention 168h -batch-size 2 -max-batches 2 -timeout 10s
{"deleted_count":0,"batches":1,"limit_reached":false,"cutoff":"2026-10-09T04:07:20.605469Z","duration_ms":1,"outcome":"success"}
```

同时输出了 JSON `session.cleanup` 安全事件，只含请求 ID、分类、计数和耗时，没有 Token/哈希/连接密码。此空库烟雾测试不代替上面的实际删除测试。

## 安全日志与回归

秘密值脱敏测试实际通过：

- `TestAllowlistedMetadata`、`TestHTTPDoesNotLogUntrustedRequestsOrPanics`、`TestFailureLogBudget`。
- `TestSecurityEventsNeverContainRequestOrDatabaseSecrets`：请求密码、Bearer Token、Token 哈希、完整 Authorization、原始数据库错误、查询参数及不可信请求 ID 均不进入日志；JSON 行可解析。
- `TestPanicDoesNotRecordSuccessfulBusinessEvent`：认证与取消 panic 返回安全 500，不误记为业务成功；不输出 panic 值或请求转储。

阶段 4～6 的主要真实 MySQL 回归均通过：注册/登录/退出、失效 Token、A/B 越权与历史无归属订单隔离、用户级同键/并发幂等、多商品订单和金额快照、库存并发竞争、SQL Trigger 故障回滚、详情一致性、首次与重复取消，以及 20 请求取消同一订单仅回补一次。共享库存的 10 个不同订单并发取消后，测试断言保留 11 个订单/22 个明细，库存恢复到 100/200；不仅检查 HTTP 状态。

## 开发库只读复核

最终仅查询，未清理已有 Session 或业务数据：

```text
order-api-mysql: healthy, 127.0.0.1:3307
MySQL 8.4.12 / migration 6
orders=4, order_items=5, users=2, sessions=2（均已撤销，仍保留）
inventories: 1=49, 2=25, 3=10, 4=0
order_api_it_* databases=0
development triggers=0
```

订单 1/2 仍为 NULL 用户历史订单；订单 3/4 用户分别为 1/2，均 CANCELLED。订单金额、状态、数量和库存与本阶段开始时一致。未改写历史提交身份。

## 真实限制

- Windows 原生 `go test -race -count=1 ./...` 实际失败：`go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`。当前 CGO_ENABLED=0，未发现 GCC；没有修改 Windows 全局环境。Linux 两类 Race 已实际通过，不能据此说 Windows 原生 Race 通过。
- 无 GitHub 云端运行记录，actionlint 与本地 Linux 不能证明远程事件、权限、缓存和镜像拉取一定成功。
- 日志不是完整审计：所有 HTTP failure 共享每分钟 30 条预算，第 31 条为抑制标记；包含数据库/panic 失败，之后可能不留逐条记录。没有轮转/持久化平台。
- 清理 LIMIT 不限制扫描成本，尚未有大规模执行计划、吞吐或延迟结论；未启用周期调度。
- Runner 被强制终止时 EXIT trap 可能无法执行；一次性服务仍由运行平台销毁。没有为通过测试关闭并发用例或放宽一致性。
