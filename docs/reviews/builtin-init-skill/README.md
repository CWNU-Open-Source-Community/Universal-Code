# 内置 init skill（AGENTS.md 项目指令）：OCR delegate 复审

日期：2026-11-04。基线：`b58f8981db87dd983e221839283e43f145b92b03`（main）。
评审对象：PR #525（head dbeb302b）。
主题：新增内置 `init` skill——从仓库证据构建或刷新 `AGENTS.md` 项目指令（对齐
Claude Code `/init` 与 Codex `/init` 的工作流，复用本仓库既有的指令发现/快照体系）。
CodeRabbit 此前 4 条意见（3 major + 1 minor）已由提交 14815321/14815321 修复链与
dbeb302b 收口（前 3 条标记 ✅ dbeb302；第 4 条 instructions.ignore 排除检查 + 先问
操作员的指引已含于当前 SKILL.md：".prayu/instructions.ignore can also exclude a
path by glob, so confirm the file you target is really read, and ask before editing
that ignore list"）。

OCR 负责文件选择（`ocr delegate preview --from main --to pr-525`：1 个可审生产文件
manifest.json + 排除的 SKILL.md/registry_test.go 人工补入）与规则解析，主 agent 与
独立评审子代理实际阅读判断。

## 覆盖范围

纳入审查 5 个（评审记录不计入）：
- internal/skills/builtins/init/SKILL.md（39 行 prompt 正文，全文阅读）
- internal/skills/builtins/init/manifest.json（结构 + 完整性字段）
- internal/skills/registry_test.go（目录顺序测试 + 新增防"幽灵工具"回归测试）
- docs/builtin-skills.md、docs/usage.md、docs/SKILL_PACKAGE_PLAN.md（计数一致性核对）
- internal/approval/bounded_grant_test.go（+767，与 #526 搭车的另一 session 成品测试，
  逐字节相同，CI 已证明编译通过，仅作用域发现）

具体明细见 [coverage.json](coverage.json)。

## 发现与修复（评审子代理报告，全部已修复并复测）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| B-1，P3 | `TestBuiltinSkillContentNeverNamesAnOperatorTool` 的 operator 工具标签列表手工复制 5 项中的 4 项（漏 `shell`），且 registry.go 未来新增 operator 工具时测试不会自动覆盖。 | toolgateway 注册内联 slice 提升为包级 `operatorToolDefinitions` 并导出 `OperatorToolLabels()`；测试改为派生列表（现含全部 5 项，`shell` 无 prose 碰撞），`IsSupervisorTool` 入参做 ToolName 转换。 |

评审确认（No blocking findings）：manifest 完整性字段与 SKILL.md 字节逐项一致
（4074 bytes / sha256 3f60ef…668）；SKILL.md 无 operator 工具名残留，交付机制指向的
`workspace_change`/`workspace_apply` 均为真实 agent-code-tools.v1 模型工具；指引文件
清单与 `internal/projectconfig/instructions.go` 逐项吻合（AGENTS.override 空文件回退、
CLAUDE.md、.claude/CLAUDE.md、.prayu/instructions.md、.prayu/rules/**、
.prayu/instructions.ignore、.prayu/config.yaml 非 guidance）；`phases:[plan,deliver]`
与 "Plan 停在草稿" 自洽；`tool_dependencies` 三项经 `validateToolDependencies` 校验
（字典序唯一、profile 兼容）；docs 三处计数一致（15−3 explicit=12）；
`TestBuiltinSkillContentNeverNamesAnOperatorTool` 的 supervisor 守卫方向正确、非空转。

## 验证

- `go build ./internal/toolgateway/` / `go vet` 无告警；`go test ./internal/skills/ -count=1`
  全过（含派生标签后的防幽灵工具回归测试与目录顺序测试）。
- 作用域发现（如实记录）：`bounded_grant_test.go` 与本 PR 及 CSV 导出主题均无关，
  属同基分支搭车文件，维护者可决定是否随本 PR 合入。
