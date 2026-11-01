# Agent 浏览器下载审批释放：OCR delegate 复审

日期：2026-10-30。基线：`Qiyuanqiii/browser-download-quarantine`（2a20c5a，即 #520）。
主题：#491 P1 下载故事的收口——操作员把一个已完成摘要的隔离捕获释放进 Run 的
工作区工件。释放调用本身即审批记录：control token 绑定精确 run+GUID，字节在复制前
重算 SHA-256，GUID 仍是磁盘上唯一文件名，页面控制的建议文件名只进溯源 sidecar
（sha256/url/suggested_filename/run_id/workspace_id/captured_at，确定性 JSON）。
重复释放幂等。捕获随会话 profile 消亡，因此释放必须发生在会话存续期。

OCR 负责文件选择与规则解析（Rule Group 1 system Go），主 agent 与独立评审子代理
实际阅读判断。评审子代理全文核验 5 个生产文件、3 个测试文件与全量 diff，并对
ack token（前一 PR 模式）与释放错误映射做了脱敏断言。第二轮外部复审又提出重复释放
的响应与审批记录不一致（P2，见 F6）与工件写失败后无法重试（P2，见 F7），本轮修复并
复测；OCR 本轮 `delegate preview` 在工作树模式下检出 2 个生产文件、按 `default_path`
排除 2 个测试文件，排除项同样人工全文过一遍（连同 #520 分支那一轮的测试文件）。

## 覆盖范围

纳入审查 13 个（评审记录不计入；PR 全量 diff 共 15 个文件）：
- 生产代码（5 个，OCR 检出）：`internal/browserruntime/agent_browser_download.go`
  （追加 ReadDownload）、`internal/application/agent_browser_download.go`（新增）、
  `internal/httpapi/agent_browser.go`（新增端点/视图/路由）、
  `internal/httpapi/server.go`（分发）、`internal/httpapi/openapi.go` + `docs/openapi.json`
  （注册与再生成，快速核对）。
- 测试（4 个，人工补入，OCR 按 `default_path` 排除）：application 释放测试、
  browserruntime ReadDownload 测试、httpapi 端点测试、`internal/httpapi/openapi_test.go`
  （live-handler 补 release body）。
- 生成物与注册文档（4 个文件，人工核对；含生产条目里已列出的 `docs/openapi.json`）：
  `protocols/registry.json`、`web/src/api/schema.d.ts`、
  `docs/convergence/protocol-registry.md`（注册表说明，OCR 按 `unsupported_ext` 排除）。

第二轮（F6–F8）改动 4 个文件，均已在上面的清单内：`internal/application/agent_browser_download.go`、
`internal/httpapi/agent_browser.go`（OCR 检出）与 `internal/application/agent_browser_download_test.go`、
`internal/httpapi/agent_browser_test.go`（OCR 按 `default_path` 排除，人工全文核验）。
响应字段形状不变，`internal/httpapi/openapi.go` 与 `docs/openapi.json` 本轮未改动。

第三轮（F9–F10）只改 `internal/application/agent_browser_download.go` 与其测试（OCR 检出生产 1 个、
`default_path` 排除测试 1 个再人工全文核验）。同时把 #520 分支尖端的隔离修复并入本分支（merge commit）：
这些提交落在本 PR 的 base 分支里，因此不出现在上面 15 个文件的清单中，本 PR 相对 `main` 才带上它们。
早先记录的 11 个文件是按当时的 base `2a20c5a8` 数的，口径已随 #520 的推进变化，以本轮
`ocr delegate preview --from 76fa2d3a --to HEAD`（9 个可选 + 6 个排除）为准。
响应字段形状与 OpenAPI 注册本轮未变。

具体明细见 [coverage.json](coverage.json)。

## 发现与修复（评审子代理报告，全部已修复并复测）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| F1，P2（阻塞） | `ReadDownload` 读失败时把原始 `*fs.PathError` 原样上抛；释放并发清理的 TOCTOU 窗口内，`apperror.Normalize` 会把 `ErrNotExist` 的消息（含隔离区绝对路径）带进 HTTP 响应，违反"路径不出 runtime"。 | 读失败统一包装为 `ErrAgentBrowserDownloadGone`（不携带 PathError）；application 层将 `Gone` 映射为脱敏的 `agentBrowserUnavailable("browser download capture is gone with the session quarantine")`。补"文件消失 → Gone"与"gone 错误不含路径"断言。 |
| F2，P3 | server.go 全局 token 分支残留本提交引入的空 `if` 体（DEBUG 清理残留），位于鉴权路径上易误读。 | 删除。更正：上一轮把这条写成"残留空体已清理完毕"是过头了——`if !a.authorized(request, a.tokenHash) {` 之后还有一个本 PR 引入的空行仍在 diff 里（不影响行为，但同样落在鉴权分支上），本轮一并删除。 |
| F3，P3 | `matchAgentBrowserDownloadsPath` 零引用死代码，与真实路由逻辑分叉。 | 删除，路由单一来源（HasSuffix + matchAgentBrowserPath）。 |
| F4，P3 | application 层"invalid GUID"用例是恒过断言（fake runtime 忽略 GUID，实际失败源是数据不一致）；runtime 测试名里的 "Lifetime" 未真正覆盖清理竞态。 | 移除 application 层无意义用例（GUID 形状校验由真实 runtime 的 `validDownloadGUID` 承担且已有测试）；runtime 层补"cleanup 已删除捕获 → `ErrAgentBrowserDownloadGone`"真实覆盖。 |
| F5，P3 | 释放响应回显 `TrimSpace(input.Operator)`，与 application 层空白归一化为 `httpapi_operator` 的生效身份不一致。 | 当时用同一归一化（`releaseOperator`）回显；第二轮复审证明"归一化后的请求身份"仍不是生效身份——生效身份是磁盘上那条审批事件，见 F6。`releaseOperator` 已随 F6 删除。 |
| F6，P2（第二轮外部复审） | 重复释放的响应与持久化审批记录不一致：释放是幂等的，第二次请求保留第一次写入的 `.approval.json`，但响应回显本次请求的 `operator` 与 `time.Now()`。Alice 先释放、Bob 再释放同一 GUID 时，磁盘记 Alice，HTTP 却报 Bob 和第二次时间——按响应建审计日志的客户端会凭空多出一个审批人。 | `ReleaseBrowserDownload`/`persistAgentBrowserDownload` 返回**真正落盘**的审批元数据（新导出类型 `application.AgentBrowserDownloadApproval{DeclaredOperator, ApprovedAt}`），httpapi 用回显值答复；重复释放返回第一次的 operator 与 `approved_at`。审批事件里 `declared_operator` 为空视为冲突（fail-closed），保证回显值永不为空。响应字段形状未变，`docs/openapi.json` 无需再生成（`TestOpenAPIGoldenDocumentMatchesGoDTOs` 通过）。 |
| F7，P2（第二轮外部复审） | 工件持久化失败后无法重试：捕获文件、`.json` 来源记录、`.approval.json` 都用 `O_CREATE \| O_EXCL` 新建后交给 `writeFullCDPArtifact`，写/同步/关失败时不清理，半截文件留在原地。下次释放走"已存在→比对字节"分支，内容不符即 `browser artifact already contains different bytes`，审批 JSON 半截则 `approval event is invalid`——该 GUID 的释放被永久楔死。旧实现实测确认：首次写失败后重试固定报 `browser artifact already contains different bytes`。 | 新增 `writeNewAgentBrowserDownloadArtifact`：写失败即 `root.Remove` 删掉**本次刚创建**的那一个文件，与 `internal/application/full_cdp_browser_actions.go` 里截图工件的既有做法一致；`O_EXCL` 不覆盖已完成工件的性质保持不变，前序已写成的工件下次按字节比对复用。删除只在本次创建的分支上执行，因此不会碰别人的文件。 |
| F8，P3（评审子代理） | 失败注入测试把包级钩子换成失败闭包后只在内联路径恢复，中途 `t.Fatalf` 会把故障留给同一包的后续用例。 | 换钩子后立即注册 `t.Cleanup` 恢复，内联恢复仍保留用于验证重试。 |
| F9，P2（本轮 delegate 复审） | 释放的**写侧**把原始文件系统错误直接上抛：`os.OpenRoot(workspace.RootPath)`、`root.MkdirAll`、三次 `root.OpenFile` 与两次写入失败都是 `return "", AgentBrowserDownloadApproval{}, e`，而 `*fs.PathError.Error()` 里带 Workspace 绝对路径（本机形如 `C:\Users\<u>\AppData\Local\Temp\…\missing-workspace: The system cannot find …`）；httpapi 的 `writeError` 会把 message 原样放进响应体，release 端点虽受 control token 保护，仍把服务器目录结构交给调用方——与 F1 修的是同一类泄露，只是发生在持久化分支而不是读分支。 | 八个原始返回点统一改为 `apperror.Wrap(apperror.CodeUnavailable, "<静态描述>", e)`（与 `internal/httpapi/run_operation_control.go` 等既有写法同形）：cause 保留在链里可被 `errors.Is` 命中，message 不含路径与 GUID。唯一保持原样上抛的是 `GetWorkspaceInfo` 的 store 错误（store 自有错误约定，不在本 PR 范围）。回滚用例的断言从 `err.Error() == "simulated artifact write failure"` 改为 `errors.Is(err, simulated)`——它该证明的是 cause 存活，而不是措辞相等。 新增 `TestAgentBrowserDownloadReleaseDoesNotLeakWorkspacePaths`（断言响应 message 等于静态文案、不含 Workspace 路径与 GUID，且 `errors.Is(err, os.ErrNotExist)` 仍成立）；旧实现红为 `release error is not the static artifact message: open C:\Users\22977\AppData\Local\Temp\…\missing-workspace: The system cannot find the file specified.`。 |
| F10，P3（本轮 delegate 复审） | 重复释放的完整性检查比对了 GUID/RunID/WorkspaceID/SHA-256/Bytes、`approved_at` 非零与 `declared_operator` 非空，却没有比对审批事件里的 `authorized_principal` 与 `authorized_principal_scope`：磁盘上那条 `.approval.json` 被改写（或将来由别的身份写入）后，释放仍把它当作"真正落盘的审批"回显给调用方。 | 回放检查追加两个身份字段必须等于当前释放所用的常量（`control_plane` / `credential_class`）。两者都由服务端写入、调用方无法声明，因此不一致只可能是工件树下事件被篡改，故 fail-closed 返回 `browser download approval event conflicts with the released capture`，不回显也不覆盖。新增 `TestAgentBrowserDownloadReleaseRejectsForeignApprovalIdentity`（逐字段变异两个身份字段各一个子用例）；旧实现红为 `an authorized_principal mismatch was accepted: <nil>`。 |

评审确认（生产代码其余部分 No blocking findings）：ReadDownload 锁内拷贝 receipt 后
解锁再读文件、重算摘要；释放链 GetRun 归属、slot 判活、窄接口断言、持久化双重摘要
校验；工件持久化镜像 screenshot 的 O_EXCL + same-bytes 幂等模式（16MB/16KB 上限、
Lstat+SameFile 防符号链接）；httpapi 的 GET=read token / POST=control token 与
close/screenshot 同构，错误顺序 nil→404→405→401→400→415→严格 JSON→精确 version；
openapi.json 与注册一致，live-handler 测试补了 release body。

更正：上一轮把"工件持久化镜像 screenshot 的 O_EXCL + same-bytes 幂等模式"记成无缺陷事实，
但只镜像了一半——截图分支在写失败时 `_ = root.Remove(relative)`，下载分支没有，缺口即 F7；
同一轮的 F5 结论（回显归一化后的请求 operator）也已被 F6 取代。

## 验证

- `gofmt -l` 无输出（改动包）；`go vet` 三包无告警。
- `go test -race ./internal/application/ ./internal/browserruntime/` 通过；
  `go test ./internal/httpapi/` 全套通过（含 OpenAPI golden 与 live-handler 路由检查，
  `docs/openapi.json` 已用 `cyberagent api openapi` 再生成）。
- 释放语义由三层测试覆盖：runtime（摘要/篡改/消失）、application（成功/幂等/未知/Gone 脱敏）、
  httpapi（GET 列表 200、POST 释放 200 + locator、read token 被拒 401）。
- 第二轮（F6–F8）实测，全部先证红再证绿：
  `TestAgentBrowserDownloadReleaseReportsTheRecordedApproval`（Alice 释放后 Bob 重放：断言返回
  与磁盘 `.approval.json` 的 `declared_operator`/`approved_at` 一致；旧实现报
  `replay reported the replaying caller: first={operator-alice ...} replay={operator-bob ...}`）、
  `TestAgentBrowserDownloadReleaseRollsBackFailedArtifactWrite`（逐个失败第 1/2/3 次写入：断言本次
  新建的文件不留存、前序工件仍在、恢复写入口后同一释放重试成功；旧实现在三个子用例都报
  `failed create left a partial artifact`）、`TestAgentBrowserDownloadListAndReleaseEndpoint`
  （响应必须报记录的审批而非请求方；旧实现 body 里是 `"operator":"openapi-operator"`）。
- 楔死后果单独验证过：临时摘掉回滚后，首次写失败 → 恢复写入口 → 重试固定失败于
  `browser artifact already contains different bytes`（一次性探针用例，未提交）。
- 第三轮（F9–F10）实测，同样先证红再证绿：`go test -count=1 -run "AgentBrowserDownload" ./internal/application/` 9 条用例通过，两条新用例在 `9e3c02a1` 的生产代码上分别报上面引用的两条原文；回滚用例改用 `errors.Is` 后仍是绿的，且它现在检验的是包装没有丢 cause。本轮未改 httpapi 代码，`go test -count=1 ./internal/httpapi/` 复跑 210.66s 通过、`TestOpenAPIGoldenDocumentMatchesGoDTOs` 通过，`docs/openapi.json` 无需再生成。
- `gofmt -l` 对本 PR 改动的文件无输出；`internal/application/` 里 `child_task_review.go`、`child_task_tool.go`、`dependency_wait.go`、`mcp_client_tool.go`、`mcp_operation_approval.go` 与 `internal/httpapi/` 里 `fanout_execution_control.go`、`github_review_test.go` 在本分支基线 `2dcf89ba` 上就已经不合规（逐文件 `git show <base>:<path> | gofmt -l` 核对过），非本次引入，也未顺手格式化。
- 本轮复跑：`go test ./internal/httpapi/` 全套 223.97s 通过；
  `go test -run TestAgentBrowserDownload ./internal/application/`、
  `go test ./internal/browserruntime/`（携带 #520 的两条新回归）通过；
  `TestOpenAPIGoldenDocumentMatchesGoDTOs` 通过，`docs/openapi.json` 无需再生成。
