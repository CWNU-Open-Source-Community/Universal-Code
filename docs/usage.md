# Usage

See [Execution workers and saved workflows](execution-workers.md) for delegated command, file, browser and MCP execution.

## Canonical product vocabulary / 规范产品词汇

| User term / 用户词汇 | Meaning / 含义 |
| --- | --- |
| Thread（任务） | Stable task, URL, Run succession, and history identity / 稳定任务、URL、Run 后继与历史身份 |
| Run（执行尝试） | One finite execution attempt / 一次有限执行尝试 |
| Step / Tool Item（步骤 / 工具项） | User-readable grouping / one structured tool lifecycle / 用户可读分组 / 单个结构化工具生命周期 |
| Workspace（工作区） | Operator-selected source scope; not a sandbox or permission / 操作者选择的源码范围；不等于沙箱或权限 |
| Plan item（计划项） | User label for one Plan/Delivery `WorkItem` / Plan/Delivery 中一个 `WorkItem` 的用户标签 |

`Mission` is the immutable Thread intent/Profile/Workspace/Scope object, and
`Session` is one Run-local context and authority boundary. They appear in advanced
diagnostics and compatibility commands only; a Session is never merged across Runs.
`Mission` 是 Thread 的不可变意图/Profile/Workspace/Scope 对象；`Session` 是单个
Run 独占的上下文与授权边界。二者只用于高级诊断和兼容命令，Session 不跨 Run 合并。

`cyberagent`, `cyberagent-workbench`, `CYBERAGENT_*`, and `.prayu/...` are stable
compatibility identifiers. The CLI binary and commands, `/api/v1/runs`,
`/api/v1/sessions`, OpenAPI schemas, event types, and SQLite `work_item` identities
remain unchanged. / 上述 CLI、API、协议、事件与 SQLite 标识保持不变。

## 外观 / Appearance

打开「设置 → 外观」选择主题，偏好保存在当前设备，重新打开应用后继续生效。
Open **Settings → Appearance** to choose a theme. Your choice is saved on the current device.

| 选项 / Option | 效果 / Behavior |
| --- | --- |
| 系统 / System | 随设备的浅色或深色设置即时切换，包括已打开的代码编辑器。Follows device appearance changes, including open code editors. |
| 浅色 / Light | 明亮的中性白色界面。A bright, neutral white interface. |
| 深色 / Dark | 深灰界面与玻璃材质。Dark gray surfaces with glass materials. |
| 雾透 / Mist | 灰蓝透色与轻盈磨砂，可调模糊度、磨砂度、背景亮度和光斑。Muted blue-gray frosted surfaces with adjustable blur, opacity, brightness, and ambient glow. |

![外观设置中的四个主题选项](images/appearance/theme-options.png)

系统选项的预览左右分色；其他预览展示对应主题的侧栏、对话与输入框。手动选择浅色、
深色或雾透后，设备设置的变化不会覆盖该选择。雾透的「还原默认值」恢复标准云母参数。
System uses a split preview; the other previews show their sidebar, conversation, and composer.
An explicit theme stays selected when the device appearance changes. Mist’s **Reset to defaults**
restores the standard Mica settings.

查看[浅色原图](images/appearance/conversation-light.png)、[深色原图](images/appearance/conversation-dark.png)
和[雾透原图](images/appearance/conversation-mist.png)。截图于 2026-10-19 在 Windows / Edge
中从生产构建拍摄，窗口为 1440 × 900；项目、回复和用量属于离线演示数据。
README 的分割图由同一对话的浅色与深色原图组合。
Screenshots were captured from a production build in Edge on Windows at 1440 × 900 on
2026-10-19. Project content, replies, and usage are offline demonstration data. The README
combines the Light and Dark captures of the same conversation.

### 窄窗口 / Narrow windows

桌面窗口最小尺寸为 **480 × 600 逻辑像素**；系统显示缩放会影响截图中的实际像素数。
The desktop window has a minimum size of **480 × 600 logical pixels**; display scaling changes the physical pixel count.

两侧面板按窗口宽度依次收起：左侧栏展开时，文件或终端面板在 ≤960px 收起；
左侧栏在 ≤720px 收起。左侧栏已隐藏时，右侧面板的阈值为 ≤720px。
重新拉宽后恢复原文件或终端，手动关闭的面板保持关闭。左侧使用 300ms、右侧使用 500ms
的推拉过渡；系统开启「减少动态效果」后立即切换。
The file or terminal pane collapses at ≤960px with the sidebar open, or ≤720px with it hidden.
The sidebar collapses at ≤720px. Widening restores the selected file or terminal; a manually closed pane stays closed.
Transitions take 300ms on the left and 500ms on the right, and are disabled when reduced motion is enabled.

窗口变窄时，侧栏收为可打开的浮层，并保留宽屏时的显示偏好。标题与常用工具合为一行，
「审阅改动」「查看上下文」可从右上角对话菜单打开。输入框保留常用图标和模型选择，
窄输入框的编辑模式切换移入「＋」菜单；窗口缩放、切换编辑模式都保留草稿。
底部统计在窄窗口保持一行，点击轮次或用量可查看生成速度、缓存命中等完整说明。
The sidebar becomes an overlay in narrow windows and remembers its desktop visibility.
The title and frequent tools share one row. Review and context move into the conversation menu; editor mode moves into the composer’s
**+** menu. Drafts remain intact when resizing or switching editors.
Statistics stay on one line; click activity or usage to see generation speed, cache usage and metric details.

| 深色 / Dark | 雾透 / Mist |
| --- | --- |
| ![480px 深色对话](images/appearance/narrow-dark.png) | ![480px 雾透对话](images/appearance/narrow-mist.png) |

以上为 480 × 844 的 Windows / Edge 生产构建截图，使用本地离线演示数据。
These 480 × 844 captures use the production build in Edge on Windows with local demo data.

## Portable Build Diagnostics

```powershell
cyberagent doctor portable
cyberagent doctor portable --json
powershell -ExecutionPolicy Bypass -File scripts/build-desktop.ps1 -VerifyReproducible
```

`doctor portable` is read-only: it does not open SQLite or read Provider credentials.
An ordinary `go run` build should report unpinned revision/source date/trimpath warnings.
The release script supplies reproducible metadata, builds twice when requested, and
runs the PE/hash/module/non-installing checklist. Automated checks do not replace the
manual Windows 10/WebView2/display/launch/recovery matrix, so release readiness remains
false until that matrix is signed.

## Threads, Runs, and advanced diagnostics / Thread、Run 与高级诊断

### 补充消息与排队 / Follow-up messages and queue

任务运行中，发送的补充消息会排到输入框上方，当前执行结束后依次处理。文字消息可点击
条目右侧的「引导」加入当前任务；「撤回」取消待处理消息，更多菜单提供编辑和全文、附件预览。
图片和文件附件保留在下一轮消息中。输入框增高时，排队卡片随它向上移动；多条消息在卡片内滚动。
底栏按输入框的可用宽度收起次要标签，模型名称过长时省略显示，完整名称仍可在模型菜单查看。

![紧凑宽度下的新对话输入框](images/composer/new-conversation.png)

![输入框上方的待处理消息与引导操作](images/composer/queued-messages.png)

以上为 Windows / Edge 生产构建截图，任务与回复使用本地离线演示数据。

Follow-up messages sent during execution appear above the composer and run in order afterward.
Use **Steer** on a text-only queued message to add it to the current task, **Withdraw** to cancel it,
or the more menu to edit and preview its full content and attachments. Images and files stay queued
for the next turn. The queue moves with the composer as it grows and scrolls internally when needed.

### 模型之间的聊天协作 / Model collaboration across chats

在当前聊天说明协作目标，例如「和接口实现那条聊天确认字段，再继续完成页面」。模型可以
查找同一项目内的其他聊天，主动发送问题或已有结论，并接收对方模型的回复。聊天记录
显示消息来源、目标和投递状态，点击对方标题可查看对应聊天。

运行中的接收方会在当前轮结束后接收消息；允许自动继续的空闲聊天会由后台处理。
接收方沿用自己的任务范围和权限。「已送达模型」表示消息已进入对方上下文，处理结果
以对方回复为准。

Describe the collaboration goal in the current chat, for example: “Confirm the fields with the
API implementation chat, then finish the page.” The model can find another chat in the same project,
send questions or findings, and receive its model's reply. Message cards show the source, destination
and delivery status, with a link to the other chat. Busy chats receive messages after their current
turn; eligible idle chats continue in the background. Each recipient keeps its own task scope and
permissions. “Delivered to model” confirms context delivery; the reply reports the outcome.

### 用量统计 / Usage

在「设置 → 用量统计」查看 Universal-Code 自身的模型调用。今日、近 30 天和全部
历史可切换 Token 总量、输入/输出/缓存明细、趋势、模型占比和最近八个会话；点击
会话标题可回到对话。活动热图固定展示最近 26 周；悬停、点击或方向键选中格子时，
白色边框标出当前日期，浮层显示日期和 Token 数，按 Esc 可关闭。页面沿用当前主题
和 Mono 字体，中文与 English 即时切换。

[深色页面](evidence/usage-statistics/usage-dark.png)和
[窄屏页面](evidence/usage-statistics/usage-mobile.png)来自实际浏览器；截图中的用量为
演示数据，用于检查图表与排版。

Token 按持久化调用完成时间统计，包含主代理、子代理、并行调用、上下文压缩及
已经产生用量的失败调用。缓存是输入 Token 的一部分；没有返回的用量和缓存明细
会明确标注。日期按页面显示的当前 UTC 偏移划分，页面打开时每分钟更新。

费用按实际结算时间统计，只展示已有完整价格记录的本地 USD 估算，并分别注明
已定价、缺少完整计价和未记录费用的调用。未启用费用记录的历史调用不能据此推算
真实账单。跨日结算与 Token 的日期可能不同，实际收费以供应商账单为准。

`GET /api/v1/usage?period=30d&utc_offset_minutes=480` uses existing read access.
`period` accepts `today`, `30d`, or `all`; the fixed UTC offset is in minutes.
Only this application's durable local records are aggregated. The endpoint does
not contact providers or inspect other applications' logs. Model token totals
include all agent roles; cache tokens are already included in input. Costs use
priced settlements, independently dated from call receipts. Missing usage and
pricing remain visible through coverage counts.

### Markdown 输入与复制

Web/Desktop 的输入框默认显示 Markdown 格式，支持标题、粗体、引用、列表、任务列表、
表格和代码块。输入 `# `、`- ` 等前缀可开始对应格式；粘贴 Markdown 会保留结构。
点击输入框底部的“Markdown”可切换到“纯文本”编辑原文，偏好保存在当前设备。
切换视图不会改写尚未编辑的原稿，发送和草稿恢复仍使用 Markdown 文本。

普通段落中 Enter 发送；标题、列表、引用、表格和代码块中 Enter 继续编辑。
Ctrl+Enter（macOS 为 Cmd+Enter）始终发送，Shift+Enter 换行。
Ctrl+Shift+V（macOS 为 Cmd+Shift+V）按纯文本粘贴，保留 Markdown 标点符号。

用户消息与模型回复使用相同的 Markdown 展示。需要单独复制的文字可放在 `text`
围栏块中；块顶部显示“文本”和复制按钮，复制内容不包含围栏。代码围栏显示语言名称。
普通段落保留整条回复的复制入口。

聊天正文使用 14 px 字号、1.625 倍行高和正常字距，代码块保持 13 px。
已完成的段落采用智能换行，标题均衡换行，长标识符在空间不足时折行。
实时回复的新片段以 150 ms 的轻微模糊淡入显示，不额外等待或延迟输出；历史消息和
代码块直接显示。开启系统的“减少动态效果”后，文字直接出现。

AI 回复下方的“本轮改动”卡片汇总本轮已应用的文件编辑，显示文件数和净增删行数。
默认展示三个文件，可展开全部；点击文件或“查看变更”可查看本轮起始内容与最终内容的差异。
统计范围为文件编辑工具留下的应用记录；命令直接写入的文件不在此范围。敏感内容或不连续的
记录会标明差异与行数不可用，没有净改动时不显示卡片。

卡片上的“撤销”会先检查当前文件，确认后将本轮记录的改动恢复到本轮开始时的内容。
同一文件的多次编辑会一起撤销；其他路径与 Git 暂存区保持不变。有后续文件修改、任务仍在
执行或编辑记录不完整时会显示原因。成功后显示“已撤销”，刷新页面后仍保留此状态；
请求中断时可“重新检查”结果。若恢复中途失败，会分别列出已恢复和未恢复的路径。

Agent 建议固定任务要求时，问题会显示在输入框上方的独立卡片中。可选择“本次任务始终保留”
或“仅当前讨论”，也可直接撰写回复，再点击“发送”。自定义回复只有勾选“将这段回复固定到本次任务”
才会成为固定要求；“跳过”不固定内容，也不发送答复。回答失败时可重试原答复。
卡片沿用任务要求确认流程，当前用于确认压缩后仍需保留的要求。
提问期间，空消息编辑器收为单行；点击“输入消息”展开编辑与权限、网络等控制。
已有草稿或附件时保持完整编辑器。暂停状态与继续操作沿同一条底栏排列。

输入框变高或切换编辑模式时，对话仍会跟随最新内容；主动向上阅读历史时保留阅读位置。
窄窗口中的问题内容可以单独滚动，回答和主输入框分别保留自己的操作区。

### 输入框下方的对话统计

输入框下方显示当前任务主代理的轮数、模型步骤、生成速度、累计 Token、缓存命中率和
上下文比例。点击指标可查看计算说明；键盘可用 Tab、Enter 和 Escape 操作。

轮数统计已接收的用户输入，模型步骤合并同一步的重试。Token 按实际调用累计，输入
已包含缓存，不重复相加。生成速度只使用有完整计时的流式调用，计算为输出 Token
除以生成时长，不含首 Token 等待和工具执行时间；历史调用没有计时时不显示速度。
只有全部用量记录都包含缓存明细时才显示命中率，否则显示“—”。子代理用量不计入此栏。

上下文比例来自最近一次模型请求的输入估计和应用规划窗口，未取得窗口信息时不显示。
新对话产生统计数据后才显示此栏。

### 任务与执行

In Web and Desktop, a newly created task opens automatically only while its
original waiting page is still current. After navigating elsewhere, use the
creation notice to open it; its first message continues with the original
request identity. Resuming a paused task also keeps its pending result, retry
identity, and refresh bound to that task and Run, without changing another task's
draft or controls.

Web 和 Desktop 创建任务后，仅在仍停留于原等待页时自动打开。切换页面后，可通过
“打开已创建的对话”提示主动进入；首条消息仍按原请求继续交接。解除暂停的等待、
错误与重试归属原任务及 Run，不影响当前任务的草稿和操作状态。

Both connection paths preserve the enabled batch delivery and host-validation
capabilities together with their existing permission requirements. Read-only
connections can inspect pending approvals and the API's redacted previews;
approval, denial, and decision recovery still require approval control authority.
两种连接入口均保留已启用的批次交付能力及原有权限组合。只读连接可查看待审批条目
与接口允许展示的脱敏预览；批准、拒绝和恢复决定仍需审批控制权限。

Long conversations keep older pages available through “加载更早记录”. Live updates
synchronize recent persisted records independently of that pagination, and preserve
the reading position while older history loads or the operator switches tasks.
Settings, Inspector and change review load when opened; their first opening may
briefly show a loading message. See the [frontend performance evidence](FRONTEND_PERFORMANCE_260.md).

长会话可通过“加载更早记录”继续查看历史；实时更新单独同步最新持久记录，加载旧页
或切换任务时保留阅读位置。设置、Inspector 与改动审阅在打开时加载，首次进入可能
短暂显示加载提示。请求量、首屏资源和字体评估见[前端性能验证记录](FRONTEND_PERFORMANCE_260.md)。

Application preview lists the current task's managed command Jobs with their original
Run and output. “把启动要求放回草稿” preserves the draft for review and sending; it
does not launch a command. After submission, select the associated Job, inspect its
stdout/stderr and explicitly choose a candidate local URL. Browser preview still
requires the existing live Full and browser-control permissions. Closing its browser
and stopping the selected service are separate actions; hiding the panel performs
neither. Output URLs are unverified candidates, and exited services stay visibly exited.
See [ADR 0166](adr/0166-thread-application-service-preview.md).

“应用预览”列出当前任务所属命令的原始 Run、Job 与输出。“把启动要求放回草稿”会保留
原稿，确认发送后才进入普通任务执行。提交后选择对应 Job，查看 stdout/stderr，并明确
选择一个候选本机地址。浏览器仍需要现有的 Full 激活与浏览器控制权限。“关闭浏览器”
和“停止此命令”分别作用于该浏览器会话和选中的 Job；收起面板不执行这两项操作。
输出地址不等于服务已就绪，失败或退出的命令会保留其真实状态及输出。

```powershell
cyberagent run create "review this workspace" --workspace demo --profile review --surface code --phase plan
cyberagent run create "explain this code" --profile learn --max-turns 40 --max-tokens 20000 --timeout 20m
cyberagent run adapt-task <legacy-task-id>
cyberagent run list
cyberagent run list --status paused
cyberagent run show <run-id>
cyberagent run mode <run-id>
cyberagent run plans <run-id>
cyberagent run plan show <proposal-id>
cyberagent run plan choose <proposal-id> 2 --operation-key <stable-key>
cyberagent run plan selection <run-id>
cyberagent run phase <run-id> deliver --operation-key <stable-key> --reason "plan accepted"
cyberagent run delivery checkpoint <work-id> --operation-key <stable-key> --focused "focused tests passed" --diff-audit "diff reviewed" --security-audit "security reviewed" --handoff "slice handoff"
cyberagent run delivery checkpoint <final-work-id> --operation-key <stable-key> --focused "focused tests passed" --diff-audit "diff reviewed" --security-audit "security reviewed" --handoff "module handoff" --functional "full suite passed" --robustness "race and failure paths passed"
cyberagent run delivery list <run-id>
cyberagent run delivery show <checkpoint-id>
cyberagent run delivery report <run-id>
cyberagent run delivery report <run-id> --json
cyberagent run delivery record <run-id> --operation-key <stable-key> --verification-jobs <job-id-1>,<job-id-2> --json
cyberagent run delivery record <run-id> --operation-key <stable-key> --declaration no_applicable_tests
cyberagent run steer enqueue <run-id> "review the current diff" --operation-key <stable-key>
cyberagent run steer cancel <steering-id> --operation-key <stable-key> --reason "requirement withdrawn"
cyberagent run steer drain <run-id> --max-steps 1
cyberagent run steer list <run-id> --limit 100
cyberagent run steer show <steering-id>
cyberagent run events <run-id>
cyberagent headless events <run-id> --max-events 1000
cyberagent headless events <run-id> --after-sequence <n> --follow --timeout 30m
cyberagent run start <run-id>
cyberagent run step <run-id>
cyberagent run execute <run-id> --max-steps 3
cyberagent run execute <run-id> --max-steps 3 --finish --summary "planning complete"
cyberagent run finish <run-id> --summary "review complete"
cyberagent run fail <run-id> --reason "blocked by provider"
cyberagent run checkpoint <run-id>
cyberagent run graph <run-id>
cyberagent run lease <run-id>
cyberagent run execution-interaction <run-id>
cyberagent run execution-interaction set <run-id> controlled --operation-key <stable-key> --trust trusted --confirm-workspace-trust --operator operator
cyberagent run execution-permission <run-id>
cyberagent run execution-permission set <run-id> ask --operation-key <stable-key> --enable-permission-control
cyberagent run execution-permission set <run-id> auto --operation-key <stable-key> --enable-permission-control
cyberagent run execution-permission set <run-id> full --operation-key <stable-key> --enable-permission-control --enable-danger-full-access --confirm-full
cyberagent run browser-cdp-permission <run-id>
cyberagent run browser-cdp-permission set <run-id> restricted --operation-key <stable-key> --enable-browser-cdp-control
cyberagent run browser-cdp-permission set <run-id> full_debug --operation-key <stable-key> --confirm-full-cdp-debug --enable-browser-cdp-control --enable-full-cdp-debug --enable-permission-control --enable-danger-full-access --confirm-full
cyberagent run capability-readiness <run-id>
cyberagent run capability-readiness <run-id> --json --enable-permission-control --enable-workspace-sandbox --enable-browser-cdp-control --enable-docker-execution
cyberagent sandbox local-readiness --enable-workspace-sandbox --json
cyberagent run command-plan <run-id> git-status
cyberagent run command-plan <run-id> git-diff-check --timeout 30s
cyberagent run command-plan <run-id> go-version
cyberagent run command-plan <run-id> powershell-workspace-list --path scripts
cyberagent run command-execute <run-id> go-version --operation-key <stable-key> --confirm-execution
cyberagent run command-execute <run-id> powershell-workspace-list --path scripts --operation-key <stable-key> --confirm-execution
cyberagent run command-proposal list <run-id> --limit 50
cyberagent run command-proposal show <proposal-id>
cyberagent run pause <run-id>
cyberagent run resume <run-id>
cyberagent run cancel <run-id>
cyberagent ui-evidence list --run <run-id> [--status <status>] [--limit <n>]
cyberagent ui-evidence show <attempt-id>
cyberagent ui-evidence artifact <attempt-id> <artifact-id> --output <new-path>
```

`run delivery report` reads the same freshness-aware
`standard_code_delivery.v1` projection used by Desktop, authenticated HTTP,
Code Handoff, GitHub Review, and Standard Code completion. `run delivery record`
first creates an aligned final Checkpoint and then seals selected terminal
Command Runtime Jobs. Use `--declaration user_skipped|budget_exhausted|missing_dependency|approval_denied`
when that explicit non-test outcome applies; a declaration cannot be combined
with Job ids. Only `passed` is verified. Changes after verification, including a
Supervisor mutation epoch change, make the old receipt `stale` and require a new
verification. The report links affected relative files, output Artifacts and
Checkpoint recovery operations. It does not use Agent prose or CI names as
evidence and does not commit, push, merge, overwrite source files, or reverse
effects outside the Workspace. See [Standard Code Delivery Truth Gate](standard-code-delivery.md).

Schema v115 exposes `agent-code-tools.v1` to the root Supervisor during ordinary
`run step` or `run execute` model rounds. It is not a separate user command and it
does not grant a shell. Code/Plan exposes bounded `workspace_list`,
`workspace_read`, `workspace_glob`, and `workspace_grep`. Code/Deliver keeps those
reads and, only for Code or Script Profiles, adds `workspace_change`,
`workspace_apply`, and the separately confirmed `workspace_delete`. Review/Learn
remain read-only; Cyber and Specialist receive none of these tools.

`workspace_change` can propose one exact replace, create, or move. A human reviews
the resulting FileEdit through the existing CLI/API/Desktop approval flow before
`workspace_apply` can write it. Deletion uses its own tool and confirmation path.
Go revalidates the persisted Run/Mission/Workspace/root, mode and permission
revisions, exact reviewed operation, source/destination hashes, Policy, and budget
at execution time. Directory/file results are stable, cursor-paginated, bounded,
secret-redacted untrusted evidence; hidden entries other than the Go-allowlisted
`.github` code-evidence tree, ignored entries, links/reparse points,
binary/non-UTF-8 data, oversized files, root escape, and casing ambiguity fail
closed. Calls, refusals, bounded results, and read Artifacts survive Run recovery.

`cyberagent run show <run-id>` prints `agent_code_tools_protocol`, the capability
generation, and one availability/refusal line per tool. The same snapshot is
available in `GET /api/v1/runs/{run_id}` and the Desktop Run overview. Runs created
without a registered Workspace retain compatibility, but their snapshot marks all
seven tools unavailable and the Supervisor does not advertise them.

### Read-only LSP code intelligence

Web and Desktop **Settings → Extensions** can stage a language-server configuration
for a registered Workspace. Supply the installed executable's absolute path and
SHA-256, language/file suffix mappings and any required arguments. Review the
staged fingerprint explicitly, then use the separate test action with a
Workspace-relative source file or symbol query. A saved configuration does not
mean that initialization or a query succeeded. Reviewed settings are saved in
the application's managed `code-intel.json` and loaded on the next startup;
explicit CLI/environment configuration continues to take precedence and makes
settings configuration read-only. Edit that selected file using the existing CLI
flow, or restart without an explicit configuration to manage it from settings.

Select an absolute operator-reviewed `code-intel-config.v1` file with
`CYBERAGENT_CODE_INTEL_CONFIG`, `cyberagent code-intel ... --config`,
`api serve --code-intel-config`, or Desktop `--code-intel-config`. A status read does
not start a server; `qualify --workspace <name> --start=false` rechecks the Workspace,
review fields, and executable SHA-256, while the default `qualify` also initializes the
server and records its negotiated capability fingerprint and process generation.

Only a Code-surface root in Plan or Deliver can receive the negotiated
`code_workspace_symbols`, `code_document_symbols`, `code_definition`,
`code_references`, `code_implementation`, `code_hover`, `code_signature_help`,
`code_diagnostics`, `code_call_hierarchy`, and `code_type_hierarchy` definitions.
They reuse the exact `agent-code-tools.v1` read authority and its invocation, result,
refusal, budget, paging, and Artifact ledger. Cyber and Specialist are denied even if
a stale or forged capability payload reaches the Gateway.

Results label semantic evidence `current|partial|stale|unavailable` and bind the
Workspace/root, repository commit/branch/dirty state, document URI/hash/version,
server generation/capability fingerprint, and query/page. `review@1.5.0` and
`focused-checks@1.2.0` preserve those bindings and never treat an LSP answer as proof.
Configuration examples, API/Desktop metadata, real gopls/TypeScript coverage, and the
explicit non-sandbox trust boundary are documented in
[Code Intelligence](code-intelligence.md).

`browser-cdp-permission` 以独立快照记录现代 Full 的 Full-CDP 子开关。
`restricted` 保留导航、有界 DOM 和截图上限；`full_debug` 还包含请求捕获、
改写、重放、Cookie 和任意 CDP 方法。进入 Full 时默认开启，可独立关闭，
重新开启必须精确确认，并在操作时验证当前进程激活；离开 Full 强制回到 `restricted`。当前两种快照都固定
`transport_enabled=false`、`browser_start_authorized=false`、
`runtime_authorized=false` 与 `capability_grant=false`，因此上述命令不会启动
浏览器、访问网络或创建 Profile。

A Thread is the stable user task and history identity; a Run is one finite execution attempt. The `run create` and `--session` paths retain compatibility behavior: internally each new Run normally receives a dedicated Run-local Session, while Mission and Session creation/attachment plus initial events commit together in SQLite. Use `thread` commands for canonical user history, `run` commands for attempts, `session` commands for diagnostics or compatibility, and `run adapt-task` only for a qualified legacy `agent.Task`.

Schema v86 separates execution interaction intent from general runtime authority. `preview` is the default. `controlled` requires a Code-surface Run, the Local execution profile, explicit operator trust, and an explicit Workspace-boundary confirmation. `debug` additionally requests a user-owned ConPTY terminal, while `cyber` requires a Cyber-surface Run and Docker profile. Models, Agents, Skills, and repository content cannot select these modes. Every interaction snapshot still fixes process execution, network, capability grants, and execution authorization to false. The old one-shot execution path is retired; its immutable receipts remain historical evidence only.

Current execution preferences are `ask|auto|full`. Ask and Auto use the common per-operation authorizer and exact approvals when required; Full requires explicit confirmation and live activation in the current process. The Standard Code preset selects Ask. Commands use the same `command-runtime.v2` protocol with an independently ready Local or explicit Docker sandbox adapter, or an explicitly installed host adapter. A missing sandbox never falls back to host execution. `--enable-workspace-sandbox` opens the Local gate only after its real AppContainer/WFP/Job/ACL readiness proof; Docker remains independently enabled. Host Ask/Auto calls require exact durable approval, while host Full requires live activation. Saved permission rows cannot restore either authority. A permission revision change fences old calls and Jobs. See [Command Runtime adapter split](architecture/command-runtime-adapter-split.md) and [retirement decision](adr/0165-retire-legacy-command-execution.md).

The current Web/Desktop permission selector and readiness projection use only
Ask / Auto / Full; the five legacy values remain historical read formats. The
execution environment, Debug interaction/runtime, exact URL fetch scope and
browser-CDP permissions are separate controls. A Debug restart does not restore
Full activation or enable independent batch-validation/UI-evidence capabilities.

In a task, open **审阅改动 → 更多交付工具** to select batch delivery, advanced Git,
GitHub review or UI evidence. Select the execution record before opening a tool;
its reads, review and approval flow remain bound to that Run. Switching records
clears the retained tool review. Missing startup capabilities show their existing
configuration requirements and keep execution disabled. UI evidence history is
still read independently of write capability; an unavailable reader is shown as
unknown history, never as an empty evidence ledger. See
[the #287 acceptance record](FRONTEND_CAPABILITY_ALIGNMENT_287.md).

`run capability-readiness` and `GET /api/v1/runs/{run_id}/capability-readiness`
return the same Go-owned `run_capability_readiness.v1` projection used by Desktop.
Each Permission, Profile, Interaction, browser-CDP, and Standard Code option reports
`selected`, `selectable`, `runtime_available`, stable `blocked_by` and `remediation`
arrays, and `restart_required`. Its `command_runtime` object separately reports
`protocol_available`, `adapter_installed`, `adapter_ready`, and
`current_run_granted`, plus the selected kind/backend when one is actually bound.
Local or Docker intent may be selectable while its backend remains unavailable.
Human output is stable for diagnosis; `--json` emits
the exact protocol. Startup flags describe only the current invocation and do not
prove backend readiness. The read response contains no private path or bearer and
always has `capability_grant=false`; every mutation and execution path rechecks its
own control token, state, lease, confirmation, Policy, and backend gates. Unknown
protocol versions fail closed. See [ADR 0128](adr/0128-go-owned-run-capability-readiness.md).

### Run-owned Drydock

`cyberagent drydock create` is a two-step operation: first inspect the exact source and
copy the returned `trust_digest`; then call create with a new operation key,
`--confirm-workspace-trust`, and `--expected-trust-digest`. The product derives the
managed path and branch below `$CYBERAGENT_HOME/drydocks`; source dirty content is
reported but not copied. `status|use|checkpoint|rewind|undo|fork|deliver|cleanup|reconcile|gc`
all share the v127 ownership, generation, checkpoint, event, and receipt ledger.

Checkpointing observed changes requires `--confirm-observed-changes`; Rewind and Undo
return a preview before an explicitly confirmed apply. Delivery returns only a bounded
review patch and cannot push, merge, force-update, or overwrite the source. Cleanup and
GC preserve any dirty, drifted, ambiguous, or unknown directory and use non-force Git
removal only after exact clean ownership is re-proved. The worktree itself supplies no
process/network/credential isolation and Workspace Trust is not runtime authority. See
[Drydock Workspaces](drydock.md) for complete commands and recovery steps and
[ADR 0129](adr/0129-run-owned-drydock-workspaces.md) for the durable contract.

### Standard Code fixed Docker fallback

The opt-in Docker adapter uses the current Ask/Auto/Full operation contract and
the existing immutable Docker admission ledger. The Supervisor
command contains only `go|node|python|rust`, argv, a Drydock-relative cwd, timeout, and
purpose. One operator-configured exact image digest and the fixed local Engine are
readiness inputs; the product never accepts an image/endpoint/mount/flag from the
command and never pulls. The current Drydock is the sole writable host projection at
`/workspace`, with a fixed read-only metadata mask over `/workspace/.git`; network
is none, rootfs/toolchains are read-only, user/resources are fixed, and host credentials
are absent. Docker/daemon/image failures remain blocked results and do not invoke a host
runner. See [Standard Code Docker](standard-code-docker.md) and
[ADR 0131](adr/0131-standard-code-docker-network-none-backend.md).

### Standard Code atomic preset

Schema v133 adds `cyberagent run standard-code preset` as the ordinary Start-coding
entry point. It calls one Go Application operation that creates or reuses a compatible
Run and commits Code/Plan, a ready Local backend or explicitly requested Docker,
controlled interaction, Ask for a new Run, restricted browser CDP, and the exact
trusted ready Drydock together. The preset fixes network to disabled and credentials
to none. Existing Ask/Auto/Full preferences are preserved, while historical modes
migrate to Ask. It does not activate Full, start a process, enter Deliver or grant a bearer.

The first call without trust confirmation returns an exact `trust_digest` and does not
persist a preset tuple. After reviewing the Workspace, use a new operation key with
`--confirm-workspace-trust --expected-trust-digest <sha256>`. Exact replay of that
confirmed key returns the same configured snapshot identities; changing an intent
field conflicts. `auto` only selects ready Local. To use Docker after Local is
unavailable, explicitly pass `--backend docker --enable-docker-execution`; no path
falls back to a host runner or Full activation.

An existing Run must be created or paused with no active lease. A running Run returns
the separate `pause-and-configure` next step. Invoke
`cyberagent run standard-code pause-and-configure <run-id>` and retain the same
operation key while it waits for lease and Supervisor quiescence. An incompatible
Surface produces a new Code/Plan Run without changing the original Run identity.

Control-token HTTP/OpenAPI and Desktop expose the same operation, response facts,
blockers, next steps, and events; React sends one request rather than sequencing old
policy endpoints. See [Standard Code atomic preset](standard-code-preset.md) for
commands, routes, trust confirmation, and recovery, and
[ADR 0136](adr/0136-atomic-standard-code-preset.md) for the transaction boundary.

### Standard Code bounded completion loop

After the preset is configured, the root Supervisor uses the existing tool loop
under `standard_code_supervisor.v1`. In Plan it must complete two consecutive
Workspace/Code Intel read rounds before proposing directions. Only an explicit
operator selection and Plan-to-Deliver transition unlock reviewed Workspace
proposals/apply and the sandboxed Command Runtime.

An applied edit is progress only when its receipt owns a completed after-Checkpoint.
The Supervisor then chooses repository-derived build/test/lint commands; a failed
exit enters Diagnose, a fix advances the mutation epoch, and only successful
structural verification of the current epoch permits `finish`. Persistent failure,
budget exhaustion, permission/context drift, or missing evidence stops with a stable
reason and requires `wait`, never a completion claim.

Background `start/read/wait/write_stdin/cancel/kill` calls retain their Run owner,
permission revision, mutation epoch, and exact output cursor across turns. Exact
recovery reuses existing receipts; a different call repeating an already handled
side effect is not invoked. See [Standard Code bounded completion loop](standard-code-supervisor.md)
and [ADR 0137](adr/0137-bounded-standard-code-supervisor.md).

The `restricted|full_debug` browser-CDP snapshot is independent from process authority. Full CDP requires modern Full with live activation, its dedicated runtime gate and exact risk confirmation. It controls only a Traverse-managed isolated browser, never the Wails WebView or the user's system Chrome. Windows Desktop exposes Run-scoped Open/Status/Close and owns the browser, disposable Profile and bounded transport lifetime; changing the snapshot alone launches nothing. Ordinary CLI and Supervisor callers do not receive that browser process authority.

Schema v119 supplies that concrete operation only for source-bound local UI
verification. `ui-evidence.v1` seals the exact Git/non-Git source state, reviewed
build/start recipe, fixed browser executable and version, literal loopback URL,
viewport/DPR, locale/theme/reduced-motion state, fixture/seed, ordered steps,
capture policy, and fail-closed diagnostic policy before execution. It rejects an
occupied readiness port instead of adopting another service, revalidates source
before build, after readiness, and before pass, and owns the application process,
browser Job, disposable Profile, network guard, port, and cleanup receipt. Only
`passed` is success; `not_run` is deliberately neutral.

Execution is currently a Windows Desktop capability. Build the Desktop and launch
it with all five independent gates:

```powershell
.\build\desktop\TraverseBoard.exe `
  --enable-permission-control `
  --enable-danger-full-access `
  --enable-run-execution `
  --enable-browser-cdp-control `
  --enable-ui-evidence
```

The selected Run must also be Code/Local/Deliver with modern Full, current
process activation, an active root execution lease, and current `restricted` browser-CDP permission.
The Desktop panel requires review of the complete JSON request before start and
shows the manifest, steps, diagnostics, cleanup, and content-addressed artifacts.
Disabling control later leaves historical evidence readable. The standalone CLI
intentionally supports only list/show/hash-verified export and cannot start or
cancel a browser. Export creates a new `0600` file exclusively and labels its
contents untrusted. Full request examples, artifact rules, and CI receipt fields
are documented in [Real-browser UI Evidence](ui-evidence.md).

Debug Agent terminal input is a separate process-local lease bound to one Workspace, Code Run, terminal session, exact interaction snapshot ID/revision, and Local profile. The opaque bearer is never persisted or returned to the renderer/model, lasts at most 15 minutes, can be revoked immediately, and becomes invalid on restart. Active leases and revoked-token summaries are bounded. Host lock/disconnect/logoff, sleep/resume, Run termination, Workspace or interaction rebinding, terminal replacement, and shutdown revoke affected leases. A selected mode is not a lease, and a lease is not process authority. The Desktop bridge exposes only explicit grant/query/revoke; the root Supervisor receives only a token-free active binding and can write/read through Go after every scope, permission, phase, policy, and expiry check. Cyber terminal input remains unavailable.

`run command-plan` accepts only `git-status`, `git-diff-check`, `go-version`, and `powershell-workspace-list`. The PowerShell option is a Go-owned fixed `-NoProfile -NonInteractive -ExecutionPolicy Restricted` template. Its Workspace-relative path is transported as canonical UTF-8 hex data and decoded inside the fixed script, so it is never evaluated as a PowerShell expression. Callers cannot supply executable names, raw script text, environment variables, stdin, pipelines, or shell chaining. The plan remains non-starting.

`run command-execute` runs one of the same four fixed templates through Command
Runtime. It requires a stable operation key, exact execution confirmation and
current operation authority. The Windows fixed adapter retains restricted-token,
Job Object, executable-pinning, output and cancellation checks. A stored intent
with no receipt is never automatically retried.

The old `controlled_command_propose` producer and `run command-proposal review`
are retired. `run command-proposal list/show` reads saved proposals, decisions and
receipts without creating execution authority.

The Windows Desktop user terminal is default-off. Build normally, then launch it explicitly:

```powershell
.\build\desktop\TraverseBoard.exe --enable-user-terminal
```

The selected Run must have exact Code/Local/Debug bindings and a trusted Workspace. The user must click Start and remains the only default input source. Traverse Board starts Windows PowerShell with `-NoLogo -NoProfile` in a ConPTY assigned to a creation-time Job Object, keeps at most eight process-local sessions and 4 MiB of rolling raw output per session, and closes the session if its durable binding changes or its Run terminates. A second explicit grant can let the root Supervisor submit policy-checked commands for 15 seconds to 15 minutes during Deliver; revoke is immediate. Raw user input, raw PTY output, environment, process identity, and the bearer are not written to SQLite. Model-authored command text and its sanitized bounded result are durable Supervisor evidence. This is not an always-authorized Agent Shell and not a Cyber Docker terminal.

Schema v41 gives each Run one immutable `run_mode.v1` snapshot with two independent axes. `--surface code|cyber` selects the work domain and cannot change inside that Run. `--phase plan|deliver` selects whether the Supervisor is preparing a bounded plan or delivering it. Omitting both preserves the compatibility default `code/deliver`. Neither axis grants tools, network, Shell, file mutation, approval, or child-Agent authority; those remain separate Go-owned Policy and Scope decisions.

`run mode` prints the current snapshot and revision. `run phase` is an explicit operator transition that requires a stable 16-256-byte operation key. It is accepted only for `created` or `paused` Runs with no active execution lease; the Store rechecks these conditions transactionally. Exact replay returns the existing revision, changed intent conflicts, and the raw key is never persisted. Surface, Profile, Workspace scope, protocol, and policy version remain fixed for every revision. Network scope may only append exact public HTTPS hostnames through the separate revision-bound, audited network-authority control; it never implies a search provider or wildcard/public-network grant. To move from Code to Cyber or vice versa, create a new Run under the intended authorization scope.

Session messages, assistant policy decisions, ToolRun changes, and FileEdit changes are projected into `run events` transactionally. Activity carrying a workspace different from the Run scope is rejected. `run start` advances the lifecycle from `created` through `preparing` to `running`; it does not invoke a model by itself.

`run step` asks the RunSupervisor to execute exactly one root Agent turn. It writes a `turn_started` checkpoint before the model call, loads the persisted execution-mode snapshot, permits only the bounded create-only WorkItem/Note tool loop, and ultimately requires one strict `root_lifecycle.v1` JSON action. The Supervisor validates and interprets the action, then atomically stores the user-facing message, policy decision, model usage, lifecycle events, cumulative token/model-time counters, and next checkpoint. Raw protocol JSON is not written to Session history. `run checkpoint` displays the durable Supervisor phase, protocol-repair phase/reason, next turn, token counters, and execution milliseconds. A process restart resumes an unfinished started turn or pending tool batch; a committed turn is never appended twice. Turn or token budget exhaustion returns exit code 8, while the persisted execution-time boundary returns a deadline error.

Root actions use `continue`, `finish`, or `wait`. `continue` advances to another idle turn. `finish` requires a summary and atomically completes the Run. `wait` requires a reason, atomically pauses the Run, and resumes at the next turn after `run resume`. Unknown fields, trailing data, Markdown fences, invalid combinations, and responses over 64 KiB fail the current turn without writing user/assistant messages. Assistant prose by itself cannot mutate Run state.

In `plan` phase, `finish` is deliberately invalid: the model receives one bounded lifecycle repair and must return `continue` or `wait`, while explicit operator completion is also rejected. After the plan is accepted, pause the Run if needed and use `run phase <id> deliver`; the following turn receives the new durable mode revision. The current root tools remain proposal/create-only, so Plan mode cannot silently execute Shell, files, processes, network calls, or Specialist schedules.

Schema v42 adds the strict `plan_delivery.v1` proposal tool for root Plan turns. A valid proposal has exactly three directions. Each direction contains 1-8 ordered delivery modules with a title, summary, acceptance criteria, bounded tradeoffs, and dependencies that may reference earlier modules only. Unknown fields, duplicate titles or dependencies, stale mode revisions, inactive root turns, invalid leases, Policy denial, and exhausted tool budgets fail closed. Proposal creation records no selection and authorizes no phase change, work execution, tool, Shell, network, file mutation, or child Agent.

Use `run plans` and `run plan show` to inspect proposals after the Run pauses. `run plan choose` is the only selection path and accepts direction `1`, `2`, or `3`, an optional bounded operator identity, and an exact normalized 16-256-byte operation key. It requires a paused Plan Run with no active execution lease, then atomically creates the selected WorkItems and backward dependency graph, a pinned decision Note, the immutable selection, and metadata-only events. Exact cross-process replay returns the same objects; a changed direction or identity under the key conflicts. Selection still leaves the Run in Plan, so `run phase <id> deliver` remains a separate explicit action. HTTP, TUI, and Web only read this state.

Schema v44 enrolls new and untouched legacy selections in `delivery_checkpoint.v1`. Before a selected WorkItem can complete, move it to `in_progress`, pause the Run in Deliver phase, and record one checkpoint for its exact current WorkItem version and mode revision. `--focused`, `--diff-audit`, `--security-audit`, and `--handoff` are mandatory, redacted, normalized operator attestations. The last selected module is the deterministic larger-module boundary and also requires `--functional` plus `--robustness`; non-final modules reject those flags. Recording atomically creates an immutable pinned handoff Note, a digest-keyed idempotency operation, and metadata-only events. Exact retries converge across processes; changed evidence under the same key conflicts. Afterward, `todo complete <work-id>` uses the existing WorkItem transition path and rechecks the gate in Go and SQLite.

The model has no checkpoint tool. HTTP, TUI, and Web expose only enforcement, required/ready counts, and bounded checkpoint metadata; they omit evidence, internal digests, operation keys, and requester identity. Policy also denies obvious Agent attempts to execute `cyberagent run delivery checkpoint` through Shell, process, script, or Sandbox tools. This is defense in depth rather than a claim that command-text regexes are a complete OS security boundary; real process execution remains disabled. A pre-v44 selection that already contained a completed or cancelled WorkItem is left explicitly compatibility-exempt (`delivery_gate_enforced=false`) rather than receiving invented evidence.

Schema v45 adds ordered operator steering for running or paused Runs. `run steer enqueue` requires a normalized 16-256-byte operation key and accepts one normalized UTF-8 message up to 16 KiB. A Run may hold at most 64 pending messages and 256 KiB of pending text. Exact replay returns the existing message; changed content, Run, or operator under the same key conflicts, and SQLite stores only a domain-separated key digest. `list` shows counts and ordered metadata, while trusted local `show` also displays the redacted content and its digest.

The Supervisor consumes only the oldest message at the next safe root-turn boundary. A failed model/tool turn leaves it pending and supersedes only that attempt's delivery; restart or lease takeover prepares it again. Session history receives the operator message and assistant response only in the same successful lifecycle transaction. If another message remains, model `finish` or `wait` is deferred to an effective `continue`, and Run completion is rejected until the queue drains. Failing or cancelling the Run cancels all outstanding steering. The queue never interrupts an active tool/model commit and grants no tool, Shell, network, write, approval, or child-Agent capability.

For a Run-bound Session, an ordinary `session send` automatically uses this queue when an execution lease is active, a recoverable attempt already owns PendingInput, or the Run already has queued steering. The command reports `queued`, steering ID, and sequence instead of pretending a model reply was produced. During a busy TUI action, plain text follows the same path without clearing live progress; slash commands remain blocked. HTTP, React, and the TUI queue view expose metadata only and cannot enqueue. A paused Run remains paused after enqueue and must be resumed explicitly.

Schema v46 adds local operator controls without changing queue authority. `run steer cancel` requires a stable 16-256-byte operation key and a non-empty reason of at most 2 KiB. It creates an immutable cancellation fact only while the message is pending and has no prepared delivery. Exact retry returns the same fact; changed intent conflicts. Prepared, committed, already-cancelled, or terminal-Run messages cannot receive a new operator cancellation. Editing and reordering remain unsupported. Run failure/cancellation closes remaining messages with bounded terminal facts in the lifecycle transaction.

`run steer drain` processes one queued turn by default and at most 64 per invocation. It acquires the Run execution lease before explicitly resuming a paused Run. A conflicting lease leaves the Run paused. The steering-only begin path refuses to generate a Mission-goal turn or recover an unrelated failed ordinary input, and every real turn still consumes the existing token/turn/time budgets and passes Policy. Empty queues do not wake paused Runs. This is an explicit local operation, not a background worker or new execution capability.

Use `session send <id> "message" --operation-key <stable-key>` when a Run-bound client needs durable retry identity. With this flag, the command always enqueues or replays steering and never performs a synchronous model call. D1-S1 exposes the same enqueue/replay through HTTP/Desktop; D1-S2 separately exposes exact pending-only cancellation; schema v73 exposes strict Run lifecycle and an explicit at-most-eight-item frozen RunSupervisor handoff. HTTP/OpenAPI/React still cannot edit/reorder input, hold a private lease, bypass Policy/budgets, or grant tool/process authority; TUI, models, and child Agents retain no queue control capability.

## Skills

```powershell
cyberagent skill list
cyberagent skill list --profile review
cyberagent skill show review
cyberagent skill validate
cyberagent skill package validate <package.zip>
cyberagent skill import <package.zip> --surface code --operation-key <stable-key> --confirm-untrusted-skill
cyberagent skill installed [--surface code|cyber] [--profile code|review|learn|script] [--include-removed]
cyberagent skill installed show <name>@<version>
cyberagent skill remove <name>@<version> --operation-key <stable-key> --confirm-remove
cyberagent skill select-external <run-id> <name>@<version>... --operation-key <stable-key> --confirm-untrusted-skill-context
cyberagent skill external-selection <run-id>
cyberagent skill select <run-id> review --operation-key <stable-key> --token-budget 4096
cyberagent skill selection <run-id>
cyberagent skill candidates [--run <run-id>]
cyberagent skill candidate show <candidate-id> [--show-content]
cyberagent skill candidate approve <candidate-id> --candidate-fingerprint <sha256> --operation-key <stable-key>
cyberagent skill candidate reject <candidate-id> --candidate-fingerprint <sha256> --operation-key <stable-key> --reason <text>
cyberagent skill candidate import <candidate-id> --candidate-fingerprint <sha256> --operation-key <stable-key> --confirm-untrusted-skill
```

The embedded read-only `skill.v1` Registry currently exposes fourteen workflows.
The maintained catalog and invocation rules are in [Built-in skills](builtin-skills.md);
exact current versions are defined by the [embedded manifests](../internal/skills/builtins/).
Each manifest declares compatible `profiles`, `surfaces`, `phases`, and receiving
`roles`. All four dimensions must match before content is delivered.

Invocation policy is separate from delivery compatibility. `user_invocable` permits
operator selection; `model_invocable` permits compatible model-origin reads through
`skill_read`. `explicit_only` requires operator selection, with `user_invocable=true`
and `model_invocable=false`. The three explicit workflows are `plan-delivery`,
`loop-monitor`, and `run-skill-generator`. Existing operator-pinned versions take
precedence. Reading a skill does not grant tools, network, files, processes, Provider
access, or approval authority.

`doctor` provides read-only Plan diagnostics. `debug` diagnoses reproducible faults
and permits repair only in Deliver;
`run-verify` verifies actual program behavior, and `focused-checks` selects tests by
change and risk. `run-verify` treats `ui-evidence.v1` as an optional operator workflow,
not a requirement for every task. `simplify` requires call-site evidence before
deletion, and `security-review` remains read-only unless separate Deliver authorization exists.

`run-skill-generator` does not create a trusted Skill. When it is explicitly selected and actually delivered to a Code/Deliver root turn, Go exposes `skill_candidate_propose`; otherwise the tool is omitted and forged calls are rejected. A successful call stores at most 4096 bytes of validated, secret-screened Markdown as an inert `proposed` candidate bound to the real tool invocation, Run/Session/Workspace/root, deterministic package, and exact fingerprints. The ordinary tool result and candidate list never expose the body; `skill candidate show --show-content` uses explicit untrusted-content delimiters for human inspection.

Schema v39 `skill select` must create the Run's single immutable selection before `run start`. It accepts one to eight names compatible with the Run Surface and Mission Profile, evaluates the invocation policy, deterministically pins each version/content hash/byte count/token upper bound, and rejects an aggregate above `--token-budget` (maximum 8192). Schema v110 keeps that selection immutable while recording the exact Run mode and only the compatible root subset for each turn; zero-item delivery is an explicit auditable fact, so Plan-only `doctor` and Deliver-only `run-verify` can coexist or safely become inactive after a phase transition. Operation keys must be stable normalized 16-256-byte values; SQLite stores only a domain-separated digest. Exact selection replay returns the original tuples after Run start, while changed intent conflicts. `skill selection` reads those pinned tuples.

`skill package validate` is a read-only, non-schema preview for external `skill_package.v1` files. The deterministic ZIP must contain exactly `manifest.json` and `SKILL.md` in that order and fit the structural, size, decompression, CRC, UTF-8, manifest, byte/token, and hash limits fixed by ADR 0024. The command rejects whitespace-rewritten paths, symlinks, and non-regular files, reads at most 64 KiB with identity rechecks, and prints only bounded manifest metadata, exact archive/semantic digests, trust/risk codes, and false authority flags. It does not print the body or source path and does not install, persist, execute, access the network, call a Provider/tool, or grant declared dependencies.

The non-schema Desktop pathless boundary reuses the same reader and parser behind a Go-native selector. After validation, Go forgets the path and exposes only short-lived opaque handles. Desktop D0-A/D0-B connects that selector to a Wails native `.zip` dialog and a bounded React risk preview. D1-B1 adds a separate one-time confirmation handle that the fourth narrow native method may consume to register the exact package; the renderer still cannot provide a path or bytes. The HTTP control accepts only strict bounded canonical base64. Both paths keep installation inert. ADR 0033 records path isolation, ADR 0034/0035 the shell/recovery boundary, and ADR 0041 the install control.

Schema v69 adds the inert local user Registry. `skill import` requires an explicit Code/Cyber surface, a normalized stable 16-256-byte operation key, and `--confirm-untrusted-skill`. It first commits an immutable installation intent, publishes the validated archive to content-addressed storage, verifies a complete readback, and then records completion. Same-key retries recover an interrupted import; reusing the key for changed content/surface/operator conflicts. Built-in names are reserved. Code accepts validated compatible Profiles, while Cyber accepts exactly `script`. Schema v111 adds canonical `surfaces/phases/roles` and the three invocation flags to that installation ledger; mode-aware requests use a v2 intent fingerprint, while legacy rows retain their exact v1 fingerprint and conservative effective policy. Every imported package remains `operator_installed_untrusted` with command, hook, network, Provider, tool-grant, Run-selection, and context-injection authority false.

`skill installed` and `skill installed show` verify the stored object on every read and print metadata only. `skill remove` requires a separate stable key and `--confirm-remove`; it appends an immutable tombstone, retains package bytes for audit/recovery, and refuses a version already pinned by a Run. Removed packages are hidden unless `--include-removed` is supplied and cannot be silently reinstalled. Schema v70 `skill select-external` requires a separate Run-level confirmation, pins one to four exact active versions, and delivers only redacted user-role guidance under independent root/Specialist budgets; declared tools still grant nothing. Because that selection predates phase-subset deliveries and is immutable for the whole Run, a mode-aware external package must currently support both Plan and Deliver for root (and for the selected Specialist, if any); phase-specific packages remain installable but fail selection closed. `skill external-selection` prints metadata only. D1-B1 makes the same inert Registry import available through independently enabled HTTP/Desktop confirmation, but installation still does not select, restore, execute, or authorize a package. ADR 0031, ADR 0032, ADR 0041, and ADR 0113 record the boundaries.

Schema v112 keeps generated-candidate status append-only: a candidate starts `proposed`, receives one exact-fingerprint human `approve` or `reject`, and only an approved candidate can receive one import receipt. Rejection is terminal. Model/Agent/Skill/Supervisor identities are reserved and cannot review or import; approval and import need separate stable operation keys, and import additionally requires `--confirm-untrusted-skill`. Import reconstructs the deterministic ZIP and reuses the v69/v111 Registry service, so a crash after package installation but before the candidate receipt is recovered by retrying the same key. `imported` still means only “installed into the inert Registry”; it never means selected. The ledger is bounded to four candidates per Run and 64 total. See ADR 0113.

Schema v40 loads the mode-compatible selected set for root Supervisor turns, and schema v110 binds phase-specific subsets to the exact mode snapshot. Before every Provider call, Go reconstructs `skill_context.v1` from the persisted tuples and embedded Registry, rechecks exact version/hash/bytes/Profile, filters by the active Surface/Phase/root role, redacts it, and enforces a separate deterministic token budget. New selection sees only current manifests; a hard-bounded embedded history resolves the archived versions, including `review@1.2.0`, `review@1.3.0`, and `focused-checks@1.0.0`, exactly and is not a user-controlled load path. Legacy manifests retain their old fingerprint and conservative explicit-user behavior. A metadata-only preparation, including an explicit empty subset when applicable, is committed with the first model-start event and safely replays after restart; neither SQLite nor Run events contain Skill text, paths, names, or hashes. A selected Skill never authorizes its declared tool dependencies.

Schema v47 derives `specialist_skill_context.v1` for each active child Attempt. Go reloads the child after Attempt start, binds the current immutable Run mode and parent selection, requires delegated `model.chat`, and selects at most one already-pinned guide by that exact manifest version's Surface/Phase/Profile/Specialist metadata. This replaces the active hard-coded built-in name table: Code-only guides cannot enter Cyber, `script` explicitly supports both surfaces, and `plan-delivery` is root-only. The default child budget is 1,024 conservative tokens with a 2,048 hard maximum. Preparation is idempotent across concurrent Store callers and commits atomically with the first Specialist model start; a selected Run cannot start that call without preparation. Child assignment text, model output, HTTP, Tool Gateway, and external directories cannot select Skills. The body remains in the current Go Provider request only, while SQLite and events store aggregate metadata and fingerprints.

## Signed Skill Packages and Team Catalog

```powershell
cyberagent skill catalog list
cyberagent skill catalog trust <publisher> <base64-ed25519-public-key> [--team label] [--operator admin]
cyberagent skill catalog revoke <publisher-fingerprint> [--operator admin]
cyberagent skill catalog pin <name>@<version> --surface code|cyber [--operator admin]
cyberagent skill catalog enable <name> --surface code|cyber [--operator admin]
cyberagent skill catalog disable <name> --surface code|cyber [--operator admin]
cyberagent skill catalog audit
cyberagent skill import-url <https-url> --sha256 <hex> --surface code --operation-key <stable-key> --confirm-untrusted-skill
cyberagent skill import-git <https-repo-url> --commit <40-hex-sha> --surface code --operation-key <stable-key> --confirm-untrusted-skill
cyberagent skill import-dir <directory> --surface code --operation-key <stable-key> --confirm-untrusted-skill
```

Signed packages use `skill_package.v2`: the deterministic ZIP holds `manifest.json` (with a `publisher` field), `SKILL.md`, and `SIGNATURE.json`. The Ed25519 signature covers `sha256(manifest.json) || sha256(SKILL.md)`; a package with an invalid or stale signature is rejected outright. A publisher identity is the SHA-256 fingerprint of its public key. A valid signature proves provenance only — it never grants trust or capability. Signed packages install under the same `operator_installed_untrusted` class as v1 packages after the signature envelope is verified and stripped; the signed archive digest and publisher fingerprint are kept in the import ledger.

`skill catalog trust` is the explicit operator/admin trust decision. A signed package can only be pinned once its publisher is trusted and not revoked; `revoke` blocks new pins without touching already-installed packages. `skill catalog pin` pins the active version for a skill + surface; re-pinning is upgrade or rollback. When a pin exists, `skill select-external` only accepts the pinned version and only while enabled; skills without a pin keep the existing operator-confirmed flow.

Web/Desktop **Settings → Skill packages → Legacy Skill Catalog publisher trust**
also lists these public keys and their current trust state. To add a publisher,
enter its name and canonical base64 Ed25519 public key, verify the key's source,
and explicitly confirm trust. Existing rows offer confirmed revoke or re-trust.
Each change is bound to the row's observed `updated_at`; a stale decision or an
uncertain response requires refreshing and confirming the current row again.
The control API uses `skill-catalog-control.v1` at
`GET/POST /api/v1/skills/catalog/publishers`, with separate read/control bearer
authorization. These Catalog identities govern the existing Catalog pin flow;
Plugin publisher trust remains in Extensions. Catalog pin/enable/disable commands
remain available through the CLI.

URL imports require an absolute HTTPS URL without credentials plus the expected SHA-256 pin: the response body must match the pin byte-for-byte, redirects must stay HTTPS on the original host, and the body is capped at 1 MiB, so redirects or upstream drift cannot change reviewed content. Git imports require an HTTPS repository URL plus a full lowercase 40-character commit SHA: the staging clone (`--no-checkout`, `core.autocrlf=false`) checks out the exact commit and re-verifies `HEAD`; no hooks, scripts, submodules, or build tools from the repository ever run. Directory/staging packaging accepts only `manifest.json` + `SKILL.md` (+ optional `SIGNATURE.json`) at the root and rejects symlinks, junctions, subdirectories, special files, and oversized or non-UTF-8 content.

Every catalog mutation appends a bounded audit row (`catalog.trusted`, `catalog.revoked`, `catalog.pinned`, `catalog.enabled`, `catalog.disabled`, `import.completed`) to the append-only `skill_catalog_audit` table (schema v104). Audits never contain Skill bodies, secrets, or raw outputs. Skills remain declarative prompt-only resources; declared tool dependencies still grant nothing and all tools flow through the Go Tool Gateway.


## Windows Desktop D0-A Through D1-G13/V12

Build the Windows product candidate from the repository root:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/build-desktop.ps1
.\build\desktop\TraverseBoard.exe
```

The build script installs the locked frontend dependencies, checks the generated API contract, runs frontend and focused Go tests, builds the production renderer, and then compiles the Windows GUI binary with the mandatory `desktop,production,wv2runtime.error` tags. The machine needs Windows 10/11 and WebView2 Evergreen Runtime `94.0.992.31` or newer. The binary checks that prerequisite before opening SQLite and never downloads or installs it. `web/dist` is generated and ignored by Git; a direct Desktop build intentionally fails if the production bundle or secure WebView2 strategy tag is absent.

The resulting `TraverseBoard.exe` is the sole zero-argument direct product entry and may be opened
from File Explorer. New source trees and release packages ship neither
`Start-Prayu-Operator-Preview.cmd` nor a renamed Start helper. Existing data is migrated
transactionally in place and is never deleted or reset to recover startup. The portable ZIP is an
internal reproducibility and compatibility container, not the ordinary user download; it does not
move the data directory beside the EXE. Unless `CYBERAGENT_HOME` is explicitly set, every copy uses
`%USERPROFILE%\.cyberagent-workbench`. If a historical profile needs diagnosis, copy
`~/.cyberagent-workbench/cyberagent.db` before launch and keep that copy outside the active
filename. The known Windows-preview v30 and pre-final v97 checksums are accepted only for their
exact migration versions and names; schema v125 then transactionally restores the canonical v97
cleanup trigger without rewriting the historical row. Unknown histories still fail closed. Do not
delete the database, reset it, or edit its checksum as an upgrade workaround. ADR 0068 records the
real Wails request shape and the v30-to-v84 data-preserving launch verification;
[ADR 0126](adr/0126-legacy-v97-docker-trigger-compatibility.md) records the exact v97 compatibility
boundary and repair. The two-product publication and Store-completion rules are defined by
[ADR 0145](adr/0145-windows-two-deliverable-release-contract.md).

全新且经 SQLite 事务证明没有 `schema_migrations`、用户 table/index/trigger/view 的数据库使用生成式 latest-schema baseline；任何已有对象或证明缺失都继续走完整历史 migration。baseline 与旧链生成相同 v136 schema 和 canonical ledger，不会转换已有 profile。升级前须停止全部 Desktop/CLI/API 进程并离线备份数据库；只有已经支持相同 v136 历史计划的旧二进制才能读取 baseline 产物，更早版本必须恢复升级前备份。磁盘错误、取消或建库中断会整体回滚，恢复空间/权限后可重启；不要通过删除非空数据库或修改 ledger 强制重试。完整边界和 runbook 见 [SQLite 全新安装基线](schema-baseline.md) 与 [ADR 0139](adr/0139-clean-install-schema-baseline.md)。

With no flags, the Desktop opens the safe product bundle and first-use path; it does not silently enable Full Access, debug controls, Full CDP, a background worker, or a persistent terminal. `--safe-view` remains the explicit read-only diagnostic surface. Both modes open the same `$CYBERAGENT_HOME/cyberagent.db` as the CLI. The app generates an ephemeral in-memory token and calls the existing Go API through Wails' in-process AssetServer Handler, so no TCP port or copied bearer token is required. Run events use `/events/poll` on Windows because Wails v2 does not stream AssetServer responses there; this endpoint shares the SSE Run-bound high-water cursor, while ordinary Web clients continue to use SSE. Cursor/frame memory is bounded to 16 Runs and 500 frames per Run and never enters browser storage.

To expose only the existing schema-v64 profile selector, launch explicitly:

```powershell
.\build\desktop\TraverseBoard.exe --enable-profile-control
```

To expose only schema-v72 controlled Run creation, or both narrow capabilities:

```powershell
.\build\desktop\TraverseBoard.exe --enable-run-creation
.\build\desktop\TraverseBoard.exe --enable-run-creation --enable-profile-control
```

To expose only Session message queuing:

```powershell
.\build\desktop\TraverseBoard.exe --enable-session-messages
```

To expose pending cancellation, Run lifecycle, or explicit bounded execution:

```powershell
.\build\desktop\TraverseBoard.exe --enable-session-steering-control
.\build\desktop\TraverseBoard.exe --enable-run-lifecycle
.\build\desktop\TraverseBoard.exe --enable-run-execution
```

To expose Plan/Deliver control or constrained approval decisions:

```powershell
.\build\desktop\TraverseBoard.exe --enable-plan-delivery
.\build\desktop\TraverseBoard.exe --enable-approvals
```

To expose explicit Provider diagnostics and route selection, review-only Diff decisions,
or durable wake intent independently:

```powershell
.\build\desktop\TraverseBoard.exe --enable-model-control
.\build\desktop\TraverseBoard.exe --enable-file-edit-review
.\build\desktop\TraverseBoard.exe --enable-run-wake
```

To expose the separately authorized FileEdit apply, one foreground wake consume, or
inert Skill installation:

```powershell
.\build\desktop\TraverseBoard.exe --enable-file-edit-apply
.\build\desktop\TraverseBoard.exe --enable-run-wake-execution
.\build\desktop\TraverseBoard.exe --enable-skill-installation
```

To expose Go-issued FileEdit proposals, Windows system credentials, or the bounded
process-start wake worker independently:

```powershell
.\build\desktop\TraverseBoard.exe --enable-file-edit-proposals
.\build\desktop\TraverseBoard.exe --enable-provider-credentials
.\build\desktop\TraverseBoard.exe --enable-wake-worker
.\build\desktop\TraverseBoard.exe --enable-user-terminal
```

Most flags create distinct in-memory control capabilities, while the terminal flag enables only the native user-terminal bridge. Profile selection by itself does not enable a backend. Run creation makes a default-budget, network-disabled `preview/noop` graph. Session submission, lifecycle, Plan/Deliver, approvals, FileEdit, credentials, and wake retain their existing independent boundaries. The optional wake worker remains capped at one due intent and one step. The optional user terminal is user-owned and Debug-bound; it is not a Tool Runner. There is no general Agent LocalRunner, Docker start, arbitrary Shell process, install-time execution, persistent service, startup entry, updater, or installer.

The New Run dialog selects a Workspace, Profile, Code/Cyber surface, and Plan/Deliver phase. The Models dialog reads redacted Provider/route/credential status; credentials appear only when their independent capability is enabled. The local Monaco editor and five workers load lazily from the bundle with no CDN fallback. Session, Run, Plan, Approval, Diff, and wake controls retain intent-bound retry keys only in memory. Go performs authoritative validation and transactions, and a single-capability launch does not unlock sibling controls.

The top-bar package button opens the native `.zip` picker. The operating-system path stays inside Go and is immediately validated. React receives bounded metadata plus opaque preview/confirmation handles, never the path or bytes. When Skill installation is enabled, a separate explicit confirmation consumes the one-time handle and returns only inert Registry metadata; cancellation creates no state, and installation does not select or execute the Skill. Set `CYBERAGENT_HOME` before launch only when intentionally using an isolated data directory for testing. The renderer cannot read or change that path.

D0-B also makes second-instance launch data non-authoritative: arguments and working directories are ignored, and only the existing window is restored. The in-process adapter accepts exact `http://wails.localhost`, canonicalizes `RequestURI`, and rejects other origins before the API. Wails bindings remain start-origin-only; CSP and a Desktop renderer guard block external links, forms, and popups. The automated matrix covers same-database CLI writes, six concurrent opens, close/reopen, forced-process restart, poll/SSE cursor interchange, and Windows 11. Windows 10 remains a formal-release compatibility item.

## Headless NDJSON

`cyberagent headless events <run-id>` exports the same redacted, append-only SQLite Run events used by CLI, SSE, and Web. The protocol is `headless.v1`; stdout contains only newline-delimited JSON. Each durable event is one `kind: "run.event"` record, followed by exactly one `kind: "stream.end"` record for every normal snapshot, terminal outcome, event bound, cancellation, or deadline. Human-readable diagnostics remain on stderr.

`--after-sequence <n>` resumes strictly after a previously emitted durable sequence. A cursor beyond the current tail is rejected before stdout is written. `--max-events` defaults to 1,000 and is bounded to 10,000; a truncated export returns exit 8 and reports `suggested_resume_after` in the end record. Reads occur in batches of at most 100 and validate contiguous sequence, Run/Mission identity, UTF-8 metadata, and a 1 MiB payload ceiling. The command never writes a Run event.

Without `--follow`, a nonterminal Run ends with `reason: "snapshot"` and exit 0. `--follow` polls the same local SQLite database every 250 ms by default, accepts only 50 ms through 5 s, drains any final event before returning, and may be bounded with `--timeout` up to 24 hours. Terminal completion returns 0, terminal failure returns 4, terminal cancellation returns 7, the event cap returns 8, and timeout returns 9. Caller cancellation returns 7. These codes reuse the stable `apperror` contract.

Headless mode is a read adapter, not another execution engine: it does not call `RunSupervisor`, a Provider, Tool Gateway, Sandbox, Shell, network, or file-write path. Run execution remains in the existing CLI/Session/operator services, so closing the Headless consumer cannot stop or mutate a background Run.

Schema v19 assigns every new Run one stable root Agent identity. The root is `ready` before a turn, `running` only while bound to the persisted Supervisor attempt, `waiting` when the Run pauses, and terminal with the Run. These projections, coordinator events, and a bounded recovery snapshot commit in the same transaction as the existing Run/Supervisor change. `run graph <run-id>` lazily registers older Runs when needed, validates the current node and pending-inbox metadata against the latest `agent_graph.v1` snapshot, and prints only bounded metadata. Current roots have `child_limit=0`: no public child-spawn path or concurrent sub-agent execution exists yet. The durable inbox primitive is internal and is not inserted into model prompts in this slice.

Schema v20 makes internal inbox delivery recoverably idempotent. In-process callers must supply a normalized 16-256 byte key; the Store persists only its domain-separated digest and a fingerprint of the redacted canonical intent. Exact retries return the original message with `replayed: true`, while changed intent conflicts. `wake` requires a running Run and a waiting Specialist recipient; it never wakes root or resumes a paused Run. `dependency` requires an Agent sender and a strict `dependency_id`/`state` payload. These operations remain internal: there is no CLI, HTTP, or model tool for arbitrary Agent messaging.

Schema v21 keeps Specialist admission internal and default-disabled. Go code must construct a Coordinator with a valid `SpecialistAdmissionPolicy`; then each request is limited to one of at most two depth-one children, a nonempty parent-Skill subset, a dedicated active Session, positive per-child turn/token reservations, and aggregate root headroom. Admission is idempotent and atomic. Reserved capacity reduces the root budget returned to later Supervisor turns. Pause/resume and terminal Run transitions project to children and graph snapshots, and terminal child Sessions are archived. There is still no CLI/HTTP/model spawn command and no child model execution loop.

Schema v22 binds structured memory to real same-Run Agent identities. `run graph <run-id>` prints the root and any internally admitted Specialist IDs. WorkItem/Note create, list, show, and update support `--owner-agent <agent-id>`; direct Store writes and SQLite triggers reject missing or cross-Run identities, and new assignment to a terminal Agent is refused. Historical label-only ownership remains readable. The root Supervisor automatically loads Notes visible to its root Agent identity and automatically assigns model-created WorkItems/Notes to that identity. Models cannot submit or override `owner_agent_id` because it is absent from their tool JSON schemas.

Schema v23 adds an internal-only `agent.finish` path with strict `agent_completion.v1` reports. It has no CLI, HTTP, or model-facing command. Child workers must submit the exact active attempt, `succeeded` or `partial`, a bounded summary, and only child-owned WorkItem or parent-visible Note references. Completion atomically writes the parent result inbox entry, terminal child state, archived child Session, audit events, and recovery snapshot.

Schema v24 adds the internal Specialist Attempt scheduler, but it remains a Go-only capability with no user command. A turn starts only under the current Run execution lease, consumes one reserved turn immediately, and may record model token/time usage once. `continue`, completion, crash, and Run-lifecycle interruption persist immutable attempt outcomes. Crashes deliver a redacted notification to the root and stop the child when its budget is exhausted. When an expired Run lease is taken over, the new worker recovers stale attempts once and the previous worker cannot commit new usage or terminal state. Users and models still cannot spawn, start, or finish a Specialist directly, and no child Provider loop is enabled yet.

Schema v25 lets the root Supervisor read protocol-backed direct-child inbox updates without adding a public inbox command. Before a root model call, Go prepares at most four sequence-ordered dependency, CompletionReport result, or crashed-Attempt notification messages. A successful turn consumes them atomically with its Session/lifecycle commit; a failed turn leaves them pending, and cancellation, restart, or lease takeover reuses the exact prepared batch. The prompt contains strict redacted typed data and durable sender provenance, but never message IDs, sequence values, cursors, or a model-controlled acknowledgement field. The child Provider loop and public/model spawn remain disabled.

Schema v26 adds one explicitly invoked internal no-tool Specialist model turn. Internal Go code constructs `SpecialistRunner`; no CLI, HTTP, or model-facing command can start it. It uses the Run execution lease, child turn/token budgets, strict `specialist_lifecycle.v1`, Provider retry/cancellation, Policy, durable child Session history, and CompletionReport finish. It offers no tools and cannot grant Shell, network, credential, or spawn authority.

Schema v27 adds recoverable Specialist input context. A direct root parent may send a strict `specialist_instruction.v1` message through the internal Coordinator. One AgentAttempt prepares at most four sequence-ordered instructions and consumes them only when `continue` or `finish` commits; crash, interruption, and lease takeover leave them pending for the next attempt. The request also selects active WorkItems owned by the child and active `run`/`owner` Notes owned by and visible to that child under a 4,096-token estimate and 32 KiB cap. Message IDs remain audit-only, and `model.started` contains source IDs/token estimates rather than instruction or Note bodies. There is still no public/model spawn or autonomous/concurrent child scheduler.

Schema v28 gives each Specialist Attempt one isolated lifecycle-protocol repair. Primary and repair phases have independent transport retry counters but share one contiguous global model sequence and cumulative Attempt usage. Invalid primary output never enters the repair prompt, Session, or events. A second invalid response exhausts repair; cancellation, budget exhaustion, crash, interruption, and takeover abort it before the Attempt terminates.

The Go-internal `SpecialistScheduler` may run at most two explicitly selected ready children per round under one Run execution lease and stops within 32 rounds. Parent cancellation, heartbeat loss, or the first child error fans cancellation out to the active sibling, and the scheduler waits for durable Attempt terminal state before releasing the lease. Root and child token/model-time totals are rebuilt from SQLite before and after every round. At the schema v29 boundary there was no CLI, HTTP, or model path that admitted or started a schedule; schema v38 later adds only the explicit operator CLI gate described below.

Schema v29 persists schedule boundaries and exact child cancellation. A schedule writes metadata-only `agent.schedule_started/stopped` events plus an immutable terminal summary; a later lease generation marks an orphaned running schedule `abandoned/worker_lost`. The optional control API can cancel one already-started child call at `/api/v1/runs/{run_id}/agents/{agent_id}/active-call/cancel`, with strict AgentAttempt/model identity and a digest-only idempotency ledger. Only the worker holding the Attempt lease observes the request and cancels its own Provider context. Responses and events expose no raw key, model text, lease id, or fencing generation. In a concurrent scheduler round, the selected child's cancellation error may activate the scheduler's existing local sibling fan-out, but no sibling control request is fabricated.

Schema v30 lets the root model call `specialist_delegation_propose` with one or two strict `specialist_delegation.v1` assignments. Proposal creation is additive and review-gated: Go validates the active root turn and lease, trusted Run/Session/Workspace scope, parent-Skill subsets, child capacity, and suggested turn/token headroom, then stores only a redacted immutable `proposed` record. The result always reports `admission_authorized=false`; no child Agent, Session, budget reservation, or schedule is created. Operators inspect proposals with `run delegations <run-id>` and `run delegation <proposal-id>`.

Schema v31 adds explicit operator review without adding execution authority. `run delegation approve` records one immutable approval only while the Run is still running; `run delegation reject` requires a reason and may close a proposal even after the Run is terminal. Both commands require a 16-256-byte stable operation key and default the reviewer identity to `cli_operator`. The Store redacts reasons, keeps them out of Run events, hashes operation keys, rejects changed-intent replay and second decisions, and emits one metadata-only `agent.delegation_reviewed`. Every result still reports `admission_authorized=false` and `application_required=true`; review does not create or schedule a child.

Schema v32 adds `run delegation apply` as the only current operator application entry point. The operator must match the approved review identity and provide a stable 16-256-byte operation key. Before creating state, Go reruns Policy and verifies the immutable review operation, running Run, ready root, active Session, idle child runtime, parent Skills, default limits of two children/eight turns/16,384 tokens per child, current capacity, and aggregate root headroom. It then admits each child and sends its strict parent instruction through deterministic internal keys. A restart after either write safely replays the existing Agent/Message. Applying blocks root turns, unrelated admission/messages, and child scheduling; terminal Run transitions abort the application. Applied children remain `ready`, with `scheduling_started=false`.

Schema v38 adds the separate execution entry point. `run delegation schedule <proposal-id>` and its `continue` alias require the same application operator plus a stable 16-256-byte operation key. With no `--agent`, all instructed ready assignments are selected; repeat `--agent` to choose one or two exact assignment Agent IDs. `--max-rounds` defaults to one and is bounded by 32, while each child still obeys its own reserved turn/token budget and the shared Run token/model-time budget. Reusing a key and identical intent returns the durable terminal schedule without another model call; changing targets or rounds under that key conflicts. Another continuation requires a new key. A crash after request or schedule start is recovered through the same immutable request and a higher fenced attempt ordinal. These commands grant no tools, Shell, file write, process, network, recursive delegation, or child-count expansion, and there is no HTTP/model/ordinary-tool equivalent.

Schema v33 adds planning-only read-only Fan-out. `run fanout plan` requires a running Run whose Mission and active Session bind the same local workspace and whose network mode is disabled. Tiers `auto/1/2/4/6` are concurrency caps for independent analysis shards, not Agent admission limits. The scanner walks one workspace-relative directory without following symlinks, excludes VCS/dependency/build directories, binary and secret-like files, and stores only a bounded immutable path/hash manifest. Policy and `readonly_fanout.planned` events contain metadata but no goal, file path, or local root. `run fanouts` and `run fanout show` inspect the immutable plan; planning itself performs no Provider call.

Schema v34 adds `run fanout execute <plan-id>`. Execution requires the same plan operator and a stable operation key. Go acquires the Run lease, rebuilds the manifest, then verifies every file identity, size, and hash before making any Provider call. Source content is redacted and held only in memory. Each shard gets a tool-free JSON-mode request and must return strict `readonly_fanout_report.v1`; findings outside the shard are rejected. The planned 1/2/4/6 calls run concurrently, first failure cancels siblings, and every terminal state is persisted before the lease is released. `run fanout execution <execution-id>` displays the durable result. A repeated operation key and intent returns the same execution without another model call. Root, Specialist, and Fan-out token/model-time usage share the Run budget. Crash-uncertain calls retain their reserved charge and a newer lease retries only incomplete shards. This path still creates no Agent, Attempt, schedule, tool, file edit, process, network request, or automatic source change.

Schema v35 adds `run fanout report <execution-id> --format markdown|json`. It accepts only a completed execution and performs no Provider call. Go groups only source assertions with identical severity/category/title/detail/path/line facts, retains every source row as immutable `model_assertion` Evidence, and uses the minimum claimed confidence for an exact duplicate group. Every projected Finding is `draft`; report generation does not validate a vulnerability. The `building -> generated` SQLite transaction checks source bindings, contiguous ordinals, counts, and severity totals before making the projection immutable. Repeating the command or using `report show <report-id>` renders the same persisted projection byte for byte.

Schema v36 adds an explicit operator validation workflow. First create or approve a tool output Artifact in the same Run. `report finding attach` rereads and verifies the complete Artifact before recording immutable Evidence. `report finding validate` requires at least one attached Artifact; `report finding reject` may be used with no Artifact when the model assertion cannot be reproduced. A Finding can receive only one decision, and no Evidence can be attached afterward. Reusing the same operation key and intent returns the original row; changed intent or a second decision conflicts. `report finding verify` revalidates every Artifact blob and the decision's ordered Evidence digest. Notes and reasons are redacted and excluded from Run events. These commands do not mark a Finding accepted or fixed.

Schema v37 adds an independent operator remediation workflow. `report finding accept` requires an existing `validated` decision and freezes its ID, Evidence count, and digest; it does not reuse validation as implicit acceptance. Create or approve a new same-Run tool output only after acceptance, then attach it with `report finding remediation attach`. The Store compares durable Run-event sequence numbers, so an Artifact created before `finding.accepted` is rejected even if the system clock moved backward. A validation Artifact cannot be reused. `report finding fix` requires at least one fresh remediation Evidence record and freezes the ordered remediation set. Acceptance, remediation Evidence, and fix facts are immutable and replay-safe. `report finding verify` now validates both Artifact sets and every frozen snapshot.

`report show <report-id> --format sarif` is a deterministic read-only SARIF 2.1.0 projection: it performs no Store mutation or Provider call, emits stable severity rules, workspace-relative percent-encoded paths, and the v35 Finding fingerprint, and excludes validation, acceptance, remediation, and fix narratives plus Artifact content. Only confirmed unresolved `validated` and `accepted` Findings appear in `results`. `cyberagentValidationStatus` remains `validated` for both, while `cyberagentFindingStatus` distinguishes their lifecycle. Draft, fixed, and rejected counts remain in Run properties but are not emitted as results.

Use `report check <report-id>` as the scriptable CI gate. Its default policy is `--fail-status validated --min-severity high`; a match prints the complete text or JSON result and returns the stable `FAILED_PRECONDITION` CLI exit code 4. This policy includes both validated and accepted unresolved Findings. `--fail-status active` additionally includes draft, while `--fail-status none` disables failure. Fixed and rejected Findings never match. The command reads persisted lifecycle facts only.

Use `report check <report-id> --format github` inside GitHub Actions to emit official [workflow-command annotations](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-commands) before the same gate exit. The in-memory `GateResult` owns the exact matched Finding snapshots, while its JSON representation remains the existing count-only contract. Source severity maps to `notice` for info/low, `warning` for medium, and `error` for high/critical. File, line, endLine, title, status, category, Finding ID, and fingerprint are deterministic; command data and properties follow GitHub Toolkit escaping, while other C0/DEL controls become visible `\u00XX` text, so model output cannot create another command or manipulate terminal presentation. A passing or disabled gate emits no annotation. Artifact bodies, validation/remediation Evidence notes, and operator narratives are excluded. Other CI-platform adapters remain separate future renderers.

`run execute` repeats that same durable step up to `--max-steps`; it stops immediately on root `finish` or `wait`. `--finish` remains an explicit operator fallback after a normal step limit and cannot complete a waiting Run. `run finish` and `run fail` atomically update the Run, Supervisor checkpoint, and event stream. Repeating the same terminal command or replaying the same committed lifecycle action is idempotent, while a conflicting terminal transition is rejected.

Schema v17 serializes execution across processes with a durable Run lease. `run step` acquires one lease for one turn; `run execute` holds one lease across all steps in that invocation. The Supervisor renews the lease during long Provider calls. After expiry, a new worker takes over with a higher generation and the old checkpoint token can no longer append model/tool events, charge tool budget, or mutate WorkItems/Notes. An active competing worker returns `CONFLICT`/CLI exit 4; no manual lock cleanup is required because expired leases are recoverable. `run lease <run-id>` shows owner, generation, status, activity, and timestamps but intentionally omits the internal fencing token.

Before each model call, the Supervisor passes the remaining token allowance as the request limit and applies the remaining persisted model-execution deadline. Its Context Builder considers the prepared root inbox batch, latest compacted summary, at most 20 active WorkItems, and at most 100 active Notes visible to the root Agent. It selects those structured sections under a separate 8,192-token estimate, requires every prepared inbox item to fit, keeps Work Board JSON under 16 KiB, and truncates individual Note/inbox fields. `model.started` persists only included/omitted source IDs and token estimates, never Note or inbox bodies. A model `finish` action is sent through the existing one-repair protocol when active work remains; `run finish` remains an explicit operator override. Provider-reported usage is authoritative: if one call exceeds the remaining token allowance, its full actual usage is committed and subsequent calls are blocked. `MaxToolCalls` is now enforced by the Tool Gateway with an atomic SQLite ledger; `MaxCostUSD` remains configuration-only until provider pricing metadata is available.

Provider failures are normalized as `retryable`, `rate_limited`, `invalid_response`, `cancelled`, or `permanent`. RunSupervisor retries only retryable transport/capacity outcomes, with three attempts per protocol phase by default, 100 ms exponential backoff, and a 2 second local wait ceiling. A server `Retry-After` longer than that ceiling is not shortened: the turn returns a rate-limit error and keeps its pending input for a later `run step`. Invalid lifecycle JSON is not transport-retried; instead, it receives exactly one explicit repair phase with its own transport counter. Authentication/configuration failures, policy denial, and tool calls are not repaired.

Every call attempt emits `model.started` and then `model.completed` or `model.failed` in `run events`. RunSupervisor always consumes the Provider stream interface. Schema v130 normalizes OpenAI interleaved tool deltas, Anthropic content blocks, complete-item Ollama/Mock streams, and legacy `ChatChunk` providers through `llm.item_stream.v1`. It reconstructs UTF-8 split across chunks, limits the complete response to 64 KiB, requires a final chunk with valid usage, and routes mid-stream failures through the same retry and protocol-repair path. Global attempt numbers continue across protocol phases and process restart; phase-local transport numbers reset for the one repair phase.

During an attempt, `run events` may contain at most 32 ordered `model.delta` records. They retain chunk/byte counters plus content-free response/item/call boundaries and declared delta/complete-item granularity; model text, argument deltas, complete arguments, usage payloads, provider wire data, and private thinking are not representable. Text still flushes at 2 KiB or 250 ms, while boundary-only progress is retained independently. Cancellation, EOF, missing or malformed usage, identity/model drift, and duplicate or contradictory terminals end in failed/cancelled boundaries without fabricating `output_item_completed` or `response_completed`.

`model_public_stream.v3` exposes provisional message text only through the existing safe public previewer and adds a content-free tool projection: stable response/item/call IDs, status, redacted tool name, and bounded argument byte count. A Desktop card may show “Preparing call” while bytes arrive, but no argument is shown or persisted and no execution begins. A complete call must first pass JSON, size, sensitive-data, Policy, budget, authority, and idempotency checks in Go. Schema v130 then immutably binds the stream IDs to the deterministic Supervisor call ID; durable `supervisor.tool_execution_started/completed` events carry the same safe identity projection. Model terminal events, token usage, and `execution_millis` commit together, and replay cannot double-charge the budget or duplicate logical tool effects.

### Unified Thread transcript / 统一 Thread 主工作面

侧栏搜索通过 `GET /api/v1/threads?status=active&q=...` 检索全部未归档任务的标题，
无需先加载旧任务；不搜索消息正文，归档任务仍在设置中查看。查询会去掉首尾空白，最多
256 个 Unicode 字符，ASCII 字母不区分大小写，其他 Unicode 字符精确匹配；`%`、`_`
和反斜杠按普通字符匹配。空查询与不传 `q` 相同。

结果按创建时间倒序排列，同一时间按任务 ID 倒序。继续使用响应的 `next_cursor` 获取
更早匹配项；修改查询或生命周期筛选后须从第一页开始。并发新增较新任务不会移动旧页，
但重命名、归档或删除会改变后续查询的结果集。搜索、翻页与清空搜索不会切换当前任务，
也不会覆盖正在编辑的草稿。

列表同时返回 Go 投影的 `execution_state`，区分执行中、停止中、等待审批、暂停、空闲和
终态。输入框可编辑（`composer_state=ready`）不表示空闲。状态读取失败、缺少执行观察源
或存在未确认的执行归属时显示“状态未知”；侧栏统一刷新列表，不逐行请求执行状态。
对话侧栏可见且只缓存一页时，每 15 秒自动刷新；加载多页历史后暂停定时刷新，状态代表
最近一次读取结果。可点击“刷新对话列表”更新已加载页，任务变更、重新聚焦或恢复连接
仍沿用现有刷新机制。切换到只缓存一页的搜索结果时恢复定时刷新，已加载的历史页与草稿保留。
公开合同与兼容性见 [ADR 0167](adr/0167-task-title-search-and-execution-observation.md)。

打开 `/threads/{thread_id}` 时，页面从 `GET /api/v1/threads/{thread_id}/transcript`
读取最新一页持久记录，并用不受追加事件影响的 opaque keyset cursor 向前加载。记录按
`(Run ordinal, event sequence, item position)` 排列，包括 Run/successor 边界、用户消息、公开
模型正文、白名单 Harness 事实、schema-v130 结构化工具阶段、审批、验证、检查点和交付。
`limit` 约束持久 source records；一个有界 tool-batch source 会完整展开为逐 item 卡片，不会在
批次中间丢失身份。

Thread 是普通任务的主页面：Composer、暂停/恢复、当前审批、继续执行和交付均在同一页面。
Events、Artifacts、Run 与 Session 页面仍用于专业审计和诊断。实时 `model_public_stream.v3`
内容固定标为 provisional；相同 stream item、source reference 或 attempt/model/tool-round 的 durable
记录到达后会确定性替换它，已确认历史不会重排。

Transcript 只展示公开模型文本与 Go 允许的结构化事实，不展示或推断 private chain-of-thought，
也没有工具参数、raw output、provider bytes、凭据或无限 terminal 数据字段。长细节默认使用可键盘
展开的原生 disclosure；长历史采用可变高度虚拟化。Composer 与 transcript scroller 是独立 flex
siblings，在 390px 窄屏、200% 等效高度、中文 IME、Shift+Enter、虚拟键盘、safe area 和
reduced-motion 下保持可达。完整设计与 #140 packaged E2E 的稳定入口见
[ADR 0134](adr/0134-unified-thread-transcript.md)。

### Web Search, Fetch, and Citation / Web 证据

Schema v134 adds three root-only model tools behind an explicit Run network allowlist.
`web_search` asks one configured SearXNG JSON endpoint for discovery stubs;
`web_fetch` retrieves exactly one same-Run source or authorized public HTTPS URL;
`web_citation` binds a claim only to a fetched or partial snapshot from that Run.
Search snippets are not citeable, and fetched text is always untrusted evidence rather
than instructions.

```powershell
$env:CYBERAGENT_WEB_SEARCH_ENDPOINT = "https://search.example.org/search"
cyberagent run create "Review the public specification" `
  --workspace demo --profile review `
  --network allowlist `
  --allow-target search.example.org `
  --allow-target docs.example.org

cyberagent web-evidence list --run <run-id> --limit 100
```

Network stays disabled by default. Exact hosts, HTTPS origins, wildcard DNS suffixes,
and the explicit broad `public_https` target are accepted; prefer exact hosts. The
client permits public HTTPS/443 only, rejects local/private/metadata addresses, pins
every DNS answer, rechecks up to three redirects, checks robots, uses a 15-second and
2-MiB response boundary, and retains at most 128 KiB of sanitized HTML/text/JSON or
conservative partial PDF text. Missing SearXNG configuration makes Search unavailable
without a fallback, while a network-disabled Run publishes no Web tools.

Thread source cards and `GET /api/v1/runs/{run_id}/web-evidence` use the same public
projection as the CLI: canonical link, bounded title, status, fetch/stale time, digest,
and partial/stale facts. They omit snippets, page bodies, citation claims, operation
keys, DNS addresses, and call authority, and report `untrusted=true` plus
`instruction_authorized=false`. Complete configuration, failure remediation,
robots/terms/copyright guidance, and limits are in [Web Evidence](web-evidence.md);
the security decision is [ADR 0137](adr/0137-go-owned-web-evidence.md).

Repair transitions emit `supervisor.protocol_repair_requested/started/completed/failed`. The raw invalid output is never copied into the repair prompt, Session, or event payload. `run step` prints `model_attempts`, `protocol_repairs`, `model_outcome`, `stream_events`, and `stream_bytes`. Exhausted transient retries return unavailable exit code 6, rate limits return resource-exhausted exit code 8, cancellation returns 7, and deadline expiration returns 9.

The application service exposes in-process active-call query, bounded metadata subscription, and idempotent cancellation operations. An explicit application cancellation first appends a redacted `model.cancel_requested` event and then signals the Go-owned Provider context. Subscribers receive no raw model text and are disconnected if their 32-event buffer fills. Bubble Tea consumes this interface through an adapter. Schema v18 extends cancellation across processes through a durable, exact-attempt request: a separately authorized API process records intent, and only the worker holding the private execution lease can observe it and cancel its local Provider context. Request, observation, and terminal resolution are audited without exposing the registry or fencing token.

The `cyberagent` process handles `Ctrl+C` and termination signals through its command context. An interrupted Provider call records `model.failed` with a cancelled outcome and keeps the started Supervisor checkpoint recoverable instead of abandoning an unaccounted request.

Schema v79 also stops a logically live but unproductive loop. Three completed root
turns that return the same normalized `continue` action, or six completed `continue`
turns without selected structured-state progress, atomically pause the Run with stop
reason `livelock_detected`. The completed Session turn remains durable and replaying
its original Provider result does not append it twice. Inspect the Run/events, correct
the plan, input, or structured work as needed, then use the ordinary explicit
`run resume <run-id>` control; the first later turn starts a fresh observation window.
No hidden retry or automatic resume occurs.

Workspace read/list Tools have a default 15-second execution deadline, honor caller
cancellation, and reject special files. An exit code of 124 means the Tool deadline
elapsed; 130 means the caller cancelled. These in-process guards do not enable or
execute Shell, LocalRunner, or Docker. A third-party Tool must honor its Go context;
real process-tree termination remains unavailable until the separate Runner lifecycle
gate is implemented.

Ordinary text sent to a Run-created Session uses the same RunSupervisor path as `run step`. The first message automatically starts a `created` Run, a follow-up to a model `wait` automatically resumes its paused Run, and the CLI prints `[run <id>: action=<action> status=<status>]`. Pending input is redacted, limited to 64 KiB, and stored before the Provider call; after restart the same attempt can recover it, while the committed user/assistant pair and lifecycle events are written exactly once. Completed, failed, cancelled, or approval-waiting Runs reject ordinary input instead of falling back to an unsupervised model call.

`run adapt-task` converts a v0.1 `agent.Task` into a new Mission, Run, and Session. The mapping is transactional and keyed by Task ID, so repeated or concurrent calls return the same Run and append only one `legacy.task_adapted` event. Historical task status is recorded for audit, but the new Run always starts at `created` and never executes implicitly. Legacy CTF tasks map to the safe generic `review` profile until the dedicated CTF phase.

CLI errors keep their existing text and use stable exit codes documented in [errors.md](errors.md).

Supported profiles are `code`, `review`, `learn`, and `script`. New runs start with network access disabled. Budget flags reject negative values and include maximum turns, tokens, model cost, and wall-clock timeout.

## Local HTTP API

```powershell
$env:CYBERAGENT_API_TOKEN = "<a-random-token-of-at-least-32-bytes>"
$env:CYBERAGENT_API_CONTROL_TOKEN = "<a-different-random-token-of-at-least-32-bytes>" # optional
cyberagent api serve --listen 127.0.0.1:8765
cyberagent api openapi --output docs/openapi.json
curl.exe -N -H "Authorization: Bearer $env:CYBERAGENT_API_TOKEN" http://127.0.0.1:8765/api/v1/runs/<run-id>/events/stream
```

`api serve` exposes authenticated, bodyless `GET` routes under `/api/v1` for durable Runs, Sessions, events, a bounded resumable SSE Run-event projection, the process-local redacted active-call projection, WorkItems, Notes, Artifact metadata, Supervisor tool rounds, token-free execution-lease status, and the raw OpenAPI 3.1 document. The listener, request Host, and client must all be loopback. The optional schema v18 root and schema v29 Specialist cancellation POST routes are disabled until `CYBERAGENT_API_CONTROL_TOKEN` is set; that token must differ from the read token and cannot authorize GET. Both POST routes require exact attempt identity, strict JSON, and `Idempotency-Key`, and never accept a fencing token. There is no CORS, Artifact-content route, checkpoint pending input, raw/private model stream, HTTP tool execution, or general mutation API. `api openapi` deterministically exports the same Go-generated contract without opening SQLite or reading a token. Neither process token is stored. See [http-api.md](http-api.md) for the complete contract.

## Plan items / 计划项

```powershell
cyberagent todo create <run-id> "inspect parser" --priority high --owner reviewer
cyberagent todo create <run-id> "root-owned plan" --owner-agent <agent-id>
cyberagent todo create <run-id> "write tests" --depends-on <work-id> --acceptance "tests pass"
cyberagent todo list <run-id>
cyberagent todo list <run-id> --status pending,blocked --owner reviewer
cyberagent todo list <run-id> --owner-agent <agent-id>
cyberagent todo show <work-id>
cyberagent todo update <work-id> --description "cover malformed input" --version 1
cyberagent todo update <work-id> --owner-agent <agent-id> --version 1
cyberagent todo update <work-id> --clear-dependencies
cyberagent todo start <work-id>
cyberagent todo block <work-id> --reason "waiting for fixture"
cyberagent todo reopen <work-id>
cyberagent todo complete <work-id>
cyberagent todo cancel <work-id>
```

Plan items belong to exactly one Run. Their API/DB/Go compatibility identity remains `WorkItem`/`work_item`. `--owner` remains a free-form compatibility label; `--owner-agent` is an authoritative reference to a nonterminal Agent in that same Run, and both may coexist. Dependencies must already exist in that same Run, cannot form a cycle, and must be completed before a dependent item starts or completes. Statuses are `pending`, `in_progress`, `blocked`, `completed`, and `cancelled`; priorities are `low`, `normal`, `high`, and `critical`. Blocked items require a reason, while completed and cancelled items are terminal.

Every Plan item starts at version 1. Mutation commands accept optional `--version <n>` optimistic locking; omitting it uses the version read immediately before the transaction, while an explicit stale value returns conflict exit code 4. `--acceptance` and `--depends-on` may be repeated. `--clear-acceptance` and `--clear-dependencies` replace those lists with empty values. Compatibility `WorkItem` records and `work_item.created/changed` Run events commit atomically, and terminal Runs reject later Plan mutation.

## Notes

```powershell
cyberagent note create <run-id> "parser decision" --content "Use strict JSON" --category decision --pin
cyberagent note create <run-id> "fixture evidence" --content-file C:\temp\note.txt --tag parser --source docs/spec.md --evidence evidence-1
cyberagent note create <run-id> "root summary" --content "Current root-only state" --visibility root
cyberagent note create <run-id> "specialist memory" --content "Private context" --visibility owner --owner specialist
cyberagent note create <run-id> "Agent memory" --content "Private context" --visibility owner --owner-agent <agent-id>
cyberagent note list <run-id> --status active --category decision,summary --tag parser
cyberagent note list <run-id> --visibility root --pinned true
cyberagent note list <run-id> --owner-agent <agent-id>
cyberagent note show <note-id>
cyberagent note update <note-id> --content "Revised decision" --version 1
cyberagent note update <note-id> --owner-agent <agent-id> --version 1
cyberagent note update <note-id> --clear-tags --unpin
cyberagent note archive <note-id>
cyberagent note restore <note-id>
```

Categories are `observation`, `hypothesis`, `decision`, `summary`, and `reference`. Visibility is `run`, `root`, or `owner`; owner-visible Notes require either a compatibility owner label or a validated same-Run Agent. When only `--owner-agent` is supplied for an owner-visible Note, its Agent ID is mirrored into the legacy label for old-reader and schema compatibility. The root Supervisor receives run-visible, root-visible, legacy `owner=root`, and root-Agent-owned Notes, while owner-only Specialist Notes remain excluded. Operators can still inspect all Notes through the CLI.

Each Note has normalized tags, source references, Evidence IDs, pinned state, active/archived lifecycle, and an optimistic version. `--tag`, `--source`, and `--evidence` may be repeated; update commands replace those lists or clear them explicitly. Archived Notes remain durable but cannot be edited or selected until restored. Terminal Runs reject later Note mutation.

`--content-file` reads valid UTF-8 through a bounded reader and rejects content over 64 KiB even if the file changes while being read. Content, titles, tags, references, Evidence IDs, event payloads, and model context pass through the redaction boundary. Note records and `note.created/changed` events commit together. Models receive selected Notes as untrusted `note_context.v1` JSON and may create a Note through the bounded RunSupervisor tool loop.

## Structured Memory Tools

`work-item.json`:

```json
{"title":"Inspect parser","description":"Use strict JSON","priority":"high","acceptance_criteria":["tests pass"]}
```

`note.json`:

```json
{"title":"Parser decision","content":"Use strict JSON","category":"decision","visibility":"root","pinned":true}
```

```powershell
cyberagent tool schema
cyberagent tool schema work_item_create
cyberagent tool schema note_create
cyberagent tool schema specialist_delegation_propose
cyberagent tool invoke work_item_create --run <run-id> --operation-key <stable-key> --payload-file .\work-item.json
cyberagent tool invoke note_create --run <run-id> --operation-key <stable-key> --payload-file .\note.json
cyberagent run delegations <run-id>
cyberagent run delegation <proposal-id>
cyberagent run delegation approve <proposal-id> --operation-key <stable-key> [--reviewer cli_operator] [--reason "bounded and in scope"]
cyberagent run delegation reject <proposal-id> --operation-key <stable-key> [--reviewer cli_operator] --reason "outside authorized scope"
cyberagent run delegation apply <proposal-id> --operation-key <stable-key> [--operator cli_operator]
cyberagent run delegation schedule <proposal-id> --operation-key <stable-key> [--operator cli_operator] [--max-rounds 1] [--agent <agent-id>]
cyberagent run delegation continue <proposal-id> --operation-key <new-stable-key> [--operator cli_operator] [--max-rounds 1] [--agent <agent-id>]
cyberagent run fanout plan <run-id> "audit source modules" --operation-key <stable-key> [--tier auto|1|2|4|6] [--path <dir>] [--operator cli_operator]
cyberagent run fanouts <run-id> [--limit 20]
cyberagent run fanout show <plan-id>
cyberagent run fanout execute <plan-id> --operation-key <stable-key> [--operator cli_operator] [--max-output-tokens 1024]
cyberagent run fanout execution <execution-id>
cyberagent run fanout report <execution-id> [--format markdown|json]
cyberagent report show <report-id> [--format markdown|json|sarif]
cyberagent report check <report-id> [--fail-status validated|active|none] [--min-severity info|low|medium|high|critical] [--format text|json|github]
cyberagent report check <report-id> --format github [--fail-status validated|active|none] [--min-severity info|low|medium|high|critical]
cyberagent report finding attach <finding-id> <artifact-id> --operation-key <stable-key> --note <text> [--operator cli_operator]
cyberagent report finding validate <finding-id> --operation-key <stable-key> --reason <text> [--operator cli_operator]
cyberagent report finding reject <finding-id> --operation-key <stable-key> --reason <text> [--operator cli_operator]
cyberagent report finding accept <finding-id> --operation-key <stable-key> --reason <text> [--operator cli_operator]
cyberagent report finding remediation attach <finding-id> <fresh-artifact-id> --operation-key <stable-key> --note <text> [--operator cli_operator]
cyberagent report finding fix <finding-id> --operation-key <stable-key> --reason <text> [--operator cli_operator]
cyberagent report finding verify <finding-id>
```

`work_item_create` creates one pending Plan item while retaining its compatibility `WorkItem`/`work_item` identity; `note_create` creates one active Note. They accept strict JSON with unknown fields and trailing data rejected before budget charging. The Run must already have an attached Run-local Session, and the CLI derives Session/Workspace scope from persisted Run state instead of accepting caller-supplied scope. `--payload` is also supported, while `--payload-file` avoids native-shell JSON quoting differences and is bounded to 96 KiB of valid UTF-8.

An operation key is mandatory and should remain stable across retries. The raw key is never persisted: schema v15 stores a domain-separated SHA-256 digest and a fingerprint of the normalized, redacted intent. Repeating the same tool, Run, key, and intent returns the original entity with `replayed: true`; changing intent under the same key returns conflict exit code 4. Replay, conflict, authoritative scope mismatch, and Policy-denied attempts each consume a tool-call budget entry because they are well-formed invocations. Malformed JSON, unknown fields, missing identities, and invalid field values are rejected before charging. Successful creation commits the entity, Policy decision, domain event, `tool.completed`, and operation ledger atomically. A failed event write leaves no entity or operation row.

The WorkItem and Note tools are create-only and return metadata rather than content. RunSupervisor advertises those two definitions plus `specialist_delegation_propose`: a Provider response may request at most four calls and one turn may perform at most four tool rounds. The model response and pending batch are committed together; after restart, unfinished calls are safely replayed through the semantic operation ledger and their terminal metadata is returned to the Provider. Anthropic-compatible transports encode this as `tool_use` and `tool_result`. Policy denial, invalid delegation capability requests, and budget exhaustion are returned as bounded error results, while protocol repair exposes no tools.

`specialist_delegation_propose` is Supervisor-only and cannot be invoked through `tool invoke`. Its strict payload is `{"version":"specialist_delegation.v1","assignments":[{"title":"Review parser","goal":"Inspect parser boundaries","skills":["model.chat"],"turn_limit":2,"token_limit":256}]}`. Unknown fields, more than two assignments, duplicate goals, unavailable or non-delegable Skills, stale leases, insufficient child capacity, and proposals that do not leave root budget headroom are rejected. Repeating the same redacted semantic intent returns the original proposal ID; the raw operation key and Provider call ID are never persisted. CLI output may show the redacted goals, independent review, and application state, while Run events contain only proposal identity, counts, suggested aggregate budgets, review/application metadata, and authorization phase flags. Approval/rejection has no Provider tool definition; application is operator-only and calls admission plus strict instruction delivery but never starts the scheduler.

Update, completion, archive, Shell, file, process, network, and other Provider-driven tools remain disabled pending separate lifecycle, approval, and Sandbox audits. Use the ordinary `todo` and `note` commands for operator-controlled updates.

## Sandbox Manifest

```powershell
cyberagent sandbox template
cyberagent sandbox validate configs/sandbox-manifest.example.json
cyberagent run sandbox prepare <run-id> --manifest configs/sandbox-manifest.example.json --operation-key sandbox-prepare-001
cyberagent run sandbox list <run-id>
cyberagent run sandbox show <preparation-id>
cyberagent run sandbox request <preparation-id> --operator cli_operator
cyberagent run sandbox review <preparation-id> --decision approve --operation-key sandbox-review-001 --reviewer security_operator
cyberagent run sandbox candidate <preparation-id> --manifest configs/sandbox-manifest.example.json --approval <approval-id> --operation-key sandbox-candidate-001
cyberagent run sandbox candidates <run-id>
cyberagent run sandbox candidate-show <candidate-id>
cyberagent run sandbox begin <candidate-id> --manifest configs/sandbox-manifest.example.json --operation-key sandbox-begin-001
cyberagent run sandbox executions <run-id>
cyberagent run sandbox execution-show <execution-id>
cyberagent run sandbox preflight <execution-id> --manifest configs/sandbox-manifest.example.json --operation-key sandbox-preflight-001
cyberagent run sandbox preflights <run-id>
cyberagent run sandbox preflight-show <preflight-id>
cyberagent run sandbox cancel <execution-id> --operation-key sandbox-cancel-001
cyberagent run sandbox cleanup <execution-id> --operation-key sandbox-cleanup-001
```

`sandbox validate` performs strict duplicate-aware `sandbox_manifest.v1` decoding and deterministic Noop validation without opening the runtime database. `run sandbox prepare` requires a Run whose Mission has a persisted Workspace, then binds the normalized Manifest fingerprint to that exact Run/Mission/Workspace root, Mission Scope, current Policy result, optional exact approval, requester, and a Go-generated cancellation identity. Operation keys are normalized 16-256 byte client identities; SQLite stores only their domain-separated digest.

The preparation and validation ledgers contain counts, limits, fingerprints, status, and binding identities only. Executable, argv, mount/output paths, environment values, secret references, network targets, and Manifest JSON are not stored or emitted in events. Network allowlists may only narrow a Mission allowlist. Docker/Local intent, writable mounts, network, or secret references require approval when Policy allows them, while permanent Policy denial is recorded and cannot be overridden.

Schema v49 uses the shared approval ledger rather than a Sandbox-specific bypass. `request` derives one pending approval from the preparation's exact authorization fingerprint, and `review` records an immutable operator decision. `candidate` must resupply and renormalize the complete Manifest. It rejects fingerprint, Workspace root, Mission Scope, Policy, or approval drift; resolves every mount source through Go `os.Root`; and rechecks aggregate token/model-time usage, tool-call budget, and the absence of an active Run execution lease in the candidate write transaction. Operation keys are digest-only and cross-process retries converge.

Schema v49 is still not an execution API. Candidate rows and events contain only bounded metadata and fix `backend_enabled=false` plus `execution_authorized=false`; Local and Docker remain fail-closed and no host/container process starts. Future execution must revalidate again and pass separate cancellation, cleanup, network, secret-materialization, host-path isolation, and Artifact export audits.

Schema v50 adds a disabled lifecycle, not process execution. `begin` resupplies the complete Manifest and rechecks the candidate, Run/Mission/Workspace/Scope, current Policy and approval, mount binding, aggregate budgets, Run lease, and every input Artifact. Inputs must belong to the exact Run/Session/Workspace, pass content SHA-256 verification, retain their order/source/MIME/stream metadata, and total at most 16 MiB. The output plan stores only stdout/stderr flags, output-path count, maximum bytes, and a fingerprint; raw paths are not retained.

The lifecycle owns a separate generation-fenced lease. Generation one only prepares the disabled record and is released immediately. `cancel` appends an immutable request, while `cleanup` may run after the parent Run is terminal and acquires a successor generation. The current cleanup outcome is always `backend_disabled`: no backend started, no orphan existed, all inputs were reverified, and zero output Artifacts were captured. CLI detail intentionally omits the lease ID and owner as well as Manifest, command, path, and Artifact content. Reusing the same operation key and intent is safe; changed intent conflicts.

Schema v51 adds a separate disabled preflight after `begin` and before any future backend action. `preflight` resupplies the complete Manifest and revalidates the full v48-v50 chain, current Policy/approval/Scope, mounts, cumulative budgets, Run-lease quiescence, and exact input Artifacts. It records a fixed 16-item backend threat model, but every check remains required, unverified, and not probed. The backend handshake reports unavailable, container identity is unbound, and all backend/execution/export/Artifact-commit flags remain false.

The output plan stores only opaque locator fingerprints plus `stdout`, `stderr`, or `file` kinds. File slots require regular files and reject symlinks and special files; every slot requires MIME detection and redaction. Export is all-or-nothing under one aggregate byte ceiling and must reconcile before retry. CLI detail deliberately omits locator fingerprints, raw paths, command/Manifest content, container identity, and private lease data. A successful disabled preflight proves only that the intended checks are frozen and the current authority chain still matches; it does not prove Docker availability and cannot authorize execution.

Schema v52 provides a simulation-only continuation for protocol testing. Start the complete `prepare -> request -> review -> candidate -> begin -> preflight` chain with `configs/sandbox-docker-simulation.example.json`, then use the resulting preflight ID:

```powershell
cyberagent run sandbox evidence <preflight-id> --manifest configs/sandbox-docker-simulation.example.json --image-digest sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee --operation-key sandbox-evidence-001
cyberagent run sandbox evidences <run-id>
cyberagent run sandbox evidence-show <evidence-id>
cyberagent run sandbox output-simulate <evidence-id> --manifest configs/sandbox-docker-simulation.example.json --fixture configs/sandbox-output-fixture.example.json --operation-key sandbox-output-simulation-001
cyberagent run sandbox output-simulations <run-id>
cyberagent run sandbox output-simulation-show <simulation-id>
cyberagent run sandbox observe <evidence-id> --simulation <simulation-id> --manifest configs/sandbox-docker-simulation.example.json --operation-key sandbox-docker-observe-001 --confirm-readonly-probe
cyberagent run sandbox observations <run-id>
cyberagent run sandbox observation-show <observation-id>
cyberagent run sandbox docker-plan <observation-id> --manifest configs/sandbox-docker-simulation.example.json --operation-key sandbox-docker-plan-001 --confirm-fake-write
cyberagent run sandbox docker-plans <run-id>
cyberagent run sandbox docker-plan-show <plan-id>
cyberagent run sandbox docker-rehearse <plan-id> --manifest configs/sandbox-docker-simulation.example.json --operation-key sandbox-docker-rehearsal-001 --confirm-daemon-write --stage-host-inputs --confirm-host-input-staging --handoff-host-inputs --confirm-host-input-handoff
cyberagent run sandbox docker-rehearsals <run-id>
cyberagent run sandbox docker-rehearsal-show <rehearsal-id>
cyberagent run sandbox docker-attempts <run-id>
cyberagent run sandbox docker-attempt-show <attempt-id>
cyberagent run sandbox docker-attempt-resume <attempt-id> --manifest configs/sandbox-docker-simulation.example.json --confirm-daemon-write --stage-host-inputs --confirm-host-input-staging --handoff-host-inputs --confirm-host-input-handoff
cyberagent run sandbox docker-host-inputs <run-id>
cyberagent run sandbox docker-host-input-show <intent-id>
cyberagent run sandbox docker-host-input-handoffs <run-id>
cyberagent run sandbox docker-host-input-handoff-show <handoff-intent-id>
cyberagent run sandbox docker-runtime-input-plan <handoff-intent-id> --manifest configs/sandbox-docker-simulation.example.json --operation-key runtime-input-plan-001 --confirm-runtime-input-plan
cyberagent run sandbox docker-runtime-input-plans <run-id>
cyberagent run sandbox docker-runtime-input-plan-show <projection-id>
cyberagent run sandbox docker-runtime-input-apply <projection-id> --manifest configs/sandbox-docker-simulation.example.json --operation-key runtime-input-apply-001 --confirm-runtime-input-apply --confirm-daemon-write
cyberagent run sandbox docker-runtime-input-apply-resume <application-intent-id> --manifest configs/sandbox-docker-simulation.example.json --confirm-runtime-input-apply --confirm-daemon-write
cyberagent run sandbox docker-runtime-input-applications <run-id>
cyberagent run sandbox docker-runtime-input-application-show <application-intent-id>
cyberagent run sandbox docker-runtime-input-resource-inspect <application-intent-id> --manifest configs/sandbox-docker-simulation.example.json --operation-key runtime-input-resource-inspect-001 --confirm-readonly-probe
cyberagent run sandbox docker-runtime-input-resource-inspections <run-id>
cyberagent run sandbox docker-runtime-input-resource-inspection-show <inspection-id>
cyberagent run sandbox docker-runtime-input-resource-cleanup <inspection-id> --manifest configs/sandbox-docker-simulation.example.json --operation-key runtime-input-resource-cleanup-001 --confirm-resource-cleanup --confirm-daemon-write
cyberagent run sandbox docker-runtime-input-resource-cleanup-resume <cleanup-intent-id> --manifest configs/sandbox-docker-simulation.example.json --confirm-resource-cleanup --confirm-daemon-write
cyberagent run sandbox docker-runtime-input-resource-cleanups <run-id>
cyberagent run sandbox docker-runtime-input-resource-cleanup-show <cleanup-intent-id>
```

`evidence` never contacts a daemon. It binds a canonical OCI image digest and separate simulated daemon/mount/network/secret/container/resource/termination/orphan/output fingerprints to the 16 v51 checks, but reports `trust_class=simulation_only`, `production_verified=0`, and `verified_checks=0`. `output-simulate` strictly validates and redacts fixture content, stages all slots, and commits only to an in-memory fake sink. A failure or cancellation rolls the fake transaction back to zero, and no production Artifact is created. The Store and Application revalidate the complete v48-v51 chain at both boundaries. CLI and events omit fixture bodies, locator fingerprints, raw paths, commands, Manifest content, container IDs, operation digests, and private leases. These commands test protocol behavior only; they cannot create or start a Docker container and cannot authorize real execution.

Schema v53 `observe` is the only command in this chain that may contact a real daemon, and it requires the exact `--confirm-readonly-probe` flag. Before the probe, it resupplies the complete Manifest and binds the same v52 evidence and output simulation. Linux connects only to `/var/run/docker.sock`; Windows currently records `transport_unsupported`. `DOCKER_HOST`, arbitrary TCP endpoints, caller-selected sockets, redirects, proxying, the Docker CLI, image pulls, and every container mutation are excluded. The transport can issue only `GET /_ping`, `GET /version`, `GET /info`, and exact-digest image inspection.

An observation records `observation_complete`, `daemon_unavailable`, or `image_unavailable`. A complete observation may report `production_observed=true`, which means only that bounded daemon and image metadata were read. It does not mean `production_verified`, `backend_available`, `backend_enabled`, `execution_authorized`, or `artifact_commit_authorized`; all remain false. Private-mount support is printed as `not_observable_read_only` because these GETs cannot prove it. Raw daemon ID/name/root, socket, RepoDigests, Manifest, commands, operation keys, and private leases are neither persisted nor printed. Repeating the same operation returns the existing row without probing again, and one output simulation accepts at most eight observations.

Schema v54 `docker-plan` requires the exact `--confirm-fake-write` flag and accepts only `observation_complete`. It resupplies the Manifest and revalidates the entire v48-v53 chain before compiling a deterministic in-memory container specification. The compiler fixes non-root execution, read-only root and inputs, one writable output mount, private propagation, network default deny or exact managed allowlisting, ephemeral secrets, resource/time/kill limits, and orphan reconciliation identity. Sixteen plan controls remain `compiled_not_applied`, and the seven reconcile/create/start/wait/stop/export/remove steps run only in an in-memory fake transaction. Failure, simulated crash, or cancellation commits no fake result; success still reports zero daemon writes and no backend contact, execution, export, or production Artifact authority. Durable rows and CLI output omit commands, arguments, paths, network targets, environment values, secret references, labels, and container names. No v54 command mutates Docker.

Schema v55 `docker-rehearse` is the first command that may perform real Docker writes, so it is default-disabled in the Application service and requires exact `--confirm-daemon-write`. It accepts only a current v54 plan whose profile has no network, environment binding, or secret. Linux uses fixed `/var/run/docker.sock` and API `1.40`; Windows returns unsupported. The closed transport permits exact image/container inspection, create, and a non-forced delete with fixed anonymous-volume cleanup. It ignores `DOCKER_HOST` and has no TCP, caller socket, pull, start, exec, attach, logs, export, volume management, image build, or generic request operation.

Before create, the already-present image RepoDigest must match the plan and the image must declare no `VOLUME`. The transport creates one stopped digest-pinned container, verifies attachment/device/port settings plus non-root, read-only root, no-new-privileges, drop-all capabilities, resource limits, network none, and private mounts, then removes it. Cancellation, failure, and uncertain create responses use an independent bounded re-inspection and only delete an exact authority match. Same-key replay returns before transport access. A normal successful fact records three reads and two writes, or three writes after removing one exact stale rehearsal container. It still records process execution, image pull, output export, production verification, backend enablement, execution authority, and Artifact authority as false.

Schema v56 makes that never-started rehearsal recoverable. `docker-rehearse` now persists an attempt and acquires a generation-fenced lease before contacting Docker. After create, it stores a 19-item inspected-configuration matrix before cleanup; every item reports `execution_evidence=false`. A crash or uncertain response can be resumed with the durable attempt ID. `docker-attempt-resume` requires the original complete Manifest and a fresh `--confirm-daemon-write`, recomputes the full v48-v54 authority chain, and refuses any changed intent or requester. It adopts only an exact stopped authority match, accepts already-absent cleanup, and never deletes a mismatched same-name container. The raw operation key is not required for recovery and is not printed or stored.

`docker-attempts` and `docker-attempt-show` expose bounded status, generation, timestamps, failure codes, and non-execution controls. They omit raw container IDs, host paths, commands, environment values, secrets, sockets, operation keys, and private lease owners. The inherited image/container environment must be empty, not merely absent from the Manifest. The fixed local endpoint and no-network/no-secret/no-start/no-exec/no-pull/no-export boundary are unchanged.

Schema v57 optionally adds `--stage-host-inputs`, which always requires a separate `--confirm-host-input-staging`. On Linux, the stager pins the workspace root and read-only mount trees with `openat2` no-symlink/no-magic-link/beneath/no-cross-device resolution. It uses `O_PATH` to reject FIFOs and other special files before a content open, supports both directory and single-file mounts, bounds directory enumeration before allocation, observes cancellation while reading files, rejects hard links and bounded-resource violations, rechecks descriptor metadata after the whole tree is pinned, then writes a deterministic sanitized tar to a sealable `memfd`. It applies write/grow/shrink/seal kernel seals and reads the bundle back to verify its digest. Input Artifacts are reloaded and reverified by exact Run, Session, Workspace, digest, size, MIME, stream, source, redaction state, and order immediately before staging.

The v57 intent is durable before bundle creation and is bound to the current v56 attempt, plan, stopped-container fingerprint, input digest, and lease generation. Generated row IDs are excluded from semantic fingerprints, so independent retries converge on one intent/result. SQL refuses final attempt completion until a matching result exists. A staging error triggers best-effort stopped-container cleanup and leaves a recoverable pending intent. Legacy attempts created on schema v57 retain their conservative compatibility behavior: resume must resubmit both staging flags, missing confirmation is rejected before lease acquisition, and no failure slot is consumed. `docker-host-inputs` and `docker-host-input-show` expose counts, digests, seals, and status only. They never expose source paths, content, descriptors, raw container IDs, or private lease identities.

The bundle is currently discarded after its sealed digest is verified and is not uploaded or mounted into Docker. Accordingly the durable result fixes `daemon_consumed=false`, `execution_evidence=false`, and every production/backend/execution/Artifact authority to false. v57 closes source replacement during descriptor capture, but a later independently audited daemon handoff is still required before any future start boundary. Windows returns the explicit `staging_unsupported` error before a container is created.

Schema v58 makes that staging choice durable for every new attempt. `docker-rehearse --stage-host-inputs --confirm-host-input-staging` stores one immutable host-input requirement in the same transaction as the attempt, initial lease, and audit events, before any daemon stage. The fact is bound to the plan, Run, Mission, Workspace, requester, operation digest, authority fingerprints, and bounded input counts. CLI list/show prints `host_input_required` and whether a durable requirement is present, but never paths, content, descriptors, raw container IDs, operation keys, or private lease identities.

For a v58 attempt, `docker-attempt-resume` still requires the full Manifest and `--confirm-daemon-write`, but it does not require the staging flags to be repeated. A durable `host_input_required=true` automatically resumes v57 capture before completion; explicitly resubmitting the two matching staging flags is accepted but cannot change the choice, while an unmatched flag pair is rejected before lease acquisition. A durable false choice cannot be widened into staging. Go and SQLite both reject completion without required evidence and reject staging against a false requirement. Existing v57 attempts are not backfilled because migration cannot safely invent historical operator intent. Their IDs are placed in an immutable migration-only compatibility set; inserts are disabled immediately afterward, so a new requirement-free attempt cannot use the legacy path.

Schema v58 does not add a Docker archive, volume, start, exec, pull, build, export, or Artifact endpoint. The sealed bundle remains local and `daemon_consumed=false`. A separately reviewed schema-v59 design must use a daemon-owned carrier, verify exact upload and readback bytes, remove the writable carrier, and recreate the never-started target with the verified carrier mounted read-only; making the target root or input writable is not an acceptable shortcut.

Schema v59 implements that handoff as a separate opt-in boundary. `--handoff-host-inputs` is valid only together with `--stage-host-inputs`, `--confirm-host-input-staging`, `--confirm-host-input-handoff`, and the existing `--confirm-daemon-write`. The immutable handoff requirement is created with the attempt, and a write-ahead intent commits before any archive or volume call. Resume may omit the staging and handoff flag pairs after those required choices are durable; submitting only part of either pair is rejected before lease acquisition, and a durable false choice cannot be widened.

Linux uses only the fixed local Unix socket and Docker API `1.40`. One deterministic, never-started carrier writes the sealed bytes to a daemon-owned local volume at `/cyberagent-input/bundle.tar`. Go reads that file back through Docker, verifies exact length and digest, removes the carrier and original stopped target, creates a never-started target with the volume read-only, verifies its complete configuration, then removes the target and volume. Manifest mounts may not overlap the reserved `/cyberagent-input` tree. Retry reconciles only exact request-owned residue; a foreign same-name container or volume is not modified. The target root and reviewed Manifest mounts never become writable.

`docker-host-input-handoffs` and `docker-host-input-handoff-show` expose status, bounded daemon read/write counts, generation, readback/readonly/cleanup flags, and fingerprints. They omit source paths, raw content, descriptors, raw container IDs, carrier/volume names, socket details, raw operation keys, and private lease identities. A successful record means only `daemon_consumed=true`, `readback_verified=true`, and cleanup completed. Container start, process execution, output export, backend enablement, execution authority, and Artifact commit authority remain false.

Schema v60 `docker-runtime-input-plan` separately confirms and recompiles the exact completed handoff into one canonical relative archive per reviewed directory-root input plus an optional fixed Artifact projection. It never contacts Docker. Schema v61 `docker-runtime-input-apply` then requires both `--confirm-runtime-input-apply` and `--confirm-daemon-write`. Go persists its intent and generation lease before recapture or daemon writes, revalidates v48-v60, writes each transient archive through a never-started carrier, verifies daemon readback, and retains one target with every input volume read-only/`NoCopy`. `apply-resume` reacquires only a released or expired intent and requires the full Manifest plus both confirmations; a completed operation returns metadata without contacting Docker.

Application list/show output includes only status, generation, bounded counts, fingerprints, verification flags, and false authority bits. It excludes targets, host paths, file/resource names, raw IDs, archives, socket details, raw operation keys, and lease identities. `volumes_applied_target_never_started` means the target and input volumes are prepared, not runnable. There is no `start`, process, export, backend, execution, or Artifact authority in v61, and Windows returns `application_unsupported`.

Schema v62 separates retained-resource observation from deletion. `docker-runtime-input-resource-inspect` requires `--confirm-readonly-probe`, rebuilds the exact descriptor from the completed v61 record and resupplied Manifest, and performs no input recapture. It records whether the target and each deterministic volume are exact-owned, absent, or foreign. A foreign or changed resource is persisted as unsafe evidence and the command exits with a failed-precondition status; complete read-only/`NoCopy` evidence is claimed only when the never-started target and every volume match.

`docker-runtime-input-resource-cleanup` requires both `--confirm-resource-cleanup` and `--confirm-daemon-write`, plus a cleanup-eligible inspection. Go persists the immutable intent and generation lease before contacting the write transport. The transport preflights the target and every volume before any DELETE; a single foreign collision means zero DELETE. Otherwise it removes the target by its inspected ID, removes exact-owned volumes, and rechecks that all resources are absent. Typed failures release the lease and `cleanup-resume` can acquire a later generation. Completed operation-key replay and completed resume are metadata-only. List/show output omits resource names, raw IDs, host paths, sockets, raw operation keys, and private lease identities. v62 adds no start, exec, attach, pull, output export, backend, execution, or Artifact authority; Windows returns an explicit unsupported error.

Schema v63 performs a metadata-only start-gate design review after completed v62 cleanup. It requires a resupplied Manifest, a stable operation key, and `--confirm-design-review`. The review maps all sixteen v51 threat checks to bounded v52-v62 evidence classes and explicit future blockers. It also freezes an eleven-transition start/wait/TERM/KILL/orphan blueprint with write-ahead ownership, generation fencing, cancellation fan-out, bounded logs, and orphan reconciliation. Every check remains insufficient and every transition remains unimplemented and unauthorized; the command never contacts Docker or captures input.

```powershell
cyberagent run sandbox docker-start-gate-review <cleanup-intent-id> `
  --manifest configs/sandbox-manifest.example.json `
  --operation-key <stable-key> --confirm-design-review
cyberagent run sandbox docker-start-gate-reviews <run-id>
cyberagent run sandbox docker-start-gate-review-show <review-id>
```

The only v63 outcome is `blocked/deny_start`. Output contains bounded evidence source codes, blocker codes, future gate names, and false authority bits. It omits resource names, raw container IDs, host paths, Manifest bodies, raw operation keys, and private ownership identities. A successful review records why start is still denied; it does not verify the Linux real-daemon chain and does not add start, wait, signal, logs, export, execution, or Artifact authority.

Schemas v65-v68 add a non-authorizing production-evidence receipt, a recoverable write-ahead attempt, an explicitly opted-in Linux read-only daemon harness, and one immutable operator decision over the resulting receipt:

```powershell
cyberagent run sandbox docker-production-evidence-capture <review-id> `
  --operation-key <stable-key> --confirm-machine-capture
cyberagent run sandbox docker-production-evidence-captures <run-id>
cyberagent run sandbox docker-production-evidence-show <evidence-id>
cyberagent run sandbox docker-production-evidence-attempts <run-id>
cyberagent run sandbox docker-production-evidence-attempt-show <attempt-id>
cyberagent run sandbox docker-production-evidence-attempt-resume <attempt-id> `
  --confirm-machine-capture
cyberagent run sandbox docker-production-evidence-review <evidence-id> `
  --decision accepted --reason-code metadata_scope_accepted `
  --operation-key <stable-key> --confirm-evidence-review
cyberagent run sandbox docker-production-evidence-reviews <run-id>
cyberagent run sandbox docker-production-evidence-review-show <review-id>
```

`docker-production-evidence-capture` first commits an immutable attempt, digest-only operation, generation lease, and current-generation quiescent reconciliation checkpoint. Only then can it call the collector. A typed failure releases the lease, and resume acquires generation N+1 only after a release or expiry; stale generations cannot record reconciliation, failure, or evidence. Completion atomically binds the attempt result to the v65 receipt and its sixteen fixed items. SQL rejects a v65 evidence operation without a matching v66 or v67 result, while pre-v66 receipts and in-flight v66 attempts remain readable without fabricated v67 state.

Windows records `unsupported_platform`, and Linux without `CYBERAGENT_DOCKER_PRODUCTION_EVIDENCE=1` records `opt_in_required`; neither path contacts a daemon. With explicit Linux opt-in, schema v67 persists a harness intent after the v66 zero-read checkpoint, performs one exact attempt-label container-list GET, requires an empty owned scope, persists daemon-aware reconciliation, and then GETs `_ping`, `version`, `info`, and the exact already-present image digest. Each call is bounded to four seconds and the complete attempt remains bounded to 30 seconds. The harness ignores `DOCKER_HOST`, never pulls, and exposes no create/start/exec/remove/delete method.

The resulting sixteen items are all `observed_failed` with `production_verified_count=0`; they do not authorize start, process execution, output export, or Artifact commit. CLI output omits lease IDs/owners, raw errors, sockets, paths, image repository names, resource/container identities, and daemon payloads. A persisted v67 intent cannot fall back to the inert v66 result, and resume must repeat daemon-aware reconciliation under its new generation.

`docker-production-evidence-review` accepts only an exact completed v67 harness receipt. It requires explicit confirmation and one bounded `accepted|rejected` decision. Acceptance must use `metadata_scope_accepted`; rejection must use `integrity_concern`, `environment_concern`, `scope_concern`, `insufficient_evidence`, or `operator_rejected`. There is no free-form reason or uploaded evidence body. Each evidence/attempt can receive only one immutable decision, and same-key replay must preserve the receipt, reviewer, decision, and reason.

An accepted v68 decision classifies only the bounded metadata receipt. It still reports `production_verified_count=0`, `sufficient_check_count=0`, and `blocker_count=16`, with start, process, output, and Artifact authority all false. Review performs no Docker request and migration creates no decision for legacy or incomplete receipts. List/show output omits raw operation keys, daemon payloads, resources, paths, sockets, and free-form narratives.

## Docker Sandbox 产品执行 / Docker Sandbox Product Execution

Schema v99 不改写上面的历史链：v97 lifecycle 与 v98 I/O 仍是非授权事实，只有新的
`DockerSandboxService` 能在**当前进程**同时满足所有门禁后创建产品 admission。默认
capability 为关闭，SQLite 中只保存 runtime epoch 的摘要，重启或修改数据库都不能恢复
start authority。CLI、HTTP、Desktop 和模型提案复用同一服务；模型工具
`sandbox_docker_run_propose` 只能提交严格 `{version,plan_id,manifest}` 并调用 Admit，
不能启动容器或提交 endpoint、Docker flag、host bind、环境变量、代理、镜像覆盖与网络
放宽。

### 前置条件 / Prerequisites

1. 使用 v48-v54 流程得到当前、精确、已经 per-call 批准的 Docker plan；Manifest 必须与
   plan 完全一致。
2. Run 的当前 execution profile 必须为 `docker`，当前 permission 必须是
   `ask|auto|full`；持久快照不等于 runtime capability。
3. Manifest 必须 environment-free、secret-free、`network.mode=disabled` 且零 target。
   allowlist 当前固定失败为 `managed_egress_unavailable`，因为 exact
   host/port/protocol 的 Go-owned egress guard 尚未实现。
4. 本机固定 Docker endpoint 必须可达，使用 Linux containers，支持兼容 API 与 PIDs
   limit，并已存在 plan 绑定的精确 OCI digest 镜像。产品不会 pull，也不会回退到宿主。
5. CPU、memory、PIDs、disk/output、wall-clock、log 和剩余 Tool-call budget 必须同时
   可用。范围为 CPU `1..8000 ms`、memory `16 MiB..8 GiB`、PIDs `1..512`、输出总量
   `1 byte..16 MiB`、wall clock `1..3600s`；日志每个流最多 256 KiB/4096 行，输出最多
   64 个文件且单文件最多 4 MiB。

Profile 与 permission 可分别这样选择；操作者、Run 状态与 operation key 必须满足原有
命令约束：

```powershell
cyberagent run execution-profile set <run-id> docker `
  --operation-key profile-docker-0001

cyberagent run execution-permission set <run-id> auto `
  --operation-key permission-auto-0001 --enable-permission-control
```

For a Run whose current preference is `full`, each `docker-admit` or `docker-start`
invocation additionally needs `--enable-danger-full-access --confirm-full`.
The activation ends with that invocation. The exact `sandbox.manifest` per-call
approval remains mandatory; Full does not replace it.

### CLI

Product commands intentionally use only `--manifest-file`; inline Manifest、`--manifest`、
Docker endpoint/flag/mount overrides are rejected.

```powershell
# Disabled/default process: stable disabled readiness, no Docker write.
cyberagent run sandbox docker-readiness <plan-id> `
  --manifest-file <manifest.json>

# Enabled readiness and exact admission.
cyberagent run sandbox docker-readiness <plan-id> `
  --manifest-file <manifest.json> `
  --enable-docker-execution --enable-permission-control

cyberagent run sandbox docker-admit <plan-id> `
  --manifest-file <manifest.json> `
  --operation-key docker-admit-0001 --operator cli_operator `
  --enable-docker-execution --enable-permission-control

# docker-start performs a fresh Admit + Start in the same process. Admission
# and Start have independent replay keys and each key must remain stable.
cyberagent run sandbox docker-start <plan-id> `
  --manifest-file <manifest.json> `
  --admission-operation-key docker-admit-for-start-0001 `
  --operation-key docker-start-0001 --operator cli_operator `
  --enable-docker-execution --enable-permission-control

cyberagent run sandbox docker-status <admission-id>
cyberagent run sandbox docker-cancel <admission-id> `
  --operation-key docker-cancel-0001 --operator cli_operator
```

`docker-start` is synchronous. Another client may issue cancellation while it is active; a
terminal attempt rejects a new cancellation unless it is the exact replay of the already
committed cancelled result. Status progresses through `admitted|launched|terminal` and terminal
outcomes are `succeeded|timed_out|cancelled|failed`.

Readiness uses `sandbox.readiness.v1`, a fresh 30-second result with
`ready|disabled|unavailable`, a stable reason/remediation, endpoint fingerprint, capacity facts,
and no mutation. Admission and the final pre-start fence both repeat current authority and
readiness checks. See the complete reason/remediation tables in
[HTTP API](http-api.md).

### HTTP 与 Desktop / HTTP and Desktop

Start the HTTP process only with a distinct control token and explicit capabilities:

```powershell
$env:CYBERAGENT_API_TOKEN = "<read-token-at-least-32-bytes>"
$env:CYBERAGENT_API_CONTROL_TOKEN = "<different-control-token-at-least-32-bytes>"
cyberagent api serve --listen 127.0.0.1:8765 `
  --enable-permission-control --enable-docker-execution
```

The routes are `POST /api/v1/sandbox/docker/readiness`, `POST .../admissions`,
`POST .../starts`, `POST .../cancellations`, and
`GET .../status?admission_id=...`. Readiness and status use the read bearer;
the other POSTs use the control bearer plus independent `Idempotency-Key`
headers. Exact JSON examples are in [Local HTTP API](http-api.md).

Desktop likewise requires `--enable-permission-control --enable-docker-execution` at process
startup. The renderer receives only the projected capability/readiness/status and calls the
in-process HTTP handler; it cannot set the process flag, runtime epoch, Policy decision,
permission, approval, daemon endpoint, Manifest extension, or Docker configuration.

### 退出、输出与恢复 / Exit, Output, and Recovery

After the exact exited checkpoint, Go captures bounded stdout/stderr before cleanup. Only natural
exit `0` plus a fresh current Artifact-authority check can export the dedicated output mount,
strictly validate/stage files, re-read/re-hash them, and atomically commit output records. Timeout,
cancellation, non-zero exit, I/O failure, and authority change never commit outputs. Cleanup still
runs and every product terminal receipt requires `cleanup_complete=true`.

Cancellation is sticky: it is persisted before the active context is signalled or an expired
lease is taken over. Startup recovery considers only records that already have a durable launch
binding. It may reconcile, stop, and clean the exact nine-label/configuration match without a new
start capability; an admission-only record is not auto-started, and a restarted process cannot
use the old runtime epoch to start a created container. Unknown, partial, legacy, foreign, or
mismatched containers are untouched. See
[ADR 0099](adr/0099-docker-sandbox-product-admission-and-recovery.md).

The full v67 five-GET harness has a default-skipped Linux integration test. It requires an exact image already present in the local daemon and never pulls, creates, starts, or deletes anything:

```powershell
$env:CYBERAGENT_DOCKER_PRODUCTION_EVIDENCE = "1"
$env:CYBERAGENT_DOCKER_READONLY_IMAGE_DIGEST = "sha256:<already-present-digest>"
go test ./internal/sandbox -run TestDockerProductionEvidenceHarnessRealDaemonOptIn -count=1 -v
```

The lower-level generic read-only observer has a separate opt-in test:

```powershell
$env:CYBERAGENT_DOCKER_READONLY_INTEGRATION = "1"
$env:CYBERAGENT_DOCKER_READONLY_IMAGE_DIGEST = "sha256:<already-present-digest>"
go test ./internal/sandbox -run TestLocalDockerReadOnlyObservationIntegration -count=1 -v
```

The v55 write rehearsal has a separate opt-in Linux test. The supplied digest must already be present, match a RepoDigest, and declare no `VOLUME`; the test never pulls or starts it:

```powershell
$env:CYBERAGENT_DOCKER_WRITE_TEST_IMAGE_DIGEST = "sha256:<already-present-volume-free-digest>"
go test ./internal/sandbox -run TestDockerContainerWriteRealDaemonOptIn -count=1 -v
```

The same opt-in digest can exercise the schema-v59 archive/volume handoff. The image must also expose an empty inherited environment. The harness never pulls or starts a container and asserts that the target, carrier, and volume are all absent afterward:

```powershell
$env:CYBERAGENT_DOCKER_WRITE_TEST_IMAGE_DIGEST = "sha256:<already-present-volume-free-digest>"
go test ./internal/sandbox -run TestDockerHostInputHandoffRealDaemonOptIn -count=1 -v
```

The end-to-end opt-in harness now runs the v57 capture, v59 handoff, v60 projection, v61 application, and v62 inspection/cleanup chain using the same already-present image constraints. It verifies the retained never-started target and read-only volumes, then uses the v62 exact-owned lifecycle to remove and recheck every resource. It never pulls or starts the container:

```powershell
$env:CYBERAGENT_DOCKER_WRITE_TEST_IMAGE_DIGEST = "sha256:<already-present-volume-free-digest>"
go test ./internal/sandbox -run TestDockerRuntimeInputApplicationRealDaemonOptIn -count=1 -v
```

## Workspaces

```powershell
cyberagent workspace init demo
cyberagent workspace list
cyberagent workspace show demo
cyberagent workspace tree demo
cyberagent workspace tree demo scripts --depth 2
cyberagent workspace read demo README.md
```

`workspace tree` and `workspace read` only accept paths relative to the selected workspace. Attempts to read outside the workspace, such as `../outside.txt`, are rejected. Text returned by `workspace read` is passed through secret redaction before printing.

The Web/Desktop Run **Files** tab uses the separate read-only
`workspace_explorer.v1` route. Go resolves the registered Workspace root and returns
only canonical relative child paths. It follows no links, exposes no root path, scans
at most 400 entries, returns at most 200, reads at most 64 KiB of UTF-8, and caps the
redacted projection at 128 KiB. File text is plain evidence with
`instruction_authorized=false`; notes addressed to an automated assistant do not gain
system, user, tool, or Policy authority by appearing in a repository file.

### Transactional Workspace Checkpoints

Schema v117 adds an immutable, bounded checkpoint timeline for a Run's exact Workspace
and Git index. Automatic before/after boundaries cover FileEdit apply, model-callable
file tools, Run-owned command batches/background Jobs, typed Git mutations, and the
agent-merge contract. A manual capture and every restore use a stable operation key:

```powershell
cyberagent workspace checkpoint timeline --run <run-id> --limit 100
cyberagent workspace checkpoint capture --run <run-id> `
  --operation-key before-refactor --title "Before parser refactor"
cyberagent workspace checkpoint preview --run <run-id> `
  --checkpoint <target-id> --expected-current <current-id>
cyberagent workspace checkpoint rewind --run <run-id> `
  --checkpoint <target-id> --expected-current <current-id> `
  --operation-key rewind-parser-1 --confirm --enable-permission-control
```

Undo and Redo use the same `--expected-current`, `--operation-key`, and `--confirm`
contract. Restore is permitted only for a paused Code/Deliver Run with an active
Session, no live execution lease, current Ask/Auto/Full operation authority, and matching
process capability flags. It writes a new checkpoint after an exact three-way preview;
it never invokes `git reset --hard` or blanket-deletes untracked files. Fork additionally
requires a new Git branch and an absent destination path, creates an independent
Workspace/Mission/Run/Session, and restores no historical authority.

The CLI supplies that exact destination with `--workspace-root`. HTTP/Desktop do not
accept renderer-provided absolute paths; Go derives a deterministic absent sibling from
the trusted source Workspace and operation key, and the response omits the root path.

See [Workspace Checkpoints](workspace-checkpoints.md) for CLI, HTTP, Desktop, quota,
conflict, and recovery details, and [ADR 0118](adr/0118-transactional-workspace-checkpoints.md)
for the protocol and threat model.

### Independent child tools

For an already admitted child, open **Inspector → Agents → Run child agent**.
The child can read, list, glob and grep the current Run workspace and inspect saved
GitHub review evidence. **Child tool receipts** shows its actual tool calls and
results. The card retains an outstanding operation key across a page reload so a
retry reads the original result rather than dispatching another model call.

The CLI exposes the same execution:

```text
cyberagent run specialist-step <run-id> --agent <child-id> --operation-key <stable-key> --tools
cyberagent run delegation schedule <proposal-id> --operation-key <stable-key> --tools
```

The parent Run must be running for new execution. Each child retains its own
Agent, Session, turn/token limits and model accounting, and shares the Run tool
budget. One turn supports at most 16 tool rounds, with up to four calls per round.
Project exclusions come from the saved Run configuration. Ordinary child tools
are for observation and review; use the Batch flow below for edits and validation.
Schema v190 records tool bindings, rounds and receipts while preserving existing
model and protocol-repair history.

### Deliverable child batches

Schema v118 adds a separate `batch-delivery.v1` path for an operator-approved and
admitted core child-task proposal. It does not grant tools to the existing no-tool
Specialist scheduler. A confirmed preparation creates at most two independent child
branches/worktrees and returns each narrowed owner token once. The token is not stored
by Desktop or recoverable from SQLite; if it is lost, rotate the exact expected
generation from the child-delivery panel and hand the replacement to the trusted child
worker.

The current Desktop/API Batch worker uses the native child tool loop to discover
and read files in its isolated worktree; it no longer depends on four preselected
small file snapshots. It applies owned changes, commits, and runs the declared
checks through `verify`. Submission reuses the successful recorded checks for the
same final clean commit and Batch generation. A changed commit or missing receipt
uses the existing validation path. Final review and merge remain explicit actions.

The batch spec must copy each admitted task's ordinal, dependencies, turn/token/timeout
budget, and expected artifacts exactly, then add non-overlapping file/directory
ownership and validations. `git_diff_check` is mandatory. The narrowed worker may
list/read/search, propose and apply create/replace changes, inspect Git, and create one
fixed local commit only inside its owned Scope. It cannot delete/rename, invoke Shell or
an arbitrary process, use network/credentials/Debug/approval, or spawn another child.

Use the Desktop Run tab **Child tasks & deliveries** to inspect mailbox progress,
receipt hashes, test receipts, limitations, review, and merge state. Before accepting a
delivery, inspect the exact local child branch with Git and independently review its
complete merge-base diff and call chain; enter a summary and check the explicit full
review attestation. The author summary alone is not evidence. If source base has moved,
the first merge attempt stops; inspect the new base, then explicitly confirm replay.
Text/semantic conflict, overlap, or test failure blocks the queue and preserves the
child worktrees.

By default only `git_diff_check` is admitted. To declare `go_test` or `npm_test`, the API
operator must explicitly accept host code execution, and the bound Run must still be
running with modern Full and live process activation whenever a check starts:

```powershell
$env:CYBERAGENT_API_CONTROL_TOKEN = "<different-random-control-token>"
cyberagent api serve `
  --enable-permission-control `
  --enable-danger-full-access `
  --enable-batch-validation-execution
```

For Desktop, batch mutation is an independent capability: add
`--enable-batch-delivery-control`; executable checks additionally require the three flags
above. Merely enabling an unrelated Desktop control surface does not enable batch
Prepare/Review/Merge/Cancel/Reconcile.

The test process receives a fixed offline and credential-stripped environment, bypasses
the Go test cache, and uses a Windows Job Object or Unix inherited process group for
lifecycle termination. Only complete-stream output digests are persisted. It still runs
child-authored code on the host and is not OS-sandboxed; deliberate POSIX daemonization
outside the inherited group remains a host-execution residual. Leave executable validations
out of untrusted batches until a separately contained validation backend is available.
See [Deliverable Multi-Agent Batches](batch-delivery.md) and
[ADR 0119](adr/0119-deliverable-batch-agents.md).

## Script Mode

```powershell
cyberagent script new "parse pcap http token" --workspace demo
cyberagent script run scripts/<script-name>.py --workspace demo
cyberagent script run scripts/<script-name>.py --workspace demo --local --flag value
cyberagent script run scripts/<script-name>.py --workspace demo --idempotency-key <stable-key>
```

`script new` prints both the absolute artifact path and `script_relative`; pass the latter to `script run`. `script run` never executes a Sandbox or host process. It requires a workspace-relative existing file, rejects absolute paths/traversal/symlink escape, and atomically persists a Script Profile Mission/Run/Session, initial tool-budget charge, Policy decision, typed Process, Approval, and Run events. The `script_process.v1` payload contains executable, argv, workspace-root working directory, requested backend, and the fixed execution mode `disabled`.

`--idempotency-key` is optional but recommended for retryable clients. Repeating the same key and intent returns the original Mission/Run/Session/Process without a second budget charge or duplicate events. Reusing it with changed path, arguments, backend, scope, budget, or requester returns conflict exit code 4. Only a SHA-256 digest is stored. `--local` records intent for future Sandbox work; it does not execute locally. Use the printed Process ID with `tool show`, `tool approve`, or `tool deny`; approval completes as dry-run only.

## CTF Compatibility Scaffold (Optional Add-on)

> The commands below are retained for compatibility with the original scaffold. They do not provide automated CTF solving, scanning, exploitation, browser attack tooling, or host execution. CTF-specific development is currently deferred; future capabilities must arrive as separately reviewed add-ons through the generic Tool, Skill, Analyzer, Sandbox, and Report interfaces described in [Product Scope](PRODUCT_SCOPE.md).

```powershell
cyberagent ctf init baby-web --category web
cyberagent ctf analyze baby-web
cyberagent ctf writeup baby-web
```

## Model and Provider Commands

```powershell
cyberagent provider list
cyberagent provider test
cyberagent provider test mimo/mimo-v2.5-pro
cyberagent provider test deepseek/deepseek-v4-flash
cyberagent provider test openai/gpt-4.1-mini
cyberagent provider test ollama/llama3.2:3b
cyberagent provider qualify script
cyberagent provider qualify mimo/mimo-v2.5-pro
cyberagent provider qualify openai/gpt-4.1-mini
cyberagent provider qualify ollama/llama3.2:3b
cyberagent provider price-list
cyberagent provider price-import --file configs/prices.example.json
cyberagent model list
cyberagent model set script mock/mock-code
```

`provider test` accepts either a route name, such as `learn`, or a direct `provider/model` reference. It is an explicit online connectivity diagnostic: each invocation may send one small, content-free, tool-disabled model request with a 15-second deadline and may incur Provider charges. Passing it does not qualify streamed ToolCall, ToolResult, or strict-JSON behavior. Its safe `failure_reason` distinguishes `not_configured`, `authentication`, `network`, `rate_limit`, `capacity`, `model_not_found`, and `protocol_incompatible` without returning raw Provider errors.

`provider qualify` accepts the same route or direct reference and performs the separate `model_harness_qualification.v1` contract. Built-in Mock returns immediately with zero model calls. An external model may receive at most two bounded requests under a 30-second deadline: one exact synthetic nonce ToolCall, then one exact strict-JSON acknowledgement after a synthetic ToolResult. Traverse Board never dispatches the synthetic Tool. Successful qualification is bound to the exact Provider/model/base-URL/strategy for seven days; a configuration change or expiry fails closed. The command prints status, counts, protocol strategy, and readiness only. It never prints prompts, response content, Tool arguments, API keys, endpoints, environment-variable names, or raw errors.

`model set` validates and persists the route before changing the in-memory Router, so a failed SQLite write cannot create a process-only route. Route selection does not automatically qualify the model. Root, Specialist, and read-only Fan-out use one Go-owned Harness preflight immediately before every Provider call; external models that have not passed the exact qualification are rejected before normal Agent execution.

`provider price-import --file <path>` atomically installs one operator-authored `price_snapshot.v1` document as the active price table; `provider price-list` prints the import history newest first. Prices are operator configuration, never model output: a Provider response, README, Skill, or repository file can never reach the import surface. A same-content import replays idempotently, a new import rotates the active table, and an invalid or unknown-field document is rejected without changing state. A document whose fingerprint matches an existing snapshot but whose validity window differs is rejected; changing the validity window requires a new snapshot id. The document bounds are 64 KiB, 512 unique provider/model entries, RFC3339 validity that must cover the import time and last at most one year, and integer micro-USD prices (1 USD = 1,000,000 micros). [`configs/prices.example.json`](../configs/prices.example.json) is a documentation-only example.

Model receipts normalize `input_tokens` as the complete logical input. `cache_read_tokens` and `cache_creation_tokens` are disjoint subsets: Anthropic's uncached input is combined with its cache read/creation counters once, while OpenAI's inclusive input is not increased by its cache details. `cache_usage_reported` distinguishes absent details from an explicitly reported zero; `cache_creation_breakdown_known` distinguishes an exact 5-minute/1-hour creation split from an unknown TTL. Root, Specialist, failed calls with known usage, and context-compaction receipts retain these fields in the existing event ledger. Read-only fan-out terminal receipts retain their full usage too.

Price entries accept optional `cache_read_per_million_micros`, `cache_creation_per_million_micros`, `cache_creation_5m_per_million_micros`, and `cache_creation_1h_per_million_micros`. Zero means explicitly free; an omitted category price remains unknown. Exact TTL usage requires the corresponding TTL price; aggregate creation with unknown TTL requires the generic creation price. Reservation uses the highest configured input-category rate, and both live settlement and restart reconciliation use the snapshot pinned to the call. Missing cache prices retain the reservation as an estimate with `cache_price_missing` rather than releasing it as zero cost. Existing snapshots and receipts remain readable; previously omitted cache details cannot be reconstructed.

The existing Run overview displays recorded input/output and cache categories plus the local monetary estimate, open reservations, remaining budget, and unpriced-call count. Costs are recorded only for Runs with an enabled monetary budget. These figures support the local budget ledger; use the provider's bill to confirm actual charges.

The optional `mimo`, `deepseek`, `anthropic`, and `openai` Providers load credentials from the process environment first and may then use the Go-owned system credential store. OpenAI-compatible configuration uses `CYBERAGENT_OPENAI_API_KEY`, `CYBERAGENT_OPENAI_BASE_URL`, `CYBERAGENT_OPENAI_MODEL`, and the optional `CYBERAGENT_OPENAI_TIMEOUT_SECONDS`; the base URL and model default to `https://api.openai.com` and `gpt-4.1-mini`. Its strict Chat Completions profile defaults to a 60-second total HTTP deadline, follows no redirects, sends only `Authorization`, `Content-Type`, and `Accept`, and accepts no caller- or repository-supplied custom headers or compatibility fields. The OS credential name is `openai`. Do not use generic `OPENAI_*` variables for this adapter.

### Provider request total timeout

Custom Providers use the existing Advanced JSON editor/import path in Desktop,
HTTP Provider definition control, and CLI Provider definition files. Set the
top-level `request_timeout_seconds` to an integer from **1 through 1800**:

```json
{"request_timeout_seconds":120}
```

This is local HTTP-client policy; it is not placed inside `request_body` or sent
to the upstream service. The saved definition includes the configured value;
an absent field means the effective default of **60 seconds**. Zero, negative,
fractional, null, string, overflowing, and out-of-range values are rejected
before the definition is saved. Use the canonical lowercase field name.
Existing Provider definition readback shows the value, and the normal Registry
reload applies an updated definition to subsequent requests. Active requests
keep the client captured at their start.

Built-in Providers use their existing process-environment configuration:

| Provider | Optional total-timeout variable |
|---|---|
| `mimo` | `MIMO_TIMEOUT_SECONDS` |
| `deepseek` | `DEEPSEEK_TIMEOUT_SECONDS` |
| `anthropic` | `CYBERAGENT_ANTHROPIC_TIMEOUT_SECONDS` |
| `openai` | `CYBERAGENT_OPENAI_TIMEOUT_SECONDS` |
| `ollama` | `CYBERAGENT_OLLAMA_TIMEOUT_SECONDS` |

These variables have the same 1–1800 second range and 60 second default. An
explicit empty or invalid value marks only that Provider's configuration
invalid; it never selects unlimited waiting or silently falls back to 60.
Set the variable before launching CLI, API, or Desktop, or before a normal
Registry reload in an already-running host whose process environment changed.
Neither repository YAML nor a model response supplies this configuration.

The effective deadline is the earliest of this request total timeout, the
caller's deadline, and the Run's remaining time budget. Stop still cancels an
active request immediately. The total includes connection setup, response-header
waiting, and all response-body reads, including both continuously flowing and
stalled streams; receiving another delta does not restart the timer. Separate
connection/header limits and an independent streaming idle timeout are not
configurable in this phase. Diagnostics and qualification keep their existing
shorter caller deadlines (15 and 30 seconds).

The 1800 second ceiling is a local resource bound, allowing a deliberately long
generation while keeping one request finite; it is not a Provider protocol
limit or an extension of the Run budget. Existing retry limits, error privacy,
known/unknown-cost accounting, and rejection of partial tools remain in force.
For the real HTTP regression, run
`go test -count=1 -timeout 3m ./internal/modelregistry -run '^TestConfiguredProviderStreamCompletesBeyondOneMinute$'`.
It sends local, synthetic output for 65 seconds across Chat, Responses,
Anthropic, and Ollama; `-short` skips this long test for focused checks.

Windows stores exact supported keys in Credential Manager; non-Windows has no plaintext-file fallback. Credential status and mutation never return plaintext. Desktop and API control planes atomically reload a new Registry generation after a successful change; a host without the reload dependency reports `restart_required: true`. Base URLs must be absolute HTTPS URLs unless they target an exact loopback host over HTTP; embedded credentials, query strings, fragments, and redirects are rejected. API keys are bounded normalized UTF-8 without whitespace or control characters. [`../configs/models.yaml`](../configs/models.yaml) is a documentation-only, non-secret example and is not loaded at runtime; repository or Workspace content cannot configure a Provider endpoint, header, or key.

For an opt-in live smoke, set the three `CYBERAGENT_OPENAI_*` variables in the current process, then use the exact configured model reference. With the default model, run `cyberagent provider test openai/gpt-4.1-mini` followed by `cyberagent provider qualify openai/gpt-4.1-mini`; if `CYBERAGENT_OPENAI_MODEL` has another value, replace `gpt-4.1-mini` in both commands with that exact value. These commands make real, potentially billable network calls and are never part of the default test suite. Use a local mock endpoint for automated smoke tests. Neither command prints the API key, configured endpoint, prompts, response text, Tool arguments, raw deltas, or raw errors; unset the key after the manual check.

The optional keyless `ollama` Provider connects only to an explicitly configured loopback endpoint. Configuration is `CYBERAGENT_OLLAMA_BASE_URL` (loopback `http` only; the default is `http://127.0.0.1:11434`) plus `CYBERAGENT_OLLAMA_MODEL`, the exact model used for route selection; neither variable has an implicit enablement, so the provider stays `not_configured` and `not_configured` without both. Non-loopback hosts, HTTPS, URL credentials, queries, fragments, path prefixes, redirects, and proxy-bearing transports are rejected. The provider uses the native `/api/tags` model list, `/api/chat` (including NDJSON streaming), and `/api/show` capability probe; tools, vision, JSON, and context-window capabilities stay unknown — and therefore unsupported — until the daemon reports them, and a no-tool model never receives tool schemas. Prompt/response usage falls back to a conservative character-based estimate when the daemon omits token counts. Traverse Board never installs Ollama, pulls a model, scans the LAN, or modifies a system service; an unreachable daemon surfaces the stable `network` diagnostic with the explainable "service is unreachable" message.

For an opt-in local smoke: start Ollama, `ollama pull llama3.2:3b`, set `CYBERAGENT_OLLAMA_BASE_URL=http://127.0.0.1:11434` and `CYBERAGENT_OLLAMA_MODEL=llama3.2:3b`, then run `cyberagent model set code ollama/llama3.2:3b`, `cyberagent provider test ollama/llama3.2:3b`, and `cyberagent provider qualify ollama/llama3.2:3b`. The capability probe runs automatically before route selection, qualification, and diagnostics. A local model is a deployment location, not a trust grant: output still passes through Policy, redaction, budgets, and the Tool Gateway.

## Durable Run Wake Intent

```powershell
cyberagent run wake schedule <run-id> --operation-key <stable-key>
cyberagent run wake show <run-id>
cyberagent run wake cancel <run-id> --operation-key <stable-cancel-key>
cyberagent run wake consume <run-id> --max-steps 1
```

`run wake schedule` persists bounded retry intent with at most eight attempts, bounded delay/backoff, and a fixed deadline. Schedule and cancellation are digest-idempotent. Schema v74 also generation-fences one internal owner, but public output omits lease identity. These commands do not start a background loop, resume a Run, acquire a Run execution lease, call a model/tool, or execute a process.

Schema v75 `run wake consume` is the separately gated foreground consumer. It handles at most one due intent and passes `--max-steps 1..8` only to the existing durable handoff and RunSupervisor, which keep Policy, budgets, cancellation, checkpoints, model/tool ledgers, and the private execution lease authoritative. D1-J1 may automate this same path only when `api serve` or Desktop starts with `--enable-wake-worker` and a distinct control token. The worker is serial, polls within hard bounds, and always uses `max_steps=1`; no HTTP/UI route can enable it at runtime. It is not a service/startup task and has no Shell/Local/Docker dependency.

## TUI

```powershell
cyberagent tui
cyberagent tui --workspace demo --title "Agent basics" --route learn
cyberagent tui --run <run-id>
cyberagent tui --run <run-id> --print
cyberagent tui --session <session-id>
cyberagent tui --session <session-id> --print
```

Without `--run` or `--session`, the TUI opens a Run-first picker backed by bounded Store pages. `Tab` or `h/l` switches between the latest 50 Runs and latest 50 Sessions. Press `Enter` to open the selected item, `n` to create and open a new Session, `j/k` to move, `r` to refresh, and `q` or `Esc` to quit. `--run` and `--session` are mutually exclusive; an exact Run open rechecks that its persisted Session resolves back to the same Run projection.

The chat TUI uses the same session and tool approval runtime as the CLI. Normal text sends a session message. Slash commands such as `/run echo hello`, `/model script`, and `/compact` go through the session manager. Tool approvals can be handled in the input box:

```text
/approve <tool-run-id>
/approve-session <tool-run-id>
/deny <tool-run-id> not needed
```

Keyboard controls:

```text
Tab              switch focus between input and the activity pane
Enter            send, approve a selected Tool, or open/close an Edit diff
PgUp / PgDn      scroll messages or an open diff
h / l            switch among Tools, Plan, Work, Notes, Rounds, Events, Agents, Findings, and Edits
j / k            select the next/previous item in the active view
a                approve the selected Shell proposal once
g                approve it and grant the exact current Session scope
d                deny the selected Shell proposal
Ctrl+R           refresh Session, Run, memory, and tool state
Ctrl+X           request audited cancellation of the current model call
Esc              return from diff detail, otherwise quit when idle
```

`--print` renders one snapshot and exits, which is useful for non-interactive verification.

Message sends, live-call discovery, refreshes, cancellation, and tool approval/deny actions run asynchronously. During a Run-bound model call, the status line shows provider/model, attempt, chunk/byte progress, cancellation, slow-consumer disconnect, and terminal state. `Ctrl+X` prefers the application audit-first cancellation API; if a legacy or not-yet-active request has no registry entry, it cancels the current application request context instead. Additional input is held until the current action finishes, and raw model text is never included in the live envelope.

For a Run-bound Session, the activity pane reads Plan/Delivery state, WorkItems, Notes, durable Supervisor ToolRounds, recent Run Events, Agent nodes/completions, bounded Finding-report summaries, active Shell grants, ToolRuns, and FileEdit previews from the Go Store. Plan shows the three directions, any selected direction and projected WorkItem count, and whether an explicit Deliver transition is still needed. Plan, Work, Notes, Rounds, Events, Agents, Findings, and Edits are read-only views; approval keys act only in Tools. Edits loads at most the latest 20 exact-Session/Workspace records through a metadata/diff-only SQL query that excludes `original_text` and `proposed_text`. `Enter` opens a full-screen read-only diff; `j/k` or `PgUp/PgDn` scrolls and `Enter`, `b`, or `Esc` returns. Displayed diff data is valid UTF-8, terminal-control neutralized, and capped at 128 KiB/4096 lines even though the durable proposal remains unchanged. `a` uses the existing durable per-call decision, while `g` creates or reuses a revocable Grant scoped to the exact Run, Session, Workspace, `shell` tool, and `shell` ActionClass. Keyboard and slash-command approval paths both reject ToolRun IDs outside the currently open Session. The current proposal is matched against its persisted fingerprint and rechecked by Policy before the Grant is created. Later allowed Shell calls may complete automatically as dry runs; Policy denial always wins.

The interactive TUI polls only the local Store and never starts a Run by polling. It reads at most 32 new events per batch, keeps the most recent 50 in the panel, validates contiguous sequence plus exact Run/Mission binding, and refreshes the composite Session/tool/Run/FileEdit projection only when durable events arrive. Each refresh compares the event tail before and after all reads and retries up to eight times if a concurrent transaction lands in the middle. A stale asynchronous result cannot overwrite a newer manual/action refresh; a terminal Run stops polling. Event payloads are not rendered, Finding details and Evidence remain on the existing CLI/Web detail surfaces, and all displayed C0/DEL terminal controls are converted to visible text.

TUI text layout uses terminal-cell-aware grapheme wrapping, so wide Unicode text does not break panel boundaries.

When a session has an attached workspace, the TUI side panel shows workspace identity, root path, and lightweight counts for `attachments`, `scripts`, `outputs`, `logs`, and `writeups`. This is metadata only; the panel does not read file contents.

## Run-local Session diagnostics / Run 内 Session 诊断

```powershell
cyberagent session create --workspace demo --title "Agent basics" --route learn
cyberagent session list
cyberagent session send <session-id> "summarize your current capabilities"
cyberagent session send <run-session-id> "queue this exactly once" --operation-key <stable-key>
cyberagent session send <session-id> "/help"
cyberagent session send <session-id> "/model script"
cyberagent session send <session-id> "/model mimo/mimo-v2.5-pro"
cyberagent session send <session-id> "/model deepseek/deepseek-v4-flash"
cyberagent session send <session-id> "/compact"
cyberagent session send <session-id> "/ls ."
cyberagent session send <session-id> "/read README.md"
cyberagent session send <session-id> "/write README.md # Proposed replacement"
cyberagent session send <session-id> "/run echo hello"
cyberagent session history <session-id>
cyberagent session history <session-id> --all
```

Thread history and its Run composer are the primary path for generic AI agent features. The `session` command is an advanced diagnostic and compatibility path for one Run-local context/authority boundary. Ordinary text in a Run-bound Session is supervised and consumes that Run's turn, token, and model-time budgets. Older Sessions created directly with `session create` have no Run and temporarily retain the legacy direct Router path for compatibility. Slash commands remain explicit command paths, but `/ls`, `/read`, `/write`, and `/run` now enter the same Go Tool Gateway used by the CLI and TUI and consume the Run tool-call budget. Workspace commands require an attached workspace; `/read` responses are bounded and redacted before persistence or model use. `/write` and `/run` normally create reviewable proposals. A matching active Run-local Session grant may apply a file edit immediately or complete Shell as a dry run; Shell is never executed on the host.

## Unified Tool Gateway

The Gateway validates exact argument schemas, binds calls to a Run/Session/Workspace scope, atomically charges the Run tool-call budget, runs policy checks, selects an approval mode, and normalizes execution results. `read_file`, `list_workspace`, `work_item_create`, and `note_create` use automatic approval only when scope and policy allow them. `replace_file`, `shell`, and `script_process` normally use per-call approval. A policy denial is terminal and cannot be converted into approval by a grant or later review. Schema v11 persists each per-call decision with a request fingerprint, Run/Session association, and immutable idempotency operation before the compatibility proposal advances. Schema v12 persists revocable Session grants scoped to one Run, Session, Workspace, Tool, and ActionClass; terminal Runs and archived Sessions cannot create or consume them. Schema v13 stores typed script processes and makes initial Script Run creation atomic and recoverably idempotent. Schema v14 stores source-bound tool-output Artifacts and projects metadata-only creation events. Schema v15 stores idempotent create-only structured-memory mutations without raw operation keys or content-bearing tool events.

New CLI Runs default to 100 tool calls and may set `--max-tool-calls`; zero means unlimited for compatibility. Every valid Run-bound Gateway invocation consumes one call, including Policy-denied attempts. The first attempted call beyond the limit records one `tool.budget_exhausted` event, subsequent attempts return `RESOURCE_EXHAUSTED` without duplicating that event, and `run usage <run-id>` shows consumed, limit, remaining, and exhaustion time.

Runs created with `--max-cost-usd` (or a positive `MaxCostUSD` budget) also report `monetary_*` fields in `run usage`: `monetary_cap_usd`, `monetary_reserved_usd`, `monetary_settled_usd`, `monetary_released_usd`, `monetary_remaining_usd`, and `monetary_estimate_source`. The monetary ledger is integer micro-USD with reserve-before-call, settle-or-release-after-call semantics; an active operator price table must carry an exact entry for every tracked provider/model or the call fails closed. Open reservations self-heal against terminal `model.*` events and are force-released when the Run becomes terminal. Runs without a positive monetary cap stay `monetary_tracked: false`.

Text output is valid UTF-8, secret-redacted, MIME-labelled, and bounded to 128 KiB stdout, 32 KiB stderr, and 64 KiB proposal previews. Truncation is explicit. Before this Result projection, each non-empty Run-bound output stream is captured as at most 4 MiB of redacted Artifact content with SHA-256, byte size, MIME, encoding, source proposal/invocation, and Run scope. Hashes cover the redacted content. An explicit `read_file` maximum still bounds what the tool returns and therefore what its Artifact contains. No production CLI path invokes LocalSandbox; script-process proposals remain deliberately non-executable.

## File Edit Proposals

```powershell
cyberagent edit propose --workspace demo --path README.md --content "# Updated"
cyberagent edit propose --workspace demo --path scripts/main.go --content-file C:\temp\main.go
cyberagent edit list --workspace demo
cyberagent edit list --session <session-id> --status proposed
cyberagent edit show <edit-id>
cyberagent edit review-approve <run-id> <edit-id>
cyberagent edit review-deny <run-id> <edit-id>
cyberagent edit apply <run-id> <edit-id> --operation-key <stable-key>
cyberagent edit approve <legacy-non-run-edit-id>
cyberagent edit deny <edit-id> --reason "not needed"
```

File edits replace the complete text content of one file. Existing files and new files under an existing workspace directory are supported. Absolute paths, `..` traversal, directory targets, symlink escapes, non-UTF-8 content, missing parent directories, and content over 256 KiB are rejected.

Proposals are stored without modifying the workspace. `review-approve` and `review-deny` exact-bind the Run, Mission, Session, Workspace, proposal, and durable approval. `review-approve` records approval intent only and prints `file_written: false`; it never changes the workspace. The Desktop/Web Diff surface uses this review-only path and receives bounded redacted diff metadata without original/proposed file bodies.

D1-I1 adds the interactive proposal path used by Desktop/Web. With
`--enable-file-edit-proposals`, Go issues complete, untruncated, unredacted UTF-8 for
one exact running Run/active Session/Workspace behind a five-minute opaque handle. The
locally bundled Monaco editor receives no host path and submits only that handle plus
replacement text. Go rechecks the current hash, bindings, secret policy, and
`replace_file` Policy before creating a pending proposal. The editor cannot review,
approve, apply, or directly write the file.

Schema v76 `edit apply` is the separately authorized Run-bound write path. It reloads the exact Run/Mission/Session/Workspace/Edit/Approval, rechecks current Policy, resolves the target inside the stored Workspace, compares the current SHA-256 with the proposal's original hash, writes once, verifies the proposed digest, and persists an idempotent result. A replay reports `file_written: false` and does not write again. HTTP/React submit neither path nor file body. Run-bound edits cannot use the older `edit approve` command; that compatibility command remains only for proposals that are not bound to a Run. Proposed secrets are replaced with redaction markers before persistence and before any approved write. For exact multiline or whitespace-sensitive content, prefer `--content-file`; session `/write` trims the outer message whitespace.

D1-U1 adds a content-free `operation_receipt.v1` to HTTP/Desktop apply, foreground wake
consume, and inert Skill install results. It tells the operator whether the durable
result was replayed and which exact retry strategy is allowed. For FileEdit only,
`pending_review` means an uncertain internal staging candidate was retained; retry the
same operation key after the grace period rather than creating a second intent. The
receipt never includes the key, digest, path, body, or private lease.

## Tool Proposals

```powershell
cyberagent tool list
cyberagent tool list --session <session-id>
cyberagent tool list --status proposed
cyberagent tool show <proposal-id>
cyberagent tool approve <proposal-id>
cyberagent tool deny <proposal-id> --reason "not needed"
```

`tool list` combines legacy Shell ToolRuns and typed ScriptProcess proposals and sorts them by update time. `tool show/approve/deny` resolves the proposal type from the durable approval ledger, so callers do not select an implementation-specific manager. `/run` creates a Shell `tool_runs` proposal; `script run` creates a v13 `script_process_proposals` record. A matching active Shell Session grant can authorize Shell automatically, but Shell and ScriptProcess completion remain dry-run. File edits continue to use `edit show/approve/deny`. Terminal tool detail and approval output include associated Artifact IDs. Real command execution stays disabled until Sandbox isolation, network/resource policy, cancellation, and execution-specific evidence export pass a separate audit.

## Run Artifacts

```powershell
cyberagent artifact list --run <run-id>
cyberagent artifact list --source <proposal-or-invocation-id> --stream stdout
cyberagent artifact show <artifact-id>
cyberagent artifact read <artifact-id> --max-bytes 65536
cyberagent artifact verify <artifact-id>
```

`artifact list` and `artifact show` expose metadata without printing content. `artifact read` loads and verifies the stored size and SHA-256 first, then prints at most the requested UTF-8-safe byte limit; its default is 64 KiB and maximum is 4 MiB. `artifact verify` reloads the blob and reports the verified digest and size. Capture is idempotent by Run, source, and stream. Reusing a source with changed content or MIME is a conflict, and a Policy-denied proposal creates no output Artifact.

## Approval Ledger

```powershell
cyberagent approval list --run <run-id> --status pending
cyberagent approval list --session <session-id> --tool shell
cyberagent approval show <approval-id>
cyberagent approval grant create --session <session-id> --tool shell --reason "trusted build commands"
cyberagent approval grant create --session <session-id> --tool replace_file --reason "bounded refactor"
cyberagent approval grant list --run <run-id> --status active
cyberagent approval grant show <grant-id>
cyberagent approval grant revoke <grant-id> --reason "phase complete"
cyberagent run usage <run-id>
```

The approval ledger stores identity, scope, mode, status, reviewer metadata, an optional Session grant ID, and a SHA-256 request fingerprint rather than duplicating command or file content. `approval.requested` is committed with the proposal. `approval.decided` and a domain-separated SHA-256 digest of the immutable review key are committed before ToolRun/FileEdit progression, so rerunning the same CLI approval after a process interruption resumes safely without persisting the raw client key. Grant create/revoke operations use separate domain-separated key digests and append `approval.grant_created` or `approval.grant_revoked`. A key cannot be reused for different intent, a revoked grant cannot authorize a new proposal, and a grant never overrides Policy.

### Bounded command review and historical escalation

New commands use `command_runtime` with an optional `review_scope`. Inspect the
exact command and risk scope before making the ordinary ApprovalControl decision.
An `approve_for_run` decision requires explicit TTL and use limits; each new
exact command still requires a decision. See [Bounded command approval](convergence/command-bounded-approval.md).

`host_command_propose` and `risk_escalation.v1` are historical records only. Their
saved command, decision, scope, receipt and uncertainty remain visible. They
cannot create a new grant or execution. A saved outcome may continue only its
original Run/call; an unknown intent is never retried. See
[ADR 0165](adr/0165-retire-legacy-command-execution.md).

## Context Compaction

```powershell
cyberagent context compact --workspace demo --task task-demo --message "user: imported challenge" --message "assistant: summarized plan"
cyberagent context show --task task-demo
```

`context compact` is the manual v0.1 version of a Codex-style compaction step. It stores a summary in SQLite and reports how many recent messages remain outside the summary.

Run-scoped WorkItems and Notes are independent from Session compaction, so compacting or replacing conversation history does not remove structured plan or memory records. The Supervisor's token-aware memory selector combines the latest summary with those durable sources before each Run model call.

## Project Config (.prayu)

`cyberagent run create` automatically loads `<workspace>/.prayu/config.yaml` and applies it as a narrowing-only snapshot (skip with `--ignore-project-config`):

```powershell
cyberagent project-config validate <path-to-config.yaml>
cyberagent project-config show <path-to-config.yaml> --profile code --max-turns 100 --max-tool-calls 100
```

The file uses `project_config.v1` (JSON schema in `configs/project-config.schema.json`, example in `configs/project-config.example.yaml`). It is untrusted input: it can only reduce the operator/process/policy limits, never widen them. `budget.max_turns`/`budget.max_tool_calls` must be strictly lower than the requested ceiling; `allowed_profiles` may only shrink the set; `read_only: true` forbids write-capable profiles (`code`/`script`); `exclude_paths` are workspace-relative paths without escapes; `skill_suggestions` use the signed Skill identity contract (`name@version`); `test_command_id`/`format_command_id` may only reference registered Tool Gateway typed action IDs — never Shell text. Unknown fields, duplicate keys, type errors, YAML aliases/anchors, files over 64 KiB, nesting deeper than 32, more than 4096 nodes, symlinks/junctions/reparse points, and oversized path lists all fail closed. Any widening attempt aborts Run creation with the offending field named.

At Run creation the normalized effective view and its SHA-256 fingerprint are pinned into the Run config snapshot; editing `.prayu/config.yaml` afterwards never changes a running Run.

`exclude_paths` applies to file-tool reads/searches/edits, Batch files and CodeIntel
evidence using root-relative `*`, `?`, character classes and whole `**` directory
segments. Matching a directory excludes every descendant, even with hidden-file
access or `.gitignore` negation. This filter does not sandbox arbitrary commands
or language-server processes.

In new-task configuration, choose exact `skill_suggestions` refs and confirm their
context before sending. Built-ins must have the requested version; external
packages must already be installed and eligible. Selection uses the existing
Skill services and never installs a package or grants permission. Failed
selection preserves the created task and draft; recovery retries the same request
before the first message is dispatched.

Use `test_command_id: test.go` for `go test ./...`, `test_command_id: test.node`
for native `node --test`, and `format_command_id: format.go` for native `gofmt -w`
on one exact Go file. In **Changes and delivery → Checks and delivery**, select
an optional relative test directory or an exact Go file and run the configured
action. The panel shows the selected backend, live Job output when available,
and the actual terminal state and exit code. The action uses the current Host,
Windows Local Sandbox, or Standard Code Docker runtime; configuration never
changes permission or supplies executable/argv/shell text. The CLI uses the same
command execution service.

For npm scripts (including Vitest), use `test_command_id: test.npm`. It resolves
the installed npm CLI through native Node, with offline/audit/fund settings and
the runtime's fixed CI environment. The default package directory is the Run
workspace; `--path web` selects an allowed relative package directory. This is
distinct from Node's built-in test runner, and adds no Jest-specific flags.

Python projects can select `test_command_id: test.python` for
`python -m unittest discover -v`, or `test_command_id: test.pytest` for
`python -m pytest`. Rust projects can select `test_command_id: test.rust` for
`cargo test --offline`. Set the ID in `.prayu/config.yaml` before creating the
task; the same actions work from the checks panel, the `run_tests` tool, and
reviewed `before_finish_test` hooks. The optional test directory becomes the
command's working directory.

Python uses `python` on Windows, `python3` on Unix, and the fixed Python 3 path
inside Standard Code Docker. Cargo uses the selected environment's native
toolchain. Install the project's dependencies in that environment beforehand;
`test.pytest` requires pytest to be installed for that Python interpreter.
These test actions do not install packages or enable network access. Missing
tools or dependencies produce an unavailable action or the actual failed Job.
The current SBX template does not support these Python/Rust presets.

The same panel can detect supported project tests when no test action is pinned;
see the current support table in [Project preparation](project-preparation.md).
Use **Prepare project** first to review the lockfile, authorize exact
package sources, and install dependencies offline in the selected sandbox.
Downloads, install scripts, cache reuse, cancellation, and interrupted requests
are described in [Prepare project dependencies](project-preparation.md).

```powershell
cyberagent run project-action <run-id> test --prepare
cyberagent run project-action <run-id> test --path web --prepare
cyberagent run project-action <run-id> format --path pkg/example.go --prepare
cyberagent run project-action <run-id> test --operation-key project-test-001 --confirm-execution --confirm-full --confirm-non-sandboxed-host-execution --enable-permission-control --enable-danger-full-access
cyberagent run project-action <local-run-id> test --path pkg --operation-key project-test-local --confirm-execution --enable-permission-control --enable-workspace-sandbox
cyberagent run project-action <docker-run-id> format --path pkg/example.go --operation-key project-format-docker --confirm-execution --enable-permission-control --enable-workspace-sandbox --enable-docker-execution
```

`--prepare` prints an inert typed intent for the selected workspace backend.
Execution retains existing permission, approval and lease checks and produces
an ordinary command Job. Host execution retains its explicit acknowledgement;
owned workspaces use their sandbox without host fallback. Test directories and
format files are checked against the pinned excluded paths; read-only projects
cannot be formatted. An existing UI operation key reads its original Job even
after restart, without starting the action again. Docker needs the updated
[fixed image](standard-code-docker.md#fixed-image) for npm and gofmt. Tool IDs
without one of these presets do not become shell commands.

### Project command hooks / 项目自动动作

To format Go edits applied by the main Agent and run tests before it finishes its response,
declare the existing project actions in `.prayu/config.yaml`:

```yaml
protocol: project_config.v1
format_command_id: format.go
test_command_id: test.go
hooks:
  after_edit_format: true
  before_finish_test: true
```

在 **改动与交付 → 检查与交付 → 项目自动动作** 审阅并信任具体配置。同一工作区的后续
任务沿用该信任；修改启用事件、命令或路径限制后需要再次确认。可以在同一卡片停用。
任务创建时固定了项目配置；配置变化后，在新任务中使用重新审阅的配置。

`after_edit_format` 通过 `format.go` 处理主 Agent 成功应用的 FileEdit 中的 `.go` 文件；
`before_finish_test` 在任务工作区根目录运行配置的 `test.go`、`test.node`、`test.npm`、
`test.python`、`test.pytest` 或 `test.rust`。
测试失败会回到 Agent 的修复循环，尚未通过的版本不能直接报告完成。动作使用当前
命令后端、Full／Ask／Auto 规则及任务预算，并记录普通命令 Job；信任项目配置不增加
执行权限。审批后的恢复继续原调用，不重复运行已完成的动作。
当前自动动作沿用前台命令的 25 秒上限；超时会保留失败回执并阻止完成。
等待审批时停用自动动作后，可拒绝已有审批以继续任务；已经派发但缺少完成回执的
命令仍需先核对原 Job 的状态。

## Durable Context Continuity / 持久上下文连续性

Schema v114 adds hierarchical `AGENTS.md`/`CLAUDE.md`/`.prayu` instruction discovery,
operator-explicit user/project memory, and a browsable Session checkpoint/Fork/Resume tree.
项目指令在 Run 创建时固定；磁盘漂移只有在显示 diff 并以旧 fingerprint 显式确认后才会
追加新 revision。长期记忆由操作者确认写入，支持编辑、禁用、保留期、导出和不可恢复的
物理删除。Schema v198 从带来源的决策/总结笔记生成待确认经验：在 **Context → 当前项目
→ 待确认经验** 展开原文与来源，再确认保存或忽略；确认后沿用记忆的编辑与失效控件。
新任务可通过 `history_search` / `history_read` 取回同工作区的有效项目记忆；原始笔记
仍只在当前 Thread 合法 Run 沿革内可见。Checkpoint 只携带有界脱敏上下文；Fork/Resume
创建新的 Run/Session，永不继承审批、capability、凭据、网络权限、进程、终端/执行 lease
或 execution profile。

Use `cyberagent context instructions`, `cyberagent context memory ...`, and
`cyberagent session tree|checkpoint|fork|resume`; the Desktop **Context** tab exposes the same
source explanations, drift, memory lifecycle, tree, and branch comparison. Complete commands,
limits, threat model, privacy deletion semantics, and English/Chinese guidance are in
[Project Instructions, Long-Term Memory, And Session Continuity](context-continuity.md).

The project Context panel also reviews source-bound experience candidates. Pending candidates
remain outside later tasks until the operator confirms them. Configured memory is distinct from
actual delivery: `model.started` context audit records included memory versions/hashes and
explains omissions under the reserved budget. Replies and tool receipts show whether the model
followed that context. Edits and disabled/expired/deleted status are reloaded before the next logical
model call, including subsequent calls within the same tool loop.


## Remote Git and Pull Requests

`cyberagent git-remote` performs network-scoped remote Git and PR operations as typed, review-bound requests (schema v107 ledger):

```powershell
cyberagent git-remote fetch --run <run-id> --remote <https-url> --branch main --ttl 30s --operation-key <key> --confirm
cyberagent git-remote pull --run <run-id> --remote <https-url> --branch main --operation-key <key> --confirm
cyberagent git-remote push-branch --run <run-id> --remote <https-url> --branch feat --operation-key <key> --confirm
cyberagent git-remote create-pr --run <run-id> --remote <https-url> --branch feat --base main --title "feat" --credential github-pat --operation-key <key> --confirm
```

Operations are fetch / pull_ff (fast-forward only) / push_branch (new branches only — an existing remote branch is default-denied) / create_pr / update_pr; force push, remote branch deletion, and protected-branch mutation cannot be expressed. Only HTTPS remotes are accepted (loopback and credential-in-URL rejected); the network scope binds host/port/protocol/TTL/Run into the request fingerprint. Credentials are referenced by name only: the secret resolves from the system credential store, reaches git through a temporary askpass helper plus a child-process environment variable, and reaches the GitHub API only as an Authorization header — never in argv, logs, SQLite, Activity, OpenAPI, or model context (asserted by tests). Proxies, SSH ProxyCommand, and protocol wrappers are switched off; the PR API accepts only github.com with explainable 401/403 rate-limit/422/404 errors. Receipts are redacted and land in `git_remote_operations` plus a `git.remote_completed` Run event.


## Typed Git Mutations

`cyberagent git-op` performs local Git write operations as typed, review-bound mutations (never free-form git argv):

```powershell
cyberagent git-op stage --run <run-id> --path src/a.go --operation-key <key>          # prints review
cyberagent git-op stage --run <run-id> --path src/a.go --operation-key <key> --confirm
cyberagent git-op commit --run <run-id> --path src/a.go --message "add a" --operation-key <key> --confirm
cyberagent git-op create-branch --run <run-id> --branch work --operation-key <key> --confirm
cyberagent git-op switch-branch --run <run-id> --branch work --operation-key <key> --confirm
```

Operations are limited to stage/unstage/commit/create-branch/switch-branch; destructive or history-rewriting git commands cannot be expressed. Every execution binds to the exact reviewed repository state (HEAD + branch + index bytes + sorted status + untracked-content digests); any drift after review fails closed and demands a fresh review. Commits use pathspecs limited to the reviewed paths. The git binary runs with a fully replaced environment: system/global config ignored, hooks pointed at an empty directory, pager/editor/external diff/credential helpers/filters disabled, no agent environment inheritance. Receipts (commit id, conflict/clean flags, bounded stderr) land in `git_mutation_operations` (schema v106) plus a metadata-only `git.mutation_completed` Run event; operation keys make retries idempotent.


## Advanced Git / 高级 Git

Schema v123 adds the separate, default-off `git-advanced.v1` lifecycle. It does not widen
`git-op` and never accepts raw Git arguments. Start a CLI invocation with the exact active
Run, the two process gates, and optionally a dedicated managed-worktree root:

```powershell
cyberagent git-advanced status --run <run-id> `
  --enable-git-advanced --enable-permission-control --json

cyberagent git-advanced discover-hunks hunk_revert --run <run-id> `
  --path internal/example.go --enable-git-advanced --enable-permission-control --json

# Without --confirm this is a non-authorizing preview and creates no Approval.
cyberagent git-advanced run hunk_revert --run <run-id> --hunk <sha256> `
  --operation-key <stable-key> --enable-git-advanced --enable-permission-control

# --confirm re-renders the evidence, approves that exact proposal, checkpoints, and executes.
cyberagent git-advanced run hunk_revert --run <run-id> --hunk <sha256> `
  --operation-key <stable-key> --enable-git-advanced --enable-permission-control --confirm
```

`status` lists the current repository/conflict state, exact stash parent roles, active durable
sequence, redacted managed-worktree registry, authority generations, and immutable operations.
`discover-hunks` is read-only. `preview` renders any closed operation without creating an
Approval. `run` is also preview-only until `--confirm`; a confirmed invocation performs a
read-only in-process inspection, creates the exact proposal, then re-renders and compares that
same inspection before Approval and execution. The confirmed result prints the reviewed
preview together with the Approval and receipt; any drift inside the invocation is rejected.

The closed operation and flag mapping is:

| Operations | Typed flags |
|---|---|
| `hunk_stage`, `hunk_unstage`, `hunk_revert` | repeat `--path`; execute with repeat `--hunk <sha256>` from discovery |
| `stash_create` | `--message`, optional `--include-untracked`, `--keep-index` |
| `stash_apply`, `stash_pop` | `--stash <full-oid>`, optional `--restore-index` |
| `stash_drop` | `--stash <full-oid>` |
| `rebase_start` | `--upstream <full-oid> --onto <full-oid>` |
| rebase/cherry-pick continue, skip, abort | `--sequence <durable-id>` |
| `cherry_pick_start` | repeat `--commit <full-oid>`; merge commits are rejected |
| `bisect_start` | `--good <full-oid> --bad <full-oid>` |
| `bisect_good`, `bisect_bad`, `bisect_skip` | `--sequence ... --expected-current <full-oid>` |
| `bisect_run` | preceding flags plus `--recipe go_test|npm_test --max-steps N --timeout-seconds N` |
| `bisect_reset` | `--sequence <durable-id>` |
| `worktree_create` | `--worktree-name <safe-name> --branch <new-branch> --commit <full-oid>` |
| `worktree_lock` | `--worktree-id ... --worktree-name ... [--lock-reason ...]` |
| `worktree_unlock`, `worktree_remove` | `--worktree-id ... --worktree-name ...` |
| `worktree_prune` | no caller path; only exact registered missing entries are eligible |

Every mutation requires current Code/Local/Deliver and Ask/Auto/Full operation authority,
active Workspace execution lease, process capability generation, exact one-time Approval,
and Workspace Checkpoint. Repository/common-dir, HEAD/branch, raw index, worktree/status,
stash, sequencer, upstream, permission revision, and lease generation are rechecked immediately
before Git. Operation-key CAS ensures only one concurrent caller invokes Git. A process restart
terminalizes but never replays `running` work; exact paused sequences remain available through a
fresh continue/skip/abort, and a provably exact created worktree can be recovered into the
registry while the interrupted receipt still fails closed.

Desktop/API hosts must additionally compose all of Git-advanced control, permission control,
operator approval, Approval control, and Workspace Checkpoint control. The Desktop panel uses
the same service and never receives private lease IDs or managed host paths. Full hunk/stash/
sequence/worktree semantics, protection rules, recovery limitations, HTTP endpoints, and
examples are in [Advanced Git Workflows](git-advanced.md); the architecture decision is
[ADR 0122](adr/0122-go-owned-advanced-git-lifecycle.md).

## GitHub Review Provider / GitHub 审阅集成

Schema v124 adds a separate default-off GitHub App review workflow. Start it with `--enable-github-review` and permission control, configure an `owner/name` connection, complete Device Flow, qualify/fetch an exact PR, and bind the immutable snapshot to a Code Run before relying on it. Remote content remains untrusted and evidence states remain `verified`, `partial`, `stale`, `unavailable`, or `not_run`. Model tools are local read-only projections; remote write-back always requires a separate exact Approval. Commands, OpenAPI routes, minimum App permissions, recovery and real-smoke guidance are in [GitHub Review Provider](github-review.md); the decision is [ADR 0123](adr/0123-go-owned-github-review-provider.md).


## Debug Terminal and Time-Bound Agent Input

The persistent Debug terminal is user-owned by default and default-off at process startup. Enable the complete runtime boundary explicitly:

```powershell
.\build\desktop\TraverseBoard.exe `
  --enable-permission-control `
  --enable-danger-full-access `
  --enable-user-terminal
```

Windows uses PowerShell in a ConPTY assigned to a creation-time kill-on-close Job Object. macOS uses Bash with `--noprofile --norc` in a PTY and an owned process group. Ordinary POSIX background jobs share that group, but a command that deliberately creates a new session/daemon can escape group cleanup; The Debug interaction is a host terminal, not a permission tier or containment boundary. Each Run may have one active process-local terminal and one active Agent-input binding; the binding lasts 15 seconds to 15 minutes, is capped and immediately revocable, and is invalid after restart. The model tool is advertised only to the root Supervisor in Code/Deliver with modern Full, current process activation, the Debug interaction and a live terminal-input lease. It supports one complete policy-checked command line, one bounded cursor read, or `action: "interrupt"` to send Ctrl+C to the foreground program. Commands that Policy classifies as denied or requiring separate per-command approval do not reach the PTY. Grant records the current output watermark, and every later read is clamped to it, so pre-grant user scrollback cannot enter model context. Output pages are at most 64 KiB, explicitly marked untrusted, stripped of terminal controls, repaired to UTF-8, and secret-redacted.

Interrupt accepts no command or cursor and sends exactly one `0x03` byte without a newline. It uses the same live input lease, Run/Workspace binding, host Policy checks, operation-key deduplication, audit, and native authorization as command input. It leaves the persistent shell available when the foreground program handles Ctrl+C; a successful tool result confirms the byte was submitted, so read subsequent output to verify the program stopped. Ambiguous writes are never automatically retried.

The canonical model command and sanitized bounded post-grant result are stored in the resumable Supervisor tool transcript. Schema v113 transactionally widens that durable call ledger for `debug_terminal` while preserving earlier calls. The process-local binding also pins the exact mode revision and a canonical Workspace-root digest; a Plan round trip or Workspace re-registration invalidates it instead of reviving authority. The bearer, user keystrokes, raw PTY stream, pre-grant scrollback, root path, environment, and process identity are not persisted. Schema v108 defines a `terminal_sessions` metadata ledger contract, but the current Desktop lifecycle remains process-local and does not use that table to restore a process or authority; no database row can revive a terminal or lease. Agent-input grant/write/revoke events store only bounded identities, sizes, and digests.

## Ordinary Run-Owned Command Runtime / 普通模式 Run-owned 命令运行时

`command_runtime` gives the root Supervisor one adapter-neutral, bounded command
and Job protocol without granting Debug-terminal input. Go selects the adapter;
the request schema has no backend field. A `sandboxed_workspace` adapter requires
Code/Deliver/root, current Ask/Auto/Full operation authority, an active execution
lease, a Run-owned Drydock, and either ready Local (`local + controlled`) or fixed
Docker Standard Code (`docker`) isolation. A `host_unsandboxed` adapter requires
Code/Local/Deliver, the active lease, and both permission-control and
danger-full-access startup gates. Its Ask/Auto calls require exact durable approval;
Full requires current process activation. The direct-launch safe bundle (and the legacy
`--operator-preview` compatibility mode) intentionally does not install the host adapter.
`--safe-view` creates no control token and exposes only read projections.

```powershell
# Desktop: the Settings capability row will show “命令运行时 / Command runtime”.
.\build\desktop\TraverseBoard.exe `
  --enable-run-execution `
  --enable-permission-control `
  --enable-danger-full-access

# Loopback API host: a control token is also required for Run execution/control.
$env:CYBERAGENT_API_CONTROL_TOKEN = "<ephemeral-control-token>"
cyberagent api serve --enable-permission-control --enable-danger-full-access

# A bounded CLI invocation may host the runtime until that invocation exits.
cyberagent run step <run-id> --enable-permission-control --enable-danger-full-access --confirm-full
cyberagent run execute <run-id> --max-steps 3 `
  --enable-permission-control --enable-danger-full-access

```

普通命令运行时由 Run/Go manager 所有，不复用用户终端或 Debug terminal。启动闸门只
提供本进程 capability；数据库中的 旧权限快照或现代 `full` 偏好本身不能恢复执行权。普通
`cyberagent run step/execute` 默认不安装 runtime adapter；同时传入两项启动开关后，可在
该 CLI 进程内执行前台命令，并在同一次 `run execute` 的多个 turn 间维持 Job。CLI 退出会
把仍活动的 Job 明确终止为 `interrupted`；需要跨调用/断线续读时应使用 Desktop 或
`api serve` 的长生命周期 Supervisor。

The model-facing request is a strict tagged union. Shell profiles accept `script`
and reject `executable`/`arguments`; the process profile requires an absolute
non-workspace native executable plus literal `arguments` and rejects shells or
script interpreters. Every boundary field is explicit, including empty arrays:

```json
{
  "version": "command-runtime.v2",
  "action": "run",
  "failure_policy": "fail_fast",
  "max_bytes": 32768,
  "commands": [
    {
      "version": "command-runtime.v2",
      "profile": "powershell",
      "script": "Get-ChildItem -Force",
      "working_directory": ".",
      "environment": [],
      "stdin_policy": "closed",
      "close_initial_stdin": true,
      "timeout_milliseconds": 10000,
      "output": {"inline_bytes": 65536, "artifact_bytes": 4194304},
      "network": "disabled",
      "credentials": "none",
      "purpose": "inspect the workspace"
    }
  ]
}
```

`run` executes one to four commands in order; `fail_fast` stops after the first
non-success terminal state, while `continue` records every result. The foreground
timeout sum may not exceed 25 seconds. `start` creates one background Job. The
remaining actions are:

| Action | Required fields | Meaning |
|---|---|---|
| `list` | none | list up to 32 Jobs for the current Run |
| `read` | `job_id`, `cursor`, `max_bytes`, `wait_milliseconds=0` | read one immediately available page |
| `wait` | `job_id`, `cursor`, `max_bytes`, positive wait up to 5 seconds | long-poll until output, terminal state, or deadline |
| `write_stdin` | `job_id`, `stdin`, `close_stdin` | one bounded, secret-screened, operation-idempotent write through the currently bound host, Windows Local, or fixed Docker adapter; sandbox input remains process-local and is never recovered after restart |
| `cancel` | `job_id`, grace up to 5 seconds | request best-effort graceful tree termination, then kill (Windows Job termination is immediate) |
| `kill` | `job_id` | terminate the owned process tree immediately |

stdout and stderr share one monotonic byte cursor but each frame retains its stream
and timestamp. `base_cursor` plus `dropped=true` means the requested prefix left the
inline ring; it never means the page is complete. Every terminal result includes
exit state/code, observed byte counts, tree-reaped evidence, output hashes, and the
reason `inline_window` or `artifact_limit` when applicable. Sanitized terminal
output up to the declared 4 MiB-per-stream cap is committed through the existing
Run Artifact path; tool metadata returns Artifact IDs and SHA-256 values.

Schema v116 stores the canonical launch intent before process creation under the
current turn generation. A separate expiring process-owner heartbeat then permits
a later turn in the same host process to continue the Job without keeping
the turn lease open. Root/mode/profile/permission/Workspace-root drift kills live
owned Jobs. Windows uses a creation-time kill-on-close Job Object. POSIX uses an
owned process group plus a parent-pipe guardian (and Linux parent-death signal).
After crash, restart waits for the owner heartbeat to expire, records
`interrupted`, and never re-executes or signals a persisted, possibly reused PID.
Deliberate POSIX daemonization into a new session can escape the inherited process
group; it remains an unsandboxed Full residual risk and is never adopted
from the durable row. Schema v131 persists the complete adapter receipt on every
Job and projects pre-v131 rows as read-only, non-executable `legacy_unbound`
evidence. Sandbox Jobs persist no host PID/process group and are never adopted
after restart.

Only `network=disabled` and `credentials=none` exist in the model request. The host
adapter fixes or clears
Profile, HOME/USERPROFILE, Git helper/hook/config, SSH agent, prompt, proxy, and
loader-related paths; pins Git to file-only transport; applies immutable offline
defaults for Go/Cargo/npm/pip/uv; and rejects secret-like env/stdin and explicit network intent.
This is not packet-level host containment: `host_unsandboxed` truthfully reports
`host_available` network and credential policies. Full remains unsandboxed host
execution; they retain the host OS user token and cannot prove that credential files are
unreadable. Commands that need network/credentials or trigger per-command approval
use the same Command Runtime with an explicit `review_scope` and current host
authority; use Docker `network none` when actual
network containment evidence is required. Local/Docker receipts report denied
network and no credentials only when their independent isolation readiness is
current. See [Command Runtime adapter split](architecture/command-runtime-adapter-split.md)
and [ADR 0117](adr/0117-run-owned-command-runtime.md).

## Historical command records

Controlled, Host/Risk and Once proposal execution systems are retired.
`cyberagent run command-proposal list/show` and `cyberagent once-command proposals`
read retained history. The HTTP history views retain exact command envelopes,
reviews, saved receipts/output and uncertainty; they have no review/execute action.
Control-authenticated continuation consumes only eligible saved outcomes for the
original Run, never a new command or an automatically renewed approval.

For a new host command, use Command Runtime or `cyberagent run host-execute` with
the exact executable/arguments, stable operation key, host-risk confirmation,
permission-control and danger-full-access gates. Full also needs `--confirm-full`
for that CLI invocation. The command remains unsandboxed and retains the current
native pinning, ownership and cleanup checks. See
[ADR 0165](adr/0165-retire-legacy-command-execution.md).

## Scheduled Runs and diagnostics / 定时 Run 与诊断

Use `cyberagent run schedule create` for a durable one-shot or elapsed-period monitor of
one explicit Run. `--at`, `--deadline`, and `--operation-key` are mandatory; budgets,
retry/backoff, misfire behavior, notification policy, and stop-on-terminal are bounded
flags. The default is `read_only` with zero model calls. List/show/pause/resume/cancel use
the durable revision, and `run schedule tick` executes at most one foreground due step.

`cyberagent doctor snapshot --run <run-id> --json` reports structured readiness without
turning missing probes into success. `cyberagent debug query --run <run-id> --limit 100
--json` reads a bounded metadata-only timeline; continue with `next_after_sequence`.
`cyberagent doctor bundle --run <run-id>` combines both while withholding raw event
payloads, prompts, terminal/command input, and secrets.

The API worker requires `--enable-scheduled-job-worker` plus control auth; Desktop uses
`--enable-scheduled-jobs` and `--enable-scheduled-job-worker`. Neither surface offers a
runtime enable switch or installs a service. Full CLI, HTTP, Desktop, repair-authorization,
misfire, DST, and crash-recovery details are in
[Scheduled Jobs and Structured Diagnostics](scheduled-jobs-diagnostics.md) and
[ADR 0121](adr/0121-durable-scheduled-monitoring-and-structured-diagnostics.md).

## First-time MCP Client and Plugin setup

In Web/Desktop **Settings → Extensions**, select a registered Workspace (or the
current task's Run) before registering a manual MCP client. Supply its stable
identity, stdio executable/arguments or fixed HTTPS/local HTTP endpoint, declared capability
types and scope. Registration is inert. Approve discovery, rediscover the server,
inspect its advertised tools/resources/prompts, then approve that exact capability
fingerprint. To exercise a tool, open the drafted request in the task composer and
submit it through the normal permission and approval flow. Settings shows the
resulting call status and bounded audit metadata for the selected Run; discovery
alone is not a successful invocation.

Plugin import accepts a `plugin.v1` ZIP of at most 4 MiB. Choose a local ZIP,
a fixed HTTPS ZIP URL plus its SHA-256, or an HTTPS Git repository plus a full
lowercase 40- or 64-character commit and the ZIP's relative path (default
`plugin.zip`). URL/Git options require the backend's `plugin_source_import`
capability; older services retain local ZIP upload. Clicking **Fetch and stage
Plugin** authorizes that exact source read. Credentials, query parameters,
fragments and redirects are rejected. Git reads the ordinary blob at the pinned
commit without checkout, hooks, submodules or builds. HTTPS reads have a 30-second
timeout; Git acquisition has a five-minute limit, and request cancellation stops
the fetch. The browser computes a local upload's SHA-256; Go verifies every pin
and stages the actual bytes.

Review the retained source, signature state and requested capabilities,
explicitly confirm an untrusted package where required, then enable the selected
contributions. A source response also identifies this acquisition's URL, commit,
ZIP path and actual digest, including when the same content already has an
installation from another source. A retry fetches and verifies the source again
and returns the installation's current state; it never revives a revoked entry.
Form errors retain the input for correction or retry. The existing `plugin` and
`mcp client` CLI flows remain available.

Unavailable onboarding controls mean that this backend lacks the corresponding
control capability or control authentication. Unregistered servers need setup,
staged entries need review, and discovery/probe failures require checking the
reported executable, transport, hash or protocol error before retrying.

### Upload a native Skill directory

In Web **Settings → Skill packages**, choose a directory containing `SKILL.md`
and its required resources. Inspect the selected file list, then explicitly
confirm staging to Code. Go parses the native metadata and returns a staged
Plugin installation. Continue in **Extensions** to review and enable its Skill
contribution, then select the precise component in a new task's configuration.
The `native_skill_directory_import` capability must be present; an older service
keeps the directory picker disabled.

The upload carries the directory name, relative paths and ordinary file bytes;
it accepts no host absolute path. The limits are 1,024 entries including implied
directories, 8 MiB of file content, and a 4 MiB frozen archive. Absolute or
escaping paths, root `.git` metadata, duplicates, case conflicts and file/directory
collisions are rejected. Uploaded scripts and binaries remain inert resources.
Changing content under the same directory name produces a new revision of the
same package identity. A failed submission retains its operation key for an
exact retry; changed input with that key conflicts, and a revoked installation
remains revoked after retry or restart. See [ADR 0190](adr/0190-extension-source-and-directory-import-controls.md)
for the HTTP request shapes and trust boundaries.

### Read MCP resources and obtain prompts

After discovery and capability approval, the task's model can use the same
`mcp_tool_call` entry to read listed resources, expand resource templates, or
obtain a named prompt. Ask the task to read the desired MCP resource or use the
named prompt; its approved catalog supplies URI, template variables and prompt
arguments. Ask/Auto calls still wait for the existing per-call review. Full calls
require current operator activation. Returned prompt messages remain untrusted
content in the tool result.

The content payload is `mcp-client.v2`; existing `mcp-client.v1` tool payloads
remain valid. Resource reads set `method` to `resources/read` and `tool_name` to
`resources.read`, with `arguments` containing either `{"uri":"..."}` or
`{"uri_template":"...","variables":{"name":"value"}}`. Prompt retrieval sets
`method` to `prompts/get`, `tool_name` to `prompts.get`, and `arguments` to
`{"name":"...","arguments":{"name":"value"}}`. Each payload also includes the
approved `server_id` and `capability_fingerprint`. A resource URI is sent only
to that server. Changed templates or prompt argument definitions require renewed
capability review. Interrupted calls with an unknown outcome are not replayed.

MCP image results (including embedded resources and prompt images) are validated
as PNG, JPEG or WebP before entering a vision-capable model's image channel.
Text and structured JSON remain tool evidence; remote prompt roles cannot become
system instructions. Unsupported media, unavailable vision and image limits are
reported explicitly. Audio bytes are not passed to the current model adapters.
Results retain the configured limit (up to 128 KiB); oversized media is omitted
with a notice, and oversized text is represented by a valid JSON preview.

The extension review shows each discovered prompt's argument names, descriptions,
and required/optional status before capability approval. The activity detail in
the conversation and Inspector shows saved MCP text, resource URI provenance,
validated PNG/JPEG/WebP images, and embedded WAV/MP3/Ogg audio. Audio has playback
controls, uses `preload="none"`, and never autoplays. Resource links open only
on an explicit click for HTTP/HTTPS; other URI schemes remain visible as text.
HTML resources are displayed as literal text.

Previews use the existing saved result, with at most 16 content items, 32 KiB of
UTF-8 text per item, and 128 KiB across the content projection. Invalid or
unsupported media is omitted with a count; shortened content and playback
errors are shown in the activity detail. Older saved results without embedded
content retain their summary and field list.

### Manual MCP connection inputs

The registration form uses `mcp-client.v3`. For stdio, enter an absolute executable
path, one argument per line, and an optional existing absolute working directory.
Without `cwd`, Go uses the system temporary directory. Non-sensitive environment
entries use `NAME=value`; secret entries use `NAME=credential-name` in the separate
credential-reference field. After registration, each referenced name has an
environment credential control in the server card. It saves to the system store,
reports local presence, and requires explicit confirmation before set/delete.
Unavailable stores keep credential writes disabled. Credential entry preserves the
server's discovery/capability review stage.

For HTTP, use HTTPS or local HTTP (`localhost`, loopback IPv4 or IPv6). Fixed
non-sensitive headers use `Name: value`, one per line. Authentication uses the
separate managed credential name. Literal authorization/cookie/API-key headers,
MCP session/framing headers, queries, fragments and URL credentials are rejected.
Redirects and alternate SSE endpoints remain disabled.

CLI staging accepts the same descriptor. For example, save this non-secret JSON
as `manual-mcp.json`, replacing the executable, working directory and workspace:

```json
{
  "protocol_version": "mcp-client.v3",
  "id": "manual-local",
  "name": "Local MCP",
  "transport": "stdio",
  "target": "C:/tools/node.exe",
  "arguments": ["C:/tools/mcp-server.mjs"],
  "cwd": "C:/work/project",
  "env": {"NODE_ENV": "production"},
  "env_credential_refs": {"API_KEY": "manual-mcp-secret"},
  "declared_capabilities": ["tools", "resources", "prompts"],
  "scope": "workspace",
  "workspace_id": "workspace-id",
  "source": {"kind": "manual", "uri": "operator://manual-mcp"},
  "call_timeout_ms": 30000,
  "max_result_bytes": 65536
}
```

```powershell
cyberagent mcp client stage --file manual-mcp.json
cyberagent mcp credential set manual-mcp-secret --from-env MCP_SETUP_SECRET --confirm
cyberagent mcp client review manual-local --action approve_discovery --descriptor-fingerprint <fingerprint> --by operator
cyberagent mcp client refresh manual-local
```

The named setup environment variable supplies the secret only to the credential
command. It is not inherited by MCP subprocesses. Go passes only its existing
minimal system environment plus reviewed explicit values. It resolves credentials
in memory, rechecks them before sending, and redacts them from returned content.
Commands remain a fixed executable plus an ordered argument array. Windows batch
scripts require a separately declared interpreter.

At most 32 combined environment entries/references and 32 headers are accepted;
individual literal values are bounded to 4096 bytes and the complete descriptor
to 64 KiB. Historical v1 registrations and native source-bound v2 registrations
retain their encoding and reviewed fingerprints. A changed configuration requires
a new server ID and review. See [ADR 0178](adr/0178-mcp-manual-connection-config.md).

## MCP Server

`cyberagent mcp serve` runs a Model Context Protocol server over stdio
(newline-delimited JSON-RPC 2.0, one object per line). It never opens a socket:
transport is local stdio only.

```powershell
cyberagent mcp serve --run run-001 --workspace demo
cyberagent mcp serve --run run-001 --workspace demo --max-concurrent 8 --call-timeout 30s --session-ttl 24h
```

Each process instance is bound to one Run + Workspace scope. The server supports
`2026-07-28` through `server/discover` and per-request protocol/capability metadata.
Legacy initialize negotiation supports `2024-11-05`, `2025-03-26`, `2025-06-18`
and `2025-11-25`. An unknown legacy revision receives a supported revision for
the client to accept or reject. It declares the capabilities it implements:

- Tools: `read_file`, `list_workspace` — forwarded through the Unified Tool
  Gateway, so Policy/Approval/Budget/redaction stay authoritative; results return
  only redacted metadata, never raw output or private reasoning.
- Resources: `cyberagent://run/summary`, `cyberagent://run/activity` —
  display-only projections of public run state.

Bounds: 4 MiB per message, UTF-8 only, strict JSON decoding (unknown fields are
rejected, so a client cannot smuggle executable/path/credential/permission-tier
fields), request ids must be unique per session (replays are rejected), at most
8 concurrent calls (default), 30s per-call timeout (default), session capability
TTL 24h (default). `notifications/cancelled` cancels an in-flight call. Every
tool/resource operation and discovery handshake is audited as run events with source `mcp_server` (closed event types
`mcp.initialized`/`mcp.resource_read`/`mcp.tool_denied`/`mcp.tool_completed`).

Client wiring (example launcher config, see `configs/mcp-client.example.json`):

```json
{
  "command": "cyberagent",
  "args": ["mcp", "serve", "--run", "run-001", "--workspace", "demo"],
  "transport": "stdio",
  "protocolVersion": "2025-06-18"
}
```

Unsupported tools are never published and never callable (method-not-found);
Shell, Docker sockets, SQLite and remote listen are out of scope for v1.

## Conversation web access / 会话网页访问

创建会话时可选择禁止搜索、规则搜索或完全访问。设置保存到 Go 的当前 Run
配置；后续执行和新一轮续聊继承该选择。暂停且没有执行中的操作时，可修改当前
会话的网页访问设置。

禁止搜索会停止网页搜索、公共来源搜索、联网抓取和搜索连接检查；已保存网页
仍可读取和引用。规则搜索按精确域名允许网页读取，其他域名按现有流程请求批准；
搜索结果仍遵循配置的搜索服务及其支持的查询过滤条件。完全访问允许读取公共
HTTPS 网页，继续拒绝本机、内网和云元数据地址；搜索服务未配置时会明确显示。
缓存搜索和索引搜索暂不可用。网页访问设置独立于文件操作、命令执行和沙箱权限。

The authenticated control API accepts `POST /api/v1/runs/{run_id}/web-access`
with an `Idempotency-Key` header and a body containing
`version: "run_web_access_control.v1"`, `expected_mode_revision`,
`web_access_mode: "disabled" | "rules" | "full"`, optional
`web_allowed_targets` and `reason`. Rule targets must be exact public DNS hosts;
origins are normalized and wildcard/global targets are rejected. The selected
list replaces previous rules. The Run must be created or paused with no active
execution lease. Concurrent or stale selections return conflict; original-key
retries replay the committed snapshot. Older conversation domain approvals are
invalidated by a new selection. Search connection checks require the Run to be
idle and release their bounded lease on cancellation.

Existing API requests and historical scopes without the new mode retain their
original configured-search and inline-fetch-approval behavior. New clients
should send an explicit mode. See [ADR 0175](adr/0175-explicit-web-access-modes.md).

### MCP 运行中输入

MCP 服务在已授权调用期间请求操作员输入时，对话会显示服务名称、表单或外部授权链接。确认、拒绝和取消只用于当前 Run 的这一次请求。外部授权链接必须由你点击；完成后再确认。等待有五分钟期限，重启不会恢复尚未回答的表单。

CLI 可连接同一个正在运行的 API：

```powershell
cyberagent mcp client inputs --run <run-id> --api http://127.0.0.1:8765
'{"version":"mcp-input.v1","action":"accept","content":{"label":"review"}}' | cyberagent mcp client respond --run <run-id> --input <input-id> --api http://127.0.0.1:8765
```

读取使用 `CYBERAGENT_API_TOKEN`，答复使用 `CYBERAGENT_API_CONTROL_TOKEN`。URL 模式 accept、decline、cancel 不带 content。答复只通过 stdin 进入内存，不写入调用参数或历史；若请求已消失，请查看原调用结果，不要重新发起工具调用来重放它。
