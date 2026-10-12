# ADR 0106: narrowing-only 的 .prayu 项目级配置

Date: 2026-08-16

## Status

Accepted for the v1 loader, fail-closed merge, and Run snapshot described
below. Desktop 分层来源展示与 HTTP 控制面作为后续 PR 落地。

## 背景 / Context

需要类似开发工具的 .prayu/ 项目级配置入口，但项目文件属于不可信输入：
只能缩小全局权限、提供非敏感默认值，不能提权或执行代码（Prompt injection 原则：
文件内容是证据，不是老板）。

## 决策 / Decision

### 1. 格式与加载

`<workspace>/.prayu/config.yaml`，协议 `project_config.v1`。文件必须为普通文件
（symlink/junction/reparse-point 拒绝），≤64KiB；YAML 先解析为节点树，拒绝任何
alias/anchor、深度 >32、节点数 >4096，再以 KnownFields 严格解码（未知字段/重复键/
类型错误/尾随文档 fail closed）。合法字段：read_only、allowed_profiles、
budget{max_turns,max_tool_calls}、exclude_paths（相对路径、无 .. 无绝对路径）、
skill_suggestions（name@version，与签名 Skill 身份合同对齐）、
test_command_id/format_command_id（只能引用 Tool Gateway 注册的 typed action ID，
绝不包含 Shell 文本）。无任何 credential/executable/argv/Docker/网络/权限档位字段。

### 2. 合并规则（只缩不放）

global/user ceiling > process capability > project narrowing > Run snapshot。
Narrow 对每个字段执行 fail-closed 检查：budget 只能严格降低、allowed_profiles 只能
收缩且非空、read_only 只能开启、exclude_paths 只能新增、command 必须已注册——
任何放宽尝试生成带字段名的 Rejection，调用方必须整体拒绝。read_only 项目禁止
write-capable profile（code/script），只允许 review/learn。

### 3. Run 快照与不可变性

Run 创建时把规范化后的 Effective 视图与 fingerprint（SHA-256 of canonical JSON）
固化进 run 的 config_json（RunConfig.project_config / project_config_fingerprint），
随后修改 .prayu/config.yaml 不会改变已存在 Run。CLI：`run create --ignore-project-config`
可跳过；`project-config show|validate` 提供预览与 fingerprint。

## 后果 / Consequences

运行消费者与产品选择流程见 [ADR 0170](0170-task-budget-and-project-configuration.md)：
排除路径作为固定快照应用于文件工具；Skill 建议由用户明确选择已安装或内置的准确
版本；测试/格式化使用已注册 native preset 与现有 command_runtime Job，不自动执行。

- 项目仓库可以附带安全默认值（更低预算、排除路径、Skill 建议），且无法借此提权。
- 后续 PR：Desktop 的 global/user/project/Run 分层来源展示、被忽略/拒绝字段列表、
  HTTP/OpenAPI 与 JSON schema 校验接入。

