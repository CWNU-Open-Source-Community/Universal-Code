# 项目指令脚手架：OCR delegate 复审

日期：2026-10-29。基线：`b58f8981db87dd983e221839283e43f145b92b03`（origin/main）。
主题：对照 Claude Code `/init` 与 Codex `/init` 补齐的空工作区项目指令引导
（`cyberagent context instructions scaffold`）：离线、确定性、不调用模型，
只检测固定根部标识文件（不读取内容），生成双语 `AGENTS.md` 草稿并输出 SHA-256；
写入模式绝不覆盖已存在的 `AGENTS.md` / `AGENTS.override.md`，`--print` 始终可预览。

OCR 负责文件选择（`ocr delegate preview --from b58f898 --to HEAD`：3 个可审生产文件）
与规则解析（`ocr delegate rule`：Rule Group 1 system Go），
主 agent 与独立评审子代理实际阅读判断。评审子代理全文核验 3 个生产文件、
1 个人工补入的测试文件与全量 diff，并对 F1 在 Windows 上做了逐字复制逻辑的实证复现。

## 覆盖范围

共修改 6 个文件；评审记录（本 README 与 coverage.json）不计入审查范围，纳入审查 6 个：
- 生产代码（3 个，OCR 检出）：`internal/projectconfig/instructions_scaffold.go`、
  `internal/app/context_command.go`、`internal/app/app.go`。
- 测试（1 个，人工补入）：`internal/projectconfig/instructions_scaffold_test.go`。
- 文档（2 个，OCR 标记 unsupported_ext，主 agent 全文核对）：`docs/usage.md`、
  `docs/context-continuity.md`。

具体明细见 [coverage.json](coverage.json)。

```text
ocr delegate preview --from b58f8981db87dd983e221839283e43f145b92b03 --to HEAD
ocr delegate rule internal/projectconfig/instructions_scaffold.go internal/app/context_command.go
```

## 发现与修复（评审子代理报告，全部已修复并复测）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| F1，P1 | `instructionBoundary` 对不存在的 target 把 targetDir 回退为父目录；scaffold 原检查对 `ErrNotExist` 不拒绝。`--target ghost.txt` / `src/typo.go` 会把 `AGENTS.md` 写进父目录，同时报告一个从未创建的幻影 `file_path`，违反"绝不覆盖 + 精确报告产物"契约。 | 写入与预览统一要求 target 经 `Lstat` 存在且为真实目录；回归测试覆盖 `missing`、`ghost.txt`、`src/typo.go`、`missing/nested` 并断言工作区根不产生 `AGENTS.md`。 |
| F2，P3 | 大小写不敏感文件系统上同一文件命中表中 `Makefile`/`makefile`（及 `Dockerfile`/`dockerfile`）两条，草稿重复列出。 | 检测阶段用 `os.SameFile` 聚合：大小写敏感文件系统上真实共存的不同文件仍各自列出，同一文件只报一次；测试断言单个 `Makefile` 恰好产生一条检测。 |
| F3，P3 | 注释承诺标记检测"never followed or read"，但 `.github/workflows` 检查用 `os.ReadDir` 会跟随指向目录的 symlink；与不变量表述不符。 | 先 `Lstat` `.github` 与 `workflows` 两段并跳过 symlink，再列目录；仅统计 regular 文件条目，注释不变量成立。 |

评审另确认（无缺陷，记录为已核实事实）：渲染确定性（固定顺序表、无时间戳、无 map 迭代）、
拒绝语义跨平台一致（Windows 大小写变体同样命中）、产物被 `DiscoverInstructions` 识别为
`kind=agents`、`trust=project_workflow_untrusted`、授权字段除 workflow/formatting/validation
外全 false、32 KiB 脚手架上限严格小于发现流程 64 KiB 单文件上限、CLI 路由仅精确匹配
`args[1] == "scaffold"` 不劫持既有 `context instructions` 用法、文档与实现一致。

## 验证

- `gofmt -l`（改动包）无输出；`go vet ./internal/projectconfig/ ./internal/app/` 无告警。
- `go test -race ./internal/projectconfig/` 与 `go test ./internal/app/ -run Context` 通过
  （Go 1.26.5 / windows-amd64）。
- 端到端冒烟（真实构建的 `cmd/cyberagent`）：`workspace init` 后对新托管工作区执行
  scaffold 写入（检出 `go.mod`、`README.md`）、`--print` 预览、已存在时拒绝、
  `--target nonexistent` 明确报错、`--json` 可解析且字段完整、
  `context instructions --workspace <name>` 能发现草稿并给出 fingerprint。
