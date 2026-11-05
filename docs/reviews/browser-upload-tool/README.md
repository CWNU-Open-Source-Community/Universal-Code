# Agent 浏览器工作区文件上传：OCR delegate 复审

日期：2026-11-05。基线：`6a10b7c8`（origin/main，含 #522/#523/#524 的合入与其他会话对隔离区的改进——会话级字节预算、文件 I/O 移出 reader 路径）。
评审对象：PR 分支 Qiyuanqiii/browser-upload-tool（单提交，已变基到新 main）。
主题：#491 P2 的上传工具（issue 问题 #4"上传没有工具"）。`browser_upload.v2` 以
snapshot 解析的文件输入 + workspace 相对路径为载荷；应用层证明路径解析为注册
workspace 根内的真实 regular 文件（1B..16MiB）后才分发；上传是外部效果——payload
必带 sensitive_intent（external_write），走与 click/type 相同的 per_call 审批账本。

OCR 负责文件选择（`ocr delegate preview --from origin/main --to HEAD`：7 个可审
生产文件）与规则解析（Rule Group 1 system Go），主 agent 与独立评审子代理实际阅读
判断。评审子代理全文核验 7 个生产文件、3 个测试文件与全量 diff，并专项核验了与
其他会话新隔离区代码的交互。

## 覆盖范围

纳入审查 10 个（评审记录不计入）：
- 生产代码（7 个，OCR 检出）：`internal/toolgateway/model.go`、
  `agent_browser_actions.go`（归一化 + ToolDefinition + 描述）、`browser_actions.go`
  （IsBrowserActionTool）、`internal/browserruntime/agent_browser_actions.go`
  （Upload + SupportedActions）、`agent_browser_transport.go`（白名单）、
  `internal/application/agent_browser_actions.go`（分发 + 路径解析）、
  `agent_browser_service.go`（接口）。
- 测试（3 个，人工补入）：toolgateway 归一化测试、application containment 测试、
  browserruntime opt-in 真浏览器 E2E（本机 Edge 实测：非文件输入被拒 → 上传 →
  表单提交 → 服务器收到的字节与 workspace 文件逐字节一致）。

具体明细见 [coverage.json](coverage.json)。

## 发现与修复（评审子代理报告，全部已修复并复测）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| F1，P2 | upload case 只要求 `Sensitive != nil`，通用校验放行 effect=`external_delete`——审批账本分类（browser_external_delete）与真实效果（写）不符，违背注释与描述声称的 external_write。 | upload case 增加 `Effect != "external_write"` 判定；补 external_delete 被拒的测试用例。 |
| F2，P3 | `validateSupervisorBrowserBatch` 未把 BrowserUploadTool 计入输入消费者/失效者：`[navigate, upload]` 等批次通过预检、运行期才以 stale reference 失败（安全方向但不合守则本意）。 | 消费者条件与失效者 switch 均加入 BrowserUploadTool。 |
| F3，P3 | TOCTOU 残留：resolve 与浏览器打开文件之间，并发 workspace 写方可换入逃逸 symlink 或超限增长文件；注释 "proves ... inside the exact workspace root" 过于绝对。 | 注释如实降级为 check-then-act 边界 + 已批准 external_write 内的可接受残留。 |
| F4，P3 | `Size() <= 0` 把空文件与超限混在同一个 "exceeds the size bound" 文案里，诊断误导。 | 拆分为 "browser upload file is empty" 与 "exceeds the size bound" 两条。 |
| F5，P3 | E2E 注释称非 file input "refused before any browser call happens"，实际 reference() 已做多次只读 CDP 调用。 | 改为 "before any state-changing browser call"。 |

评审确认（No blocking findings）：`validAgentBrowserUploadPath` 绕过面封死（JSON 转义
解码后校验、反斜杠/冒号（含盘符与 NTFS ADS）/`<>"|?*`/首尾斜杠/空段/`.`/`..`、512
字节上限严于 schema 字符限）；Windows 保留设备名经 Win32 归一化仍落在 workspace 内且
被 Lstat size<=0 拦截；resolve 双侧 EvalSymlinks + 带分隔符大小写不敏感前缀（无
root/root2 缝隙）；runtime 从活文档重观测节点并全量校验 Tag/Type/disabled/Visible/Hit/
signature；CDP 参数最小（files+backendNodeId）；白名单全仓仅 Upload 一处调用；supervisor
与 specialist 两条审批链在 `Sensitive != nil` 时零改动覆盖 upload（fingerprint 绑定含
workspace_path 的精确 canonical payload，批准后路径无法替换）；与改进后的隔离区代码无
摩擦（Upload 不做 Go 进程文件 I/O，浏览器进程自读）。

## 验证

- `gofmt -l` 无输出；`go vet` 三包无告警；`go test ./internal/toolgateway/
  ./internal/browserruntime/ -count=1` 全过。
- `go test -race -run "TestAgentBrowserUpload|TestAgentBrowserDownload|
  TestSupervisorBrowserBatch|TestAgentBrowserSensitive" ./internal/application/` 通过
  （归一化矩阵含 external_delete 拒绝、containment 含 symlink 逃逸、批次失效者、敏感审批）。
- **opt-in 真浏览器 E2E**（`TRAVERSE_TEST_AGENT_BROWSER=1`，本机 Edge）通过：非文件输入
  被拒 → workspace 文件上传 → 表单提交 → 服务器收到的字节与文件逐字节一致。
- 已知边界（如实记录）：resolve 与浏览器打开之间的 TOCTOU 窗口为已批准外部写效果内的
  可接受残留；512 字节路径上限严于 schema 的字符 maxLength；无效 effect 分支与 512
  字节边界由归一化测试锚定。

## 复审附记（2026-11-05，第二轮：入口接线修复）

独立复审实证发现上一轮的评审确认存在重大遗漏：**`browser_upload` 从未接入任何执行
入口**——gateway registry 的 agent-browser 注册循环、Supervisor 工具目录追加、
`refreshBrowserModelTools`、worker 目录、两处 `SupportedActions` 能力列表、gateway
recorded-actor 校验 case、`domain.isBrowserActionSupervisorTool`、store
`run_supervisor_tool_calls` 的三处 CHECK 约束与 artifact 来源 scope 全部缺失。用合法
authority 直调 `Gateway.Invoke` 返回 `unsupported tool "browser_upload"`；既有的全部
测试都绕过入口层直接调内部函数，因此 CI 全绿。上一轮"已审"的
`agent_browser_actions.go（Upload + SupportedActions）`与"No blocking findings"结论
对此失实，如实更正。

本轮修复（提交 1）：上述 10 处接线全部补齐；新增 store migration v220（沿用 v169 的
保 rowid 重建模式，三处 CHECK 加入 `browser_upload` 并要求 authority_json 绑定，
clean-install 基线经生成器重建，LatestSchemaVersion 219→220）；批次守卫测试把 upload
同时作为失效者与消费者加入枚举并新增合法序列；`tool schema` CLI 目录测试、API/Desktop
布线测试与 `TestAgentBrowserFirstNavigationAdvertised...` 的精确目录断言（8→9）均锚定
upload 的入口可见性。验证：`go build ./internal/...`、`go test ./internal/store/
（migration v220 + clean-install baseline）`、`./internal/toolgateway/
./internal/application/ ./internal/browserruntime/ ./internal/domain/ ./internal/app/
（structured tool）` 全过。

本轮修复（提交 2）：工具描述如实写明"任意 regular 文件类型均可，仅限制包含性与大小"；
`resolveAgentBrowserUpload` 注释补充同卷 hardlink 不在解析感知范围内（与 TOCTOU 同属
宿主沙箱假设内的残留）；`withinResolvedWorkspace` 修复盘符根（`C:\`）尾分隔符导致的
全量误拒边界并补单元测试。
