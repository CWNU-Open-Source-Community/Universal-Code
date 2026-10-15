# ADR 0180: 运行中 MCP 的操作员输入

状态：已实现

关联：#319；ADR 0177；MCP 2026-07-28 elicitation。

## 问题与决定

已授权的 MCP 调用可能返回 `input_required`，或在旧协议会话中发出 `elicitation/create`。模型不能代替操作员回答授权问题。Go Manager 持有仅存在于内存的 InputBroker，将请求绑定到当前 Run、Server、原始 Operation 和现有授权复检函数。HTTP、CLI 和对话界面使用同一个运行中 broker；重启后没有可恢复的输入队列。

现代协议每次只在操作员明确答复后发送一个新的 dispatch attempt。原始 tools/call、resources/read、prompts/get 参数保留；只增加收到的 requestState 和对应 inputResponses。SDK 自动 MultiRoundTrip 保持关闭。每轮绑定完整参数指纹，重新检查原调用权限、actor/attempt、lease、服务器配置和能力快照，再授权新 attempt。共最多四次 dispatch，每轮最多八个输入请求；空输入的负载退让、sampling、roots 与不支持的模式直接失败。

旧协议的反向请求仅能绑定到已经通过发送检查的活动调用。初始化、发现和空闲会话不能提出可答复的请求。HTTP 答复在刷新凭据前预检，并在刷新后、实际发送前复检权限且消费一次性许可。网络超时在操作员等待期间暂停，答复后恢复；整个宿主调用最多十分钟，每个输入最多五分钟。取消、Run 撤销和授权改变终止等待。已经发出的未知结果仍按原有持久账本禁止自动重放。

## 输入和控制边界

表单支持平面 string、boolean、number、integer，以及字符串单选和多选；包括 enum、带标题的 oneOf、array items.enum/anyOf、默认值和常用长度/数值/格式约束。Go 离线编译并验证 schema 和答复，拒绝外部引用、嵌套对象、未知字段及明显凭证字段。请求进入 broker 前拒绝任何包含客户端已知连接凭据的消息、schema 字段或 URL（含 URL 转义形式），不修改 schema 选项值。标准的敏感信息流程使用 URL 模式；URL 必须为无 userinfo 的 HTTPS 地址。界面展示发起服务、目标主机和外部链接，不自动访问。操作员点击外部链接后，确认完成发送 accept 且无 content；两种模式均支持 decline/cancel。

GET `/api/v1/runs/{run_id}/mcp-inputs` 使用读取令牌；POST 到具体 input_id 使用控制令牌并检查控制功能启用。答复只投递一次，跨 Run、重复、过期或撤销答复被拒绝。HTTP 禁止缓存；DTO 不含原始操作参数、内部授权或续接状态。答复不会写入 DB、事件或日志，UI 不使用 mutation cache 储存答案；反射在最终文本中的回答字符串与 URL 会被脱敏。CLI 从 stdin 读取 JSON，只向 loopback live API 发送，禁止重定向，不输出答案。

## 验证

定向测试覆盖真实 TLS 的三种调用方法、操作员 accept/decline/cancel、撤销与取消、旧 SDK 反向请求、表单约束、URL 限制、单次消费、HTTP 读写权限、CLI 保密、对话卡片以及 SQLite Supervisor 中 Ask/Auto/Full 同一 durable call 的续接与完成后不重放。

参考：https://modelcontextprotocol.io/specification/2026-07-28/client/elicitation
