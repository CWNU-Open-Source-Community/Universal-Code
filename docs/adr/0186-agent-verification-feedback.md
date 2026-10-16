# ADR 0186: 修改后诊断与服务地址反馈

Date: 2026-10-15
Status: Accepted for the bounded feedback work in [Issue #339](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/339).

`workspace_apply` 的既有 `agent-code-tools.v1` 成功结果增加 `diagnostics`。
字段包括 `status`、`path`、`document_sha256`、`coverage`，以及可选的
`server_id`、`reason` 和既有 LSP `result`。覆盖范围固定为
`single_document_single_server`：按 ID 选择一个已初始化且支持该路径的诊断
Server，移动文件使用目标路径。摘要最多五项诊断、12 KiB，写前和写后查询
各有一秒预算。

诊断请求绑定写入收据中的 SHA-256，查询前后校验当前文件，沿用 LSP 文档版本、
Workspace 和 Server generation 校验。配置更换不能借此冷启动新 Server。
pull 结果须有完整诊断列表；push 结果须明确携带当前文档版本。状态
`current` / `partial` 附带现有语义证据，`pending` / `unavailable` / `timeout`
说明尚无相应结果，均不改变已成功写入的事实，也不作为无错误结论。
已有 `code_diagnostics` 可用于后续或完整结果查询。

2026-10-16 增加 `diagnostics.delta`：replace 在写前即时查询原始 SHA-256，
与写后结果按 source、code、severity、message、range 精确比较，返回
`added`、`resolved`、`persisting`，空类别省略。`status: current` 附带原版本
`baseline_provenance`；两端须为 `current`、完整未截断，且 Workspace、URI、commit、branch、
Server descriptor 与 capability 指纹一致。descriptor 包含受审 executable
路径及 SHA、参数、初始化选项和配置来源。Git dirty 内容改变导致相同配置的
Server 重启时可跨 generation 比较，两版保留各自 provenance；既有语义证据的
失效规则不变。比较只描述两次观察间的诊断变化，同时发生的其他文件改动也
可能影响结果。位置改变记为旧位置消失及新位置出现。

旧缓存、无版本/过期结果、截断结果不能充当基线；create/move 和重放不构造
历史结果。上述情况以及诊断失败或摘要超限时，delta 返回
`status: unavailable` 与具体 `reason`，省略分类，不改变当前诊断和写入收据。

`command_runtime` 的既有结果增加可选 `candidate_urls`，每项包含 `job_id`、
`url`、`source: command_output`、`verified: false`。候选只来自该次已清洗的
输出页，并关联同次结果中的 Job；复用操作员服务视图的 loopback、凭据及
query 过滤，按 Job 去重，最多八项。没有候选不等于服务未启动，有候选也不
证明服务就绪。读取后续输出继续使用原 Job 的 read/wait。

`browser_navigate.v2` 可带 `service_job_id`，将候选网址交给当前任务拥有的
命令执行器核对。Windows Host 后端要求 Job 仍存活、权限与任务身份仍匹配、
网址实际出现在该 Job 的保留输出中，并通过 retained Job Object 确认监听进程
归属。随后进行最多两秒的 HTTP GET；禁止代理、凭据和自动跟随重定向，TLS
仍使用正常证书校验。响应 2xx 后才导航，导航结果须保持相同 origin（默认
端口与省略形式等价）。探测前后均再次核对权限与进程归属。

结果 `service` 包含 `job_id`、`url`、`state`、`verified`、`http_status` 和
`checked_at`，失败另带 `reason`。`navigated: false` 表示尚未启动导航；Agent
应继续读取原 Job 或稍后重试，不能把网址输出当作服务就绪。readiness 只证明
检查时服务响应，页面功能仍须点击、快照等实际复验。当前 sandbox 后端不能
提供相同的 Host 监听归属证明，明确返回 `unavailable`，不转用另一个后端。

Agent 浏览器目前仅在 Windows 启用。状态 API 增加可选 `unavailable_code`，
界面区分平台不支持、需要 Full、任务或会话失效、运行时不可用；macOS/Linux
明确提示手动检查，不能记录为 Agent 浏览器验收。后台浏览器显示独立后台
运行和截图提示。读取状态不会启动浏览器。

诊断沿用当前 Run、root、路径排除和策略检查；命令与浏览器继续各自授权。
验证覆盖真实 LSP 协议子进程与 SQLite 写入链路、版本拒绝及失败反馈、
清洗后的分 Job URL 输出，以及 Windows Job 内外的真实 IPv4/IPv6 HTTP
监听器。诊断增量另以
gopls v0.21.1 在 Windows 普通目录和 Git 项目中实测新增、遗留与修复后消失的
类型错误。模型修复任务与 dev server 浏览器整体验收仍需单独记录。
