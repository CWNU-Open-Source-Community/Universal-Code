# ADR 0181: MCP 内容投影与服务端协议互操作

Date: 2026-10-14
Status: Accepted for [Issue #319](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/319).

MCP Tools、Resources 和 Prompts 返回的内容统一作为外部工具结果保存。模型上下文
保留结构化 JSON 数字精度和文本；Prompt 的远端角色不会提升为宿主指令。资源链接
只作为引用，构造上下文时不读取宿主文件、不发起网络请求。

模型声明支持 vision 时，PNG/JPEG/WebP 的 base64 图片及资源 blob 经既有图片验证器
验证实际格式、字节、尺寸、像素数和摘要，再进入用户工具结果消息的图片通道。
图片沿用现有模型预算，每条消息至多四张。原始结果保持不变，派生文本移除二进制编码，
保留来源和验证后的摘要。无 vision、无效图片、超出预算和不支持的二进制格式都给出
明确状态。当前模型适配器没有音频输入通道，模型不会收到音频字节。对话和 Inspector
的活动详情从已保存结果生成有界内容投影，显示文本、资源来源、验证后的图片及
WAV/MP3/Ogg 音频播放控件。音频不预加载、不自动播放；资源链接只在用户点击时打开，
HTML 作为文本显示。投影至多 16 项、每项文本 32 KiB、总计 128 KiB，省略、截断与
媒体加载失败均显示提示。历史记录缺少这些可选字段时继续显示原有摘要。

扩展能力审核同时显示 Prompt 参数名称、说明及必填状态，来自 Go 持有的发现目录。

原有结果上限仍为每次 128 KiB，且可由登记配置进一步收紧。过大结果先明确省略媒体；
剩余内容仍超限时保存有界 JSON 预览及截断标记，避免字节截断产生无法解析的半个对象。
此限制意味着大图应由 MCP 服务提供较小表示。图片与文字沿用现有凭证和敏感字段脱敏，
不增加宿主工具权限或新的持久化媒体目录。

内置 stdio 服务端接受 2024-11-05、2025-03-26、2025-06-18、2025-11-25 的 legacy
initialize 协商；未知版本返回支持的 legacy 版本，由客户端决定是否继续。2026-07-28
使用每请求 metadata 和 server/discover；每个现代请求独立检查固定 Run 是否存在。
现代结果包含 resultType，目录与资源结果使用私有、立即过期的缓存提示。
标准 metadata 和省略的空工具参数可被读取，工具参数本身仍由既有闭合 schema 校验。
服务端继续只发布实现了的工作区读取工具和 Run 资源，调用仍经过现有 Tool Gateway。
Gateway 拒绝或失败会返回 isError，客户端不会将其误认作成功。

标准 Go SDK 互操作测试覆盖现代协商、目录、空模板目录、工具调用和资源读取；原始
JSON-RPC 测试覆盖 legacy 版本、未知版本协商和拒绝结果。模型投影测试覆盖四种图片
位置、大整数、无 vision、无效媒体、音频说明和原始结果身份校验。

回滚可停用新的模型投影和现代服务端入口；必须保留历史 MCP 内容载荷读者。已有
结果仍可作为普通工具结果读取，原始记录与授权状态无需迁移。

协议依据：[Resources](https://modelcontextprotocol.io/specification/2026-07-28/server/resources)、
[Versioning](https://modelcontextprotocol.io/specification/2026-07-28/basic/versioning)。

## 当前 Workspace 的 MCP roots

宿主只向 `roots/list` 返回当前已选 Workspace 的单个 `file:` URI。工作区作用域
的显式发现使用其发现批准和有效 lease，因此无需先创建或运行一个任务。普通 Run
使用登记根；拥有 Drydock 的 Run 复用文件工具的真实目录检查和当前归属，目标缺失
或漂移时报错，不回退到源目录。启动 cwd、用户目录和其他项目不会加入结果。

连接绑定该物理根的指纹；后续根变化重建连接，而非复用第三方缓存的旧根。每次
服务端请求都重新验证服务器状态、当前任务或发现上下文和物理目录。持久连接使用
独立任务检查，短连接使用该次连接批准；均不消费或复用工具调用的单次派发许可。
撤销或失去该上下文时返回空 roots；发送前失效的旧结果会被拒绝。根列表描述上下文，第三方服务的实际调用继续
走既有权限流程；该元数据不是操作系统对第三方可执行进程的隔离措施。

[Claude Code 官方说明](https://code.claude.com/docs/en/mcp)返回启动目录及用户授予的
附加工作目录，并通知目录变化。[所核对的 Codex 固定源码](https://github.com/openai/codex/blob/ac9db7d8b556c7c34a73793aed34b1f980a4fe39/codex-rs/codex-mcp/src/rmcp_client.rs#L1058-L1080)
从默认空能力只加入 elicitation 和 extensions，未启用 roots。本项目仅提供自身
已经授权的一个 Workspace，保留自己的目录归属和生命周期检查。
[MCP SEP-2577](https://modelcontextprotocol.io/seps/2577-deprecate-roots-sampling-and-logging)
已在 2026-07-28 将 roots 标记为 deprecated；这里保留现行服务所需的互操作。

真实服务验收固定使用官方 `@modelcontextprotocol/server-filesystem@1.0.0`，2026-10-15
查询 npm registry 的 `dist-tags.latest` 和 `version` 均为 `1.0.0`，metadata 更新时间
为 `2026-10-11T02:58:37.880Z`。该[官方服务](https://github.com/modelcontextprotocol/servers/tree/main/src/filesystem)
在初始化后立即请求 roots；测试在尚无 Run 时完成登记、发现和启用，不传允许目录
参数，再由真实 Supervisor/Gateway 验证根目录列举、根内中文文件读取及根外读取拒绝。
`TestMCPWorkspaceRootsOfficialFilesystem` 通过环境变量
`UNIVERSAL_CODE_MCP_FILESYSTEM_PACKAGE` 接收仓库外已安装的这个固定 npm 包目录。
