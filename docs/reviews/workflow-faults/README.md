# 工作流引擎故障复现与修复

本目录保留修复前的实验和失败日志。当前实现与整合验收见
[修复验收](fix-verification.md)、[取消与暂停修复](lifecycle-fix.md)和
[完成通知修复](notification-fix.md)。复现测试现已默认执行，无需环境变量。

以下内容记录修复前提交 `9d9cf3bc` 的结果。

日期：2026-11-06（Asia/Hong_Kong）。产品基线：`21f56e6b390abf1c2a7de86f9ae8af68d34edb91`。汇总分支：`Qiyuanqiii/workflow-fault-repros`。

三个 GPT-6.1 Sol / xhigh 子 agent 在独立工作树中分别复现取消、暂停到期和完成通知故障。根代理阅读复现代码、检查预期与实际结果，整合后独立复跑。此次只增加测试、日志与分析，尚未修改产品执行代码。

## 实际发现

| 问题 | 触发与实际结果 | 对用户的影响 | 证据 |
| --- | --- | --- | --- |
| 取消后无法收尾 | worker 返回 continue 后取消 Run；child 已 cancelled，workflow 却变 waiting、step 留在 pending。重开数据库仍不恢复；到期后每次推进均清理报错。 | 任务已经停止，执行记录仍显示未结束，宿主反复处理无法完成的清理。 | [取消报告](cancel.md)、[日志](cancel-output.md) |
| workflow 暂停后不处理到期 | 在真实文件写入审批停点暂停 workflow，经过原有 deadline；候选与 claim 排除 paused，宿主不执行清理。恢复后一个 tick 才清理。 | 有效审批等待、非终态子任务及未使用预算预留持续保留。 | [暂停报告](pause.md)、[日志](pause-output.md) |
| Run 暂停后清理被拒 | 同一审批停点只暂停 Run；到期候选正常，但清理调用要求 running Run 的恢复入口并失败。恢复 Run 后一个 tick 才清理。 | 暂停的任务无法完成到期收尾，未使用预算持续预留。 | [暂停报告](pause.md)、[日志](pause-output.md) |
| 已完成但总结丢失 | completed 已落盘、通知尚未入队时，GetRun/Enqueue 一次失败或实际子进程退出；重开后宿主不再选择该 execution。显式 Advance 才补发，重复调用不重复。 | 工作已完成，主对话却没有最终结果，需要人工恢复。 | [通知报告](completion.md)、[日志](completion-output.md) |

取消实验没有额外模型调用、活跃 Run lease 或 command jobs。暂停实验没有执行未批准的文件写入，计费 tokens 未增加；其未用预算预留由子任务状态与分配额度计算。这里证明的是状态收敛、预留回收和通知恢复缺陷，没有据此声称进程泄漏或越权执行。

## 哪些设计需要加固

本轮证据集中指向三个跨模块约定，尚不足以判断整个项目的所有引擎路径。

1. **Run、workflow、step、child 的终态传播不完整。** Run 的取消能终止 child，但没有同步结算 workflow/step。恢复扫描看到的是一个不可恢复的 waiting execution。
2. **继续执行与异常收尾混用准入条件。** 清理先取得执行租约、调用运行态 attempt 恢复及 dispatch 校验。暂停或取消正确地撤销了继续执行资格，却连必要清理也一起阻止。候选查询、claim 和结算的状态范围还需要一致。
3. **完成状态与通知义务没有一起持久化。** completed 落盘即退出宿主扫描，但总结还可能尚未入队。稳定通知 key 已有去重能力，缺少的是可恢复的待通知记录与宿主重试。

正常对照证明已有的数据库重开续跑、重复请求去重和双宿主排他可以工作。现有 Go 引擎、权限检查、租约与原生执行通道可以继续复用；这轮修复应把上述职责补完整。

## 建议的修复顺序与验收

1. **终态结算和资源清理。** 先定义取消、失败、到期分别如何结束 workflow/step/child/attempt，再实现允许暂停或终态 Run 收尾的受约束路径。保留归属、版本、claim 和具体 attempt 校验，不能通过恢复 Run、放开普通 dispatch 或忽略错误来清理。验收：取消及两种暂停实验自动收敛；无额外模型调用、未批准写入或未用预算残留。
2. **暂停状态的到期维护。** 让宿主接纳已到期 paused execution 的清理，同时保持未到期的暂停任务不执行 worker。与第一步共同核对候选、claim、checkpoint 和结算，避免只改一条查询。验收：无需用户点击恢复即可到期收尾，原 deadline 与累计用量不重置。
3. **可靠的完成通知。** 将待通知记录与 completed 状态在同一事务提交；宿主重试未交付通知，沿用已有稳定 key。验收：两种存储调用故障及真实进程退出后自动补发一次；重启或重复推进不重跑 worker、不重复总结。
4. **把这些复现转为默认回归测试。** 修复后移除相应测试的 opt-in 条件，继续保留正常对照。之后再处理定时工作流与旧 Run 的绑定、跨 Run 调度并发；这两项本轮没有做动态复现，不能列为本轮实测缺陷。

## 复跑方法与证据范围

Windows amd64、Go 1.26.10、CGO_ENABLED=1，使用已有 WinLibs gcc。实际 SQLite、Run/workflow 服务和宿主 RunDue；确定性 provider 替代模型回复，没有调用远端模型。暂停与取消实际等待短 deadline。完成故障包含方法级一次性错误注入以及测试子进程在提交点 `os.Exit(86)`，没有模拟断电。

```powershell
$env:UC_WORKFLOW_FAULT_REPRO='1'
go test ./internal/application -run '^TestWorkflowFault' -count=1 -timeout=120s -v
```

在历史提交 `9d9cf3bc` 上，上述命令在正确行为断言处失败；未设置环境变量时诊断测试 Skip。当前分支已移除开关，执行同名测试检查修复后的行为。

根代理整合后独立执行了以下命令：

```powershell
$env:UC_WORKFLOW_FAULT_REPRO='1'
$checks='^(TestWorkflowFault.*|TestWorkflowHostContinuesPausedCheckpointAfterDatabaseReopen|TestWorkflowHostClaimsExcludeSecondDatabaseOwner|TestWorkflowCompletionSummarizesOnlyOnceAfterAllSteps|TestScheduledAutomaticWorkflowLostResponseObservesOriginalAcrossRestart)$'
go test ./internal/application -run $checks -count=1 -timeout=120s -v

Remove-Item Env:UC_WORKFLOW_FAULT_REPRO -ErrorAction SilentlyContinue
go test ./internal/application -run '^TestWorkflowFault' -count=1 -timeout=120s -v
```

整合复跑退出 **1**：取消、两种暂停，以及通知的两种方法故障和真实子进程退出案例，均在预期的正确行为断言处失败；各自的正常对照和原有四条测试通过。子进程入口在父测试中实际执行并退出 86，单独被测试框架枚举时按设计 Skip。完整输出见 [reproduction-output.md](reproduction-output.md)。

默认运行退出 **0**，六个复现入口均 Skip，见 [default-output.md](default-output.md)。原有四条对照的首次独立执行也通过，见 [baseline-output.md](baseline-output.md)。根代理还检查了整合 diff，仅新增诊断测试与文档，`git diff --check` 通过。未运行完整测试套件或桌面 UI 验收。
