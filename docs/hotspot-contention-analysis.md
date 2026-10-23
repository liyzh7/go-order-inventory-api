# Hotspot inventory contention analysis

> 历史合成实验记录：下文测试和环境属于原注明的受测版本，不是本候选重新执行结果。原 Git 历史未随快照提供；当前候选资源名/端口及验证状态以 README 和 candidate-validation.md 为准。

## Scope and calibrated workload

This analysis uses only the isolated `order_api_perf_baseline` MySQL 8.4 database, fixed seed `20261016`, 1,000 synthetic products, 25 synthetic users and pre-created sessions. No development database or real identity data is used.

The calibrated comparison uses k6 `constant-arrival-rate`, a maximum of 25 VUs, target rates of 20/100/200 requests per second, a two-second warm-up excluded from counters, and an eight-second formal sample. Each combination runs three times after a full deterministic data reset. Scheduled iterations that cannot obtain a VU are retained as `dropped_iterations` capacity evidence rather than converted into HTTP errors.

The three distributions are:

- distributed: deterministic requests across all 1,000 products;
- skewed: 80% of requests across 20% of products;
- single hotspot: every request updates product 1.

All three use sufficient stock. The finite-stock case is measured separately so an expected `insufficient_stock` response does not obscure lock behavior.

## A evidence before optimization

A is commit `09f884f23fde5219ff6af0e67c4024def80a0267`. It adds opt-in timing only; the order write sequence is the stage 8 implementation. Values below are medians of three runs.

| Distribution | Target | Actual iterations/s | Dropped | HTTP P95 | Inventory UPDATE P95 | Commit P95 | Transaction P95 | Row-lock waits | Row-lock time |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| distributed | 200/s | 199.68 | 0 | 22.33ms | 1.81ms | 10.68ms | 20.11ms | 0 | 0ms |
| 80/20 | 200/s | 199.69 | 0 | 22.71ms | 1.77ms | 10.77ms | 20.46ms | 11 | 9ms |
| one SKU | 200/s | 131.41 | 526 | 199.80ms | 184.67ms | 7.05ms | 197.72ms | 1,074 | 178,363ms |

The competing statement is:

```sql
UPDATE inventories
SET available_qty = available_qty - ?
WHERE product_id = ? AND available_qty >= ?;
```

Every single-hotspot request addresses the same `inventories` primary-key row. The same service, SQL count, pool and MySQL instance sustain the target rate when requests are distributed, while the single-hotspot run reaches the 25-VU ceiling and drops scheduled iterations. There are no pool waits. This isolates row serialization as the main bottleneck at this load.

The Commit is not the only bottleneck: its Go-side P95 is about 7.05ms in the saturated hotspot run, compared with about 184.67ms in the conditional inventory UPDATE. MySQL row-lock time is a wait-time sum across concurrent sessions and can exceed wall-clock time.

## Transaction timing semantics

Diagnostics use Go monotonic `time.Since` measurements and perform no synchronous logging or network I/O. They record only a fixed stage name, result category and duration. Passwords, tokens, user names, order IDs, SQL arguments and request bodies are never recorded.

`http_create_total`, `service_create_total` and `transaction_attempt` are nested totals. SQL stages are children of the transaction; their totals must not be added to the outer durations. Retries use a separate outcome and `transaction_retry_delay` series. The collector is capped at 100,000 observations per stage/outcome series and is disabled unless both local diagnostics flags are enabled.

At 100 requests/s distributed writes on B, three-run medians were:

| Collector | Iterations/s | P50 | P95 | P99 |
|---|---:|---:|---:|---:|
| enabled | 99.925 | 15.507ms | 17.707ms | 26.532ms |
| disabled | 99.923 | 15.462ms | 17.624ms | 22.298ms |

The recorder itself averaged about 0.985 microseconds per observation. P50 and P95 differences were approximately +0.32% and +0.51%; P99 was +18.97%, but each run had only about 800 samples and short-run tails varied materially. This is not enough evidence to claim a stable P99 cost.

## Performance Schema boundaries

The runner captures snapshots immediately before and after each formal sample. Reported row locks, transaction timers, Commit timers and table I/O are per-run deltas; migrations, seeding and warm-up are outside these differences. `data_lock_waits` remains a point-in-time gauge and may be zero after a completed run even when the cumulative delta proves waits occurred.

Prepared DML text was not reliably available in the statement digest table on this local MySQL configuration. Go stage timing, global InnoDB row-lock deltas, deterministic single-row targeting and table I/O are therefore used together rather than attributing contention from an average Commit alone.
