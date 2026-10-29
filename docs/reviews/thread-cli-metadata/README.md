# CLI 线程元数据与搜索：OCR delegate 复审

日期：2026-10-30。基线：`b58f8981db87dd983e221839283e43f145b92b03`（origin/main）。
主题：把桌面端已有的会话重命名、置顶与标题搜索补齐到 CLI（`thread rename`、
`thread pin`、`thread list --title-query/--pinned`），与 HTTP 面
（`internal/httpapi/thread_metadata.go` 的 PUT /threads/{id}/name|pin）走完全相同的
store 语义（无乐观版本、无 operation-key、无审计事件），供 headless/NDJSON 工作流使用。

OCR 负责文件选择（`ocr delegate preview --from origin/main --to HEAD`：2 个可审生产文件）
与规则解析（`ocr delegate rule`：Rule Group 1 system Go），
主 agent 与独立评审子代理实际阅读判断。评审子代理全文核验 2 个生产文件、
1 个人工补入的测试文件与全量 diff，并交叉核对了 store/HTTP/domain 三层的既有语义。

## 覆盖范围

共修改 4 个文件；评审记录（本 README 与 coverage.json）不计入审查范围，纳入审查 4 个：
- 生产代码（2 个，OCR 检出）：`internal/app/thread_command.go`、`internal/app/app.go`。
- 测试（1 个，人工补入）：`internal/app/thread_command_test.go`。
- 文档（1 个，OCR 标记 unsupported_ext，主 agent 全文核对）：`docs/usage.md`。

具体明细见 [coverage.json](coverage.json)。

```text
ocr delegate preview --from b58f8981db87dd983e221839283e43f145b92b03 --to HEAD
ocr delegate rule internal/app/thread_command.go
```

## 发现与修复（评审子代理报告，全部已修复并复测）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| F1，P3 | 新小节称 `--title-query` 为 "case-insensitive"，但 SQLite `lower()` 只折叠 ASCII；与 openapi 及 domain 既有措辞（"ASCII case-insensitive, other Unicode exact"）相比表述过强，可能误导非 ASCII 标题的无人值守调用方。 | 中英文均改为 "ASCII 大小写不敏感（其余 Unicode 字符按精确比较）"。 |
| F2，P3 | "version 单调递增" 与 "可安全重试" 并列过强：同值 no-op 时 `updateThreadMetadata` 的 `field<>?` 守卫不递增 version。 | 改为 "version 单调不减、每次实际变更都会递增，因此同样的重试是安全的"。 |

评审确认的无缺陷事实（节选）：rename/pin 与 HTTP 面走同一 store 路径，无权限授予；
不存在/已删除线程统一映射为标准 not-found，错误经 `apperror.Normalize` 无内部细节泄露；
`--pinned=false` 与 `--pinned false` 两种写法均可达 store（Go `flag.Bool` 不消费下一参数，
故两处 `--pinned` 均为 String 旗标）；空/纯空白 `--title-query` 不加过滤；行输出新增
`pinned=` 前全仓无既有消费方断言旧格式，机器消费方走 `--json`（`Pinned` omitempty 反序列化
正确）；两个新测试各自以 `t.TempDir()` 隔离 `CYBERAGENT_HOME`、断言覆盖成功路径、传播、
大小写命中/未命中、拒绝路径与双向过滤。

## 验证

- `gofmt -l internal/app/` 无输出；`go vet ./internal/app/` 无告警。
- `go test ./internal/app/ -run "TestThreadCLI|TestPresentationVocabulary"` 通过；
  完整 `go test ./internal/app/` 中与本 PR 无关的两个失败
  （`TestRunCommandExecuteIsConfirmedAuditedAndExactlyOnce` 需要本机不可用的 approved
  executable；词表测试需要稀疏检出补齐的 `docs/openapi.json`）均在干净 main 上同样复现/
  与改动无关。
- 端到端冒烟（真实构建的 `cmd/cyberagent`）：rename 多词标题（含不带引号的裸多词）、
  `--pinned=false` 与 `--pinned false`、`--pinned maybe` 明确报错、纯空白
  `--title-query` 不过滤、大小写混合查询命中、`--pinned false` 过滤出未置顶集合。
