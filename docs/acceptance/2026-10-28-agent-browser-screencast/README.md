# 2026-10-28 Agent 浏览器实时画面（Issue #491 P0）

## 范围

在**不改变任何隔离边界**的前提下为 headless Agent 浏览器增加操作者只读实时画面：

- `internal/browserruntime`：受限 CDP 白名单新增 `Page.startScreencast` /
  `Page.stopScreencast` / `Page.screencastFrameAck`（target 级），新增
  `agent_browser_screencast.go` 帧泵：JPEG、≤1280×900、墙钟限速 ≤10fps、
  单观看者、512KB/帧上限、慢消费者丢帧保新鲜、每帧必 ack。
- `internal/application`：`AgentBrowserService.StreamScreencast`——run 绑定、
  不启动浏览器、会话结束/运行时退出即终止、每 Run 单观看者（冲突报 409）。
- `internal/httpapi`：`GET /api/v1/runs/{run_id}/agent-browser/screencast`
  （SSE，read token，run 绑定，15s keepalive，投影层逐字段复核）。
- `web`：`agent-browser.tsx` 新增 `<canvas>` 实时画面（指数退避重连、
  会话不匹配丢弃、失败回退到已验证截图），`waiting_user` 文案明确
  "当前版本暂不支持人工接管"。

## 保持不变的边界

- 不接个人 profile/cookie/扩展；一次性 profile、origin scope、请求级拦截、
  DNS/IP 复检全部维持原样。
- `Runtime.evaluate` 仍仅在 full CDP；白名单新增的三个方法不包含任何导航、
  求值、Cookie、请求改写或重放能力。
- 截图哈希证据链（`Page.captureScreenshot` + SHA-256 + artifact 归属校验）
  完全不变；直播流不落盘、不进入证据账本、不进入工具结果。

## 验证记录

- `go test ./internal/browserruntime/ -run TestAgentBrowserScreencast`（含 -race）：
  帧转发/参数边界/每帧 ack/超限丢帧仍 ack/二次观看者拒绝/无会话拒绝/stop 语义。
- `go test ./internal/httpapi/ -run 'TestAgentBrowserScreencast|TestOpenAPIRoutesMatchAuthenticatedLiveHandlers'`：
  401/400/404/200-SSE、跨 Run 帧投影拒绝、文档路由 live 一致性。
- `go test ./internal/browserruntime/ ./internal/application/ ./internal/httpapi/`：三包全量回归。
- `npx vitest run src/v2/components/agent-browser.test.tsx`：23 项（含直播流渲染、
  失败回退、waiting_user 新文案）。
- `npm run check:api`：`docs/openapi.json` 与 `web/src/api/schema.d.ts` 由
  `go run ./cmd/cyberagent api openapi` 重新生成并保持一致。

## 复审修正

- 协议登记：`agent_browser_screencast.v1` 进入 `http-openapi-contract`
  （145 → 146），`docs/convergence/protocol-registry.md` 重新生成。
- SSE 头顺序：先提交 `text/event-stream` 头再 Flush；旧顺序会让 net/http
  在设置 Content-Type 前提交裸 200，客户端拒绝流并报 superfluous WriteHeader。
- 写截止：每帧复用 `writeEventStreamFrame` 的 2s 写截止，避免全局 30s
  `WriteTimeout` 截断健康直播。
- pump 归属：停止时按 pump 身份关闭，迟到的旧 watcher 不再误关替换观看者的流。
- 客户端：同步处理 SSE 帧，`agent_browser.closed` 与坏帧以错误结束并重连。
- 验证：`go test -race ./internal/browserruntime -run Screencast`、
  `go test -race ./internal/application -run 'TestAgentBrowserStreamScreencast'`、
  `go test ./internal/httpapi ./internal/browserruntime`、
  `vitest run src/api/client.test.ts src/v2/components/agent-browser.test.tsx`（249 项）。
