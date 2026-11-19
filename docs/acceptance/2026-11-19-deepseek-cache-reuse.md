# DeepSeek 工具调用的缓存复用

在官方 Responses 接口重放同一个 Standard Code 任务，后续四次请求的缓存命中率从 **43.70%** 提高到 **92.67%**。计入每组第一次冷请求后，五次合计从 **35.52%** 提高到 **75.43%**。

| 计入的请求 | 版本 | 命中输入 token | 未命中输入 token | 命中率 |
| --- | --- | ---: | ---: | ---: |
| 全部五次 | 修改前 | 18,432 | 33,467 | 35.52% |
| 全部五次 | 修改后 | 39,552 | 12,881 | 75.43% |
| 后续四次 | 修改前 | 18,432 | 23,751 | 43.70% |
| 后续四次 | 修改后 | 39,552 | 3,128 | 92.67% |

## 哪些内容打断了前缀

修改前，剩余输出额度位于提示开头。Standard Code 每次读取文件后，又会改写正文历史之前的步骤计数和状态版本。相同任务的第四、第五次请求均因此丢失了之前的开头，两次真实服务端回执各报告 0 个命中 token。修改后两次分别命中 9,472 和 10,112 个。五次请求发出的 18 项工具定义内容相同。

当前实现将稳定策略、工具权限说明和模型配置上限放在共享前缀，实际收紧的额度和最新执行状态随本次输入或工具结果交付。工作区路径排在通用指令之后，当前任务板和笔记排在对话历史之后。工具调用仍与原始结果配对。DeepSeek 会匹配已经持久化的前缀单元，新的文件内容和用户输入仍消耗输入 token。[DeepSeek 缓存规则](https://api-docs.deepseek.com/guides/kv_cache/) · [DeepSeek Harness 的系统提示投影测试](https://github.com/deepseek-ai/deepseek-harness/blob/2e39e70496aa02986ca6a820c5d497e816b69d7e/packages/core/agent-loop/tests/system-prompt-projection.spec.ts)

内置 Anthropic Messages 接口会将系统消息集中到顶层。在同一个本地回放中，旧版四对相邻请求的系统提示分别只共享 15,575、15,559、36、36 个 UTF-8 字节。修复后，四对都共享完整的 16,097 字节。这一项测量的是实际请求内容。[DeepSeek Messages 接口说明](https://api-docs.deepseek.com/guides/anthropic_api/)

## 测量方法

- 对照使用 `e41eecdd` 和 `8d68f4bb`。两版从同一个示例工作区和初始 SQLite 状态运行 Go AgentRunner。用户先让助手比较一个 Go 常量与测试，再指定要读取的说明文件。宿主完成三次 `workspace_read`，经历两次用户输入和五次模型请求。固定的模型回复使两版读到相同文件并执行相同操作。两次独立执行分别生成了工具产物编号。
- 将两版生成的十份输入按轮交错发送到官方 `/v1/responses`。使用 `deepseek-v4-flash`、`reasoning.effort=none`、`store=false`。服务端回报模型为 `deepseek-flash`。两组各使用一个固定且独立的短标记，第一次请求各得到零命中。测量只使用示例数据。
- 命中率取 `sum(input_tokens_details.cached_tokens) / sum(input_tokens)`。各请求的实际输出额度依次是 1,024、1,024、1,024、950 和 600。对照组共使用 51,899 个输入 token，修改后为 52,433 个。其中未命中输入减少 20,586 个，约为 61.51%。[DeepSeek Responses 用量字段](https://api-docs.deepseek.com/api/create-response/)
- 离线回归覆盖有配额的工具轮、默认配额下完整历史前缀的复用、工作项更新及原生工具回放。在项目根目录可运行 `go test ./internal/application -run 'TestSupervisor(RequestKeepsPrefix|AnthropicMessagesKeepsPreviousRequest|WorkUpdatesKeepPriorConversationPrefix|ToolExposureKeepsSharedPolicy|ResponsesPreservesPrivateReplay)'`。
