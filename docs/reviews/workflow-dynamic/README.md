# 动态工作流 OCR delegate 复审

日期：2026-10-18。基线 `83745f8496d7b262c11e0d585cdb43a63436a827`（#387），最终实现 `381afd3ac4092b64dc3eff5b2641c55f07c85c80`。

OCR 负责文件选择和规则解析，主 agent 与两名独立子 agent 实际阅读判断。主 agent 另外完成页面查看、接口和输出策略定向测试；后端实现 agent 执行工作流回归。审阅子 agent 未运行 Go。

## 覆盖范围

最终实现改动 40 个文件，另读完本次验收文档，共 41 个文件：25 个文本全文审阅，10 个文本部分审阅，3 张截图实际查看，3 个生成文件排除全文阅读。部分审阅包括全部改动及相关调用上下文，不代表全文审完。文件明细见 [coverage.json](coverage.json)。

OCR 默认选中 20 个文件；默认排除的测试、说明文档和截图由审阅者补入。生成文件为 `docs/openapi.json`、`web/src/api/schema.d.ts`、`docs/convergence/protocol-registry.md`，其生成与检查见验收记录。本 README 和 coverage 属于审阅元数据，未计入实现范围。

```text
ocr delegate preview --from 83745f8496d7b262c11e0d585cdb43a63436a827 --to 381afd3ac4092b64dc3eff5b2641c55f07c85c80 --exclude 'docs/openapi.json,web/src/api/schema.d.ts,docs/convergence/protocol-registry.md' --format json --max-git-procs 2
ocr delegate rule --from 83745f8496d7b262c11e0d585cdb43a63436a827 --to 381afd3ac4092b64dc3eff5b2641c55f07c85c80 --format json --max-git-procs 2 <所选文件路径>
```

## 发现与修复

| 发现 | 具体触发和影响 | 最终处理 |
| --- | --- | --- |
| interface-001，P2 | finish 的 message、summary 正常，但受禁止的文本只放在 `report.data` 中；既有输出策略未检查它，内容会保存并进入后续 worker 输入。 | 解码后检查嵌套字段和值，保留数字精度及脱敏；覆盖转义字段、换行和大指数数字后的文本。 |
| ENGINE-1，P2 | 已分配两个子任务后，Run 的累计执行时间或工作流预算拒绝新一轮；工作流失败却仍保留待处理任务、会话和占用名额。 | 当前 Run 租约下回收持久化待处理项；事务同时核对工作流 claim/revision 与原 attempt 归属。覆盖未选中审批项、截止时间、旧版无 Maps 的普通 worker；成功项保留。 |
| ENGINE-2，P2 | 两项任务首轮恰好用完 MaxTurns，等待操作获批后，恢复原 attempt 被错误当成新轮次拒绝。 | 只统计需要创建新 attempt 的项，获批的原操作继续使用既有轮次。 |

三个问题均已修复并复核，所列范围内没有未处理 finding。共享工作区文件修改的检查点竞争也已在实现中处理：同一轮的应用和删除依次提交，读取与模型请求保持并行。

最终预算修复的 7 个文件已由引擎审阅者核对，与其冻结的最终阅读版本和后端测试提交 `7db98743` 一致。源代码整合没有产生冲突。

## 验证范围

定向 Go 检查、工作流回归与最终截止时间增量检查、22 项前端测试、TypeScript 和构建均已通过，具体命令及版本边界见[验收记录](../../acceptance/2026-10-18-workflow-dynamic.md)。最终整合后没有重复运行 Go。

后端使用真实 SQLite、子代理与工具记录，模型响应使用确定性测试 transport；页面使用正式组件与本地 HTTP 夹具。实际查看了浅色、深色和 390px 窄窗口，截图未拉伸。动态工作流尚未完成真实远端模型自主执行验收。
