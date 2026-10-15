# ADR 0177: MCP 客户端资源、模板与 Prompt 消费

Date: 2026-10-14
Status: Accepted for [Issue #319](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/319).

## 决策

模型通过现有 `mcp_tool_call` 消费 Resources、Resource Templates 和 Prompts。
旧 `mcp-client.v1` 工具调用及其 canonical JSON 保持不变。
新 `mcp-client.v2` 增加 `method`：`resources/read` 使用
`tool_name=resources.read`，`prompts/get` 使用 `tool_name=prompts.get`。
这些是宿主操作标签，真实远端工具清单仍只包含服务器发布的 Tools。

`arguments` 为资源的 `{uri}`、模板的 `{uri_template, variables}` 或
Prompt 的 `{name, arguments}`。Go 只接受已批准目录中的精确 URI、模板和名称；
模板使用现有 RFC 6570 库展开字符串变量，未提供的变量按 RFC 6570 省略。
Prompt 参数保留描述和必填标记，拒绝缺少必填参数及未知参数。
Supervisor 通过 [ADR 0182](0182-mcp-tool-search.md) 的本地搜索按需返回完整 schema，
并在准备调用、审批和执行时重新核对完整授权目录。
资源 URI 始终交给已选择的 MCP 服务，不由宿主直接打开文件或抓取网址。

## 授权与信任边界

2026-10-15：会话输入框的“添加内容”菜单可直接选择已启用服务的资源、资源模板和 Prompt。
参数以带名称的表单填写；Prompt 必填参数不可留空，模板空变量按 RFC 6570 省略。
前端提交 `mcp_references`，精确绑定服务、能力指纹、方法和完整参数，不写入让模型调用工具的提示词。
发送消息表示只授权这条消息中选定内容的本次读取；其余模型调用继续使用现有 Ask/Auto/Full 流程。
Go 在当前输入与执行租约下调用同一 Manager，重查目录、权限、运行代次、策略和 Hook。
目录指纹过期要求重新选择，不自动把已选择的条目替换成新目录。

读取资源和取得 Prompt 都可能启动进程、使用凭证或发送网络请求，因此复用
现有 `ExecuteMCP -> Manager.Invoke` 路径及 `OperationToolCall` 权限分类。
连接、目录发现、实际发送各自检查公共授权、Run/Workspace、actor/attempt、
permission/runtime fence、lease、注册代次及能力指纹。远端 annotations 不证明
操作安全；Ask/Auto 仍要求具体批准，Full 仍要求当前显式激活。

实际发送的 HMAC 绑定 launch、method、完整 URI/Prompt 名称与参数；SDK 注入的
协议 metadata 不参与业务输入指纹。读取与取得操作使用与 tools/call 相同的
发送一次 guard、请求身份绑定取消和 receipt。所有结果经已有凭证及敏感字段
脱敏与大小限制进入工具结果；Prompt 内的 role 不会成为宿主 system 指令。

## 持久化与失败

新目录字段 `resource_templates` 与 Prompt 的 `arguments` 均为 optional。
空字段不改变旧 snapshot JSON 字段序列或指纹；已有数据库无需迁移。
新增非空目录信息改变指纹，需要操作者重新批准，不能继承旧批准。

复用 Supervisor write-ahead intent、approval、result 与 receipt 账本。
发送前拒绝保留 `not_dispatched`；发送后取消/丢失响应为 `outcome_unknown`；
已收到 RPC 错误或 `input_required` 为 `result_received`，不会自动当成成功。
`input_required` 由 [ADR 0180](0180-mcp-interactive-input.md) 的宿主输入流程处理，
只在操作者答复后显式续接；SDK 自动多轮重试保持关闭。
结果落库失败后的恢复不重放读取或取得请求。

用户选择复用已有 `thread_message_intents` 的请求指纹和幂等键，以
`thread_mcp_references` 子行记录每个选择的 started、receipt 和结果。
连接前先持久记录开始；恢复时已保存的结果可继续使用，只有 started 而未保存结果的操作标为
`outcome_unknown`，不会再次读取。结果与不授予指令权限的 Session 参考资料原子保存。
当前输入的图片沿用已有 MCP 图片校验与模型投影；后续历史保留文字及图片描述。
超过预览上限的结果在同一事务中保存为现有 `run_artifacts` 的 `mcp_reference` 来源，
绑定真实选择的 operation ID；模型收到摘要、SHA-256 和 `history_read` 分页请求。
完整 JSON 经过同一脱敏边界，读取只允许当前 Thread 历史链内的材料。

## 验证与参考

定向测试使用真实本地 TLS peer 与 SQLite/Supervisor：资源、模板、Prompt 成功，
拒绝、目录漂移、取消、input_required、Ask/Auto/Full 批准及丢失结果不重放。
另验证旧 v1 canonical payload 和旧能力快照指纹。

协议依据：[Resources](https://modelcontextprotocol.io/specification/2026-07-28/server/resources)、
[Prompts](https://modelcontextprotocol.io/specification/2026-07-28/server/prompts)，
SDK `github.com/modelcontextprotocol/go-sdk v1.8.0`。

入口参考 Claude Code 的[资源引用](https://code.claude.com/docs/en/mcp#use-mcp-resources)和
[Prompt 命令](https://code.claude.com/docs/en/mcp#use-mcp-prompts-as-commands)，
以及 [Codex App Server 的 MCP 状态目录与资源读取接口](https://learn.chatgpt.com/docs/app-server)。
本产品采用现有“添加内容”菜单和命名参数表单，以消息内的结构化引用承载用户选择。
