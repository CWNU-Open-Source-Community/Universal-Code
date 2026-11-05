# Browser origins 配置可见性（ADR 0195 Phase 4 收尾片）：OCR delegate 复审

日期：2026-11-06。基线：`5bbdc2b2`（PR #542 分支，live view 投影）。
主题：#542 评审 P3 跟进的落实——操作者声明的 origins 在配置面的回显：
`run show` 条件输出 `browser_origins: <逗号列表>`（声明为空不打印，与
started_at/finished_at 的条件输出风格一致）；HTTP `RunDetailView.run.config`
暴露 `browser_origins`（omitempty，RunDetailView→RunView→RunConfigView 反射
自动传播）；openapi/schema.d.ts 生成器再生成；`TestRunViewCarriesBrowserOrigins`
单测（scoped/unscoped）+ CLI run show 回显断言并入既有 browser-origins 测试。

## 覆盖范围

6 文件：`internal/app/run_command.go`、`run_command_test.go`、
`internal/httpapi/views.go`、`agent_browser_test.go`、`docs/openapi.json`、
`web/src/api/schema.d.ts`。

## 发现与处置（评审子代理报告）

| 级别 | 内容 | 处置 |
| --- | --- | --- |
| P3 | run 详情 UI 可选展示 origins（API 已就绪；live view 已有现成 i18n 文案）。 | 记为后续跟进，不阻塞。 |
| P3 | run list API 响应自动携带 origins（RunConfigView 被 list/detail 共用）。 | 无需处理（omitempty、≤8 条短串）；未来列表 payload 精简时再拆 view。 |
| 提醒 | 提交前建分支。 | 已按提醒执行。 |

评审确认（无 P0/P1/P2）：run show 断言均为 Contains、无 golden 破坏；origins 是
操作者自己的授权声明、不属于隐私类别（隐私测试实跑通过）；fork/resume/branch
不继承的真正机制在创建侧（CreateRunRequest 不传）而非 runView；import 块修复后
字母序正确；再生成 diff 只含该字段（传播链 RunDetailView→$ref RunView→config→
$ref RunConfigView）；run list 共用 view 的影响可忽略。run 详情 UI 展示记为
后续跟进。

## 验证

- `go build ./...`、`go vet ./internal/app/ ./internal/httpapi/`；定向测试：
  CLI（创建成功/run show 回显/私网拒绝/超限拒绝）、`TestRunViewCarriesBrowserOrigins`、
  httpapi 全包（237s）全绿。
