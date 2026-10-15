# Agent Workbench Product Surface Rewrite

本文档是本次 `web/src/v2` 重构的实施合同，也是本次对话中 GPT Pro 重构意见、用户补充要求和视觉基线的仓库内记录。实现、评审和测试均以此为准。

## 只读参考边界

- 仓库外的 `codex-ui-extract` 参考根仅作为只读研究库，并由本机 `TRAVERSE_UI_REFERENCE_ROOT` 指定；不得修改、格式化、安装依赖、复制进仓库或提交到 Traverse Board。
- 参考包中的大段 JS/CSS 是 OpenAI 专有 bundle；其顶层没有可允许再分发的 LICENSE。Tailwind 注释只覆盖 Tailwind 自身。v2 只能提取几何、token、状态与交互证据，并在本目录 clean-room 重写 React 组件。
- 原项目的任务、Thread、权限、归档、审批、执行、审计和恢复内核是唯一业务真相。Wails 负责桌面壳、透明窗口和可信 bootstrap，业务继续走内嵌 `/api/v1` 控制平面。
- 原始脏工作区不得用于本轮实现。实现与验证必须在独立 D 盘 worktree 和专用功能分支中完成。
- 依赖缓存、TEMP、Go cache、Playwright、WebView 用户数据、截图和 diff 产物必须定向到 D 盘的隔离临时根或目标工作树的 `.tmp` / `output`。不得改写 `HOME`、`USERPROFILE` 或永久系统配置。

## 权威截图

以下两张用户提供的原始 PNG 是唯一像素基线；参考包自带的 `codex-window.png/jpg` 只可作来源记录，不得参加像素验收。

| 场景 | 本地证据文件名 | 尺寸 | SHA-256 |
| --- | --- | --- | --- |
| 主工作台 | `codex-clipboard-825b95c6-b835-4a58-b404-b8e397247936.png` | 2052×1371 RGBA，约 144 dpi | `2DB81D5AC7B2D253D630D81FDA4E3D4D9E87C92BBE0A17B3AE9F4DB194CE6D55` |
| 设置页 | `codex-clipboard-b606aee7-6146-4a87-b708-fe25818cd7be.png` | 2050×1357 RGBA，约 144 dpi | `A301E480D370CE5ED9F752DABBF594A64D317843E8F2EFE7482D2509EA8C20C9` |

两图约为 Windows 150% 缩放。实现应使用逻辑尺寸，不得把物理像素直接当 CSS 像素：

- 侧栏 448 physical px ≈ 299 DIP；v2 token 为 299px。
- 标题栏约 35 DIP，线程/设置工具栏 46 DIP。
- Thread 与设置内容最大宽度均为 48rem / 768px。
- Composer 约 1106×149 physical px ≈ 738×99 DIP；提交按钮 28 DIP。
- 设置行约 61 DIP；开关 32×20 DIP；设置下拉控件约 28 DIP。
- 主截图顶部 6 physical px 是窗外桌面，稳定 ROI 必须裁掉。
- Mica/Acrylic 会受到壁纸、窗口位置和系统合成影响；标题栏/侧栏玻璃区单独设置容差，稳定主表面和几何 ROI 使用严格阈值。

## 参考设计 token

参考源与截图共同证明的 token：

- 基础网格 4px；字号 11/12/14/16/18/20/24px；正文 14–16px。
- 字体：**全部英文（UI 与代码）使用 JetBrains Mono Variable**；中文使用内置 HarmonyOS Sans SC（四字重 400/500/600/700）。字体栈 `"JetBrains Mono Variable", "JetBrains Mono", "HarmonyOS Sans SC", "Microsoft YaHei UI", sans-serif`。四个 TTF 均从华为官方归档逐字节复制，不做转码或子集化；归档、文件哈希、许可文本与产品内使用声明必须一起提交。
- 内容最大宽度 48rem；toolbar padding 16px；Composer overhang 24px；消息间隔 16px，组内 4px。
- 圆角基值 2/4/6/8/10/12/16/20/24px；支持时采用 `superellipse(1.5)`。
- 模糊层级 4/8/12/16/24/40/64px。项目材质继续使用 titlebar 28px、sidebar/panel 34px、glass 46px 的高斯模糊；2026-10-10 用户要求收敛为中性白色填充，背景使用原始饱和度。
- prominent elevation：0.5px hairline、`0 3px 7.5px #0000000a`、`0 0 20px #0000000d`；Composer 另叠加项目液态玻璃的高光和柔和阴影。
- light 主表面使用白色与浅灰，dark/glass 使用中性深灰。主按钮使用不透明白底与深色文字，选中状态、焦点、普通提示复用共享中性色。颜色盘点与最新材质规则见 [中性玻璃规范](../../../docs/branding/neutral-glass-palette.md)。
- 原生 Windows 层保留 Wails 透明窗体/Acrylic；React 层使用半透明 surface、Gaussian blur、hairline、双层阴影。两层材质缺一不可。

## 架构判断（GPT Pro 意见的实施版）

当前 Traverse Board 的主要问题不是“前端有点丑”，而是把内部控制平面、审计账本和运行状态机直接当成了面向用户的产品界面。系统必须明确分为四层：

| 层级 | 判断 | 本轮动作 |
| --- | --- | --- |
| Harness Kernel：权限、工具、Event、Checkpoint、Approval、Sandbox | 有价值 | 保留 |
| Application Facade：一次输入如何驱动 Agent | 存在根本问题 | 增加产品级 Thread turn |
| Narrative Projection：事件如何成为工作叙事 | 当前近似 Event Viewer | 在 v2 重写聚合投影 |
| React/Wails 产品界面 | 偏离 Code Agent 工作台 | v2 成为新默认工作面 |

目标架构：

```text
用户
  ↓
Agent Workbench（对话、工具叙事、固定输入框、必要审批）
  ↓
Thread Turn Facade
  ↓
Existing Harness Core（Run、Tools、Events、Approval、Recovery）
  ↓
Inspector（旧 Run/Session/Event 页面，用户主动进入）
```

后端龙骨不拆，驾驶舱重做。不得重写 Run Engine、Event Store、Approval、Sandbox、Checkpoint 或 Delivery Truth。

## 强制产品抽象

1. 默认产品对象只有 Thread（中文 UI 使用“对话/任务”）。Mission、Run、Session、Event、Receipt、sequence、stage、policy revision、capability generation 全部退出默认工作面。
2. 已删除不再挂载的旧 Thread 工作面、侧栏、Run 创建和设置入口；仍在使用的 `thread-transcript.tsx` 与 Run/Session 页面保留为 Inspector，不继续堆叠产品功能。
3. React 发送消息时只调用一个产品入口：`POST /threads/{thread_id}/turns`。它不得判断 Run 是 created/paused/running，不得调用 start/resume，不得传 `max_steps`。
4. Go Application Service 负责：接收用户消息、获取/创建 active Run、必要时 start/resume、连续执行 Supervisor turn，直到 finish、wait、approval、cancel、budget 或 fatal error。
5. 新建对话没有 Run 创建向导。用户选择/继承工作区后直接看到空白对话；第一句话创建 Thread + Run 并自动工作。
6. Run terminal 后，同一 Thread 仍可继续输入；内核按现有 successor 规则创建后继 Run，且不复制 capability grant、lease、approval 或凭证权限。
7. 不为 UI 增加数据库 migration、永久事件协议、receipt 类型或 readiness evaluator。Thread turn 复用现有消息、Run event、handoff 和 operation key。

## Narrative Projection

默认界面必须让用户看到故事，系统继续保存事件。投影规则：

- operator message → 用户消息。
- 连续公开 commentary/final → 一段自然的助手消息。
- 同一类连续 search/read/edit/execute/verify → 一个可展开的工具活动组。
- tool request、arguments、start、output、completion、command receipt、verification receipt 不得逐条占据时间线，但不得因此隐藏执行事实。活动组收起时显示安全摘要；展开后按需读取由 Go 脱敏投影出的命令、工作目录、Agent、执行环境、状态、退出码、耗时以及有界 stdout/stderr。搜索/抓取、文件读写、验证、MCP 与浏览器动作使用同一惰性详情入口，但只返回工具类别允许公开的类型化事实，绝不下发原始工具 JSON。
- 连续命令继续聚合；运行中直接读取当前进程持有的有界输出 ring tail，失去进程所有权后才回退到持久快照。失败项默认展开错误摘要；截断输出通过 Thread 与活动双重绑定的 opaque artifact 引用按需加载完整脱敏产物。普通对话不得接收原始 `payload_json`、环境变量值、凭证、stdin 或隐藏推理；完整审计事实继续留存在 durable ledger 与用户主动触发的完整导出中，Inspector 的 JSON 视图只能接收 Go 端严格白名单化的元数据投影。
- approval request/resolution → 只有需要用户动作时才挂载可操作 Approval Card。
- Run boundary、Harness checkpoint、selection drained、普通 started/running 等内部噪声默认隐藏。
- 失败、阻塞、交付和验证结论保留为人类可理解的 notice。
- 原始 transcript、Run/Event/Receipt 留在 Inspector。

产品叙事示例：

```text
我先检查运行失败的原因。

⌕ 搜索了 “AgentRunner”
↳ 读取了 3 个相关文件

问题已经定位：执行循环在返回 continue 后被前端截断。

✎ 修改了 2 个文件
⚙ 运行测试
  ✓ 318 项通过
```

不得回退成：`Harness 事实 · 运行中 · R1 #26`。

## 默认桌面工作面

默认侧栏只包含：

- 当前 Workspace / 项目分组；
- 新对话；
- 紧邻“新对话”的“接入模型”入口；
- 搜索历史；
- Thread 列表；
- 设置。

“接入模型”是本轮用户明确要求的唯一主导航例外，并与设置中的“模型”指向同一产品页。Pull Request、插件、定时任务等仍不得和“新对话”同级占据主导航；它们进入设置、命令面板或 Inspector。

主工作面：

- 顶部只显示对话标题和简洁操作；不显示 Thread/Run/Mission ID。
- 中部显示自然语言和聚合后的工具叙事。
- Composer 永远位于可见视口底部；窗口缩短时中间叙事滚动，输入框不被挤出。
- 权限 popover 锚定 Composer 权限 chip；状态来自真实 Thread permission API，不能根据 Run 状态自行猜测。
- archive/restore/delete 使用真实 Thread lifecycle API，并按 `hasThreadControl` 禁用写操作。
- Approval Card 仅在 active Run 真实处于 `waiting_approval` 且 queue 非空时出现。
- Inspector 由标题菜单或设置主动打开，显示 Thread/Mission/Run ID 和旧控制面入口。

## Apple 式材质与交互约束

- 指针按下必须立即有反馈；发送按钮使用短促 scale，开关、菜单和列表行即时变色。
- 浮层从触发源出现，支持 Escape、外部点击、焦点圈与焦点回收；动画必须可打断。
- 材质层级要有物理含义：titlebar、sidebar、main、composer、popover/dialog 的透明度和 blur 逐级增强。
- 支持 `prefers-reduced-motion`、`prefers-reduced-transparency`、forced colors 和键盘导航。
- 不把玻璃当作装饰噪声；透明度降低时必须回退为可读的不透明 surface。

## 网页证据、搜索与受控浏览器

- 网页证据与 Full CDP 是两条独立能力。`web_search` / `web_fetch` / `web_citation` 用于公网资料检索、受限抓取和可追溯引用；Full CDP 只操作用户已显式打开的任务专属浏览器会话，不得替代搜索授权。
- 搜索后端按当前模型供应商的显式策略选择：`provider_native` 使用已经通过能力探测的供应商托管搜索，`searxng` 使用用户配置的自托管搜索，`disabled` 不提供搜索；`auto` 只在已验证原生能力可用时选 `provider_native`，否则选择已显式配置的 SearXNG。不得仅凭 URL 含 `/responses` 推断托管工具，也不得在失败后暗中切换到另一个收费服务。
- 供应商设置页负责选择搜索策略；当前 SearXNG JSON endpoint 仍由 Desktop 启动配置 `CYBERAGENT_WEB_SEARCH_ENDPOINT` 绑定。SearXNG 是通用兜底，不是所有模型的必经代理；没有可用搜索后端时，`web_fetch` 与引用能力仍可用，`web_search` 必须明确报告 provider unavailable。后续若增加页面内 endpoint 编辑，必须使用独立、无凭据的持久配置与热重载契约，不能把 endpoint 塞进未执行的高级 JSON。
- 供应商原生搜索结果必须归一化为 Traverse 的 Run-local Source/Citation 证据链，并标注 `provider_grounded`、供应商 binding 和来源 URL。由供应商搜索工具返回的来源可直接成为“供应商佐证引用”，无需再次 `web_fetch` 才能回答；正文仍是不可信内容，后续直接抓取、浏览器访问和本地网络权限保持独立。原生搜索不等于任意 URL 抓取，更不等于浏览器控制。
- Provider API 出站与网页抓取授权必须彻底分离。调用模型/托管搜索只使用当前 Provider endpoint 的私有传输 authority，不得把该 endpoint 写入网页白名单，也不得据此扩大 `web_fetch`。
- 保守、工作区访问与逐次审批默认不拥有任意公网抓取。`web_fetch` 遇到未授权的新公网 HTTPS 主机时进入就地授权：用户选择“允许一次 / 本对话允许 / 拒绝”后，内核在当前 Turn 自动重试或返回拒绝，不结束 Run、不要求重建 Thread；精确 hostname 白名单保留为高级严格模式。授权必须绑定当前 Thread/Run/工具调用、幂等键和实时 capability fence。
- Full Access 与 Debug 默认允许匿名访问任意公网 HTTPS；仍硬拒私网、loopback、link-local、云 metadata、DNS rebinding、非 HTTPS、未复核重定向，并继续限制方法、跳转、响应体和超时。UI 必须显示“公网 HTTPS”，不能因 durable exact-host 列表为空而误报“无网络”。
- 直接搜索后端返回的普通发现记录仍是未信任线索；只有带有效 provider-grounded provenance 的托管搜索结果可直接引用。对需要读取正文的 URL 仍执行当前模式的抓取授权和安全策略。
- `web_fetch` 必须继续执行 HTTPS canonicalization、DNS/public-IP 固定、重定向逐跳复核、metadata/SSRF 拦截、MIME、响应体、超时和凭证清理。Conservative、Workspace 与 Approval 强制执行 robots policy；Full Access 与 Debug 仍展示和保存 robots 审计事实，但 disallow、缺失或无法确认不得阻断抓取。robots 审计绕过不放松私网、loopback、云 metadata、DNS rebinding、HTTPS、重定向、响应体或超时硬边界，也不表示获得版权、许可或站点条款授权。网页正文永远标为 untrusted evidence，不能成为授权来源。
- 模型浏览器工具只包含 `browser_status`、`browser_navigate`、`browser_snapshot`、`browser_click`、`browser_type` 与 `browser_screenshot`；不得注册 Open/Close、任意 CDP method 或提权工具。只有 Root Supervisor、Full Access/Debug、已确认 Full CDP、operator-opened ready session 与实时 fence 同时成立时才可见。
- 模型浏览器导航保持单一 literal-loopback origin。Selector 必须来自当前页面最近一次受控 snapshot 并绑定 document identity；导航、页面身份变化、撤权或会话变更立即使其失效。截图只写入 Workspace 的受控 artifact 目录并返回 locator/SHA/bytes，不把 PNG base64 塞进普通工具 JSON。

## 自定义模型供应商与 Harness

- 模型页使用两列小卡片，第一张固定为齿轮图标的“自定义配置”，随后提供独立的 ChatGPT 订阅账户入口与 API 供应商卡片，显示供应商、模型和连接状态；卡片左侧使用白色填充图标槽与深色单色品牌图形。窄窗改为单列，并覆盖 reduced motion、reduced transparency、high contrast 与 Windows forced colors。
- 10 个 API 预设依次提供 Claude、OpenAI、DeepSeek、Gemini、Grok、MiniMax、MiMo、Kimi、Kimi for Coding 和 OpenCode Go。API 预设只提供可编辑的官方端点、协议、模型和搜索策略初值，不是锁死模板；进入编辑器后用户仍可修改模型列表、请求地址与高级 JSON。
- Kimi 开放平台与 Kimi for Coding 必须使用独立卡片、供应商 ID、域名、Key 和配额，不能互换。OpenCode Go 明确标为订阅网关，不冒充模型厂商。ChatGPT 订阅通过独立账户页面登录、选择账户并检查模型；真实账户授权、订阅推理与撤销的验收状态见 [账户登录验收记录](../../../docs/acceptance/2026-10-15-chatgpt-sign-in.md)。GitHub Copilot 登录尚未实现，模型目录不提供占位入口。
- 模型模块中的“自定义配置”允许用户持久新增供应商；“集成”下不再保留重复的“智能伙伴”分类。供应商定义和 API 凭证必须分离：名称、协议、端点、模型映射和能力策略写入 SQLite；API Key 只进入操作系统凭证管理器，任何 GET、事件、日志或导出均不得回传明文。
- 高级 JSON 是用户可完整编辑的供应商配置层，不要求遵循产品预置模板；保存前必须进行重复键、体积、深度、危险 header、URL 凭证和 Harness 保留字段校验。需要认证的 JSON 位置使用 `{"$credential":"provider-id"}` 引用，运行时才从系统凭据库解析。API Key 表单默认把这个引用同步到协议对应的 `request_headers`，用户可以关闭同步、移动引用或修改模板；同步和持久化的始终是引用而不是明文。用户粘贴明文密钥时，UI 必须先显示迁移确认，把密钥写入系统凭据库并用引用替换；取消迁移则不得保存明文。
- 用户配置可以新增 header、环境映射和供应商扩展字段，但不得覆盖 `model`、`messages`/`input`、`tools`、`tool_choice`、`stream`、`response_format`/`text`、`previous_response_id` 等由 Harness 持有的协议字段。UI 必须明确标出冲突位置，不能静默丢弃用户 JSON。
- 当前 HTTP Harness 只执行 `request_headers`、`request_body` 与 `model_mapping` 三个标准容器；`env` 和任意未知扩展仍完整持久化、可编辑和导出，但在存在对应进程型 Harness 前不得擅自写入应用环境或执行。用户 JSON 的“可保存”与某个适配器的“会解释”必须在页面上明确区分。
- 协议类型至少区分 Anthropic Messages、OpenAI Chat Completions、OpenAI Responses 和本地 Ollama。所谓“OpenAI-compatible”只说明 wire shape；函数工具、严格 JSON、流式 item、原生 web search 等能力必须分别探测、保存 binding digest 并经过 Harness qualification，不能按品牌或路径猜测。
- 用户可填写 Base URL 或完整请求 URL，但 UI 必须显示最终 canonical endpoint 和实际认证方式；禁止 URL 内凭证、跟随跨源凭证重定向和任意认证字段名。高级 JSON 中的认证只能引用当前供应商的系统凭据；公网端点默认 HTTPS，loopback 本地供应商使用单独、明确的本地端点模式。
- 每个供应商保存 `provider_native / searxng / auto / disabled` 搜索策略。切换供应商、模型、端点或定义 revision 会使旧能力探测与 Harness qualification 失效；路由只能选择当前可用且与 exact model binding 匹配的供应商。
- Composer 发送按钮左侧提供线程级模型路由切换器。第一层显示模型、推理强度、速度与重置默认；模型第二层按供应商分组列出 `provider + exact model`，不可选择项原位显示凭据或 Harness 未就绪原因，底部固定“管理模型供应商…”并返回设置中的模型模块。
- 新对话选择的 `provider + model` 必须随 `thread_creation.v1` 原子固化，保证首轮就使用所选路由；不得先创建后补写。已有 Thread 的切换只写入 successor/next Run，当前正在运行的 Run 保持不变，并在 Composer 明确标注“下一轮”。

## Inspector、任务权限与调试运行时

- 完整 Inspector 使用无查询参数的 Legacy SPA 命名空间：`/legacy`、`/legacy/threads/{id}`、`/legacy/runs/{id}` 与 `/legacy/sessions/{id}`。旧的 `?legacy=1` 不再是导航协议；UI 请求继续拒绝全部查询参数。
- v2 到完整 Inspector 的内部跳转必须使用同一文档内的 History API 导航，保留仅驻留内存的连接凭据，禁止把 bearer 写入 URL、`localStorage` 或 `sessionStorage`。用户主动刷新或从外部深链进入时，仍由 Wails bootstrap 恢复连接，普通浏览器则重新连接。
- Full Access 是当前 Thread 的权限档位：React 显示 Codex 风格风险清单，确认后通过 Thread permission API 即时生效，不重启应用、不改变其他 Thread；切到低档位必须能够立即撤销后续高风险授权。
- Debug 继承 Full Access，并额外开放持久终端、后台进程与单独限时授权的 Agent 终端输入。只有这些长生命周期调试能力需要应用级初始化，因此 Debug 保留“确认并重启”入口；该入口不依赖已选 Thread。
- 完整 CDP 是 Full Access/Debug 下的可选子开关，不是同级权限档。进入 Full Access 或 Debug 时默认开启；权限页可立即关闭，重新开启必须确认 Cookie、请求捕获/修改/重放和任意 CDP 方法风险；保守、工作区访问与逐次审批不得开启。它只约束 Traverse 管理的隔离浏览器，不接管系统浏览器或 Wails WebView，选择本身也不会启动浏览器。
- `desktop_risk_restart.v1` 仅用于 Debug 调试运行时重启。Renderer 不能提交 executable、argv、PID、cwd、env 或任意启动参数，也不得用重启桥实现 Full Access 切换。
- 父进程与 helper 使用内部生成的匿名管道和 256-bit READY token；helper 验证真实父 PID、同一 executable 并取得进程 waiter 后才回传 READY，父进程只有收到精确握手才退出。失败、超时或伪造响应会回收 helper，当前安全进程保持运行。
- Debug 重启不会改写任何 Thread 已保存档位；重启后仍由内核逐任务复核。Debug 运行时只在本次应用会话有效，完整退出后普通启动会恢复标准运行时；Full Access 仍可按 Thread 即时选择。

## 真实交互验收

| 场景 | 必须结果 |
| --- | --- |
| 打开软件 | 立即看到 Thread 列表与输入框 |
| 新建对话 | 输入第一句话即可创建，不先配置 Run |
| 接入模型 | 主侧栏与设置均可进入同一模型目录；预设卡打开可编辑草稿，自定义卡保留完整高级 JSON，ChatGPT 使用独立账户登录入口 |
| 发送消息 | React 只提交一个 turn；Go 自动工作到自然停止边界 |
| Agent 工作 | 显示自然语言以及聚合的搜索、读取、修改、运行、验证 |
| 内部状态 | 默认不显示 event sequence、Run ID、stage |
| Run terminal | 同一 Thread 仍能继续输入 |
| 等待审批 | 仅需要动作时显示真实 Approval Card；按钮回读 API 状态 |
| 权限 | 读取真实 Thread permission；高风险切换确认并显示后端 effect |
| 浮层 | 权限 menu、线程 menu、确认 dialog 支持点击、Escape、焦点与取消 |
| 归档 | 侧栏归档后消失；设置归档页可真实恢复/删除，并回读持久状态 |
| 窗口缩短 | Composer 始终可见 |
| 查看审计 | 通过 Inspector 主动进入旧 Run/Event/Receipt 页面 |
| UI PR | 包含真实截图、diff/metrics、黑盒流程和 Wails 构建，不只合同测试 |

## 测试合同

- 所有测试运行时使用 `scripts/ui-test.ps1` 提供的 D 盘隔离环境。
- 两张用户提供的 PNG 先按本文件的哈希验证，再暂存到 gitignored `.tmp/reference-baselines`；图片本身不得提交。
- 主工作台与设置页分别拥有 capture recipe，不得强行共用 viewport/crop。
- 每次视觉运行输出 `actual.png`、`diff.png`、`metrics.json` 到 `output/playwright`。
- 全图差异用于报告；稳定 ROI（侧栏宽、toolbar、内容宽、Composer、设置卡和行高）用于门禁；身份、动态内容、滚动条、Mica 区域使用 mask/独立阈值。
- 浏览器黑盒测试必须同时验证 DOM 结果和真实 API/持久状态。原生 Acrylic 另用固定 Windows 主题、150% DPI、固定背景的 Wails 截图验证。
- 发布前至少通过 API schema、TypeScript、Vitest、相关 Go application/httpapi/store 测试、Wails desktop tags、生产 bundle 和 Windows EXE 构建。

## 禁止事项

- 禁止把参考目录或其 bundle 加进 Git。
- 禁止复制专有 React 组件或大段 CSS；只能重写提取出的 token、几何和行为。
- 禁止在 v2 组件里恢复 `start/resume/execute(max_steps)` 编排。
- 禁止把 Run/Session 查询放回默认侧栏。
- 禁止为了一个 UI 状态新增 migration/receipt/readiness evaluator。
- 禁止用旧参考包截图替代用户提供的新基线。
- 禁止只做 mocked jsdom 后宣称完成真实输入、权限、归档或 Wails 交互验收。

一句话验收：**用户看到工作叙事，系统保存可审计事件；默认工作面像一个 Code Agent，而不是状态机调试器。**

## 本轮验收记录（2026-08-30）

- 全量 Web 回归：86 个测试文件、422 项测试通过；TypeScript 与 Vite production build 通过。
- Go 定向回归：Thread turn application、HTTP/OpenAPI、Desktop 真实模型链路、Wails desktop tags、重启 READY/失败路径与 visualdiff 通过；Windows 重启测试另通过 race detector。
- 本机浏览器诊断 + Go API：首句创建后自动进入 `/turns` 并执行到自然边界；输入、权限升级确认/API 回读、浮层、归档、设置恢复均完成一次闭环。它在真实浏览器 capture flow、固定 ROI/mask/threshold 和可回放状态夹具进入仓库前只算人工证据，不作为可复现 CI 门禁。
- 本轮权限结构调整为 Thread 级 Full Access 即时确认与应用级 Debug 重启；无 Thread 时 Full Access 不可选择，Debug 入口仍可见。完整 CDP 作为 Full/Debug 的默认开启子开关，可即时关闭、风险确认后重新开启。高风险详细页默认聚焦“取消”，取消后回焦到原激活按钮。Inspector 抽屉具备 dialog 语义、背景隔离、Escape、Tab trap 与稳定回焦；完整页只跳转 `/legacy/...`。
- 权威尺寸截图：主工作台 `2052×1371`，设置页 `2050×1357`。本机稳定几何 ROI 在 channel threshold 18 下得到：主工作台 diff ratio `5.9023%`、MAE `6.0261`；设置页 diff ratio `4.5475%`、MAE `2.4503`。这些参数尚未固化进清单，当前只作诊断记录。
- 本轮设置页全图浏览器诊断在 `2050×1357`、channel threshold 16 下为 diff ratio `11.2990%`、MAE `5.1952`；该值包含无原生 Acrylic 的合成差异，只作报告，不作为稳定 ROI 门禁。
- 严格全图零容差结果保留为诊断且预期失败（真实内容、字体抗锯齿和 Mica 合成不同），不得用提高全图容差伪装为像素相同。
- Windows portable EXE 已连续构建验证可复现；产物与截图/diff 均在 D 盘的 gitignored `build`、`.tmp`、`output/playwright` 中。
