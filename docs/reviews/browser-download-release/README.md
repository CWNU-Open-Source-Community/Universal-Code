# Agent 浏览器下载审批释放：OCR delegate 复审

日期：2026-10-30。基线：`Qiyuanqiii/browser-download-quarantine`（2a20c5a，即 #520）。
主题：#491 P1 下载故事的收口——操作员把一个已完成摘要的隔离捕获释放进 Run 的
工作区工件。释放调用本身即审批记录：control token 绑定精确 run+GUID，字节在复制前
重算 SHA-256，GUID 仍是磁盘上唯一文件名，页面控制的建议文件名只进溯源 sidecar
（sha256/url/suggested_filename/run_id/workspace_id/captured_at，确定性 JSON）。
重复释放幂等。捕获随会话 profile 消亡，因此释放必须发生在会话存续期。

OCR 负责文件选择与规则解析（Rule Group 1 system Go），主 agent 与独立评审子代理
实际阅读判断。评审子代理全文核验 5 个生产文件、3 个测试文件与全量 diff，并对
ack token（前一 PR 模式）与释放错误映射做了脱敏断言。

## 覆盖范围

纳入审查 9 个（评审记录不计入）：
- 生产代码（5 个，OCR 检出）：`internal/browserruntime/agent_browser_download.go`
  （追加 ReadDownload）、`internal/application/agent_browser_download.go`（新增）、
  `internal/httpapi/agent_browser.go`（新增端点/视图/路由）、
  `internal/httpapi/server.go`（分发）、`internal/httpapi/openapi.go` + `docs/openapi.json`
  （注册与再生成，快速核对）。
- 测试（3 个，人工补入）：application 释放测试、browserruntime ReadDownload 测试、
  httpapi 端点测试。

具体明细见 [coverage.json](coverage.json)。

## 发现与修复（评审子代理报告，全部已修复并复测）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| F1，P2（阻塞） | `ReadDownload` 读失败时把原始 `*fs.PathError` 原样上抛；释放并发清理的 TOCTOU 窗口内，`apperror.Normalize` 会把 `ErrNotExist` 的消息（含隔离区绝对路径）带进 HTTP 响应，违反"路径不出 runtime"。 | 读失败统一包装为 `ErrAgentBrowserDownloadGone`（不携带 PathError）；application 层将 `Gone` 映射为脱敏的 `agentBrowserUnavailable("browser download capture is gone with the session quarantine")`。补"文件消失 → Gone"与"gone 错误不含路径"断言。 |
| F2，P3 | server.go 全局 token 分支残留本提交引入的空 `if` 体（DEBUG 清理残留），位于鉴权路径上易误读。 | 删除。 |
| F3，P3 | `matchAgentBrowserDownloadsPath` 零引用死代码，与真实路由逻辑分叉。 | 删除，路由单一来源（HasSuffix + matchAgentBrowserPath）。 |
| F4，P3 | application 层"invalid GUID"用例是恒过断言（fake runtime 忽略 GUID，实际失败源是数据不一致）；runtime 测试名里的 "Lifetime" 未真正覆盖清理竞态。 | 移除 application 层无意义用例（GUID 形状校验由真实 runtime 的 `validDownloadGUID` 承担且已有测试）；runtime 层补"cleanup 已删除捕获 → `ErrAgentBrowserDownloadGone`"真实覆盖。 |
| F5，P3 | 释放响应回显 `TrimSpace(input.Operator)`，与 application 层空白归一化为 `httpapi_operator` 的生效身份不一致。 | httpapi 侧用同一归一化（`releaseOperator`）后再回显。 |

评审确认（生产代码其余部分 No blocking findings）：ReadDownload 锁内拷贝 receipt 后
解锁再读文件、重算摘要；释放链 GetRun 归属、slot 判活、窄接口断言、持久化双重摘要
校验；工件持久化镜像 screenshot 的 O_EXCL + same-bytes 幂等模式（16MB/16KB 上限、
Lstat+SameFile 防符号链接）；httpapi 的 GET=read token / POST=control token 与
close/screenshot 同构，错误顺序 nil→404→405→401→400→415→严格 JSON→精确 version；
openapi.json 与注册一致，live-handler 测试补了 release body。

## 验证

- `gofmt -l` 无输出（改动包）；`go vet` 三包无告警。
- `go test -race ./internal/application/ ./internal/browserruntime/` 通过；
  `go test ./internal/httpapi/` 全套通过（含 OpenAPI golden 与 live-handler 路由检查，
  `docs/openapi.json` 已用 `cyberagent api openapi` 再生成）。
- 释放语义由三层测试覆盖：runtime（摘要/篡改/消失）、application（成功/幂等/未知/Gone 脱敏）、
  httpapi（GET 列表 200、POST 释放 200 + locator、read token 被拒 401）。
