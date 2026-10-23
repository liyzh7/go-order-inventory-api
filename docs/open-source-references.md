# 开源参考与第三方依赖

本候选提供独立源码，不导入原 Git 历史。下列项目仅用于架构研究或作为 Go Module 依赖；除依赖管理器正常引入的模块外，本项目当前未复制第三方源码。

## 架构参考

| 项目 | 许可证 | 固定参考版本 | 借鉴内容 |
| --- | --- | --- | --- |
| [gin-gonic/examples](https://github.com/gin-gonic/examples) | MIT | `439005e6e50c4abc65b0fd0a57aa88ce828cc9ee` | `net/http.Server` 超时与基于 Context 的优雅退出思路 |
| [bxcodec/go-clean-arch](https://github.com/bxcodec/go-clean-arch) | MIT | `e06c6d0cb37069b0ef56e3df67f80ca130a1ab82` | 业务包拥有所需接口、数据库实现位于基础设施层的依赖方向 |
| [go-kratos/examples](https://github.com/go-kratos/examples) | MIT | `61daed1ec4d5a94d689bc8fab9bc960c6af73ead` | 事务边界与基础设施生命周期的设计思路；未引入 Kratos、Wire 或微服务组件 |

这些内容均根据项目需求重新实现，没有复制参考仓库的业务代码或目录树。

## 直接依赖

| 依赖 | 许可证 | 用途 |
| --- | --- | --- |
| [gin-gonic/gin v1.12.0](https://github.com/gin-gonic/gin) | MIT | HTTP 路由、中间件与 JSON 响应 |
| [go-sql-driver/mysql v1.10.1](https://github.com/go-sql-driver/mysql) | MPL-2.0 | `database/sql` 的 MySQL 驱动；本项目未修改驱动源码 |
| [pressly/goose v3.29.0](https://github.com/pressly/goose) | MIT | 执行版本化 SQL 迁移 |
| [golang.org/x/crypto v0.58.0](https://pkg.go.dev/golang.org/x/crypto/argon2) | BSD-3-Clause | 已有间接依赖提升为直接依赖，复用维护中的 Argon2id 实现，未复制第三方源码 |
| [MySQL Community Server 8.4](https://dev.mysql.com/doc/refman/8.4/en/docker-mysql-getting-started.html) | GPL-2.0 | Oracle 官方容器镜像提供本地开发和集成测试数据库 |
| [Grafana k6 v2.3.0](https://github.com/grafana/k6/releases/tag/v2.3.0) | AGPL-3.0 | 未修改的项目本地性能测试 CLI；二进制下载到忽略目录并校验官方 SHA-256，不链接或复制其源码 |

已从缓存中的 37 个 go.mod require 项原样保存根许可证/声明及 SHA-256 到 `third_party/`，见该目录 README。项目采用 MIT，版权通知经权利人授权保留，见根目录 LICENSE；尚未发布应用二进制、vendor、k6 或 MySQL 镜像。若未来直接改编第三方源码或分发构建物，需要复核实际内容，记录源文件、提交 SHA、修改与必要声明，不能以项目许可证覆盖第三方条款。

Argon2id 参数依据：[OWASP Password Storage Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html)，访问于 2026-10-16。当前采用 19 MiB、2 次迭代、并行度 1，随机盐 16 字节、派生结果 32 字节；PHC 字符串记录参数，校验前严格限制参数，防止异常哈希造成无界分配。
