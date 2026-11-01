# Agent 浏览器下载隔离捕获：OCR delegate 复审

日期：2026-10-30。基线：`b58f8981db87dd983e221839283e43f145b92b03`（origin/main）。
主题：issue #491 P1 的地基切片（对齐 Codex 的下载能力而不削弱隔离）：agent 浏览器
对下载从 `Browser.setDownloadBehavior=deny` 改为 `allowAndName` 隔离捕获——
Chromium 以下载 GUID 命名文件、落入一次性会话 profile 内的 `downloads/` 目录
（随 profile cleanup 消亡）；16MB 字节预算对进度流硬限（超限 `Browser.cancelDownload`
+ 删除残留，取消失败则关闭会话）；完成捕获按 `downloadPath/<guid>` 计算 SHA-256；
回执有界（16 条）并挂到 runtime status。审批释放（release-to-workspace）明确不在
本切片，留待后续 ADR。对照讨论见 #491：Codex/Qoder/ZCode 三家实现对比的结论是
"允许下载 + 隔离边界" 是主流做法，UC 此前是唯一全局 deny 的。

OCR 负责文件选择（`ocr delegate preview --from origin/main --to HEAD`：5 个可审
生产文件）与规则解析（`ocr delegate rule`：Rule Group 1 system Go），主 agent 与
独立评审子代理实际阅读判断。评审子代理全文核验 5 个生产文件、1 个人工补入的测试
文件与全量 diff，并核对了 cdp_test.go 审计适配器守卫、screencast 事件泵先例与
profile 生命周期。此后一轮外部代码复审又提出会话级累计下载字节无上限（P1，见
F7），已在本轮修复并复测。第二轮外部复审提出超限下载取消打错了 BrowserContext（P1，见
F8），同样已在本轮修复并复测。

## 覆盖范围

共修改 6 个文件；评审记录（本 README 与 coverage.json）不计入审查范围，纳入审查 6 个：
- 生产代码（5 个，OCR 检出）：`internal/browserruntime/agent_browser_download.go`、
  `agent_browser_runtime.go`、`agent_browser_transport.go`、`agent_browser_types.go`、
  `profile_materializer.go`。
- 测试（1 个，人工补入）：`internal/browserruntime/agent_browser_download_test.go`。

具体明细见 [coverage.json](coverage.json)。

```text
ocr delegate preview --from b58f8981db87dd983e221839283e43f145b92b03 --to HEAD
ocr delegate rule internal/browserruntime/agent_browser_download.go
```

## 发现与修复（评审子代理与外部复审报告，全部已修复并复测）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| F1，P2 | `case "canceled"` 无条件覆盖 State：预算拒绝后代码自己调用 `cancelDownload`，Chromium 确定性回推 `canceled`，把安全相关的 `rejected_budget` 翻成普通取消，操作员不可观察。 | 终态迁移规则：仅 `active`/`unverified` 可进入 `canceled`；`rejected_budget` 与 `completed` 均为终态；迟到的 inProgress/completed/canceled 一律不复活已终态回执。补"拒绝后收到 canceled 不回退"回归断言。 |
| F2，P3 | GUID 校验放行 `..`/`.` 等：GUID 实为捕获文件名组件却未约束字符集，可经 `filepath.Join` 指向隔离目录之外（防御纵深缺口）。 | 新增 `validDownloadGUID`：规范 36 位小写 UUID（连字符固定在 8/13/18/23 位），点段/分隔符/保留形状不可达文件系统助手；附正反用例表。 |
| F3，P3 | `AgentBrowserMaxDownloadBytes` 注释声称镜像 `ProfileLimits.MaxDownloadBytes`，但无测试钉住，单改一处会静默漂移。 | 新增 `TestAgentBrowserDownloadBudgetMatchesBuiltinProfileLimits`，断言与 `BuiltinRegistry().Lookup(ProfileSafeWeb).Limits.MaxDownloadBytes` 相等。 |
| F4，P3 | 容量注释与实现不符（实际是"仅驱逐终态回执、全 active 时对新下载 fail-closed"）。 | 注释改为准确描述 fail-closed 语义。 |
| F5，P3 | Windows 句柄共享：拒绝路径 `os.Remove` 可能因 Chromium 持句柄而失败（超大残留滞留至 cleanup）；hash 读文件与 cleanup 的 rename 存在良性竞争。 | 注释明示 best-effort 语义：残留随 profile 消亡；hash 与 cleanup 的竞争是"丢哈希保 unverified + cleanup 可重试"，不夸大承诺。 |
| F6，P3 | 重复 `completed` 事件把已 `completed`（有摘要）的回执重置回 `unverified` 并重触发哈希，与回执注释矛盾。 | completed 分支对非 active 回执幂等短路；结构体注释改为"终态不回退"；补重复 completed 不回归摘要的断言。 |
| F7，P1（外部复审） | 会话级累计下载字节无上限：单文件 16MB、回执表 16 条，但隔离目录的总占用没有封顶；回执表满时驱逐终态回执只做 `delete(r.downloads, candidate)`，对应文件留在 `downloads/` 里直到 profile 清理，页面可连续完成多个小于 16MB 的下载把会话磁盘撑到无界；被驱逐的完成下载也无法再经下游释放流程找回。 | 新增 `AgentBrowserMaxDownloadTotalBytes`（= 4 × `AgentBrowserMaxDownloadBytes` = 64MB，是单文件上限的整数倍）：会话隔离目录的总字节**计账**上限，按"仍可能被回执命名的存活字节之和"计账，在 `inProgress`/`completed` 进度流上与单文件上限同点强制，越界下载 fail-closed 落 `rejected_budget`、best-effort 删除捕获文件并 `Browser.cancelDownload`（取消失败仍杀会话）。回执驱逐时同步 best-effort 删除捕获文件并停止计账——被驱逐的捕获无法再经下游释放流程释放，其字节随会话 profile 一同消亡。 |

| F8，P1（第二轮外部复审） | `cancelDownload` 只发 `{guid}`：隔离捕获安装在自建 BrowserContext（`setDownloadBehavior` 带 `browserContextId`），而 CDP 的 `Browser.cancelDownload` 省略 `browserContextId` 时在**默认** context 里查 GUID，超限下载的取消因此注定失败；按既有策略"取消失败即关闭会话"，一个本应局部拒绝的超限下载会终止整个浏览器会话（连带在途回执与下游释放）。 | 取消请求携带与隔离同源的 context id，由两个持锁调用点（回执表越界 fail-closed、字节预算拒绝）读取 `r.browserContextID` 后传参，避免在 worker 里无锁读。新增两个回归：mock CDP 侧把"context 必须与隔离同源"做成契约（context 不对即回错误），断言取消参数与会话 `life` 未被取消；opt-in 真浏览器侧用 17MB 载荷打真实 Edge，超限拒绝后会话必须存活且随后一个普通下载仍能完成。 |
| F9，P3（CodeRabbit，本轮唯一的 actionable） | 评审记录把 64MB 写成"可观测的硬上限"，常量注释也写成 "bounds the bytes one session may keep"——口径比实现强：预算是对**存活回执上报字节**的计账上限，检查发生在进度流到达之后，且被拒/被驱逐捕获的删除是 best-effort，删除失败的残留不再计账。按字面读会让维护者以为隔离目录的磁盘占用被硬封顶。 | 采纳为文档与注释口径修正（非行为缺陷）：常量注释新增"accounting ceiling, not a disk-usage limit"一段并写明残留字节的去向，README 的 F7 行与取值理由同步改为"计账上限"，PR 正文同样更正。CodeRabbit 四条前提里三条成立（计账口径、驱逐后停止计账、进度到达后才检查），第四条"测试在检查预算前先建好下一个捕获"是 mock 夹具的模拟顺序——真实 Chromium 本就是边收边写，不构成缺陷，也未改动测试。真要磁盘硬上限属另一件事（目录配额或写入侧计量），本 PR 不做。 |

F7 的取值理由：回执表最多 16 条、每条最多 16MB，仅靠回执表本身就能让 16 × 16MB = 256MB
落在会话临时存储里；因此 64MB 上限取单文件上限的 4 倍整数倍，既是可观测的**计账**上限（第五个
满额捕获被拒），又给多个普通捕获留出余量，单次普通下载完全不受影响。口径要说清：它是**对存活回执上报字节的计账上限，不是隔离目录的磁盘硬上限**——预算在进度流到达之后才检查，此时字节已经落盘；被拒与被驱逐捕获的删除是 best-effort（Windows 上 Chromium 可能仍持句柄），删除失败的残留不再计账，随会话 profile 一起清理。要真正的磁盘硬上限得另设 enforcement（目录配额或写入侧计量），本轮不做。`downloadBytesLocked` 与常量注释同样按这个口径写（CodeRabbit 提出，见 F9）。内置 profile 只暴露
单文件上限（`ProfileLimits.MaxDownloadBytes`），没有会话级下载上限可镜像，故该常量按上述
推导定义并在注释中说明，`TestAgentBrowserDownloadTotalBudgetDerivation` 钉住整数倍关系。

评审确认的无缺陷事实（节选）：单一 `r.mu` 贯穿下载路径、CDP/文件 I/O 均在锁外
goroutine、`-race` 干净无死锁；`Browser.download*` 事件在 target switch 之前消费，
畸形 payload 与外会话事件不产生状态；事件 `filePath` 字段完全不解析，捕获只从
`downloadPath/<guid>` 读取且先 Lstat 验证；FS 原语全部落在审计适配器
`profile_materializer.go`（`os.MkdirAll` 禁令未触碰）；Status 新字段纯新增（omitempty），
application/httpapi 现有消费方不受影响。

更正：本轮之前的"白名单最小性成立（仅 `{guid}`、结果忽略）"结论有误——参数集确实最小，
但少了 `browserContextId` 会让取消落到错误的 BrowserContext 上，见 F8。

## 验证

- `gofmt -l internal/browserruntime/` 无输出；`go vet ./internal/browserruntime/` 无告警。
- `go test -race -count=1 ./internal/browserruntime/` 全通过；完整套件通过。
- F7 回归（新增，均在 Windows 上数秒内跑完、不依赖符号链接）：
  `TestAgentBrowserDownloadSessionBudgetBoundsConsecutiveDownloads`（连续满额下载填满会话
  预算后，后续下载落 `rejected_budget`、捕获文件不留存、隔离目录不超过预算）、
  `TestAgentBrowserDownloadExhaustedBudgetKeepsFailingClosed`（预算耗尽后持续 fail-closed）、
  `TestAgentBrowserDownloadEvictionRemovesCaptureFromQuarantine`（驱逐回执同时删除捕获文件、
  回执表保持有界、会话重新接受新下载）、
  `TestAgentBrowserDownloadTotalBudgetDerivation`（会话预算是单文件上限的整数倍）。
- **opt-in 真浏览器端到端**（`TRAVERSE_TEST_AGENT_BROWSER=1`，本机已安装 Edge）：
  `TestAgentBrowserRealDownloadQuarantine` 通过——真实页面点击 → 真实下载 →
  GUID 命名捕获落入 profile 内隔离目录 → SHA-256 与字节长度一致 → 回执 completed。
- 审计适配器守卫 `TestBrowserRuntimeProductionPrimitivesStayInAuditedAdapters` 通过。
- F8 回归（新增两条，均先证红再证绿）：
  `TestAgentBrowserDownloadCancelTargetsTheQuarantineContext`（mock CDP 把"context 必须与隔离
  同源"当契约，context 不符即回错误；断言取消参数等于 `r.browserContextID` 且 `r.life` 未被
  取消）与 `TestAgentBrowserRealDownloadOverBudgetKeepsSessionAlive`（`TRAVERSE_TEST_AGENT_BROWSER=1`、
  本机 Edge：17MB 载荷触发 `rejected_budget` → 会话必须存活 → 随后一个普通下载仍能完成并核对
  SHA-256）。在 `2cd5cbb9` 的旧实现上，前者报
  `cancel targeted "", but the quarantine lives in "ctx-1"`，后者报
  `over-budget cancel tore the browser session down`——即缺陷已在真实浏览器上复现，不是静态推断。
- F8 修复后 `go test -count=1 ./internal/browserruntime/` 全通过；`gofmt -l`、`go vet` 无输出。
