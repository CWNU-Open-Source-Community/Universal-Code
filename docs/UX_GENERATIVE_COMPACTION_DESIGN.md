# 生成式上下文压缩：最小接入设计

> 历史批次记录：状态仅对应文内日期。当前验收边界见 [公开审阅说明](UX_PUBLIC_REVIEW_STATUS.md)；本地日志、截图与安装包未纳入公开分支。

日期：2026-09-12；实施更新：2026-09-13。状态：**已按用户授权完成接入、定向验收和本地预览构建。** 以下保留实施前设计依据，当前结果以 [生成式摘要验收记录](UX_GENERATIVE_COMPACTION_VALIDATION.md) 和工作必读为准。未调用真实供应商、读取凭据或进行付费实验。

## 可复用的部分

现有 `contextmgr.Manager.PrepareCandidate` 已把候选计算移出 SQLite 事务；提交重新核验原消息、前摘要、checkpoint 和 lease/generation，再原子保存摘要与 compacted 标记。这个分界可以保留。原历史搜索与按来源/SHA分页回读也可以复用，生成文本不必承担无损记忆职责。

模型传输可继续用 `llm.Router`、当前 ModelRef 和现有供应商适配器，不必为了摘要引入 Python 或 HTTP 记忆服务。Router 自身不负责持久计费、Run 租约或取消，这些属于 application/store 的执行链。

源码入口：

- [PrepareCandidate](../internal/contextmgr/context.go)、[策略接口](../internal/contextmgr/summary_strategy.go)：当前 Rank 只允许排列来源记录，不能产生新正文或改变 authority。
- [压缩快照校验](../internal/store/supervisor_context_snapshot.go)、[提交](../internal/store/supervisor_context_compaction.go)：完整 checkpoint 与来源比较，不能为了方便接模型而整体放松。
- [模型调用链](../internal/application/run_supervisor.go)、[取消监听](../internal/application/model_cancellation_watch.go)、[活跃调用](../internal/application/active_calls.go)：已有预算预留、持久 started/terminal、实际调用注册与停止边界。

## 不能直接复用普通回答调用的原因

`callModelWithRetry` 使用普通 Supervisor model attempt。当前 ModelAttempt 只有 protocol repair/tool round 等区分，没有摘要用途；store 的 purpose 计数也按这些字段分桶。直接使用该路径会占用主回答重试次数。

`RecordSupervisorModelStarted` 还提交 Root Skill 上下文消费记录；`model_stream.go` 创建普通对话 commentary previewer。内部摘要不能因此消耗本轮 Skill 输入，或作为面向用户的助手回答流出。

模型终态计账会更新 checkpoint 的 token、执行时间和 UpdatedAt；现有候选提交比较完整 checkpoint。因此在快照之后进行一次正常计账，再提交原候选，当前 CAS 会正确判定为陈旧。不能简单跳过这些字段比较：必须能证明差异只来自本次摘要调用。

对应源码：[ModelAttempt](../internal/llm/outcome.go)、[模型计数与计账](../internal/store/supervisor.go)、[流式投影](../internal/application/model_stream.go)。

## 本次实施采用的最小流程

1. 用短只读快照固定来源消息集合、原文/SHA、前摘要、输入、checkpoint 与 lease/generation。
2. 在事务外进行一次有界、无工具的摘要调用。给调用增加明确的 `context_compaction` 用途；保留全局唯一 model attempt、Run 活跃调用和取消机制，同时隔离主回答重试、工具回合、Skill 消费和公开流式输出。
3. 持久保存该用途的模型终态及已知 usage。失败、超时和取消如实记录；供应商未返回 usage 时，不声称已知精确 token 或费用。
4. Go 验证候选正文、来源引用、输出预算和输入指纹；候选生成内容一律标记为模型生成、非授权。原用户锚点及工具证据仍保留原来源身份，不能把模型改写冒充用户原文。
5. 原子提交时重新核验所有来源和活跃租约。若 checkpoint 的计账字段变化，必须由同一快照/同一摘要 attempt 的已保存 terminal receipt 精确解释；其他变化仍拒绝。不能整体削弱 `sameSources`。

可以用小型生成候选 `{text, input_fingerprint, source_refs, model_attempt_ref}` 与现有摘录候选并列。无需新增 Run、独立执行框架或长期记忆数据库。模型不负责批准工具、选择新权限、重写 source/hash 或改变历史记录。

## 错误与恢复边界

- 来源或输入在调用期间改变：候选不提交；已发生且已知的模型使用量仍保存，不能因 CAS 拒绝把费用抹掉。
- Stop、取消、lease 丢失：终止本次工作，不以“摘要失败后回退”继续执行用户任务。
- 普通无效候选可保留全部原历史，并按明确政策回退现有摘录；回退不是生成质量成功。
- 重启不得无条件重发可能已经收费的摘要请求；需沿原 attempt/终态的既有恢复语义处理未知结果。
- 手动 Thread 压缩可以按持久 operation_id 核对、恢复已保存候选，或放弃采用结果。放弃未知调用保留费用预留，迟到响应只记账；操作入口与状态语义见 [压缩恢复说明](THREAD_CONTEXT_RECOVERY.md)。
- 摘要拒绝不能借用普通协议修复失败接口，避免错误改变主回合的 repair/failure 状态。

## 实施前的必要验收

用固定协议供应商验证取消、takeover、调用后来源变化、已计账但候选拒绝、重启未知响应、主回答重试/工具回合不受影响、内部摘要不出现在公开助手流。再以相同真实任务样本比较摘录与生成式策略的遗漏、回读成本和任务结果；固定响应夹具只验证接线，不证明语义摘要质量。

前批滚动窗口解决后继完整摘要反复累积的容量问题，继续明确有损；完整旧记录通过受 Thread/Workspace 约束的历史来源回读。本次在此基础上增加生成式调用生命周期、独立用途与计账提交，不替换原历史事实账本。
