# 自动轮次失败后的队列交接

2026-11-07，Windows amd64。基底：#555 `953f3570`。

## 实际问题

自动轮次的 provider 返回 `FAILED_PRECONDITION`，真实 SQLite
`EndFailedThreadTurn` 已经提交失败后，再取消 automatic caller。
原 drain 条件把任何已取消的 caller 都当成交接理由，因而继续执行两条已接受的普通输入。

对照 caller 不取消时只有一次模型调用，两输入保持 `received/pending`；
晚取消时模型调用变为三次，两输入均变为 `completed`。原自动调用仍返回
`ThreadTurnFailedError`，原失败记录的错误码、attempt 和事件序号保持不变。
缺陷是晚取消绕过了真实失败后停止排队的语义。

原代码回归失败：单独失败组 0.94s/application 1.228s；加入真实 caller deadline
正例后的组为 6.581s。[原始输出](before.txt)。

## 最小修改

只收紧 `thread_turn_control.go` 的已有交接条件：

- 成功可以交接。
- caller 已终止且主错误为 `CANCELLED` 可以交接。
- 主错误为 `DEADLINE_EXCEEDED` 时，仅 caller 自身确实到期才可以交接。
- 其他失败停止本 owner 的队列推进，保留原错误和待处理消息。

封存的 `ThreadTurnFailedError` 以应用错误码保存结果，已经没有原 context error 的
identity。实测五秒 caller deadline 的返回码和失败记录均为 `DEADLINE_EXCEEDED`，
而 `errors.Is(deadline)`、`errors.Is(cancelled)` 都为 false。因此判别使用现有
`Normalize/CodeOf` 合同，不直接以 `errors.Is` 判断封存后的错误。

独立 deadline 反例使用 provider 自己已经到期的子 context；automatic caller 当时
仍未取消，失败记录确实为 `DEADLINE_EXCEEDED`，仅在失败提交后才普通 cancel caller。
该反例同样保持一次模型调用和两条原输入 `received`，不会把任意 timeout 当成合法交接。

Stop、Shutdown、active owner、execution ID、FIFO 与宿主 operation 登记均沿用原实现。
审批和普通输入共用此交接条件。

## 验证

普通定向组通过：6.509s，[输出](after.txt)。
最终专项 race 通过：53.966s，[输出](race.txt)，包括：

- `FAILED_PRECONDITION`、独立 `DEADLINE_EXCEEDED` 在 caller 仍活跃/失败后取消的四例；
- 五秒真实 caller deadline 后两条普通输入完成；
- 真实 caller cancel 后文本、附件、commit 时点与同 key 重放/FIFO；
- automatic 成功后的交接、普通模型失败保留后续输入；
- 交接前后 Stop/Shutdown，automatic 仍由 caller 取消；
- 现有完整公共 Review/ResumeApproval 六种边界和真实文件 apply。

```powershell
go test -race ./internal/application -run '^(TestHostOwnedAutomatic(SealedFailureKeepsOrdinaryQueueWhenCallerCancels|CallerDeadlineHandsOrdinaryQueueToHost)|TestHostOwnedQueuedOrdinaryTurnSurvivesAutomaticCallerCancel|TestHostOwnedAutomaticSuccessHandsQueuedOrdinaryToHost|TestHostOwnedAutomaticQueue.*|TestHostOwnedQueuedApprovalSurvivesAutomaticCallerCancel|TestHostLifetimeDoesNotDetachAutomaticThreadTurn)$' -count=1 -v -timeout=120s
go vet ./internal/application
git diff --check
```

vet 与 diff check 通过。SQLite、公共应用入口、失败封存与队列读回真实执行；
模型响应和失败提交后的取消时点由 fixture 控制，没有外部 provider 请求。
本轮没有 UI、schema 或公开接口改动。
