# 阶段 6 验收报告

> 历史合成实验记录：下文测试和环境属于原注明的受测版本，不是本候选重新执行结果。原 Git 历史未随快照提供；当前候选资源名/端口及验证状态以 README 和 candidate-validation.md 为准。

日期：2026-10-16（Asia/Shanghai）。环境：Windows amd64 / Go 1.27.2、Docker Engine 29.8.2、MySQL 8.4.12。

## 实际交付

注册、登录、退出、当前用户 API；Argon2id 密码哈希；数据库 Session 与 Bearer 中间件；订单用户归属与用户级幂等；历史无归属订单隔离；本地认证限流与有界密码处理并行度。保持回环地址监听。

密码使用 x/crypto Argon2id（m=19456 KiB、t=2、p=1），盐 16 字节、结果 32 字节、PHC 参数记录。Token 由 crypto/rand 生成 32 字节，经 base64url 编码；数据库只保存 SHA-256。有效期 24 小时，支持单 Session 撤销并检查用户状态。

## 迁移和开发库

- 新增 00005_create_auth.sql：users、auth_sessions、外键/唯一约束/索引/检查约束。
- 新增 00006_order_ownership.sql：orders.user_id nullable 外键、用户级幂等复合唯一索引和历史键生成列唯一索引。
- 迁移 1～4 没有改动。
- 隔离库实际完成 4→6→4→6，以及全新 0→6→0→6。
- 跨用户合法同键存在时 Down 拒绝恢复旧全局唯一索引，测试确认两条用户订单仍保留。
- 开发库实际 4→6 成功，升级前后全部旧订单字段、明细、库存和时间戳一致；订单 1/2 的 user_id 均为 NULL，未分配给任何新用户。
- 最终 MySQL 版本 8.4.12、迁移版本 6；容器 healthy。

## 自动化测试

实际通过：

```powershell
gofmt -w internal cmd tests
go mod verify
go vet ./...
go vet -tags=integration ./...
go test -count=1 ./...
go test -tags=integration -count=1 ./tests/integration
```

完整 MySQL integration 结果：`ok internshiplab/order-api/tests/integration 16.265s`。

认证覆盖：正常注册/登录、归一化后的重复名、错误密码与不存在用户相同错误、非法 Token、到期、退出、停用用户、密码安全哈希、Token 数据库仅有哈希、用户名数据库规范化 CHECK、Session 用户外键、严格请求格式、并发限流、伪造 X-Forwarded-For 无法绕过 IP 限流。

授权覆盖：A 自己查询和取消成功；B 查询/取消 A 订单均 404 且状态库存未变；JSON user_id 被拒绝，X-User-ID 不改变身份；未认证创建/查询/取消均 401；旧 NULL 所有者订单不可读也不可取消。

幂等覆盖：A 同键同请求重放、不同请求冲突；A/B 同键各建一单；12 个并发请求（每用户 6 个）同键最终仅 2 个有归属订单、扣减 4 件库存，没有跨用户返回同一个订单。提交未知结果的查询辅助函数和取消故障驱动均断言 SQL 包含正确 user_id，属于受控单元测试，不宣称已复现真实网络故障。

阶段 4/5 回归使用真实 API 注册和登录签发的 Token，经真实 Session middleware 后访问订单，没有关闭认证：并发抢库存、多商品事务回滚、同键竞争、快照一致性、首次/重复取消、20 并发取消、共享库存多订单取消、SQL 故障 Trigger 回滚及重试均通过。

## 真实 HTTP 验收

执行 `scripts/verify-auth-http.ps1`。密码在内存中随机生成，不写入文件；登录 Token 不打印、不写入 Git。以下为真实返回：

- 注册 201：用户 1 `smoke_a_0aece2730c41`、用户 2 `smoke_b_7d0fcf5e734a`。
- 登录各 200，token_type 为 Bearer，24 小时有效。敏感 Token 未记录。
- 两用户使用相同幂等键各返回 201，订单 ID 分别 3 和 4，各 5999 分。
- 用户 1 重放订单创建返回 200、Idempotency-Replayed=true。
- 用户 1 详情返回：

```json
{"data":{"id":3,"order_no":"ORD202610160319440b28fde7bfa0","status":"CREATED","total_amount_cents":5999,"items":[{"product_id":1,"sku":"BOOK-GO-001","name":"Go Programming Essentials","unit_price_cents":5999,"quantity":1,"subtotal_cents":5999}],"created_at":"2026-10-16T03:19:44.590266Z"}}
```

- 用户 2 查询、取消订单 3 各 404；无 Token 查询为 401。
- 两用户各取消自己的新订单返回 200 CANCELLED，商品 1 库存恢复 49→49。
- 两用户退出各 200，随后 /auth/me 均 401。
- 最终数据库：订单 1/2 保持历史状态与金额；订单 3/4 为 CANCELLED，user_id 分别 1/2；两个 Session 均 revoked，token_hash 长度均 32 字节。
- 临时测试库残留 0，开发库 Trigger 数 0。HTTP 验收服务已停止，18080 无监听。没有删除旧业务数据。

## Race Detector

Windows 原生仍失败：CGO_ENABLED=0、gcc=NOT_FOUND，`go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`。

替代环境已实际通过：

```powershell
./scripts/test-race-linux.ps1
```

- 官方 `golang:1.27.2-bookworm`，镜像摘要 `sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61`。
- `go version go1.27.2 linux/amd64`，CGO_ENABLED=1，GCC 12.2.0。
- `go test -race -count=1 ./...` 全部通过，包括并发限流测试。
- 这是无 integration 标签的单元测试竞态检测；MySQL integration 已独立执行，但未在 race 模式执行。
- 容器非 root、只读根文件系统、cap-drop ALL、无新权限、内存/PID 上限；仅只读挂载 Go/SQL 源码快照，不挂载 .env、Git、宿主家目录或 Docker socket。未修改 Windows 全局环境。

初次沙箱外权限审核两次超时，之后正常 Docker 启动和测试获准执行；超时不计为测试通过或业务失败。

## 当前限制

仍只面向本地模拟；没有公网 TLS、密码恢复、MFA、全部设备退出、会话自动清理、支付或退款。限流按进程计数，重启清零。退出和停用阻止后续请求，不追溯撤销已认证的在途事务。nullable 历史兼容列允许受信任的直接 SQL 写入 NULL，应用所有新订单都要求认证用户；未来历史归属处理需要明确审计与授权。
