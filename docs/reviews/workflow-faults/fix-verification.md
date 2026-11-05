# Workflow 故障修复验收

日期：2026-11-06。产品基线 `21f56e6b390abf1c2a7de86f9ae8af68d34edb91`；整合分支 `Qiyuanqiii/workflow-engine-recovery`。
生命周期修复来源 `bff2ba40`，通知修复来源 `5e295e4b`，整合代码提交 `2cd1fe7b`。根代理另补协议标识登记和行为文档。

## 修复结果

| 原故障 | 当前行为 |
| --- | --- |
| 取消 Run 后 workflow/step 仍未结束 | 宿主结算 unfinished workflow；保留已有 child 报告，不再执行 worker。 |
| 暂停 workflow 后 deadline 清理被跳过 | 到期或 Run 终态时进入受约束的清理路径；未到期暂停仍不调度。 |
| 暂停 Run 后到期清理要求 running 并报错 | 清理独立校验归属、claim、版本、租约和准确 attempt；不恢复 Run，释放未用预留。 |
| completed 已保存但总结尚未入队，存储错误或进程退出后丢失通知 | completed 和待通知记录同事务提交；宿主自动恢复，稳定通知 key 和精确回复事件防止重复总结。 |

审查中另修正：旧 lease 失效后的未知 attempt 只失败收尾、不重放；代码交付不能仅凭模型报告完成；活跃清理不能挡住其他 Run；通知 wake 耗尽或未消费就到期时，只在没有实际 handoff 的情况下换代；模型响应已保存但确认写入失败时，使用精确完成事件结清。

## 根代理独立整合验证

环境：Windows amd64、Go 1.26.10、CGO_ENABLED=1、WinLibs GCC。以下测试使用真实 SQLite、应用服务和调度入口，模型由确定性 provider 替代。完成故障测试实际启动子进程并检查提交后退出码 86。

```powershell
go test ./internal/application ./internal/store -run '^(TestWorkflow|TestSpecialistContinuation|Test.*(Lifecycle|RecoverSpecialist|ExecutionLease|RunPause|RunCancel)|TestSchemaV203|TestSchemaV221|TestSchemaHistory|TestCleanInstallBaseline|TestScheduledAutomaticWorkflow)' -count=1 -timeout=300s
go test ./internal/httpapi ./internal/desktop -run Workflow -count=1 -timeout=120s
go vet ./internal/application ./internal/store ./internal/httpapi ./internal/desktop
go test ./internal/producte2e ./internal/releasegate -count=1 -timeout=120s
go run ./cmd/protocolregistry -check -baseline 21f56e6b390abf1c2a7de86f9ae8af68d34edb91
go run ./cmd/surfacecheck -base-registry docs/convergence/surface-registry.json
git diff --check
```

以上命令全部退出 0。workflow/生命周期/通知/迁移组合：application `181.004s`，store `136.351s`；HTTP `7.924s`、desktop `1.263s`；producte2e `1.471s`、releasegate `9.712s`。`go vet`、协议/Surface 检查及 diff 空白检查均通过。未运行本地全项目 `go test ./...`。

Surface registry 与产品基线逐字相同，因此 Surface 演进检查使用同一文件作为基线。首次协议检查发现 `workflow-summary-wake-retry.v1` 未登记，已加入现有持久化 family 并重新生成文档，复查通过。

原 fault tests 已移除 `UC_WORKFLOW_FAULT_REPRO` 开关，正常测试命令直接执行。子进程专用入口独立枚举时按设计 Skip，父用例仍实际执行崩溃实验。修复前红日志保留为历史证据，不能作为当前结果读取。

本次没有前端改动。未进行真实远端模型或桌面 UI 验收。外部执行已经发生而结果未知时仍保留原执行身份，需现有恢复路径处理；不为补发总结盲目重放副作用。

## 审查覆盖

使用 OCR delegate 做文件选择与规则解析，根代理审生命周期、共享接口及整合 diff；独立 GPT-6.1 Sol / xhigh 审查代理复核通知实现，结论绑定 `5e295e4b`。实现代理各自执行测试，根代理再独立执行上述整合检查。

最终变更清单 41 文件；OCR 可审 17 项全部覆盖，skipped 0，coverage_rate 100%。以下清单按实际变更审读及相关调用链计，不把生成文件全文计为人工审读。

| 文件 | 方式 |
| --- | --- |
| `internal/application/specialist_continuation.go` | 通知代理全文审读；根代理检查 workflow-only wake 参数变更。 |
| `internal/application/workflow.go` | 审查接口新增、终态/到期处理及调用链。 |
| `internal/application/workflow_lifecycle.go` | 根代理全文审读。 |
| `internal/application/workflow_round.go` | 根代理审变更及预算失败收尾调用链。 |
| `internal/application/workflow_summary.go` | 通知代理与根代理审读交付/重试路径。 |
| `internal/application/workflow_summary_worker.go` | 通知代理与根代理全文审读。 |
| `internal/application/workflow_worker.go` | 审查合并后的通知调度、终态候选和无进展时公平性。 |
| `internal/store/workflow.go` | 审查 claim 准入与 completed/通知事务。 |
| `internal/store/workflow_lifecycle.go` | 根代理全文审读。 |
| `internal/store/workflow_worker.go` | 审查候选与 claim 条件一致性。 |
| `internal/store/workflow_notification.go` | 通知代理与根代理全文审读。 |
| `internal/store/migration_v221.go` | 审查表、索引与历史 completed 回填。 |
| `internal/store/migrations.go`、`internal/store/sqlite.go` | 审查版本和迁移注册变更及上下文。 |
| `internal/store/clean_install_baseline.sql` | 人工审新增表/约束/索引；摘要与升级等价性检查，不全文审约 1.5 万行生成 SQL。 |
| `internal/store/clean_install_baseline_generated.go` | 核对版本、SQL SHA 与生成/升级一致性测试。 |
| `protocols/registry.json` | 根代理审新标识、持久化边界；生成及 baseline 演进检查。 |

OCR 默认排除的 8 个测试文件仍补读并执行：application 的 `workflow_fault_cancel_test.go`、`workflow_fault_pause_test.go`、`workflow_fault_completion_test.go`、`workflow_lifecycle_test.go`、`workflow_summary_worker_test.go`；store 的 `workflow_lifecycle_test.go`、`workflow_notification_test.go`、`migration_v221_test.go`。其余 16 项 Markdown 是行为说明、版本历史、生成协议说明及本目录的历史/修复报告和输出；核对变更与实际实验，历史失败结果明确标记。最终未留下已确认但未修复的高/中缺陷。
