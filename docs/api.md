# HTTP API

所有响应使用统一包络：成功为 `{"data": ...}`，失败为 `{"error":{"code":"...","message":"..."}}`。

服务端为请求生成随机 `X-Request-ID` 响应头，用于关联安全事件；不信任客户端提供的同名头。安全日志不记录请求正文、完整 URL、Authorization 或其他秘密值。日志采用失败限量策略，不能作为完整业务审计记录，详见 [运维说明](operations.md)。

## 用户认证

`POST /auth/register` 和 `POST /auth/login` 均要求 JSON：`{"username":"<交互输入>","password":"<交互输入>"}`。Body 最大 4096 字节，拒绝未知字段和多个 JSON 对象。用户名去首尾空白、转小写后匹配 `[a-z0-9][a-z0-9_]{2,31}`，密码为 12～128 UTF-8 字节，不去空白或转换大小写。

- 注册成功 201：`{"data":{"id":123,"username":"alice"}}`，不自动登录。重复名返回 409 `registration_failed`，使用通用失败说明。
- 登录成功 200：`data` 包含 `user`、`access_token`、`token_type: "Bearer"` 和 `expires_at`。Token 为 crypto/rand 生成的 32 字节随机数，经无填充 base64url 编码（43 字符），只在登录时返回；数据库仅保存其 SHA-256。
- 错误密码、不存在用户或停用用户：统一 401 `invalid_credentials`。不存在用户也执行等成本 Argon2id 校验。
- `POST /auth/logout`：要求有效 Token，撤销当前 Session，返回 200 `{"data":{"logged_out":true}}`。该 Token 再使用为 401。
- `GET /auth/me`：要求有效 Token，返回当前用户 `id`、`username`。
- Token 24 小时后过期。过期、撤销、停用用户的 Session 或非法 Token 均为 401 `unauthorized`，附带 `WWW-Authenticate: Bearer`。
- 失效 Session 默认保留 7 天后可由手动 CLI 分批清理；没有自动调度。清理不改变到期/撤销判断，也不延长 Token 有效期。
- 注册/登录共享每源 IP 20 次/分钟、进程总计 60 次/分钟限流，拒绝时 429 `rate_limited`。源 IP 取直接连接，不信任 X-Forwarded-For。最多 2 个并行密码哈希操作，满额也返回 429，避免无界内存消耗；重启会清空速率窗口。
- 所有认证响应和认证后订单响应使用 `Cache-Control: no-store`。

所有订单接口必须携带且只携带一个 `Authorization: Bearer <access_token>` Header。身份由 Session 中间件校验后进入服务端 request context，`user_id` 不是订单 JSON 字段；客户端自定义 Header 不能选择身份。其他用户订单和历史无归属订单均返回与不存在相同的 404。

## `POST /orders`

请求必须使用 `Content-Type: application/json`，Body 最大 16 KiB，并提供且只提供一个 `Idempotency-Key` Header。幂等键区分大小写，只允许 1～64 个 ASCII 字节并匹配：

```text
[A-Za-z0-9][A-Za-z0-9._:-]{0,63}
```

请求示例：

```http
POST /orders HTTP/1.1
Content-Type: application/json
Idempotency-Key: checkout-20261015-001
Authorization: Bearer <登录返回的 access_token>

{
  "items": [
    {"product_id": 1, "quantity": 2},
    {"product_id": 2, "quantity": 1}
  ]
}
```

规则：商品种类为 1～50，每项数量为 1～1000；商品 ID 必须为正数且不能重复。客户端不能提交价格或总金额，未知 JSON 字段会被拒绝。服务从事务内的商品记录生成 SKU、名称和单价快照。

首次成功返回 HTTP 201：

```json
{
  "data": {
    "id": 1,
    "order_no": "ORD20261015123000a1b2c3d4e5f6",
    "status": "CREATED",
    "total_amount_cents": 15498,
    "items": [
      {
        "product_id": 1,
        "sku": "BOOK-GO-001",
        "name": "Go Backend Engineering",
        "unit_price_cents": 5999,
        "quantity": 2,
        "subtotal_cents": 11998
      },
      {
        "product_id": 2,
        "sku": "MOUSE-001",
        "name": "Developer Mouse",
        "unit_price_cents": 3500,
        "quantity": 1,
        "subtotal_cents": 3500
      }
    ],
    "created_at": "2026-10-15T12:30:00Z"
  }
}
```

示例中的 ID、订单号、时间和金额仅展示结构，不是测试结果。同一用户、同一幂等键与同一规范化业务请求会返回原订单、HTTP 200 和 `Idempotency-Replayed: true`。商品输入顺序不影响摘要。同一用户同键对应不同请求为 409，不同用户可独立使用同键。

## 错误约定

| HTTP | code | 含义 |
| --- | --- | --- |
| 400 | `invalid_json` | JSON 非法、包含未知字段、多个对象或超过大小限制 |
| 400 | `invalid_argument` | 商品项、数量、重复 ID 或金额边界非法 |
| 400 | `invalid_idempotency_key` | 幂等键缺失、重复或格式非法 |
| 401 | `unauthorized` | 缺失、非法、已撤销、过期 Token 或用户已停用 |
| 401 | `invalid_credentials` | 登录失败，原因统一隐藏 |
| 409 | `registration_failed` | 注册未能完成，包括重复用户名 |
| 429 | `rate_limited` | 认证请求过多或密码处理并行度达到上限 |
| 404 | `product_not_found` | 至少一个商品不存在 |
| 404 | `order_not_found` | 订单不存在 |
| 409 | `product_inactive` | 至少一个商品已停用 |
| 409 | `insufficient_stock` | 至少一个商品库存不足 |
| 409 | `idempotency_conflict` | 同一个幂等键已用于不同业务请求 |
| 409 | `order_state_conflict` | 当前状态不支持取消 |
| 409 | `inventory_overflow` | 回补会超过库存 INT UNSIGNED 上限，整个取消回滚 |
| 415 | `unsupported_media_type` | Content-Type 不是 JSON |
| 504 | `request_timeout` | 请求上下文取消或超时 |
| 503 | `temporarily_unavailable` | 数据库不可用、事务重试耗尽或提交结果无法确认；创建使用原幂等键重试，取消使用原订单 ID 重试 |
| 500 | `internal_error` | 未分类的服务端错误 |

`GET /health`、`GET /products` 与 `GET /products/:id` 的使用示例见项目 README。

## `GET /orders/:id`

认证后订单 ID 必须是正整数，非法为 400；不存在、不归当前用户或历史无归属订单均为 404。成功返回 200，`data` 结构与创建响应一致：订单号、当前状态、整数分总金额、UTC 创建时间，以及按商品 ID 升序排列的全部明细。商品名称、SKU、价格取自下单快照，不受商品后续修改影响。响应不包含幂等键或请求摘要。

使用单条 SQL JOIN 获取订单头与明细，拥有同一 InnoDB 语句读视图。查询可在并发取消提交前看到完整 `CREATED` 快照，也可在提交后看到完整 `CANCELLED` 快照。

## `POST /orders/:id/cancel`

使用 URL 中订单 ID，无需 Body 或 `Idempotency-Key`。首次取消与重复取消均为 200，返回完整订单结构，`status` 为 `CANCELLED`；已经取消时增加 `Cancellation-Replayed: true`。不存在为 404，非法 ID 为 400，状态不支持为 409。订单 ID 就是此操作的自然幂等标识。

```powershell
$headers = @{ Authorization = "Bearer $($login.data.access_token)" }
Invoke-RestMethod -Method Get -Uri "http://127.0.0.1:18080/orders/$($created.data.id)" -Headers $headers
Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:18080/orders/$($created.data.id)/cancel" -Headers $headers
```

`CREATED` 表示订单、快照明细和库存扣减已经原子提交；`CANCELLED` 表示取消及库存回补已经原子提交。金额和明细作为历史记录保留，取消不执行支付、退款或删除订单。

## 本地安全限制

所有接口仅面向本机模拟环境。已有基础认证和订单归属授权，但仍要求字面量回环监听 IP，并拒绝其他地址启动。不得代理到公网、通过隧道公开访问或用于真实客户订单。退出或停用用户会阻止后续认证，不取消已经通过中间件的在途事务。

