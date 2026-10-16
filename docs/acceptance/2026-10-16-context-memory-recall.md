# #338 跨任务经验与记忆回读验收

2026-10-16 在 Windows 使用生产 Desktop composition、HTTP、SQLite、Supervisor 和
工具网关完成真实模型验收。Provider 为 `deepseek/deepseek-v4-flash`，凭据由系统凭据库
读取。使用独立应用目录和 Git fixture，通过正常 Thread、候选确认及记忆管理接口操作。

## 实际流程

| 步骤 | 观察到的结果 |
| --- | --- |
| 记录纠错 | 模型调用 `history_search` 后，通过 `note_create` 保存 decision/root 笔记，记录 QUARTZ-338 的标记为 Amber-17，旧标记 Violet-09 作废。 |
| 生成候选 | 已完成回复的持久事件成为来源依据，候选含 Run、Note 版本、完成记录和待审核说明。来源 Run 仍为 `running`，没有用 Run 终态冒充回复完成。 |
| 确认前的新 Thread | `history_search` 返回 0 条，模型请求审计没有长期记忆来源，回复明确未取回已确认记忆。其他 Thread 的原始笔记未被返回。 |
| 确认后新 Thread | 操作者提交已查看的版本、指纹和 `confirm=true`，得到 v1 项目记忆；新任务经 search/read 读取全文，回复 Amber-17 并引用记忆 ID 和版本。 |
| 编辑后新 Thread | 操作者把内容更新为 Jade-42；新任务检索、读取及注入审计均为 v2，回复使用 Jade-42。该任务的工具结果和回复没有旧标记。 |
| 禁用后新 Thread | 检索无结果，请求审计无长期记忆注入，模型没有猜测标记。 |
| 删除与确认重放 | 物理删除当前版本后重放原确认，返回无正文的已处理候选；正式记忆读取为 404，未被重新创建。 |

共 5 个 Thread、14 次工作流模型调用、176,774 token，另有 2 次 Provider 资格调用。
全部工具调用为 `history_search`、`history_read` 或 `note_create`；没有命令执行。
所用 Thread 保持默认 Ask，候选确认和记忆回读没有升级执行权限。
检索结果的全文 SHA-256、记忆版本、实际模型注入审计和最终回复均作了交叉检查。

结构化结果见 [真实模型记录](../evidence/context-memory/issue338-real-model.json)。
原始记录、隔离数据库和启动日志保存在 `C:\p338-a77cab1f`；读写令牌未纳入报告。
验收驱动保留于忽略目录 `output/playwright/issue338`。
流程期间为最新来源 token 编码重新构建并正常重启了一次服务，候选保留并可确认。
该构建之后补充了候选指纹对脱敏状态的绑定、正式记忆的证据引用、交付检查点对当前
WorkItem/Deliver 状态及交接摘要的复核，以及 OpenAPI 字段约束。这些增量由候选和
接口定向回归覆盖；上面的真实模型流程使用 Thread 回复来源，未重跑整个流程。

## 定向回归

- SQLite：来源资格、跨 Workspace、owner 可见性、版本变更、并发确认、重放、
  删除后重启及 v197→v198 迁移；支持真实已通过的交付报告来源。
- 模型回读：原有历史协议与旧游标、当前 Thread 沿革、其他 Thread 隔离、
  记忆禁用/过期/删除、元数据变更、分页过程变更、敏感内容和引用处理。
  最大正文和特殊字符来源 ID 的分页边界通过。
- 请求预算：普通笔记压力、必需上下文、项目指令、超量记忆及省略原因；
  同一工具轮次内更新、禁用、过期、删除后，下一逻辑模型请求重新加载。
  这些请求回归使用受控 provider 检查实际构造的请求，另有上面的真实 Provider 流程。
- HTTP：control bearer、显式确认、精确版本及指纹、严格 JSON、只读模式、
  新 OpenAPI 路由与真实 handler 对照。
- 界面：候选原文与来源展示、明确确认、忽略、只读禁用、冲突后重新读取、
  项目范围过滤以及原上下文面板共 6 项交互测试通过。TypeScript 和生产构建通过。
- `go vet ./...`、协议 registry 与既有 baseline 对照、Surface registry 检查通过。

## 复现入口

在隔离 Workspace 的普通 Thread 中要求模型把一项明确纠错记为带来源的 decision 笔记，
正常完成回复。打开 **Context → 当前项目 → 待确认经验**，核对原文和来源。
确认前在另一个 Thread 搜索相同短语；确认后重新搜索并读取正文，再通过原有记忆控件
编辑、禁用或删除，分别在新 Thread 重试。用 `model.started` 的 context 审计核对
实际注入版本和摘要；用工具回执及模型回复核对检索与使用结果。

范围、预算和数据保留规则见 [上下文文档](../context-continuity.md)。
