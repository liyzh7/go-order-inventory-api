# 订单服务数据库设计

状态：四张业务表与两张认证表；注册登录、用户归属和用户级订单幂等已接入

最低数据库版本：MySQL 8.4，存储引擎为 InnoDB

默认字符集：`utf8mb4`，默认排序规则：`utf8mb4_0900_ai_ci`
标识符字段会按业务语义单独指定二进制类型或大小写敏感排序规则，不能依赖数据库默认排序规则。

## 1. 当前事实与范围

现有代码包含 `GET /health`、基于 MySQL 的只读商品查询，以及订单创建、详情查询和事务取消回补。

迁移 1～4 保持原样；迁移 5 新增认证表，迁移 6 增加订单归属并升级幂等索引。当前阶段的边界是：

- 已实现只读商品查询 API，不实现商品管理后台。
- 已实现订单创建、详情查询和取消 API，不实现支付与退款。
- 不加入 Redis、消息队列、支付服务或微服务。

## 2. 核心业务约束

1. MySQL 是商品库存的唯一真相来源。
2. 商品 SKU 在 API 层去除首尾空白并统一转换为大写，只允许匹配 `[A-Z0-9][A-Z0-9._-]{0,63}`；数据库使用大小写敏感规则并通过同等 `CHECK` 阻止小写、空白或其他非法字符；订单号唯一；创建订单的幂等键按原始字节唯一。
3. 库存不能小于 0；单个订单项购买数量为 1～1000；商品单价最多为 1,000,000,000,000 分；订单总金额最多为 9,000,000,000,000,000 分。
4. 创建订单、写入订单项、扣减所有商品库存和更新订单总额必须在同一事务中完成。
5. 任意商品库存不足时，整个事务回滚，不产生部分订单或部分扣减。
6. 同一订单保存下单时的 SKU、名称与单价快照，商品后续修改不影响历史订单。
7. 订单不物理删除；商品通过状态停用，避免破坏历史关联。

## 3. ER 图

```mermaid
erDiagram
    USERS ||--o{ AUTH_SESSIONS : authenticates
    USERS o|--o{ ORDERS : owns
    PRODUCTS ||--|| INVENTORIES : has
    PRODUCTS ||--o{ ORDER_ITEMS : appears_in
    ORDERS ||--|{ ORDER_ITEMS : contains

    PRODUCTS {
        BIGINT id PK
        VARCHAR sku UK
        VARCHAR name
        BIGINT price_cents
        VARCHAR status
        DATETIME created_at
        DATETIME updated_at
    }

    INVENTORIES {
        BIGINT product_id PK_FK
        INT available_qty
        DATETIME updated_at
    }

    ORDERS {
        BIGINT id PK
        BIGINT user_id FK
        VARCHAR order_no UK
        VARBINARY idempotency_key UK
        BINARY request_hash
        VARCHAR status
        BIGINT total_amount_cents
        DATETIME created_at
        DATETIME updated_at
    }

    ORDER_ITEMS {
        BIGINT order_id PK_FK
        BIGINT product_id PK_FK
        VARCHAR sku_snapshot
        VARCHAR name_snapshot
        BIGINT unit_price_cents
        INT quantity
    }
    USERS {
        BIGINT id PK
        VARCHAR username UK
        VARCHAR password_hash
        VARCHAR status
        DATETIME created_at
    }
    AUTH_SESSIONS {
        BIGINT id PK
        BIGINT user_id FK
        BINARY token_hash UK
        DATETIME expires_at
        DATETIME revoked_at
        DATETIME created_at
    }
```

关系说明：

- 一个商品恰好有一行库存。
- 一个订单至少包含一个订单项。
- 一个商品可以出现在零个或多个订单项中。
- `order_items` 使用 `(order_id, product_id)` 复合主键，阻止同一订单重复保存同一商品；API 层仍需拒绝或合并重复商品输入。

外键只能保证“库存必须属于已有商品”和“订单项必须属于已有订单”，不能反向保证新商品一定有库存行、订单一定有明细。后续 service 必须在同一事务中创建这些成组数据。

## 4. 表设计

### 4.1 `products`：商品主数据

| 字段 | 建议类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | 内部短主键，减小 InnoDB 二级索引负担 |
| `sku` | `VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin` | NOT NULL, UNIQUE, CHECK | 对外稳定商品编码；API 写入前 `TrimSpace` 并转换为大写，数据库只接受 `[A-Z0-9][A-Z0-9._-]{0,63}` |
| `name` | `VARCHAR(255)` | NOT NULL | 商品名称，要求去除首尾空白后非空 |
| `price_cents` | `BIGINT UNSIGNED` | NOT NULL, CHECK 1～1,000,000,000,000 | 以最小货币单位保存，避免浮点误差并限制后续乘法边界 |
| `status` | `VARCHAR(16)` | NOT NULL, CHECK | 允许值：`ACTIVE`、`INACTIVE` |
| `created_at` | `DATETIME(6)` | NOT NULL | UTC 创建时间，微秒精度 |
| `updated_at` | `DATETIME(6)` | NOT NULL | UTC 最后更新时间，微秒精度 |

索引：

- `PRIMARY KEY (id)`
- `UNIQUE KEY uk_products_sku (sku)`
- `KEY idx_products_status_id (status, id)`：支持按状态过滤并按主键做游标分页。

### 4.2 `inventories`：库存事实

| 字段 | 建议类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `product_id` | `BIGINT UNSIGNED` | PK, FK | 一件商品恰好一行库存 |
| `available_qty` | `INT UNSIGNED` | NOT NULL, DEFAULT 0, CHECK >= 0 | 当前可售库存，是库存真相来源 |
| `updated_at` | `DATETIME(6)` | NOT NULL | 最后一次库存变化时间 |

外键：

- `product_id -> products(id) ON DELETE RESTRICT ON UPDATE RESTRICT`

库存独立成表的原因是让高频库存写入只锁定窄行，避免与商品名称、价格等低频主数据更新互相干扰。替代方案是把库存直接放在 `products`，表更少，但热点写入和商品管理耦合更紧。

### 4.3 `orders`：订单头

| 字段 | 建议类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | 内部数据库主键 |
| `user_id` | `BIGINT UNSIGNED NULL` | FK users(id) RESTRICT | NULL 只用于历史无归属数据；新 API 订单必须来自有效认证上下文 |
| `order_no` | `VARCHAR(32)` | NOT NULL, UNIQUE | 对外展示的订单号，与内部主键解耦 |
| `idempotency_key` | `VARBINARY(64)` | NOT NULL, UNIQUE | 客户端每次创建意图的 UTF-8 原始字节，长度为 1～64 字节；`Key` 与 `key` 是不同键 |
| `request_hash` | `BINARY(32)` | NOT NULL | 规范化请求体的 SHA-256 原始 32 字节摘要 |
| `status` | `VARCHAR(16)` | NOT NULL, CHECK | 当前允许值：`CREATED`、`CANCELLED` |
| `total_amount_cents` | `BIGINT UNSIGNED` | NOT NULL, CHECK 1～9,000,000,000,000,000 | 订单总金额，由服务根据订单项计算并进行溢出检查 |
| `created_at` | `DATETIME(6)` | NOT NULL | UTC 创建时间 |
| `updated_at` | `DATETIME(6)` | NOT NULL | UTC 最后更新时间 |

索引：

- `PRIMARY KEY (id)`
- `UNIQUE KEY uk_orders_order_no (order_no)`
- `UNIQUE KEY uk_orders_user_idempotency (user_id, idempotency_key)`：迁移 6 替换原全局唯一索引，用户内按字节唯一。
- `legacy_idempotency_key VARBINARY(64)` 是存储生成列，仅对 `user_id IS NULL` 返回原键，其他行返回 NULL；其唯一索引保留历史集合原有唯一性。
- `KEY idx_orders_status_created_id (status, created_at, id)`：用于后台按状态和时间分页；若当前 API 不提供该查询，可在迁移时暂缓创建。

状态约定：`CREATED` 表示订单及库存扣减已经在同一事务中提交；本阶段不接真实支付。唯一允许的取消转换是 `CREATED -> CANCELLED`。取消事务先锁定订单并通过带状态条件的更新取得一次且仅一次的转换权，再按商品 ID 升序回补全部库存；任一步失败都回滚。重复取消返回已有 `CANCELLED` 状态，不能再次回补。未来模拟支付需要单独设计状态迁移，本阶段不实现支付或退款。

### 4.4 `order_items`：订单明细与价格快照

| 字段 | 建议类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `order_id` | `BIGINT UNSIGNED` | PK, FK | 所属订单 |
| `product_id` | `BIGINT UNSIGNED` | PK, FK | 下单商品 |
| `sku_snapshot` | `VARCHAR(64)` | NOT NULL | 下单时 SKU 快照 |
| `name_snapshot` | `VARCHAR(255)` | NOT NULL | 下单时商品名称快照 |
| `unit_price_cents` | `BIGINT UNSIGNED` | NOT NULL, CHECK 1～1,000,000,000,000 | 下单时单价快照 |
| `quantity` | `INT UNSIGNED` | NOT NULL, CHECK 1～1000 | 购买数量 |

主键与外键：

- `PRIMARY KEY (order_id, product_id)`
- `order_id -> orders(id) ON DELETE RESTRICT ON UPDATE RESTRICT`
- `product_id -> products(id) ON DELETE RESTRICT ON UPDATE RESTRICT`
- `KEY idx_order_items_product_id (product_id)`：显式支持商品侧关联与外键检查。

不保存 `subtotal_cents`，因为它等于 `unit_price_cents * quantity`，可在查询时计算。订单头的总金额属于读取频繁且需要冻结的汇总值，由创建订单事务保证等于各订单项小计之和。

## 5. 并发库存扣减方案

当前创建订单事务采用以下顺序：

1. 校验输入，并把商品 ID 去重后按升序排列。
2. 开启数据库事务，先查询是否已有相同幂等键；若存在则比较请求摘要并返回已有结果。
3. 按商品 ID 升序读取并共享锁定商品行（`SELECT ... FOR SHARE`），确认商品为 `ACTIVE`，保存价格快照并计算最终总金额。共享锁避免商品价格或状态在本事务提交前被修改。
4. 用已经计算完成的总金额插入带唯一 `idempotency_key` 的订单头。并发请求仍可能同时通过第 2 步，因此必须以唯一索引冲突作为最终裁决。发生重复键错误时，必须先回滚当前事务；随后在新事务或自动提交查询中重新读取已提交订单，比较 `request_hash` 后返回已有结果或幂等冲突。不能在已经失败的事务中继续读取。
5. 按商品 ID 升序逐条执行原子条件更新：

```sql
UPDATE inventories
SET available_qty = available_qty - ?,
    updated_at = CURRENT_TIMESTAMP(6)
WHERE product_id = ?
  AND available_qty >= ?;
```

6. 每次更新必须恰好影响 1 行；影响 0 行表示商品库存记录不存在或库存不足，整个事务回滚。
7. 使用第 3 步保存的商品快照写入订单项，然后提交事务。

InnoDB 的 `UPDATE` 会对命中的库存行加排他锁。条件判断和扣减由同一 SQL 完成，因此不会出现两个请求都先读到相同库存、随后分别扣减而导致超卖。多商品按相同顺序加锁可降低死锁概率，但不能消除死锁；Go 层对 MySQL 1213 和 1205 做最多三次整事务尝试，并在上下文取消时停止。

替代方案是先对库存执行 `SELECT ... FOR UPDATE`，锁定后再检查和更新。它更便于一次返回各商品的具体库存，但多一次查询，且仍需统一锁顺序。当前选择原子条件更新，因为不变量更集中、往返更少。

订单事务显式使用 `READ COMMITTED`，商品快照仍通过 `SELECT ... FOR SHARE` 锁定到事务结束，库存通过条件更新加排他锁。这样避免依赖全局隔离级别，同时减少不需要的历史版本可见性；核心正确性来自锁定读、唯一索引和条件写。

## 6. 创建订单幂等方案

作用域为认证用户：所有首次查找、唯一键竞争后的读取和 Commit 未知结果核验都同时过滤 `user_id`。客户端不可提交 `user_id`。NULL 历史行不匹配任何普通用户；同键不会返回历史行。用户与订单的外键阻止无效拥有者，但为了历史兼容列保持 nullable，直接 SQL 管理操作仍须遵守不制造新无归属订单的规约。

客户端必须通过请求头提供幂等键。服务只接受 1～64 字节并匹配 `[A-Za-z0-9][A-Za-z0-9._:-]{0,63}` 的 ASCII 字符串，再以原始字节写入 `VARBINARY(64)`；服务和数据库都不进行大小写折叠。

请求摘要的规范化过程固定如下，避免 JSON 字段顺序或空白造成不同摘要：

1. 请求只允许包含创建订单的业务字段；当前仅包含商品 ID 与购买数量，服务端价格、订单号、时间和状态不进入摘要。
2. 拒绝重复商品 ID，不自动合并，避免同一输入在不同客户端产生歧义。
3. 校验每个商品 ID 大于 0、数量在 1～1000 之间，并按商品 ID 升序排列。
4. 对规范化结构使用固定字段顺序编码：版本字节、商品数量，以及每个商品的无符号 64 位商品 ID 和无符号 32 位数量，整数统一使用大端序。
5. 对上述字节流计算 SHA-256，将原始 32 字节结果写入 `BINARY(32)`。规范化格式发生不兼容变化时必须提升版本字节。

处理规则：

- 新键：正常创建订单。
- 相同键、相同摘要：返回第一次创建的订单，不重复扣库存。
- 相同键、不同摘要：返回冲突错误，防止一个键代表两个创建意图。
- 并发相同键：数据库唯一索引只允许一个事务插入成功；另一事务收到重复键后先回滚，再读取已提交订单。

该方案适合当前单一的创建订单操作。替代方案是独立 `idempotency_requests` 表，能够记录处理中状态、响应码和响应体，适合多个写接口，但现在引入会超过最小范围。

## 7. 金额计算边界

- 商品单价必须在 1～1,000,000,000,000 分之间，单项数量必须在 1～1000 之间。
- Go 层计算单项金额前必须检查 `unitPrice > MaxOrderAmount/quantity`；累计订单金额前检查 `total > MaxOrderAmount-subtotal`，不得依赖整数溢出后的结果。
- 订单总金额必须在 1～9,000,000,000,000,000 分之间。数据库 `CHECK` 是最后防线，不能替代 Go 层的输入和溢出校验。
- API 的金额字段始终表示整数分，不接受浮点金额。

## 8. 数据类型与约束选择

- 金额使用整数分而不是 `FLOAT/DOUBLE`，避免二进制浮点舍入。若系统需要多币种或小数位不固定，可改用 `DECIMAL(19,4)` 并增加币种字段。
- 状态使用 `VARCHAR + CHECK`，比 MySQL `ENUM` 更容易通过迁移增加状态；代价是占用略多空间。
- 本项目最低版本固定为 MySQL 8.4，迁移可以依赖该版本实际执行 `CHECK` 约束；不支持忽略 `CHECK` 的旧版本。
- 时间使用 `DATETIME(6)` 并约定 UTC，避免依赖数据库会话时区；Go 连接后仍需显式设置 UTC。
- 业务唯一性用数据库唯一索引兜底，不能只依赖 Go 代码的“先查询再插入”。
- 外键使用 `RESTRICT`，避免误删商品或订单时级联破坏历史数据。

## 9. 取消订单与库存回补

取消接口执行一个新的数据库事务：

1. 锁定并读取订单及订单项，商品按 ID 升序处理。
2. 执行 `UPDATE orders SET status = 'CANCELLED' ... WHERE id = ? AND status = 'CREATED'` 并检查恰好影响一行，以此获得库存回补权。
3. 按商品 ID 升序回补每个订单项数量；任何更新失败都回滚状态转换与已完成回补。
4. 提交成功后才对外返回取消成功。影响零行时重新读取状态：已取消属于幂等成功，其他状态返回状态冲突。

取消实现复用现有状态和 `updated_at` 字段，不修改已执行迁移。库存回补采用上限条件更新并检查影响行数。

## 10. 实现状态与后续边界

当前迁移位于 `migrations`，由项目内 Goose 命令执行。迁移不会在服务启动时自动运行，破坏性 `down` 需要显式确认和精确数据库名。订单创建实现遵守：

- MySQL 是库存唯一真相来源。
- 创建订单后直接进入 `CREATED` 状态，不接入真实支付。
- 订单创建、全部库存扣减与订单项写入必须同事务提交。
- Redis、消息队列、支付和退款不在当前阶段实现。

## 11. 参考资料

- [MySQL 8.4：InnoDB 事务模型](https://dev.mysql.com/doc/refman/8.4/en/innodb-transaction-model.html)，访问日期：2026-10-14。
- [MySQL 8.4：事务隔离级别](https://dev.mysql.com/doc/refman/8.4/en/innodb-transaction-isolation-levels.html)，访问日期：2026-10-14。
- [MySQL 8.4：锁定读](https://dev.mysql.com/doc/refman/8.4/en/innodb-locking-reads.html)，访问日期：2026-10-14。
- [MySQL 8.4：CREATE TABLE、索引与约束](https://dev.mysql.com/doc/refman/8.4/en/create-table.html)，访问日期：2026-10-14。
- [MySQL 8.4：外键约束](https://dev.mysql.com/doc/refman/8.4/en/create-table-foreign-keys.html)，访问日期：2026-10-14。
- [MySQL 8.4：CHECK 约束](https://dev.mysql.com/doc/refman/8.4/en/create-table-check-constraints.html)，访问日期：2026-10-14。

本设计未复制第三方项目代码。

## 12. 用户与 Session

`users`：`id BIGINT UNSIGNED` 主键；`username VARCHAR(32) ascii_bin` 唯一，应用先 TrimSpace 再转小写，数据库 CHECK 只接受 `[a-z0-9][a-z0-9_]{2,31}`；`password_hash VARCHAR(255)` 保存带参数和随机盐的 Argon2id PHC 字符串；`status` 为 ACTIVE/DISABLED；`created_at DATETIME(6)` 为 UTC。

`auth_sessions`：主键 id、用户外键 user_id、唯一 `token_hash BINARY(32)`、UTC `expires_at`、nullable `revoked_at` 和 `created_at`。到期时间必须大于创建时间，用户和到期时间各有普通索引。只存 256 位随机 Token 的 SHA-256，不存明文。查询 Session 同时检查用户 ACTIVE、未撤销和未到期。每次请求均查数据库，退出后无需等待进程缓存失效。

阶段 7 新增手动清理命令，不修改迁移 1～6。每次运行固定 `UTC now - retention` 截止点（默认 7 天），以 `expires_at <= cutoff OR revoked_at <= cutoff` 判断可删除记录；NULL 撤销时间不命中撤销分支。使用带谓词、按 id 排序、LIMIT 的原子 DELETE，每批独立提交，活跃记录不被匹配。错误后立即停止并保留已提交批次计数，不自动回滚已完成批次。

LIMIT 限制每批删除数而非扫描成本，现有索引没有专用 revoked_at 索引，OR 与排序可能引入额外扫描/锁等待。当前有查询超时、总超时和批次数上限；大量数据前需要 EXPLAIN 和实际负载验证，再决定是否用新迁移优化索引或拆分扫描。当前不声称具备大规模清理性能指标。详见 [Session 运维规则](operations.md)。

密码参数：Argon2id v19、m=19456 KiB、t=2、p=1、salt=16 字节、hash=32 字节。当前只接受这一有界参数集合；未来提升参数需要兼容校验和登录时重哈希迁移。密码不做大小写或空白归一化。

## 13. 历史订单升级和回退

- 迁移 6 只加列/约束和更换索引，不更新订单归属、状态、金额、明细或库存。
- 历史行 user_id 保持 NULL，不提供普通用户认领接口。后续处理必须通过经审计、明确授权的管理迁移，不按用户名或新注册顺序推断所有者。
- 用户级同键合法存在时，旧全局唯一索引无法恢复。Down 使用单条原子 ALTER，遇到这种数据拒绝执行，不自动去重或删除订单。只允许在隔离测试库验证回退。
- 迁移 5 包含两个 DDL；MySQL DDL 不能视作跨语句事务。迁移失败应核对 schema 和 Goose 版本后诊断，不通过清库或强制重建修复。
