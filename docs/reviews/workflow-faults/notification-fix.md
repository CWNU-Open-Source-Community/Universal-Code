# 工作流完成通知恢复修复

2026-11-06，分支 `Qiyuanqiii/workflow-notification-fix`，修复基线 `9d9cf3bc`（产品 main 加故障复现）。

完成 checkpoint 现在与待通知义务在同一 SQLite 事务提交。宿主从独立通知表恢复总结，不再次执行 completed worker，也不等待遗留的解释器 claim 到期。通知继续使用原有 specialist summary、Run wake 和主会话 handoff，沿用 execution ID 派生的稳定操作 key。

每个宿主 tick 最多尝试一条待通知记录，之后仍给普通 workflow 推进机会。失败在处理结束后退避 5 秒；后续通知可先交付。通知使用独立的 15 分钟 claim，处理沿用 workflow 的 10 分钟上限；只有持有当前未过期 claim 的 owner 可以结清。总结交付以该通知精确 attempt / turn / user_message_id 的 AgentTurnCompletedEvent 为准；该事件与模型回复同事务保存。输入 committed 或聚合 handoff 的 model_called 字段均不足以单独结清，缺少响应证据的义务保持 pending。取消的通知可结清为 cancelled。

wake 预算耗尽且所有消费均未创建实际 handoff 时，当前通知 owner 在同一 SQLite 写入中核对 claim、代次与实际 operation digest，再推进持久化 wake generation。新代只更换 wake key，通知 key 保持不变。实际 handoff 已存在而结果未知时保留原代，防止重复模型执行。

没有 summary handoff 的服务保留义务，等待具备主会话执行能力的宿主。Run 暂停、等待审批或持有执行租约时，宿主不尝试总结；恢复后接续。Run 已 completed / failed / cancelled 时，通知义务记为 cancelled，并保留 workflow 的 completed 状态和已保存输出，不重启 Run。

v221 为历史 completed execution 补待核对义务，再用原有稳定 key 和已保存 wake / handoff 收据清算。输入消息 committed 早于模型回复，迁移不能据此直接认定总结已完成。已交付的旧总结通过精确完成事件结清，不重复模型调用。全新安装基线已通过 `go generate ./internal/store` 重新生成，版本历史追加 v221。

## 实际验证

Windows amd64，Go 1.26.10，CGO_ENABLED=1，已有 WinLibs GCC。测试使用实际 SQLite、workflow / Run 服务、worker 调度、通知队列和主会话 handoff；确定性 provider 替代远端模型。

以下检查实际通过：

```powershell
go test ./internal/application -run '^TestWorkflowFaultCompletion' -count=1 -timeout=120s -v
go test ./internal/application ./internal/store -run '^(TestWorkflowCompletion|TestWorkflowSummary|TestWorkflowFaultCompletion)' -count=1 -timeout=120s -v
go test ./internal/application -run '^TestWorkflowCompletion(RetriesLost|FailedDelivery)' -count=1 -timeout=120s -v
go test ./internal/application ./internal/store -run '^(TestWorkflow|TestSchemaV203|TestSchemaV221)' -count=1 -timeout=180s
go test ./internal/store -run '^TestSchemaV221' -count=1 -timeout=120s -v
go test ./internal/application -run '^TestWorkflowCompletionRecoversAfterSummaryWakeExhaustion$' -count=1 -timeout=90s -v
go test ./internal/store -run '^(TestWorkflowSummaryWakeGeneration|TestSchemaV221)' -count=1 -timeout=120s -v
go test ./internal/application -run '^TestWorkflowCompletion(ReplacesExpired|ModelResponse)' -count=1 -timeout=90s -v
go test ./internal/application ./internal/store -run '^(TestWorkflow|TestSpecialistContinuation|TestSchemaV203|TestSchemaV221|TestSchemaHistory|TestCleanInstallBaseline)' -count=1 -timeout=240s
go test ./internal/httpapi ./internal/desktop -run 'Workflow' -count=1 -timeout=120s
```

最终组合命令退出 0：application 116.420 秒、store 92.163 秒；HTTP / desktop 检查退出 0，分别为 7.497 / 0.781 秒。`git diff --check` 通过。

默认回归保留并通过 GetRun / Enqueue 各一次错误，以及真实测试子进程在 completed 事务返回后 `os.Exit(86)` 的故障。两个存储错误在同进程重试和数据库重开后均自动补发；实际进程退出后无需等待解释器 claim 即可补发。每例均只有一条已提交通知、一条主会话总结，worker token 用量保持 5、worker 模型不重跑；重复 Advance 和宿主 tick 不重复总结。子进程入口独立枚举时仍按设计 Skip，父测试实际启动并检查退出码 86。

新增测试还验证：通知插入失败会回滚 completed checkpoint；第二数据库宿主不能抢活跃 claim，过期或被替换 owner 不能结清；无 handoff、暂停和审批等待保留义务；三种 Run 终态保留输出；通知失败不阻挡普通 worker 或后续通知；交付后确认写入失败的接管不会再调用模型。

复核新增并实际通过三项恢复：连续三次 handoff 不可用至 wake exhausted 后，重开健康宿主换代并只总结一次；Schedule 成功但未开始消费的 wake 到期后自动换代；模型回复成功保存、handoff 结果首次写入失败后，无模型回放产生 model_called=false 的 handoff 结果，通知仍凭精确完成事件结清。另有 store 用例确认消费的 handoff_operation_id 尚空、但实际 operation 已存在且结果未知时不得换代，并验证错误 owner、过期 owner 和重复代次请求均不能推进。

历史迁移测试从真实 v220 库先实际交付一次总结，再打开升级并运行宿主；升级后没有第二次模型调用或重复消息。历史待通知、已交付、已取消及普通用户消息均保留原记录。

未运行远端真实模型或桌面 UI 验收。此次只处理完成通知恢复；取消和暂停到期清理由另一修复分支处理。原始复现报告与历史日志保持不变。
