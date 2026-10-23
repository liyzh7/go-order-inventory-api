# Stage 9 performance optimization

> 历史合成实验记录：下文测试和环境属于原注明的受测版本，不是本候选重新执行结果。原 Git 历史未随快照提供；当前候选资源名/端口及验证状态以 README 和 candidate-validation.md 为准。

## Stage 8 calibration

The committed stage 8 raw summary identifies code commit `354dadd50b1d5e6f67783127c3fac8465ee20cf4`; report commit `6e4f60adccdf4486bd4d61a0190b963f4f6927dd` only records the results.

The 200 requests/s validation in stage 8 applied to the `RATE` parameter of `constant-arrival-rate`. The reported inventory competition, idempotency and cancellation rates above 200 requests/s came from finite `shared-iterations` bursts, for which `RATE` and configured duration do not cap throughput. They were valid burst measurements but not steady-state capacity.

Stage 8 used 20 samples for idempotency and 100–251 samples per round for most other scenarios. Those P99 values are retained but marked as limited samples. Its Performance Schema report was cumulative across container startup, resets and workloads, so it was supporting evidence rather than a per-scenario measurement.

Before changing code, the original scripts were reproduced for three rounds:

| Scenario | Stage 8 median | Reproduction median | Interpretation |
|---|---|---|---|
| order create, target 20/s | 20.00 req/s, P50 19.03ms, P95 81.79ms | 20.13 req/s, P50 19.68ms, P95 37.23ms | target rate reproduced; short-window tail varied |
| 200-request/100-stock burst | 209.17 req/s, P50 98.96ms, P95 149.23ms | 169.60 req/s, P50 119.12ms, P95 173.02ms | result is volatile and mixes 50% expected rejection |

The calibrated stage 9 workload therefore uses fixed arrival rate, sufficient stock for contention analysis, three deterministic distributions, per-run counter deltas and three rounds per cell. Stage 8 files remain unchanged.

## Selected optimization

A order flow inserted each order item immediately after deducting the matching inventory. The first inventory lock therefore remained held while the item snapshot INSERT ran.

B commit `96ff2506a5728fc0b90fe8c46b9e954c1d0dfa58` performs all order-item snapshot INSERTs before the first inventory UPDATE, then deducts inventories in the same ascending product-ID order and commits.

This is a transaction-local reorder, not a consistency relaxation:

- order header, item snapshots and every inventory update remain in one MySQL transaction;
- insufficient stock or any later SQL error rolls back the previously inserted header and items;
- conditional stock update and `RowsAffected` verification are unchanged;
- product data still comes from the transaction and snapshots remain immutable;
- user-scoped idempotency, deadlock retry and uncertain Commit reconciliation are unchanged;
- inventory lock acquisition remains ascending by product ID;
- no constraint, lock, transaction or authorization check was removed.

Rejected alternatives were a pool increase (there were zero pool waits), a new index (plans already use primary/unique indexes), a weaker isolation/constraint scheme, and Redis/distributed locking. None addresses serialization of one authoritative MySQL inventory row without adding consistency risk.

## A/B result

A and B use the same machine, MySQL container and configuration, fixed seed, product/user counts, 25-VU maximum, 20/100/200 target rates, two-second warm-up and eight-second formal sample. Each cell contains all three runs; the comparison uses the median, not the best run. Percentage difference is `(B median - A median) / A median * 100`.

| Distribution | Target | A actual/s | B actual/s | Throughput Δ | A P95 | B P95 | P95 Δ | A dropped | B dropped |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| distributed | 20 | 20.06 | 20.00 | -0.28% | 79.39ms | 28.76ms | -63.77% | 0 | 0 |
| distributed | 100 | 99.92 | 99.93 | 0.00% | 17.55ms | 17.71ms | +0.88% | 0 | 0 |
| distributed | 200 | 199.68 | 199.70 | +0.01% | 22.33ms | 20.81ms | -6.80% | 0 | 0 |
| 80/20 | 20 | 20.00 | 20.00 | 0.00% | 95.85ms | 63.15ms | -34.12% | 0 | 0 |
| 80/20 | 100 | 99.92 | 99.92 | -0.01% | 17.79ms | 17.31ms | -2.70% | 0 | 0 |
| 80/20 | 200 | 199.69 | 199.69 | 0.00% | 22.71ms | 20.80ms | -8.41% | 0 | 0 |
| one SKU | 20 | 20.00 | 20.08 | +0.41% | 45.98ms | 26.08ms | -43.28% | 0 | 0 |
| one SKU | 100 | 99.93 | 99.93 | 0.00% | 17.26ms | 17.95ms | +4.02% | 0 | 0 |
| one SKU | 200 | 131.41 | 159.02 | **+21.01%** | 199.80ms | 166.30ms | **-16.77%** | 526 | 307 |

The saturated single-hotspot result is the only strong optimization claim. Inventory UPDATE P95 fell from 184.67ms to 150.87ms (-18.31%). Median summed InnoDB row-lock time changed from 178,363ms to 172,800ms while B completed about 21% more transactions; row-lock wait time per successful order fell from a three-run median of 166.39ms to 133.13ms (-19.99%).

P99 for the same cell rose from 210.69ms to 223.66ms (+6.15%), driven by one B run at 241.49ms; B's other two P99 values were 223.66ms and 169.17ms. The optimization therefore improves saturated throughput and P95 but does not establish a P99 improvement. At 100/s, hotspot P95 was 4.02% worse with unchanged throughput, another reason not to generalize the saturated result to every load.

Low-load P95 values show storage/Commit spikes and high natural variation in short windows. Their large percentage differences are not claimed as optimization benefits. No A/B run had an HTTP error, business error, deadlock, transaction retry, connection-pool wait or consistency failure.

## Correctness and independent scenarios

Every one of the 54 formal A/B runs verified:

- order and item deltas equal successful order responses;
- total inventory reduction equals successful quantity;
- no negative stock, amount mismatch, orphan item, itemless order or user-scoped duplicate idempotency key;
- zero HTTP and unexpected business errors.

Three separate finite-stock B runs targeted 200/s with stock 500. Each created exactly 500 orders, reduced stock by exactly 500 and returned the remaining completed requests as `insufficient_stock`; no negative stock or partial order appeared. The three rejection counts were 972, 996 and 970.

The independent closed-model run used 20 fixed VUs and a 100ms iteration pause. It achieved 151.04 iterations/s with P50 28.02ms, P95 44.04ms and no dropped iterations. It is not compared with fixed-arrival results because VU pacing changes the offered load when latency changes.

Unit tests, full MySQL integration tests and the Linux Race Detector remain the correctness gates. Large raw k6 output, local tokens, server logs and profiles stay ignored; the repository contains only the generated aggregate JSON/CSV/SVG.

Final validation on 2026-10-17 passed `gofmt`, `go mod verify`, `go vet ./...` and `go test -count=1 ./...`. The complete isolated MySQL integration suite passed in 19.910 seconds, including authentication/ownership, user-scoped idempotency, concurrent stock deduction, multi-item rollback, cancellation, 20-way concurrent cancellation and trigger-based rollback tests. Linux `go test -race -count=1 ./...` passed in the project container with Go 1.27.2 and GCC 12.2.0. A final read-only check of the preserved performance database reported 1,502 orders and 1,502 items, zero negative inventories, amount mismatches, orphan items, itemless orders or duplicate user-scoped idempotency keys.

## Remaining bottleneck and Redis decision

The authoritative single-row inventory update still serializes a sufficiently hot SKU. B reduces the lock-hold tail but does not remove this database consistency boundary. Longer steady-state tests and more than three rounds would be required for capacity planning or a stable P99 conclusion.

There is not enough evidence to introduce Redis. Caching cannot safely replace the conditional MySQL deduction, and read queries are not the observed bottleneck. A future architectural change would need a separately specified reservation model, recovery rules and correctness tests; it should not be justified by this local eight-second benchmark alone.
