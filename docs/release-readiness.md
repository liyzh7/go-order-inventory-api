# 本地源码候选范围

独立[展示仓库](https://github.com/liyzh7/go-order-inventory-api)仅包含经审查的源码、配置、文档与必要许可证。已核实的 P6 根提交与 P7c 文档提交及云端 Run 见 [CI 说明](ci.md)；最新 main 状态由 README 动态 Badge 展示，不把某次历史成功扩张到未验证的提交或能力。

原私有项目永久保留，其 Git 历史、远端设置和作者元数据未导入独立仓库；“尚无 Git/云端 CI”仅属于 P1 源码快照准备阶段的历史状态。

保留 Gin API、MySQL store、库存条件扣减、创建与取消事务、用户级幂等、认证/隔离、6 个迁移、单元/集成测试、Docker、CI、性能与恢复工具和第三方声明。主要业务 Go/SQL 内容保持原已提交版本。

旧阶段/性能/恢复报告仅描述各自注明的历史本地合成实验，并非当前候选重跑结果。历史受测 Commit 标识用于来源对应；快照不携带这些历史版本，不能在此 checkout。P95 改善与 P99 回退、采样和故障注入边界保留。

P1/P3 本地验证与 P6 证据区分见 [candidate-validation.md](candidate-validation.md)，云端统计见 [ci.md](ci.md)。仓库地址已经确定，Go module 仍为 internshiplab/order-api，与内部 import 一致；说明见 [README](../README.md)。每次变更应核验准确 SHA 的自动 CI，并单独审查内容摘要、分支/外部 Fork 策略和私密安全报告渠道；源码公开不代表批准生产部署。

源码 MIT 与第三方声明独立适用；本候选不分发可执行程序或镜像。未来二进制、vendor、镜像/工具包和 covered source/NOTICE 等义务需按实际内容复核，不能从当前仅源码范围推导为已完成。
