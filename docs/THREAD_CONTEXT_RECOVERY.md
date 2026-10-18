# 对话压缩的核对与恢复

在任务的「上下文与恢复」中，压缩按钮下方可以读取最近 20 次压缩记录。未完成操作排在前面；刷新页面、丢失原请求 key 或应用重启后，记录仍来自同一份 SQLite 数据。

| 记录状态 | 可以进行的操作 |
| --- | --- |
| `prepared` | 尚未发出模型请求。可放弃，释放这次调用的金额预留。 |
| `started` | 请求已准备发送，可能仍在运行，也可能在中断后留下未知结果。可刷新核对，或放弃采用本次结果。 |
| `generated` | 摘要和模型用量已落盘。可恢复应用；只有当前来源版本仍一致且任务空闲时才安装摘要。也可放弃。 |
| `completed` / `noop` | 显示已有结果；重复恢复返回相同结果。 |
| `failed` | 显示失败原因，原历史保留。需要再次压缩时由用户发起一个新操作。 |

恢复应用只读取已经保存的摘要，不调用模型。来源变化时拒绝安装旧摘要，用户可以放弃它，再按当前历史重新压缩。原始消息仍可回读，固定要求仍单独进入后续模型请求。

放弃意味着不再采用该操作的摘要。它不保证停止已发给供应商的请求；未知用量的金额预留和 token 上限继续计入预算。迟到响应只补记一次真实用量并结算原预留，不能把操作恢复为成功，也不能覆盖当前对话。没有收到真实响应时不会制造「已完成且免费」的记录。放弃记录和已知预留的核对在同一事务内保存。

## HTTP 和 CLI

读取使用现有只读 bearer；恢复使用现有 control bearer：

```text
GET /api/v1/threads/{thread_id}/context/compactions
POST /api/v1/threads/{thread_id}/context/compactions/{operation_id}/recover
{"version":"thread_context_compaction_recovery.v1","expected_run_id":"run-id","action":"resume"}
```

`action` 也可为 `discard`。恢复身份是原 `operation_id` 与 `expected_run_id`，不需要找回浏览器中的原始 Idempotency-Key。不能跨 Thread 或 Run 操作；重复请求返回已有事实。列表不包含原历史、摘要正文或请求 key。

```powershell
cyberagent thread compactions <thread-id> --json
cyberagent thread compactions <thread-id> --operation <operation-id> --action resume --expected-run <run-id> --json
cyberagent thread compactions <thread-id> --operation <operation-id> --action discard --expected-run <run-id> --json
```

本入口处理手动 Thread 压缩操作；自动 Supervisor 回合的中断继续通过既有任务恢复流程处理。数据库沿用 v208 的压缩操作和金额账本，不增加迁移版本。
