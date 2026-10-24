# 本地源码候选范围

状态记录：2026-10-24（P7b）。已建立独立[展示仓库](https://github.com/liyzh7/go-order-inventory-api)，仍为 **Private，尚未切换 Public**。P6 根提交 `9e40815202220dd919e3db485e38c1e53c0bdb99` 已完成首次 Push，其 [Run 39962402403](https://github.com/liyzh7/go-order-inventory-api/actions/runs/39962402403)两个 Job 均 SUCCESS，Artifact=0。没有 Tag 或 Release。

本轮只在本地提交 Markdown 文档，未 Push；**新文档提交尚无自身 SHA 的 GitHub CI 结果**。原私有项目永久保留，其 Git 历史、远端设置和作者元数据未导入独立仓库；“尚无 Git/云端 CI”仅属于此前源码快照准备阶段。

保留 Gin API、MySQL store、库存条件扣减、创建与取消事务、用户级幂等、认证/隔离、6 个迁移、单元/集成测试、Docker、CI、性能与恢复工具和第三方声明。主要业务 Go/SQL 内容保持原已提交版本。

旧阶段/性能/恢复报告仅描述各自注明的历史本地合成实验，并非当前候选重跑结果。历史受测 Commit 标识用于来源对应；快照不携带这些历史版本，不能在此 checkout。P95 改善与 P99 回退、采样和故障注入边界保留。

P1/P3 本地验证与 P6 证据区分见 [candidate-validation.md](candidate-validation.md)，云端统计见 [ci.md](ci.md)。仓库地址已经确定，Go module 仍为 internshiplab/order-api，本阶段不改 import；说明见 [README](../README.md)。以后获授权 Push 文档提交，须核验新 SHA 的自动 CI，并审查内容摘要、分支/外部 Fork 策略和私密安全报告渠道；当前没有批准 Public 或生产部署。

源码 MIT 与第三方声明独立适用；本候选不分发可执行程序或镜像。未来二进制、vendor、镜像/工具包和 covered source/NOTICE 等义务需按实际内容复核，不能从当前仅源码范围推导为已完成。
