# 工具调用对照：Codex、DeepSeek Harness、Claude Code

核查日期：2026-11-12。本文依据官方源码和官方文档，区分工具本身、工具调用编排和宿主权限。本次优先补齐本地图片读取和已有工具的实际调用链；原生并发与 PTC 是后续优化，PTC 不是具备完整 Agent 工具能力的前提。

## 比较基准

| 对象 | 固定版本与可验证范围 |
| --- | --- |
| Universal Code | `dc3e688945ad502e3f3337a3a46ddb7c8e8f610e`。本文描述这个提交的现状，后续实现不回写为该版本已经具备的能力。 |
| DeepSeek Harness（dsh） | 官方仓库 [`deepseek-ai/deepseek-harness`，`e8f7b93a344964de3dca1ace9e72b6e4d6de87cb`](https://github.com/deepseek-ai/deepseek-harness/tree/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb)，提交时间 2026-11-11。检查 native/PTC executor、共享 invocation pipeline、Node runtime、图片工具及沙箱契约。 |
| Codex | 官方仓库 [`openai/codex`，`c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee`](https://github.com/openai/codex/tree/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee)，提交时间 2026-11-12。检查公开 core 中的 direct/code-mode 派发、并发、发现和图片读取；这些源码不能用于断言整个桌面产品的全部内部实现。 |
| Claude Code | 官方仓库 [`anthropics/claude-code`，`927b66497b89542dc1242e26d5735dbf880c829e`](https://github.com/anthropics/claude-code/tree/927b66497b89542dc1242e26d5735dbf880c829e)，以及核查日的官方文档。公开仓库提供安装说明、更新记录、插件和示例等，未提供可用于本次审计的完整核心 executor；[许可证](https://github.com/anthropics/claude-code/blob/927b66497b89542dc1242e26d5735dbf880c829e/LICENSE.md#L1)为保留权利并适用商业条款。本文不采用泄露源码或第三方复刻来补齐内部调度细节。 |

这是静态源码和文档核查，没有运行外部仓库、调用付费模型或测量产品性能。没有实测数据支持“采用 PTC 后一定更快或更省”的定量结论。

## 调用方式与能力对照

原生调用是模型直接提交工具名和参数。PTC（programmatic tool calling）是模型先写程序，再由程序调用已提供的工具。两者都需要真正实现并授权的工具；将调用改成代码不能增加原本不存在的图片、浏览器或文件能力。

| 维度 | Codex 公开实现 | dsh native / PTC | Claude Code 官方可确认能力 | Universal Code 基准版本 |
| --- | --- | --- | --- | --- |
| 模型入口 | 有 direct 与 CodeMode 路径；CodeMode 的 `exec` 执行 JavaScript，再调用宿主工具。[C1]、[C2] | native 直接提供工具 schemas；PTC 直接提供 `run_code`，其程序通过 `await tools.name(args)` 调用工具。[D1]、[D2] | 官方描述工具结果反馈给模型的 Agent 循环；另有独立的 Claude API PTC 产品能力，不能据此断言 Claude Code 内部统一使用 PTC。[A1]、[A4] | 原生 function-call，工具经 Go 提供；`command_runtime` 执行进程/脚本，与可调用宿主工具的 PTC 是不同入口。 |
| 批量与并发 | 工具声明支持并行后取得共享锁，否则取得排他锁；取消贯穿等待与派发。[C3] | 两种 executor 都复用 native 并发契约。默认并发 10；工具只有明确判为 concurrency-safe 才并发，exclusive 调用形成屏障。PTC 可在程序内组织并行、循环、条件和结果筛选。[D1]、[D3][D4] | 可确认子 Agent 等编排能力；本次没有官方核心源码证据可以确认 direct 工具池上限和排他屏障实现。 | 每模型响应最多 4 calls；每内部段最多 4 个 native rounds。当前批内逐个调用，属于串行批量。段末可以自动续段，不是整任务最多 16 calls。 |
| 工具发现 | 有 `ALL_TOOLS` 元数据和 `tool_search`；CodeMode 搜索也可返回工具信息。[C4] | PTC 从获准工具生成 TypeScript/Python SDK 文本，执行绑定限定于已接纳的 registry/toolSet。将 schema 换成 SDK 本身不等于按需发现，也不自动消除全部工具描述成本。[D2]、[D5] | `ToolSearch` 搜索并加载 deferred tools。[A2] | 已有 `mcp_tool_search`，按当前授权目录返回 schema、精确调用字段和分页；搜索本身不授权执行。 |
| 本地图片 | `view_image` 读取受当前 sandbox context 约束的文件，解码验证后进入模型图片通道。[C5] | `read_image` 本身提供图片工具；native 返回 image block。TypeScript PTC 对 `read_image`/`cua` 适配成 Image，经 `inspect` 选择展示；其他图片结果有 deferred context 路径。[D6]、[D7] | `Read` 可把 PNG/JPEG/GIF/WebP 作为视觉内容返回，文档说明缩放和文件大小限制。[A2] | 有用户图片附件、浏览器截图，以及 MCP 图片装配；`workspace_read` 只读 UTF-8 文本，缺工作区本地图片观察工具。 |
| 取消、审批与记录 | CodeMode 子调用回到普通 `ToolCallRuntime`，传入 cancellation token 并记录子调用来源；不是在 JS 中另建一套不受宿主管理的执行器。[C2]、[C3] | PTC 子调用有 root/parent/sub-call 身份、prepare/dispatch/complete、独立审批和日志。外层取消/结束会中止活动调用、放弃未开始调用并等待清理；已发生的效果不自动撤销。[D4]、[D8] | 官方权限规则、hooks、用户中断等有公开契约；不据此补写其未知内部事务和恢复算法。[A1]、[A3] | 已有持久 call/result、Run/attempt/lease/权限绑定、精确审批和 outcome_unknown 防重放。PTC 如接入，必须保留这些约束。 |
| 程序运行环境 | 当前 CodeMode 使用 V8 isolate，安装明确的宿主 globals；模块导入被拒绝。文件/网络能力通过工具提供。[C6] | 检查的 Node provider 默认每次启动新 Node 进程，可选保留状态；允许 Node API。OS 沙箱约束文件效果，不能将它描述成完整网络隔离。[D9]、[D10] | Claude Code shell sandbox 与其他工具权限分开；官方文档说明其 shell sandbox 支持 macOS/Linux/WSL2，native Windows 不提供该 shell sandbox。[A3] | 宿主命令、浏览器、MCP 各有实际适配器和权限；添加 PTC 不应把这些边界变成一个“程序已获准”的总授权。 |

## dsh PTC 的实际机制

`NativeToolExecutor.describe` 发出每个工具的 schema，`execute` 进入共享 `executeToolBatch`。`PtcExecutor.describe` 只发出 `run_code`，同时把当前工具的输入、输出和说明生成 SDK 放入提示。PTC 中的子调用仍使用同一个 invocation pipeline，不直接调用工具 body。[D1]、[D2]

并发能力先存在于 native 调度器：按工具参数重新分类，只有显式 `true` 才进入 parallel 池；未声明、隐藏、未知或分类异常均按 exclusive 处理。准备/策略阶段与结果提交有顺序，只有工具 body 允许重叠。PTC 程序可以写 `Promise.all`，但最终是否重叠仍由这套调度规则决定，不会因为程序提出并行就让写操作任意交错。[D3]、[D4]

PTC 将子调用的中间结构化结果交给程序，允许先筛选、聚合再选择外层输出，因此可以省掉确定性数据处理中本来用于搬运结果的模型往返。它仍记录每个子调用，而非只留一段代码和最后一句总结。绑定参数、call ID、parent/root 身份、错误及结果各有记录；图片也经过工具输出策略。[D4]、[D7]

调用数量有独立预算：配置默认 `maxParallelSubCalls=10`、`maxSubCallsPerMinute=1024`。实际 allowance 是 `floor(resolved timeoutMs / 60000 × maxSubCallsPerMinute)`，不是每分钟滚动限流；默认 Node 超时 120 秒时，单程序允许 2048 次尝试，失败和排队调用也计数。运行时若明确无 deadline，则该计算没有有限上限。超限终止程序，不靠 `try/catch` 继续执行。Node 的默认 deadline/最大 deadline 分别为 120/600 秒，另有输出、消息、pending calls 和 V8 old-generation heap 限制；heap 限制不包含全部 native 内存。[D4]、[D9]

外层程序提权也不替代子工具审批：程序启动前可以为本次执行申请更宽的文件沙箱；每个子调用继续经过 `tools/pre-execute`、审批和 guards。缺审批服务时 ask 被拒绝。官方测试明确检查“外层允许一次、内层仍拒绝”，以及审批取消时不得启动程序。[D8]

这里需要单独看直接 Node API。Node runtime 的 `await import(...)` 可访问 Node 文件、网络和子进程能力；worker 在启动时应用 `ctx.sandbox.confine`。沙箱契约明确只表达文件效果，不表达网络、进程、syscall、设备或凭据限制。Windows ACL 后端还明确说明读、网络和进程可见性不受其约束，文件写限制为 partial。因此，“通过工具绑定的子调用保留审批”不能扩大成“程序每个副作用都经过逐工具审批”。复制这种运行环境会改变 Universal Code 当前的授权边界。[D9]、[D10]

图片能力也依赖具体工具。`read_image` 返回 attachment 支持的 image block；PTC TypeScript 路径对 `read_image` 和 `cua` 有明确适配，生成带 attachment ID、路径和尺寸的 Image，由 `inspect` 输出。源码仍标有图片适配应由工具声明的 FIXME，不能据此声称任意第三方工具图片都具有完全相同的程序内接口。[D6]、[D7]

## Codex 当前 CodeMode 与 Claude API PTC

Codex 在本次固定的公开提交中已经存在代码编排能力。`exec` 执行 JavaScript，V8 中有 `tools`、`ALL_TOOLS`、`text`、`image`、`store/load` 等宿主接口；静态和动态模块导入最终都被 loader 拒绝。子工具经普通 `ToolCallRuntime` 派发，并携带 CodeMode 来源和取消 token。这个实现可作为“纯计算加宿主工具桥”的参考，不能简化成“开启完整 Node 即获得 Codex 的执行方式”，也不能假定所有模型和部署默认启用 CodeMode。[C1]、[C2][C4]、[C6]

Claude 的官方 API PTC 则是 Messages API 的一项功能：模型在 code-execution container 内写 Python，通过 `allowed_callers` 声明可从代码调用的工具。程序调用时，API 仍把 `tool_use` 返回给客户端处理，再把结果交回程序；中间结果可留在程序中。该能力不证明 Claude Code 核心使用同一执行机制。官方文档还限制 programmatic 子调用结果只能为 text/string，不能返回 image/document blocks；MCP connector 和官方 computer/browser toolsets 也有 programmatic 调用限制。不能将该接口当成 dsh 的 `Image` 或 Codex 的 `image(...)` 的等价物。[A4]

## Universal Code 的真实现状

以下路径和行号均在固定提交 `dc3e6889` 下读取：

| 现状 | 源码位置 |
| --- | --- |
| 4 calls/响应、4 native rounds/内部段 | [`internal/domain/supervisor_tool.go`](../../internal/domain/supervisor_tool.go)，13–20；[`supervisor_tools.go`](../../internal/application/supervisor_tools.go)，277–281。 |
| 当前逐个审批、开始、调用、持久化，没有批内并发池 | [`supervisor_tools.go`](../../internal/application/supervisor_tools.go)，843–984；实际 `Invoke` 使用派生 timeout context，1227–1228。 |
| 浏览器、MCP、命令有各自 preflight；started 无完成回执时阻止不安全自动重放 | 同文件，890–953。 |
| 第四轮后请求 lifecycle 决策，可以 continue；外层继续同一已接受输入 | [`supervisor_tool_boundary.go`](../../internal/application/supervisor_tool_boundary.go)，22–60；[`agent_turn_action.go`](../../internal/application/agent_turn_action.go)，162–171。 |
| MCP 工具发现已存在 | [`internal/toolgateway/mcp_search.go`](../../internal/toolgateway/mcp_search.go)，19–32。 |
| 工作区文件读取仅 UTF-8 行范围 | [`internal/toolgateway/agent_code.go`](../../internal/toolgateway/agent_code.go)，311–313。 |
| 浏览器截图经过精确 call/result 与 artifact 校验，转为 `llm.ImagePart`，按视觉能力和图片预算装配 | [`supervisor_browser_images.go`](../../internal/application/supervisor_browser_images.go)，39–176、192–250。 |
| MCP 返回的验证图片也能进入模型图片通道 | [`supervisor_mcp_content.go`](../../internal/application/supervisor_mcp_content.go)，26–114。 |

因此，当前差距包括“没有本地图片工具”和“原生批量仍串行”。四轮边界会产生一次不提供工具的模型决策及后续上下文续接成本，但它不是任务结束条件；引入 PTC 不应被当作修复整任务只能调用 16 次的方案。

## 建议顺序

### 本次现场核查

常用工作能力按实际工具入口核对如下。“已有”表示存在注册和执行实现；下发仍取决于当前运行配置，并不表示每项都在本次进行了完整交互验收。

| 工作能力 | 本项目入口 | 本次结论 |
| --- | --- | --- |
| 浏览目录、读文件、按名称或内容搜索 | `workspace_list/read/glob/grep` | 已有；现场任务实际调用成功。 |
| 创建、修改、移动、删除与撤销文件 | `workspace_change/apply/delete` | 已有；写入仍经过现有精确修改和授权流程。 |
| 查看本地图片 | `workspace_view_image` | 本次补齐，像素进入支持视觉的模型请求。 |
| 执行脚本、运行测试与管理进程 | `command_runtime`、`run_tests` | 已有；`preview` 不提供进程执行，需匹配可用的执行适配器。 |
| 操作真实浏览器 | `browser_status/navigate/snapshot/click/type/scroll/key/upload/move/screenshot` | 已有；修复 Windows 冷启动隐藏页的输入与截图，验证真实本地网页流程。 |
| 搜索网页、抓取内容及引用 | `web_search`、`source_search`、`web_fetch`、`web_citation` | 已有；与真实浏览器交互是不同能力。 |
| 扩展工具发现与调用 | `mcp_tool_search`、`mcp_tool_call` | 已有；保持服务目录和每次调用的授权。 |
| 代码导航与诊断 | `code_*`：symbols、definition、references、hover、diagnostics 等 | 已有；需要工作区对应的代码服务。 |
| 历史、记忆、技能与委派 | `history_*`、`note_create`、`work_item_create`、`skill_read`、`specialist_delegation_propose`、`child_task_propose` | 已有；不能因为这些仍可见，就认为执行类工具也已就绪。 |

入口依据：[`toolgateway/registry.go`](../../internal/toolgateway/registry.go)、[`toolgateway/agent_code.go`](../../internal/toolgateway/agent_code.go)、[`application/supervisor_tools.go`](../../internal/application/supervisor_tools.go)。

两次相关运行的持久配置均为 `execution_profile=preview`、`browser_network_access=off`，所以模型没有收到进程执行和浏览器工具。制作 SVG 的运行有成功的目录浏览、文本读取、搜索、修改提议和写入记录；不能根据后续回复中的“没有文件工具”判断这些能力不存在。旧记录没有保存完整的请求工具清单，也不能从持久 Full 选择反推当时进程内的激活状态。

另用隔离数据库复现了一个实际问题：Full 选择仍保存、进程授权已失效时，旧代码会过滤基础工具而继续请求模型。此次增加了模型调用前检查和可用工具清单；清单来自实际请求的 schemas，并给出有效文件根目录、执行环境，以及被配置关闭或未连接的能力。四批工具后的调度回复会明确说明工具只是暂时收起，继续执行时将重新校验并提供。

基础工具方面，此次增加 `workspace_view_image`，使用现有工作区读权限与排除规则读取 PNG、JPEG、WebP，保存实际图片并经图片通道发送给有视觉能力的模型。文本模型收到明确的未提供像素说明。此工具不把 SVG/HTML 源码视为已渲染的页面；本地网页仍通过所属工作区的服务、导航、DOM 和截图验证。

Windows 真实调用链还暴露了隐藏 WebView2 页面的两个缺陷：从未显示过的页面接收点击后可能不触发 DOM 事件，截图请求可能长期不返回。修复让 Agent 持有页面期间的隐藏原生子窗口在空区域内渲染，保持不可见、不可命中、不抢焦点；切换可见页面、关闭与释放控制时恢复。截图失败有独立期限并保留 DOM 能力。隔离的原生测试实际启动本地服务、导航、点击，再检查页面颜色确实出现在保存的 PNG 中；没有操作用户前台窗口。

图片保存与模型装配按当前 root Agent 和精确的持久调用校验。隔离测试覆盖有视觉/无视觉/视觉未知、原文件删除后的原图重放、同一路径更新后的再次查看，以及同工作区其他合法图片回执的错配拒绝。同路径重读在初版也能成功；此次加固的是可独立核对的调用绑定。

Full 未就绪的恢复说明沿原有聊天错误路径显示，并支持失败记录重放；普通提供商错误继续使用脱敏文案。没有修改用户现场的权限选择、执行环境或浏览器网络设置。

| 优先级 | 改动 | 具体完成条件 |
| --- | --- | --- |
| P0：基础观察能力 | 增加工作区本地图片读取入口 | 在当前工作区读权限内解析相对路径，复用现有安全路径读取和拒绝规则；限制字节、尺寸、解码后像素等资源，验证真实 MIME。用持久 call 绑定保存的图片/hash，不接受伪造 artifact 或任意宿主路径。复用 `imageattachment.Validate`、`llm.ImagePart`、视觉能力检查与图片预算；不支持视觉的模型得到明确结果，不能假装看到了图片。 |
| P1：减少等待 | 为原生工具增加有限并发，先覆盖独立 workspace 只读查询 | 明确声明可并发的工具和参数才进入池；默认串行。保持有序 durable 记录、每调用权限及取消检查。文件写、同页浏览器交互、审批边界等维持排他；不能仅信任远端 MCP 的 read-only/concurrency 提示就授权并行。先在现有最多 4 calls 中验证重叠，无须同时扩大调用上限。 |
| P1：已有能力更可用 | 改善工具发现和续段上下文的可发现性 | 保留现有 MCP 搜索和精确调用字段；测试当前可用工具会进入模型 schema、错误不被解释成永久缺能力。减少重复读取和边界搬运，不能靠 PTC 掩盖缺工具、schema 未曝光或图片未入模的问题。 |
| P2：有收益再做 | 可选的受限代码编排 | 适用于多独立读取、分页筛选、确定性聚合等。与 native 方案比较实际完成时间、模型往返、上下文体积和失败恢复；对每步需要模型判断、操作少、频繁等用户审批的流程保留 native。没有收益证据前不建设通用 PTC 框架。 |

如果进入 P2，最小可接受设计是隔离的纯计算程序，通过现有宿主工具 RPC 工作。程序不能直接取得 Node 的文件、网络、进程、凭据能力；若确需这些能力，应显式经过已有 adapter 和授权。至少还需要：

1. 每个子调用仍有 Run/Agent/attempt/parent 身份、durable pending/started/result、操作去重和原始参数；外层程序成功不能掩盖子调用失败或未知结果。
2. 每次子调用重新检查当前可用工具、权限快照、租约和精确审批；程序授权不能覆盖未来任意网络写入、文件修改或 MCP 操作。
3. deadline、工具调用数、并发、输出、内存分别有上限；子调用计入现有 Run 预算，不能把整个程序算成一个调用来绕开预算。
4. 取消及权限撤回必须使队列中未派发的调用失效，并到达真实派发点；等待已启动工作清理。恢复时保留已完成效果，未知写入不得自动重跑整段程序。
5. 图片/文本的选择输出与审计分开：模型可只看选定结果，宿主仍保留实际子调用和完整受限回执。网页和文件结果继续是非授权数据，不能成为新能力来源。
6. 共享浏览器仍绑定稳定页面、document epoch 和既有网络策略；代码编排不能让旧 ref 恢复有效，不能把同一页面上的写操作并发化。

不必为了形式一致复制的部分包括：dsh 的具体 Node REPL/插件系统、Anthropic 托管容器的协议和产品配套、Codex 特定服务或桌面内部组件。它们解决各自平台的部署问题；当前应复用本项目已经有的运行时、审批、图片管线和恢复记录。

## 官方证据索引

下列源码链接均固定到上述提交；文档链接为核查日在线版本。

- [D1：dsh NativeToolExecutor schemas、默认并发与共享 batch](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tool-executor-native/src/index.ts#L7-L21)。
- [D2：dsh PtcExecutor 的单 transport、SDK 与共享 pipeline](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tool-executor-ptc/src/index.ts#L23-L61)。
- [D3：native concurrency fail-closed 分类](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tools/src/pipeline.ts#L99-L114)、[batch prepare/dispatch/ordered commit 与 pool](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tools/src/batch.ts#L138-L221)。
- [D4：PTC run-scoped abort 与有序并发](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tool-executor-ptc/src/ptc.ts#L423-L446)、[子调用身份和预算计数](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tool-executor-ptc/src/ptc.ts#L554-L579)、[timeout allowance 与最终清理](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tool-executor-ptc/src/ptc.ts#L747-L769)、[默认配置](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tool-executor-ptc/src/config.ts#L4-L16)。
- [D5：PTC 绑定当前 admitted schemas](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tool-executor-ptc/src/ptc.ts#L719-L726)、[生成全部获给工具的 SDK](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tool-executor-ptc/src/ts-types.ts#L296-L329)。
- [D6：read_image 的原生 image block](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/fs/tool-fs/src/read-image.ts#L196-L204)。
- [D7：PTC 图片专用适配](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tool-executor-ptc/src/ptc.ts#L554-L557)、[完整 pipeline、图片 deferral 与 binding 结果](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tool-executor-ptc/src/ptc.ts#L648-L711)、[runtime Image 描述符](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/ptc-runtime/ptc-runtime-node/src/worker.ts#L305-L308)。
- [D8：共享 prepare 的审批与 guards](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tools/src/pipeline.ts#L191-L230)、[缺审批渠道拒绝](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tools/src/pipeline.ts#L429-L477)、[外层审批取消测试与内外审批独立测试](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tool-executor-ptc/tests/ptc.spec.ts#L2913-L2992)。这些是读过的上游测试，本文未执行它们。
- [D9：Node 执行模式、fresh process 与 Node APIs](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/ptc-runtime/ptc-runtime-node/src/execution.ts#L150-L174)、[资源默认值及 native memory 边界](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/ptc-runtime/ptc-runtime-node/src/config.ts#L21-L52)、[进程 confine/spawn](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/ptc-runtime/ptc-runtime-node/src/worker.ts#L311-L331)。
- [D10：dsh 沙箱文件效果范围](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/sandbox/sandbox/README.md#L164-L168)、[Windows 后端的读取/网络/进程与 partial 限制](https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/sandbox/sandbox-windows-acl/README.md#L123-L129)。
- [C1：Codex CodeMode 的 JavaScript tool spec](https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/core/src/tools/code_mode/execute_spec.rs#L1-L44)、[Direct/CodeMode 配置路径](https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/core/src/tools/code_mode/mod.rs#L106-L124)。
- [C2：CodeMode 子工具回到普通 ToolCallRuntime、来源与取消 token](https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/core/src/tools/code_mode/mod.rs#L343-L425)。
- [C3：Codex parallel/exclusive gate 与调用派发](https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/core/src/tools/parallel.rs#L278-L338)、[取消处理](https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/core/src/tools/parallel.rs#L373-L435)。
- [C4：CodeMode ALL_TOOLS 与宿主 globals](https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/code-mode-runtime/src/runtime/globals.rs#L16-L54)、[tool_search 的 CodeMode/direct 分支](https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/core/src/tools/handlers/tool_search.rs#L247-L276)。
- [C5：view_image sandbox 文件读取与图片解码](https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/core/src/tools/handlers/view_image.rs#L151-L205)、[模型 InputImage 输出](https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/core/src/tools/handlers/view_image.rs#L238-L249)。
- [C6：V8 isolate 和 globals 安装](https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/code-mode-runtime/src/runtime/mod.rs#L187-L204)、[module loader 拒绝 imports](https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/code-mode-runtime/src/runtime/module_loader.rs#L201-L212)。
- [A1：Claude Code 官方 Agent 循环和工具概述](https://code.claude.com/docs/en/how-claude-code-works#the-agentic-loop)。
- [A2：Claude Code 官方工具表与 Read 图片行为](https://code.claude.com/docs/en/tools-reference#read-tool-behavior)。
- [A3：Claude Code 权限规则](https://code.claude.com/docs/en/permissions)、[shell sandbox 覆盖范围及平台](https://code.claude.com/docs/en/sandboxing#what-the-sandbox-restricts)。
- [A4：Claude API PTC 的工作流](https://platform.claude.com/docs/en/agents-and-tools/tool-use/programmatic-tool-calling#quick-start)、[allowed_callers](https://platform.claude.com/docs/en/agents-and-tools/tool-use/programmatic-tool-calling#the-allowed_callers-field)、[工具与结果类型限制](https://platform.claude.com/docs/en/agents-and-tools/tool-use/programmatic-tool-calling#constraints-and-limitations)。

[D1]: https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tool-executor-native/src/index.ts#L7-L21
[D2]: https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tool-executor-ptc/src/index.ts#L23-L61
[D3]: https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tools/src/pipeline.ts#L99-L114
[D4]: https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tool-executor-ptc/src/ptc.ts#L423-L446
[D5]: https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tool-executor-ptc/src/ptc.ts#L719-L726
[D6]: https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/fs/tool-fs/src/read-image.ts#L196-L204
[D7]: https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tool-executor-ptc/src/ptc.ts#L554-L557
[D8]: https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/core/tools/src/pipeline.ts#L191-L230
[D9]: https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/ptc-runtime/ptc-runtime-node/src/execution.ts#L150-L174
[D10]: https://github.com/deepseek-ai/deepseek-harness/blob/e8f7b93a344964de3dca1ace9e72b6e4d6de87cb/packages/sandbox/sandbox/README.md#L164-L168
[C1]: https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/core/src/tools/code_mode/execute_spec.rs#L1-L44
[C2]: https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/core/src/tools/code_mode/mod.rs#L343-L425
[C3]: https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/core/src/tools/parallel.rs#L278-L338
[C4]: https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/code-mode-runtime/src/runtime/globals.rs#L16-L54
[C5]: https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/core/src/tools/handlers/view_image.rs#L151-L205
[C6]: https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/code-mode-runtime/src/runtime/mod.rs#L187-L204
[A1]: https://code.claude.com/docs/en/how-claude-code-works#the-agentic-loop
[A2]: https://code.claude.com/docs/en/tools-reference#read-tool-behavior
[A3]: https://code.claude.com/docs/en/permissions
[A4]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/programmatic-tool-calling#quick-start
