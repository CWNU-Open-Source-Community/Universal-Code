# ADR 0184: MCP SSE 与 WebSocket 兼容传输

Date: 2026-10-15
Status: Accepted for [Issue #330](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/330).

手动登记支持 stdio、Streamable HTTP、legacy SSE 和 WebSocket。Agent Plugins
已有的 `sse` 声明也进入同一 resolved launch 及 SDK client；插件格式未定义的
WebSocket 声明仍返回不支持的格式诊断。新传输使用现有 v3 手动描述符或原生
插件描述符，历史 v1 描述符的含义保持不变。

SSE 保留最初的接收连接，握手超时只限制连接建立；后续 RPC 使用各自的调用
上下文。WebSocket 使用 JSON 文本帧，每帧沿用 4 MiB 上限，并由同一个发送
guard 检查当前权限。连接失败不重放已经发出的操作。会话生命周期及目录更新
由 Manager 管理，与具体传输适配器分开。

远程地址要求 HTTPS/WSS，本机 loopback 可用 HTTP/WS。SSE endpoint 必须与
登记地址同源，HTTP 和 WebSocket 握手均不跟随重定向。固定非敏感 header 和
系统凭据库中的 Bearer 令牌沿用既有配置；协议及 WebSocket 握手 header 由宿主
管理。SSE 可使用既有 OAuth 流程，WebSocket 提供 header/Bearer 认证。

SSE 和 WebSocket 使用 legacy JSON-RPC profile，最高为 2025-11-25。选择宿主
默认版本时只保留该传输支持的版本；显式指定不支持的版本返回错误。
2026-07-28 profile 的传输绑定仅用于 stdio 和 Streamable HTTP，不能把其
`server/discover` 请求发送给 legacy SSE 服务端。

SQLite v196 仅扩展 `mcp_client_servers.transport` 的取值约束。迁移保留已有
rowid、描述符、状态、审计关联及不可变/代际触发器。回退旧程序前应停用新传输
登记；历史数据和已有连接登记不需要重写。

验证使用真实 Go SDK SSE 服务端，以及通过 WebSocket 连接适配的同一 SDK
服务端，覆盖发现、握手结束后调用、原生插件版本选择、禁用后拒绝、跨源与
重定向拒绝。迁移检查既有行内容和约束保留；前端检查登记表单到 API 解析。

参考：[Claude Code MCP 连接说明](https://code.claude.com/docs/en/mcp)、
[Go SDK v1.8.0 transports](https://github.com/modelcontextprotocol/go-sdk/tree/v1.8.0/mcp)。
