# ADR 0182: 按需检索完整 MCP 模型目录

Date: 2026-10-15
Status: Accepted.

## 决策

Supervisor 向所有支持普通结构化工具调用的模型提供本地 `mcp_tool_search`，
按名称、描述和服务器检索当前 Run/Workspace 已授权目录。空查询分页浏览。
匹配结果包含工具、Resource、Resource Template 或 Prompt 的完整输入 schema，
以及 `mcp_tool_call` 所需的精确版本、方法、服务器、操作名和能力指纹。
模型按返回 schema 填入 `arguments`，继续使用现有调用与审批路径。

模型入口不再把前 128 项或 128 KiB 的远端 schema 嵌入每次请求，也不再把这个
上下文预算作为可执行目录的白名单。Supervisor 保留完整已授权目录，并在准备
调用时使用 `ValidateInvocation` 校验精确身份与参数。

## 分页与边界

搜索仅读取宿主缓存的已授权目录，不连接或调用远端。名称和描述使用 Unicode
不区分大小写的关键词匹配；名称匹配优先。无匹配项时返回空结果。
每页默认 5 项、最多 20 项，JSON 最多 128 KiB。条目不会被切断或悄悄略过；
返回的 `next_request` 绑定目录 revision、查询、服务器与下一偏移量。
目录变化使旧分页请求明确失败，模型从第一页重新检索。
单条 schema、描述与封装超过页面上限时返回明确错误，要求服务器缩小该条目。

协议发现层原有显式上限继续生效：每服务器最多 256 个工具、256 个资源、
256 个资源模板、128 个 Prompt，宿主最多 64 个服务器；超限报错。
这些上限与模型搜索分页独立。

搜索复用 ToolGateway、当前 actor/attempt、Run、Workspace、permission、runtime
epoch/fence 和 lease 检查。目录专用 authority v3 不含服务器调用绑定，不能用于
远端调用；调用保留原有 v2 精确注册绑定与运行时审批。搜索结果不授予权限。
禁用、撤销和调用时 schema/身份检查继续在原有执行路径生效。
远端描述与 schema 始终作为不可信数据处理。

迁移 v194 仅扩大现有 Supervisor 工具名与 authority 约束，保留历史行的 rowid、
payload、authority、actor 绑定和不可变调用身份。

## 参考与验证

[Codex tool_search.rs](https://github.com/openai/codex/blob/ac9db7d8b556c7c34a73793aed34b1f980a4fe39/codex-rs/core/src/tools/handlers/tool_search.rs)
使用宿主注册表中的延后工具信息建立 BM25 检索，再返回可加载工具定义。
[Claude Code MCP tool search](https://code.claude.com/docs/en/mcp#scale-with-mcp-tool-search)
同样按需加载定义，并说明部分代理或部署不支持 `tool_reference`。
本实现采用宿主检索与延后 schema 的共同思路，通过普通工具 JSON 返回结果，
适配现有多供应商调用流程。

定向测试覆盖真实 TLS/SQLite/Gateway 中第 128 项之后的工具检索与执行，
禁用与撤销、目录 authority 不能用于调用、未知操作、中文名称与描述、
工具/资源/模板/Prompt 分页、revision 变化以及结果字节边界。
