# MySQL 8.4 性能与锁竞争分析

> 历史合成实验记录：下文测试和环境属于原注明的受测版本，不是本候选重新执行结果。原 Git 历史未随快照提供；当前候选资源名/端口及验证状态以 README 和 candidate-validation.md 为准。

分析对象仅为 `order_api_perf_baseline`。查询使用 `EXPLAIN ANALYZE`；会修改数据的库存/状态 UPDATE 只使用 `EXPLAIN FORMAT=JSON`，没有通过分析命令执行副作用。原迁移 1～6、索引、约束和隔离级别均未修改。

## 核心执行计划

| SQL | 计划与实际证据 | 结论 |
| --- | --- | --- |
| 商品列表：ACTIVE、`id > 500`、limit 100 | products 使用 PRIMARY 范围扫描，inventories 每行 PRIMARY 单行查找；实际 100 行 `0.0861..0.186 ms` | 无全表扫描/N+1 网络查询；全为 ACTIVE 时优化器选择主键范围合理 |
| 商品详情 ID 2 | `Rows fetched before execution`，实际约 `0.000051..0.000081 ms` | products/inventories 常量主键访问，无新增索引依据 |
| 用户订单详情 ID 1/user 1 | orders 单行后，order_items 使用复合 PRIMARY 的 order_id 前缀；1 行排序结束约 `0.0802 ms` | 所有权条件在 SQL 内；明细索引有效，单条 JOIN 保持响应一致性 |
| 用户级幂等查找 | `(user_id,idempotency_key)` 唯一索引，`Rows fetched before execution`，约 `0.000060..0.000090 ms` | 用户作用域唯一索引有效，无跨用户扫描 |
| 库存条件 UPDATE | PRIMARY，`rows_examined_per_scan=1` | SQL 访问本身精确，热点来自同一行排他锁串行化，不是缺索引 |
| 取消状态 UPDATE | PRIMARY，`rows_examined_per_scan=1`，附加 user/status 条件 | 条件状态转换不需要额外复合索引 |

商品列表已有 `(status,id)` 索引，但本次全体商品均为 ACTIVE，并附带高选择性的主键游标，优化器选择 PRIMARY。不能仅凭这次选择删除现有索引；若未来 ACTIVE/INACTIVE 分布不同，应以相同数据分布重新 ANALYZE/EXPLAIN。

## Performance Schema 证据

下列状态从性能容器启动、迁移、22 次确定性重置和全部负载累计而来，不是单个场景独占值：

- COMMIT 2028 次，总计 29100.660 ms，平均 14.349 ms，是累计 statement digest 中等待时间最高的类别。
- ROLLBACK 697 次，总计 2068.103 ms，平均 2.967 ms。竞争场景库存不足会合法回滚，因此不能把这些都视为系统错误。
- inventories 表累计读取 284,771、写入 56,010；读取等待约 99,192.579 ms、写等待约 593.504 ms。
- 全局 `Innodb_row_lock_waits=1289`、`Innodb_row_lock_time=100162 ms`；采样结束时 `Innodb_row_lock_current_waits=0`，`performance_schema.data_lock_waits=0`。
- `Created_tmp_tables=355`、`Created_tmp_disk_tables=0`；`Aborted_connects=0`。
- 最终应用库连接 4，其中查询瞬间活动连接 1；HTTP 轮次的 `database/sql` 快照最高打开/使用 20/20，连接池上限 25，等待增量 0。

库存竞争在三轮中均为 200 请求、100 成功、100 库存不足，P95 约 149.23 ms，MySQL CPU 最高 50.42%；普通订单创建目标 20/s 的 P95 约 81.79 ms。结合行锁累计值和 PRIMARY 单行计划，可以确认本次最主要竞争点是同一 inventory 行的串行排他锁与事务提交，不是 SQL 扫描或连接池排队。

`inventories` 的累计 table I/O 读等待远高于其他表，但这个表级统计包含竞争等待、种子重置、正确性查询和全部轮次，不能解释为一次简单 SELECT 的执行时间。精确归因需要按场景重置 Performance Schema 统计，或在独占测试实例按前后差值采集；本阶段没有为了得到漂亮数字清空全局统计。

## 可验证建议

1. 暂不新增商品、订单、幂等或库存索引。当前计划都是 PRIMARY/唯一索引单行或范围访问；新索引会增加写放大，缺少收益证据。
2. 暂不引入 Redis。商品读尚未饱和，库存与幂等仍必须依赖 MySQL 事务；缓存不能解决单库存行写锁，并会增加失效一致性问题。
3. 下一轮先把场景持续时间从 5 秒增加到至少 60 秒，并分级提高到达率，单独记录每场景前后 Performance Schema 差值，确认 COMMIT 延迟和 Docker Desktop 存储的影响。
4. 对热点库存只能在业务允许时考虑分桶库存、排队削峰或预约模型；这些都会改变一致性模型，本阶段不实施。不能通过去掉条件 UPDATE、约束、事务或降低隔离保护来换取数字。
5. 若未来 Session 数量显著增长，另行对清理 DELETE 的 OR 谓词做 EXPLAIN 和索引评估；它不在本次 HTTP 基线中。

本报告没有运行 `EXPLAIN ANALYZE UPDATE`，没有向开发库写入，没有删除任何数据库或 Volume，也没有修改历史迁移。
