# Performance test entry points

These bounded local workloads target only the isolated `order_api_perf_*` environment created by `scripts/performance-start.ps1`. The k6 script never registers or logs in during a measured iteration; disposable sessions are generated during the seed step and kept in `.tmp/performance/manifest.json`.

`workload.js` supports the stage 8 scenarios plus calibrated `order-create-distributed`, `order-create-skewed`, `order-create-hotspot`, and `order-create-hotspot-limited` scenarios. The PowerShell runner enforces at most 25 VUs, 200 requests/s for fixed-arrival runs, 60 seconds, and a 3-second request timeout. `shared-iterations` is a finite burst and has no requests/s setting; its rate must not be presented as a steady-state limit.

Historical stage 9 A/B results are retained in `results/` with their original tested source identifiers. This source-only candidate contains the current implementation, not the old A revision or Git history. Merely running both `a3` and `b` labels on this snapshot does not reproduce an A/B comparison. Use a separately reviewed implementation for any new comparison and record its source SHA-256.

For a bounded run of this candidate only (creates/reset synthetic data in the dedicated candidate performance instance):

```powershell
./scripts/performance-start.ps1 -ResetData -CompetitionStock 1000000
./scripts/run-performance.ps1 -Scenario order-create-hotspot -Model constant-arrival-rate -Rate 50 -Duration 10s -RunID candidate-smoke
```

The candidate uses MySQL port 13308 and API port 28081 with showcase-specific resource names. These commands were not run during candidate preparation. Historical aggregates are not new measurements.

The transaction collector is enabled only when both `DIAGNOSTICS_ENABLED` and `TRANSACTION_DIAGNOSTICS_ENABLED` are true. It performs no synchronous log I/O and exposes only aggregate durations on the loopback diagnostic routes. A production/default environment keeps both flags false.

Do not invoke the script against a shared, development, production, or public endpoint. See `docs/performance-baseline.md` for stage 8 and `docs/performance-optimization.md` plus `docs/hotspot-contention-analysis.md` for the calibrated A/B results.
