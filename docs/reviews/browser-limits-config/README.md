# 浏览器会话预算的 .prayu 窄化：OCR delegate 复审

日期：2026-11-05。基线：`6a10b7c8`（origin/main，含 #519–#524 与其他会话对隔离区的改进）。
评审对象：PR 分支 Qiyuanqiii/browser-limits-config（单提交，9 文件 / 331 行）。
主题：#491 P2 的预算运维项（issue 问题 #7："5 分钟 / 500 请求 / 8 origin 对'调一个
bug'不够"）。`.prayu/config.yaml` 新增 `browser_limits` 段——操作者对 builtin profile
预算（8 origins / 4096B URL / 500 req / 8 MiB 响应 / 16 MiB 下载 / 300000 ms）的窄化：
零值/缺省字段保留 profile 值、至少一个正整数、只窄不宽（widen 在 session plan 构建
时以命名错误失败；恰等于 ceiling 合法为 no-op——与 budget.max_turns 的 strictly-lower
差异已注释锚定并有测试）。SessionPlan 新增 `limits_override`（进 plan 指纹），验证器
从 override+profile 重算期望 limits，篡改变宽即使重签指纹仍被拒（不可超越 builtin
ceiling 的不变量无条件成立，信任模型已注释）。

OCR 负责文件选择（`ocr delegate preview --from origin/main --to HEAD`：7 个可审生产
文件）与规则解析（Rule Group 1 system Go），主 agent 与独立评审子代理实际阅读判断。

## 覆盖范围

纳入审查 10 个（评审记录不计入）：
- 生产代码（7 个，OCR 检出）：`internal/browserruntime/plan.go`、
  `internal/projectconfig/projectconfig.go`、`internal/application/browser_runtime.go`
  （browserLimitsOverrideForRun 解码 helper + safe-web 接线）、
  `internal/application/full_cdp_runtime_service.go` 与 `full_cdp_service.go`
  （另两个调用方接线）、`configs/project-config.schema.json` + example.yaml。
- 测试（2 个，人工补入）：`internal/browserruntime/plan_test.go`
  （窄化矩阵/篡改拒绝/ProfileToken 不变性/等值 no-op）、
  `internal/projectconfig/projectconfig_test.go`（TestBrowserLimitsValidateAndNarrowPassthrough）。
- 工作树中其他会话遗留的 gofmt 残留（dependency_wait.go、child_task_*.go 等）不在
  本提交内，不在评审范围。

具体明细见 [coverage.json](coverage.json)。

## 发现与修复（评审子代理报告，全部已修复并复测）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| F1，P3 | schema 自相矛盾：description 写 "zero/absent fields keep the profile value" 但字段 `minimum: 1` 拒绝 0；且缺 `minProperties: 1`（空对象过 schema 但被 Go 拒）。 | schema 改 `minimum: 0` + `minProperties: 1`，description 补"至少一个正整数、widen 在 plan 构建时失败"，与 Go 语义对齐。 |
| F2，P3 | override 恰等于 profile ceiling 被静默接受为 no-op（`> base` 才拒），与 budget.max_turns 的 strictly-lower（`>=` 拒）语义不同，且无注释锚定与边界测试。 | 注释写明"等于 ceiling 合法且为 no-op"及理由（浏览器 ceiling 即 profile 本身，等值既不宽也不窄）；补等值通过用例。 |
| F3，P3 | 指纹为无密钥 sha256：同时篡改 override+limits 并重签指纹可使 Validate 通过（回到 builtin ceiling）；"篡改 override 变宽→拒绝"仅在超 profile 时成立。 | LimitsOverride 注释写明信任模型（篡改上限=builtin ceiling，不变量无条件成立；plan 仅在构建时同步验证，无不可信存储回读路径）。 |
| F4，P3 | usage.md 的 .prayu 章节声称 "Any widening attempt aborts Run creation"，但 browser_limits 的 widening 在 plan 构建时才失败；且 browser_limits 未见于该章节。 | 章节补 browser_limits 语义（零值=保留、只窄化、ceiling 检查发生在 plan 构建）并修正表述（browser_limits 的 widening 在 plan 构建时以命名错误失败）。 |

评审确认（No blocking findings）：ProfileLimits 6 字段全覆盖、builtin 三 profile 共享
严格为正的 baseLimits（无 base=0 边界）；LimitsOverride 经 `json.Marshal` 整 plan 自动
入指纹；`Validate` 从 override+profile 重算 expectedLimits 并复验指纹/Token/isolation/
scope/backend；ProfileToken 不含预算（token 标识一次性 profile 目录身份，预算不影响
profile 内容，测试断言该不变性）；执行侧消费窄化后的 session.Limits（cdp.go、
full_cdp_transport.go），LimitsOverride 仅为验证溯源；三个调用方统一经
browserLimitsOverrideForRun 解码（空/畸形/缺段 nil 回退 builtin ceiling 兜底）；
projectconfig validate 镜像 budget 分支（负值/全零拒绝）、Narrow 透传、schema/example
一致；go test browserruntime+projectconfig 全过。

## 验证

- `gofmt -l` 无输出（改动包）；`go vet` 三包无告警；`go build ./internal/...` 通过。
- `go test ./internal/browserruntime/ ./internal/projectconfig/ -count=1` 全过（窄化矩阵、
  篡改拒绝、等值 no-op、ProfileToken 不变性、projectconfig 解析校验）。
- `go test -race ./internal/application/ -run "TestAgentBrowserUpload|
  TestAgentBrowserDownload|TestBrowserHeaded|TestBrowserSessionLifetime"` 通过
  （相邻浏览器功能无回归）。

## 复审附记（2026-11-05，第二轮）

独立复审发现 F2 的"最终处理"与代码不符：plan_test.go 此前只有"全零 override = no-op"
用例，恰等 profile ceiling 的通过分支没有任何测试锚定（上文覆盖范围里"等值 no-op"的
表述实际指全零用例，不准确）。F2 所述的注释锚定确实存在，本轮已补上恰等 ceiling 的
通过用例并断言其仍进入 plan 指纹。另按复审意见补两处：
`browserLimitsOverrideForRun` 对畸形 pinned 快照的静默回退改为 `slog.Warn` 留痕（快照
在 Run 创建时已 validate + 指纹固定，触发即存储损坏，但操作者窄化意图被静默忽略应可
观察）；usage.md 补写作用面——browser_limits 只约束走 BuildSessionPlan 的会话面
（Safe Web 与两个 Full CDP 产品），agent-managed browser 工具链不在该预算内。
验证：`go test ./internal/browserruntime/ ./internal/projectconfig/ -count=1` 全过。
