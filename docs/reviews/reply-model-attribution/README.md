# 每条回复的模型归属：OCR delegate 复审

日期：2026-10-28。基线：`8d9ce6ac918c0e142ac1089afa1022d85b750281`（origin/main）。
主题：#470 使模型可中途切换后，transcript 无法回答"这条回复来自哪个模型"。
本变更把模型调用事件已有的身份（`modelIdentity()`：`provider / model`）作为
只读 `omitempty` 字段穿透 runactivity → threadtranscript → HTTP view，
web 叙事投影按 attempt 关联，并在持久化回复的操作行显示灰字徽标（流式不显示）。

OCR `delegate preview` 选出 5 个可审生产文件；`delegate rule` 选定 Go 组
（错误/API 契约、nil 与值语义、性能分配）与 TS/React、CSS 组。独立评审子代理
全文核验 5 个生产文件与全量 diff。

## 覆盖范围

共 13 个文件；排除审阅元数据 2 个后纳入 11 个：生产 5（activity.go、
transcript.go、views.go、narrative.ts、narrative.tsx）+ 再生成 2（docs/openapi.json、
schema.d.ts，delegate 核验为纯再生成产物）+ 样式 1（styles.css）+ 测试 3。
明细见 [coverage.json](coverage.json)。

```text
ocr delegate preview --from 8d9ce6ac918c0e142ac1089afa1022d85b750281 --to HEAD
ocr delegate rule internal/runactivity/activity.go internal/threadtranscript/transcript.go internal/httpapi/views.go web/src/v2/projection/narrative.ts web/src/v2/components/narrative.tsx
```

## 发现与修复（评审子代理报告，全部已修复并复测）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| JOIN-001，P1 | model_call 投影只设置了 `Model` 而未提取 `attempt_id`，web 端 join 键永远缺失——真实数据下徽标不会渲染（首版 web 测试以手写键掩盖了断链）。 | ModelStarted/ModelCompleted 两个 case 增加 `base.AttemptID = cleanLabel(stringField(payload,"attempt_id"))`；新增 Go 贯穿测试以真实负载形状断言 call 项同时携带 attempt 与 model。 |
| REPLY-001，P2 | 最终持久回复经 SessionMessageEvent 投影，负载不含 attempt——仅 commentary 行可归属，主表面无效。 | threadtranscript 构建循环按 run 跟踪最近一次模型调用的 attempt，助手回复项继承之；零 store 负载改动。新增贯穿测试断言 reply 项继承 attempt-7。 |
| PERF-001，P3 | modelIdentity 在 started/completed case 各被重复调用（completed 路径叠加 completedModelDetail 内部一次），重复反序列化负载。 | 提升为单次 `identity := modelIdentity(...)` 同时赋 Detail/Model；completedModelDetail 改为接收 identity 参数（唯一调用方）。 |
| MERGE-001，P3 | 叙事合并分支（重复收尾回复并入）只更新文本与 ID，后来条目的 model 被静默丢弃。 | `previous.model ??= model`（首见优先，缺失时回填），注释写明语义。 |

评审子代理同时核验通过：字段仅沿既有只读投影链流动（无新写路径/锁/goroutine）、
`omitempty` 契约与同类字段一致、docs/openapi.json 与 schema.d.ts 为纯再生成产物、
attempt_id 全局唯一（idgen）无需 run 复合键、徽标无 XSS、durable-only 守卫成立、
CSS 仅用既有变量。

## 验证结论

- Go：`go test ./internal/runactivity/ ./internal/threadtranscript/ ./internal/httpapi/` 全过（含新增贯穿测试）；`go build ./...`、`go vet` 通过。
- Web：`vitest` 投影+组件 38/38（含归属关联、无 attempt 不挂徽标、流式不显示）；`npm test` 全量通过；`tsc -b` 零错误。
- 已知范围：attribution 依赖模型调用事件负载中的 attempt_id/model/provider——与 #469 的 usage 口径同源；无 attempt 的历史项不显示徽标（不猜测）。
