# Usage CSV 导出：OCR delegate 复审

日期：2026-11-05。基线：`b58f8981db87dd983e221839283e43f145b92b03`（main）。
评审对象：PR #526（head 5ef035d7）。
主题：Settings → Usage 新增 "Export CSV"——把已加载的 usage 投影（totals/按日/按模型/
近期会话 + 全局成本账本）导出为 BOM 前缀 UTF-8 CSV。CodeRabbit 此前的 minor（CSV 公式
注入）已由提交 5ef035d7 修复并标记 ✅。

OCR 负责文件选择（`ocr delegate preview --from main --to pr-526`：3 个可审 TS/CSS
生产文件）与规则解析（Rule Group 1 system TS/JS/TSX），主 agent 与独立评审子代理
实际阅读判断。

## 覆盖范围

纳入审查 5 个（评审记录与验收文档不计入）：
- web/src/v2/usage-export.ts（新增 65 行，全文阅读）
- web/src/v2/components/usage-settings.tsx（+13/-3，全文阅读）
- web/src/v2/components/usage-settings.css（+1）
- web/src/v2/components/usage-settings.test.tsx（+85，全文阅读）
- web/src/lib/download.ts（main 既有，复用核对）
- docs/acceptance/2026-11-01-usage-export/README.md（验收记录，诚实披露截图所需的
  本地临时 tweak）
- internal/approval/bounded_grant_test.go（+767，与 #525 搭车的同一成品测试，
  逐字节相同，CI 已证明编译通过，仅作用域发现）

具体明细见 [coverage.json](coverage.json)。

## 发现与修复（评审子代理报告，全部已修复并复测）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| A-1，P3 | 导出 `catch { /* inert */ }` 完全吞错：`downloadTextFile` 因文件名/内容校验抛错时用户无任何信号，且与规则组的错误处理要求不符。 | 保留不弹窗 UX，改为 `console.warn` 记录错误（on-screen usage 仍是权威数据源，行为不变）。 |

评审确认（No blocking findings）：公式触发正则 `/^[\s\u0000-\u001F\u007F]*[=+\-@]/u`
完备（\s 已含 NBSP/FEFF 等，叠加 C0+DEL；撇号前缀在引号包裹前施加，测试含叠加场景）；
数字单元格不做触发检查正确（负数成本是数值不是公式）；RFC4180 引号+双写与 `\r\n`
行尾正确；BOM 由 settings 前置且测试断言；`UsageStatistics` schema 含
period/start_date/end_date 与全部 token 列（schema.d.ts:15971），文件名拼接不会命中
download.ts 的 `unsafeFilename`；`known_cost_micros/1e6+toFixed(6)` 与 main 上既有
$ 展示同源；+85 行测试全部真断言（21 列逐列比对、行序 totals→day→model→session、
totals 行成本 idx15-20 与 fixture 125000 micros 吻合、注入矩阵覆盖 =/@/-/前导空白/
非起始 =）；按钮 disabled 条件与测试一致。

## 验证

- `go build`/`go vet` 不涉及（纯前端）；web 测试由 CI 执行（此前 14/14 通过）。
- 作用域发现（如实记录）：`bounded_grant_test.go` 与本 PR 主题无关，属同基分支
  搭车文件（与 #525 的副本逐字节相同），维护者可决定是否随本 PR 合入。
  （复审更新 2026-11-05：该文件已随 #521 合入 main，不再出现在本 PR 的 diff 中，该作用域发现就此解决。）
