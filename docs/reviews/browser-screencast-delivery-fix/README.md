# Agent 浏览器实时画面交付修复：OCR delegate 复审

日期：2026-10-30。基线：`b58f8981db87dd983e221839283e43f145b92b03`（origin/main）。
主题：issue #491 P0（只读 screencast 实时画面）的**真浏览器实测暴露的交付缺陷修复**：
main 上已有的 screencast 实现（泵、白名单、httpapi、前端 canvas）在真实 Chromium 上
从未交付过任何一帧——`screencastFrame.params.sessionId` 的类型跨 Chromium 版本
从 string 变为 number（或缺席），typed 解码到 `SessionID string` 使**整个事件
unmarshal 失败**，帧在到达泵之前被静默丢弃。四个泵单测全部使用带 string sessionId
的脚本事件，掩盖了该缺陷。修复后真浏览器 E2E：5 帧 / 1.2s、1250×805、JPEG 魔数正确。

OCR 负责文件选择（`ocr delegate preview --from origin/main --to HEAD`：1 个可审
生产文件）与规则解析（`ocr delegate rule`：Rule Group 1 system Go），主 agent 与
独立评审子代理实际阅读判断。评审子代理全文核验生产文件与测试文件，并对 ack 回传
做了逐字节等价实证（string/number/缺席三类 token）。

## 覆盖范围

共修改 2 个文件；评审记录不计入审查范围，纳入审查 2 个：
- 生产代码（1 个，OCR 检出）：`internal/browserruntime/agent_browser_screencast.go`。
- 测试（1 个，人工补入）：`internal/browserruntime/agent_browser_screencast_test.go`
  （新增 opt-in 真浏览器 E2E `TestAgentBrowserRealScreencastLiveFrames`）。

具体明细见 [coverage.json](coverage.json)。

```text
ocr delegate preview --from b58f8981db87dd983e221839283e43f145b92b03 --to HEAD
ocr delegate rule internal/browserruntime/agent_browser_screencast.go
```

## 根因与修复

探针实证（opt-in 真浏览器）：wire 层 `Page.screencastFrame` 事件正常到达
（envelope session 归属正确、params 含 data/metadata/sessionId），但
`json.Unmarshal` 报 `cannot unmarshal number into Go struct field .sessionId
of type string`。修复：

- `SessionID` 改为 `json.RawMessage` 原样接收：会话归属继续由 wire 信封保证
  （onEvent 已按 `event.SessionID == r.session` 过滤），params token 只用于 ack 回传。
- ack 原样回显 token：string 与 number 两种 screencast 会话 id 都按原始 JSON
  类型往返；字面 `null` 视为缺席不 ack。
- 缺席 sessionId 的帧从"整帧丢弃"改为"交付但不 ack"（本就无从关联）。

## 发现与修复（评审子代理报告，全部已修复并复测）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| F1，P2 | E2E 的 Stop 后"无帧"断言有真实 flake 窗口：Stop 排在某个持锁 deliver 之后时，已入 2 槽缓冲的帧会在 close 后以 `ok==true` 被收到，被误判违约。 | 改为有界排空直到 channel 关闭（关闭即通过，2s 未关闭才 Fatal）。 |
| F2，P3 | JPEG 魔数检查在 `len==1` 且首字节恰为 `0xFF` 时索引越界。 | 哨兵条件改为 `len < 2`。 |
| F3，P3 | 假想 `params.sessionId: null` 会发出 `{"sessionId":null}` 的 ack（被 Chromium 拒绝、错误被忽略，帧照常交付，无用户可见后果）。 | 字面 `null` token 视为缺席，不发送 ack。 |

评审确认（生产代码 No blocking findings）：string token 的 ack 线上字节与旧实现
逐字节一致（旧版 Chromium 行为完全不变）；token 经 encoding/json 校验回显，无注入面；
会话归属不变量成立；acks channel 关闭/发送时序未变；既有 4 个泵单测断言无漂移。

## 验证

- `gofmt -l` 无输出；`go vet ./internal/browserruntime/` 无告警；
  `go test -race -count=1 ./internal/browserruntime/` 全通过。
- **opt-in 真浏览器端到端**（`TRAVERSE_TEST_AGENT_BROWSER=1`，本机已安装 Edge）：
  修复前 `TestAgentBrowserRealScreencastLiveFrames` 复现 0 帧；修复后通过
  （5 帧 / 1.2s，1250×805，JPEG 魔数与 viewport 边界校验，Stop 后 channel 正常关闭）。
- 已知限制（如实记录）：实测帧率由 Chromium 侧损伤/采样与 ack 流控共同决定，
  动画页约数 fps；issue #491 的 "≥5fps" 验收需要在真实桌面会话上进一步观测，
  不在本修复的声称范围内。
