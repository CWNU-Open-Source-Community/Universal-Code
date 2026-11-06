# 操作者自定义指令库：OCR delegate 复审

日期：2026-10-28。基线：`8d9ce6ac918c0e142ac1089afa1022d85b750281`（origin/main）。
主题：对照 Claude Code（`.claude/commands`）与 Codex（`~/.codex/prompts`）补齐的
操作者自有提示词模板库（客户端存储 `universal-code.prompt-commands.v1`），
composer 输入 `/` 触发指令菜单，选中只填草稿、不发送、不执行、无权限提升。

OCR 负责文件选择（`ocr delegate preview`：4 个可审生产文件）与规则解析
（`ocr delegate rule`：Rule Group 1 system TS/JS/TSX + Rule Group 2 default CSS），
主 agent 与独立评审子代理实际阅读判断。评审子代理全文核验 4 个生产文件与全量 diff。

## 覆盖范围

共修改 9 个文件；排除本 README 与 coverage.json 2 个审阅元数据后，纳入审查 7 个：
- 生产代码（4 个，OCR 检出）：`web/src/lib/prompt-commands.ts`、
  `web/src/v2/components/prompt-command-menu.tsx`、`web/src/v2/components/composer.tsx`、
  `web/src/v2/styles.css`。
- 测试（3 个，人工补入）：`web/src/lib/prompt-commands.test.ts`、
  `web/src/v2/components/prompt-command-menu.test.tsx`、`web/src/v2/components/composer.test.tsx`。

具体明细见 [coverage.json](coverage.json)。

```text
ocr delegate preview --from 8d9ce6ac918c0e142ac1089afa1022d85b750281 --to HEAD
ocr delegate rule web/src/lib/prompt-commands.ts web/src/v2/components/prompt-command-menu.tsx web/src/v2/components/composer.tsx web/src/v2/styles.css
```

## 发现与修复（评审子代理报告，全部已修复并复测）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| DEAD-001，P2 | `composer.tsx` 残留未使用的 `expandPromptCommand` 导入（死代码）。 | 改为 Enter 插入路径的真实使用（见 ENTER-001），导入保留且有消费方。 |
| ARGS-001，P2 | 管理对话框承诺 `$ARGUMENTS` 会被替换为"指令名后输入的文字"，但菜单在键入空格后立即关闭，参数路径在 UI 上不可达。 | 菜单触发正则扩展为 `/^\/([a-z][a-z0-9-]*)(?:\s([\s\S]*))?$/`：指令名后键入空格与参数时菜单保持打开，参数透传至 `$ARGUMENTS`；composer 集成测试覆盖 `/rel src/main.ts` → `Check src/main.ts against the notes.`。 |
| SUBST-001，P2 | `body.replaceAll("$ARGUMENTS", args)` 的字符串替换串会解释 `$&`/`$$` 等特殊模式，操作者输入被静默改写。 | 改为替换函数 `body.replaceAll("$ARGUMENTS", () => args)`，并新增含 `$&`/`$$` 的回归断言。 |
| DEAD-002，P3 | 校验三元中 `reserved_name` 分支不可达（非法名字必然不命中保留名集合），与下一行重复。 | 简化为直接抛 `invalid_name`；合法名字的保留名校验由下一行独立承担。 |
| SYNC-001，P3 | `slashQuery` 仅在 MarkdownInput.onChange 中派生，`restoreTextAttachment` 等直接 `setContent` 的路径会绕过菜单状态同步。 | 菜单状态改为**从 content 单一派生**（`useEffect` + 锚定正则），任何 `setContent` 路径自动一致；onChange 中的重复设置删除。 |
| A11Y-001，P3 | 菜单 `role="menu"` 但纯键盘操作者无法选中（Enter 被吞）；管理对话框无 Escape、无初始焦点。 | Enter 语义最终确定：精确名匹配或唯一前缀匹配时插入，无置信匹配时作为普通消息发送（Enter 永远有合理行为，无死锁分支）；管理对话框支持 Escape 关闭并 `autoFocus` 指令名输入框。 |
| CSS-001，P3 | 新增样式硬编码 `ui-monospace, monospace` 与 `rgba(0,0,0,.45)`，与既有 `var(--v2-mono)`、`rgb(15 20 26 / 26%)` 不一致。 | 统一改用既有变量与既有遮罩写法。 |

评审过程中另发现并修复一个测试可见的交互缺陷（非规则项）：菜单初版打开时自动聚焦菜单项，
会抢走正在输入的焦点（`user.type` 后续字符落入按钮）。已改为 type-ahead 纯指针选择、
焦点始终留在输入框；菜单开闭完全由 content 派生，删除了与派生语义冲突的手动关闭按钮。

## 验证结论

- `npx vitest run src/lib/prompt-commands.test.ts src/v2/components/prompt-command-menu.test.tsx src/v2/components/composer.test.tsx`：27/27 通过。
- `npm test` 全量套件通过；`npm run typecheck`（`tsc -b`）零错误；`npm run check:api` 不涉及（无 Go/OpenAPI 改动）。
- 边界确认：指令正文永远只是填入草稿的普通文本，发送前可编辑；不新增任何工具、权限或网络能力；存储写入失败以操作者可读错误呈现且不破坏既有数据。
