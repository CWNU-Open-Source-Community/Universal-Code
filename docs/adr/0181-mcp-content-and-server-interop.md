# ADR 0181: MCP 内容投影与服务端协议互操作

Date: 2026-10-14
Status: Accepted for [Issue #319](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/319).

MCP Tools、Resources 和 Prompts 返回的内容统一作为外部工具结果保存。模型上下文
保留结构化 JSON 数字精度和文本；Prompt 的远端角色不会提升为宿主指令。资源链接
只作为引用，构造上下文时不读取宿主文件、不发起网络请求。

模型声明支持 vision 时，PNG/JPEG/WebP/GIF 的 base64 图片及资源 blob 经既有图片验证器
验证实际格式、字节、尺寸、像素数和摘要，再进入用户工具结果消息的图片通道。
图片沿用现有模型预算，每条消息至多四张。原始结果保持不变，派生文本移除二进制编码，
保留来源和验证后的摘要。无 vision、无效图片、超出预算和不支持的二进制格式都给出
明确状态。当前模型适配器没有音频输入通道，模型不会收到音频字节。对话和 Inspector
的活动详情从已保存结果生成有界内容投影，显示文本、资源来源、验证后的图片及
WAV/MP3/Ogg 音频播放控件。音频不预加载、不自动播放；资源链接只在用户点击时打开，
HTML 作为文本显示。投影至多 16 项、每项文本 32 KiB、总计 128 KiB，省略、截断与
媒体加载失败均显示提示。历史记录缺少这些可选字段时继续显示原有摘要。

扩展能力审核同时显示 Prompt 参数名称、说明及必填状态，来自 Go 持有的发现目录。

模型内联结果上限仍为每次 128 KiB，且可由登记配置进一步收紧。收到并经过原有
4 MiB JSON 安全边界的结果，先完成凭证及敏感字段脱敏；若超过内联预算，完整
JSON 通过仅宿主可见的 `FullContent` 交给 Gateway，再写入既有 `run_artifacts`。
内联保留可解析预览、原 `isError`/调用状态及 artifact 标识、摘要和字节数。
`full_result_read` 给出 `history_read` 请求；模型可按 UTF-8 偏移量继续读取，
每页验证同一 SHA-256。读取受当前 Thread 的 Run/Session/Workspace 链限制，
仅接受已有 MCP 调用来源，不创建新的工具权限。上下文预算不会删除完整存储。
原 4 MiB 网络帧、artifact 容量、图片与模型输入限制继续生效。

静态 GIF 保留原字节供支持图片的适配器消费；动画 GIF 给模型提供经过验证的
首帧 PNG，并明确标记 `first-frame-only`，对话图片仍使用原 GIF。实现只解码
受既有字节和像素约束的首帧，再扫描有界扩展块发现第二帧，不解码整段动画。
首帧 PNG 仍必须通过同一图片大小检查；模型不会被告知已经检查动画内容。

[Claude Code MCP output limits](https://code.claude.com/docs/en/mcp#mcp-output-limits-and-warnings)
将超过阈值的成功纯文本结果保存为文件并返回路径。本实现采用预览加可读引用，
并同样保留错误结果的完整脱敏 JSON，继续使用本项目已有的 artifact 和历史读取。
[OpenAI image inputs](https://developers.openai.com/api/docs/guides/images-vision)
明确接受 non-animated GIF，因此跨供应商投影不把动画直接作为可理解的模型输入。

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
