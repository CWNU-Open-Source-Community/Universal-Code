# Agent browser live view origins 投影（ADR 0195 Phase 4 尾片）：OCR delegate 复审

日期：2026-11-06。基线：`39772941`（PR #541 分支，Phase 4 CLI slice）。
主题：Phase 4 尾片 "live view origin display"——`AgentBrowserStatusView` 投影
`Capabilities.DeclaredOrigins`（omitempty），openapi/schema.d.ts 再生成，live view
双语边界声明（available 且非空时显示，live view 画布之前）；refusal 会话不显示
（无边界可言）。unscoped Full 会话字段隐藏（omitempty + 上游 nil）。

## 覆盖范围

6 文件：`internal/httpapi/agent_browser.go`（投影）、`openapi.go`（描述）、
`docs/openapi.json`、`web/src/api/schema.d.ts`（再生成）、
`web/src/v2/components/agent-browser.tsx`（显示）、`agent_browser_test.go`（投影
正反两分支测试）+ web 组件测试两条（正例渲染/不可用与无声明隐藏）。

## 发现与修复（评审子代理报告）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| P2 | web 组件测试缺失（仓库先例：UI 改动同步改测试；新增条件 note 的正例分支零覆盖）。 | 随片补两条 it()：scoped 会话渲染双语边界声明 + origin 列表；unscoped 与 unavailable 会话 note 不存在（两次独立渲染规避 refetch 不触发）。 |
| P3 | `run show` 不回显声明的 origins。 | 记录为 Phase 4 一致性跟进（CLI 展示非本片必须；run.Config 已有规范集可直接打印）。 |
| P3 | refusal 文案未提示"可声明来源"的补救路径。 | 留待 Phase 3 origin-add 操作面落地时统一改写（避免提示尚无入口的动作）。 |

评审确认（可合，无阻塞）：DeclaredOrigins 全仓写入点唯一收敛到
currentAuthority 的 scope 规范化构造（无不可信来源，透传成立、无需 redact/
重排/截断——与同函数 ProcessScope/LastAction 透传先例一致）；unscoped 与
refusal 会话不投影（上游 nil + omitempty + 测试锚定）；openapi 契约纯 additive
（process_scope 字段先例一致，无 registry/版本动作）；web 显示条件三重收敛、
轮询 refetch 自然覆盖未来 Phase 3 的声明变更；golden 契约测试与 check:api
逐字节把关无手编 schema；desktop 走同一 HTTP 端点无需改动。

## 验证

- `go build ./...`、`go vet ./internal/httpapi/`；`go test ./internal/httpapi/
  -count=1` 全绿（233s 全包）。
- `go run ./cmd/cyberagent api openapi --output docs/openapi.json` +
  `npm run generate:api` + `npx tsc --noEmit` + `npm run check:api`（提交后
  diff 为零）+ `npx vitest run agent-browser.test.tsx` 25/25。
