# ADR 0101: 金额预算、价格快照与 Provider 资格诊断

Date: 2026-08-15

## Status

Accepted for the integer micro-USD run ledger, the operator-imported price
snapshot, and the per-endpoint qualification taxonomy described below.
Mission-level caps and vendor-published price feeds remain out of scope.

## 背景 / Context

`domain.Budget.MaxCostUSD` 一直存在但从未被执行，模型调用成本也从未被记账。
直接按浮点美元记账会引入舍入误差和不可复现的账本；价格若由 Provider 响应或
仓库文件携带，则模型输出或 Prompt 注入就能抬价或绕过预算。与此同时，
diagnostic 与 Harness qualification 的结果只在单次响应里出现，缺少一个稳定、
可持久化、可供后续视图与故障排查使用的“端点资格”分类。

## 决策 / Decision

### 1. 整数 micro-USD 账本

所有金额都保存在整数 micro-USD（1 USD = 1,000,000 micros）里，浮点数只在
边界转换一次（`pricing.USDToMicros` 用于预算输入、`pricing.MicrosToUSD` 用于
展示）。成本按天花板除法向上取整，任何非零用量都不会被舍入成免费。每次
模型调用遵循 reserve → call → settle-or-release：调用前按请求字节数与
`MaxTokens` 的保守上界预留（reserve）；成功后按实际 usage 结算并把未用部分
释放（settle）；失败或运行进入终态时释放整个预留（release）。预留、结算与
释放都在同一事务里用 CAS（`UPDATE ... WHERE consumed = ?`）推进，跨并发调用
不会超卖。

### 2. Run 级聚合与自愈

root、specialist 与 readonly fanout 共享同一个 run 级聚合
（`open = reserved - settled - released`，`remaining = cap - open`）。每次预留
或读取用量前，store 会用终态 `model.completed` / `model.failed` 事件对仍然
open 的预留做对账（settle 完成、release 失败、无活动价格时按整笔预留保守
计费），worker 在 reserve 与 settle 之间崩溃不会永久占用预算。运行进入终态时
强制执行一次 open 预留释放。Mission 当前没有预算字段；“Mission 总金额上限”
由 run 聚合覆盖，Mission 级上限留待未来引入 Mission 预算字段。

### 3. 算子导入的价格快照

价格是算子数据，不是模型输出。`price_snapshot.v1` 是唯一接受的 wire 格式：
closed schema、未知字段拒绝、RFC3339 有效期必须覆盖导入时刻且最长一年、
entry 上限 512、文档上限 64 KiB、每 provider/model 唯一、内容哈希指纹强制
重算校验。同内容导入幂等回放（返回 `replayed=true`）；新导入原子轮换为唯一
active 快照。指纹不绑定 RFC3339 有效期窗口；同一 ID 与条目仅更换窗口的导入
会被拒绝并要求新的快照 ID。导入只存在于 Go 控制面（HTTP `POST /api/v1/models/prices` 与
`provider price-import`），Provider 响应、README、Skill 与仓库文件都无法触达。

### 4. 失败关闭的金额闸门

只有 `MaxCostUSD > 0` 的 run 才启用金额闸门；一旦启用，活动快照必须携带
该 provider/model 的精确条目，否则预留失败（fail-closed）并中止调用。结算
时若条目缺失，按整笔预留保守计费而不是少算。未启用闸门的 run 完全不记账
（untracked no-op）。

### 5. 资格状态分类

每个 provider/model 端点维护一个最近一次观察到的稳定资格状态（8 个 closed
值）：`not_configured`、`available`、`protocol_mismatch`、`auth_failed`、
`network_failed`、`rate_limit`、`capacity`、`model_unsupported`。diagnostic
成功 → `available`；失败原因按稳定映射折叠（`authentication→auth_failed`、
`network→network_failed`、`rate_limit→rate_limit`、`capacity→capacity`、
`model_not_found→model_unsupported`、`protocol_incompatible→protocol_mismatch`）；
provider 未配置 → `not_configured`。状态持久化在 `provider_setting` 的
`qualification_status.<provider>.<model>` 键里，`LoadRouteSettings` 时载入，
模型可用性视图、diagnostic 与 qualification 响应都返回；从未诊断过的模型该
字段为空（omitempty，Web 显示中性“未诊断”）。

## 后果 / Consequences

- 启用金额预算的 run 在价格表缺失或过期时立即失败关闭，而不是静默放行；
  模型输出无法通过返回伪价格或伪 usage 抬高预算。
- 账本全整数、预留先行的语义使并发调用与崩溃恢复可复现、可审计；
  `monetary.budget_reserved/settled/released/exhausted` 事件进入 run 事件流。
- `run usage`、Web 模型面板与 OpenAPI 暴露稳定视图：金额字段、价格快照
  列表/导入与资格状态枚举（`latest_qualification_status` 等）。
- Mission 级金额上限、vendor 价格订阅与多币种仍不属于本决策。

