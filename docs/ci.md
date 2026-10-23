# 独立仓库 CI 设计与运行

当前候选没有云端 CI 结果，也没有新远端仓库。原项目历史验收不能作为新仓库可访问的成功证据。此文件描述保留的流程和未来验证方式。

工作流在 push / pull_request / workflow_dispatch 时运行，权限仅 `contents: read`；官方 checkout/setup-go 固定完整 SHA，checkout 不持久化凭据，MySQL 固定镜像 digest，无部署、发布、Artifact 上传或特权 PR 事件。

`unit` Job 按 go.mod 安装 Go，执行 `bash scripts/test-ci-linux.sh unit`：检查工具链、Linux/CGO/C 编译器、gofmt、模块下载/校验、vet、普通单元测试、单元 Race。

`integration` Job 启动无持久化 Volume 的临时 MySQL，随机映射宿主端口，使用明确公开的 `*-not-a-secret` 一次性测试值。入口不读取 .env；只迁移 `_ci` 基库，逐测试建立随机 `order_api_it_*` schema，执行集成 vet、普通集成与集成 Race。清理只处理各测试自行创建的精确 schema，退出 trap 另做只读残留断言。

Windows 上启动 Docker 后，可运行：

```powershell
./scripts/verify-ci-local.ps1
```

该脚本创建唯一名称的测试网络、无宿主端口的 MySQL 和非 root Go runner，只挂载 Go/SQL/Bash/module 白名单源码，不挂载 .env、Git、用户目录或 Docker socket。Linux 具备匹配工具链和 C 编译器时，可运行：

```bash
CGO_ENABLED=1 bash scripts/test-ci-linux.sh unit
```

集成模式还需单独提供全新临时实例的 DB_*、TEST_MYSQL_* 与 MYSQL_ROOT_PASSWORD；不得复用开发库凭据。CI 凭据仅用于一次性实例，不是生产配置建议。

未来真实仓库仍须验证事件触发、Action SHA 可获取性、镜像拉取、缓存、外部贡献者审批、分支保护及准确候选版本的两个 Job。当前静态检查不代替这些云端结果。长压测、备份和恢复不混入默认 CI。
