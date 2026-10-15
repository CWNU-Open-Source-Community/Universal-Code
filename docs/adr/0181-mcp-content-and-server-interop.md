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
明确状态。当前模型适配器没有音频输入通道，模型不会收到音频字节；用户界面的
媒体预览沿用保存的结果，由后续展示切片提供。

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
