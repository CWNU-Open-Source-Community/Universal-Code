# 统一右侧多标签工作区、完整视图与终端主题跟随：OCR delegate 复审

日期：2026-10-27。基线：`7a16e94d855bba38c31ced621b2b1a798cc4eb99`（origin/main）。
协议标识：`desktop-web-presentation-state`（`universal-code.workspace-tabs.v1`）。

OCR 负责文件选择和规则解析，主 agent 与独立子 agent 实际阅读判断。主 agent 另外完成页面查看、接口与草稿守卫回归、E2E Playwright 旅程验证；审阅子 agent 全文核验规范与改动。

## 覆盖范围

PR 共修改 27 个文件。排除本 README 与 coverage.json 2 个审阅元数据记录后，实际纳入审查范围的文件共 25 个：
- 生产代码（14 个，OCR 默认检出）：`web/src/v2/workspace-tabs.ts`、`web/src/v2/components/workspace-dock.tsx`、`web/src/v2/components/workspace-dock.css`、`web/src/v2/components/conversation.tsx`、`web/src/components/user-terminal-panel.tsx`、`web/src/v2/components/application-preview.tsx`、`web/src/v2/components/application-preview.css`、`web/src/v2/components/task-review.tsx`、`web/src/v2/components/task-review.css`、`web/src/styles.css`、`web/src/v2/styles.css`、`web/src/lib/local-ui-message.ts`、`web/src/test/setup.ts`、`protocols/registry.json`。
- 测试与契约（6 个，人工补入）：`docs/convergence/protocol-registry.md`、`web/src/v2/workspace-tabs.test.ts`、`web/src/components/user-terminal-panel.test.tsx`、`web/src/v2/components/conversation.test.tsx`、`web/src/v2/components/conversation-inspector-feedback.test.tsx`、`web/e2e/workbench.spec.ts`。
- 视觉核对（5 张截图，逐张核验）：`docs/images/workspace-tabs/` 下 5 张截图（`before.png`、`after.png`、`after-fullview.png`、`after-dark-restart.png`、`after-narrow.png`）。

具体文件明细见 [coverage.json](coverage.json)。

```text
ocr delegate preview --from 7a16e94d855bba38c31ced621b2b1a798cc4eb99 --to HEAD --format json
ocr delegate rule --from 7a16e94d855bba38c31ced621b2b1a798cc4eb99 --to HEAD <所选文件路径>
```

## 发现与修复

| 发现 | 具体触发和影响 | 最终处理 |
| --- | --- | --- |
| DRAFT-001，P2 | 文件草稿守卫存在遗漏：对话内文件链接 `openFile` 与改动/服务卡片 `openSingletonSurface` 未接入 `leaveFileEdit`，在有未保存编辑时点击会绕过确认流程。 | 在 `conversation.tsx` 中为 `openFile` 与 `openSingletonSurface` 全面包装 `leaveFileEdit` 确认守卫。 |
| DRAFT-002，P2 | `e2e/workbench.spec.ts` 中草稿关闭抽屉测试超时：`collapsePanel` 原先仅在 `!docked` 模式下经过 `leaveFileEdit`，分栏停靠状态下关闭面板直接执行折叠；多标签下已打开的文件对应 ARIA `role="tab"`。 | `workspace-dock.tsx` 的 `collapsePanel` 统一受 `leaveFileEdit` 保护；补充 `.v2-file-drawer` / `.v2-terminal-drawer` 兼容类名；测试定位器兼容 `getByRole("tab").or(getByRole("button"))`。 |
| TABS-001，P2 | 12 个标签上限自动截断及草稿检测方案缺陷：旧版通过扫描 `localStorage` 键匹配草稿，未考虑 `RecoveryStorage` 的 `encodeURIComponent` URL 转义、内存态 `DraftDocument` 与 `sanitizeThreadState` 强制截断导致的 `activeTabID` 状态不一致。 | 放弃通过存储子串猜测草稿状态；确立严格保护原则：所有打开的文件标签（`path && path !== "."`）均视为活动编辑器，一律不得自动静默淘汰；达到 12 个标签且无干净目录浏览标签可替换时，安全拒绝新增并通过 `notice`（可解散提示栏）提示用户手动关闭暂不使用的标签；关闭标签自动清理提示并释放配额。补充覆盖真实 `DraftDocument`、内存草稿、存储写入失败及上限一致性的 4 项回归单测。 |
| TABS-002，P2 | 应用预览与改动审阅切走标签时的生命周期设计：若在后台常驻挂载会引发后台应用服务轮询、DOM 内 status 提示元素重复（如应用预览启动草稿与主 Composer 提示冲突）及性能损耗。 | 明确 Lazy Surface 架构设计预期：预览与改动审阅仅在激活且面板展开时挂载；核心操作状态（如 `reviewMemoryRef`、`reviewToolTarget`、`reviewFileTarget`、`previewStartRequest`）由上层 `conversation.tsx` 独立持久化与传递，切回时精准恢复，兼顾多标签体验与后台资源/DOM 隔离。 |
| TEST-001，P3 | `workspace-tabs.test.ts` 监听了错误的事件名 `universal-code:workspace-tabs-changed`，而实现实际派发 `universal-code:workspace-tabs`，导致无变化断言在派发出错时可能虚假通过。 | 导出统一常量 `workspaceTabsChangedEvent`，修正监听事件并补充实际状态改变派发的断言测试。 |
| TAB-001，P3 | 极端边界下 `activeTab` 可能为空，属性读取可能发生 null pointer 异常。 | 全面补齐 `activeTab?.kind` 可选链与默认兜底，并通过 TypeScript 严格模式校验。 |

所有发现均已修复并复核完毕。

## 验证结论

- **单元与组件测试**：`vitest run` 全量执行通过（277 个测试文件全部通过）。
- **E2E 旅程测试**：`npm run test:journeys` 执行通过（包含真实项目导入、对话问答、单据草稿流转与狭窄屏幕重载）。
- **类型检查与构建**：`npm run typecheck`（`tsc -b`）、`e2e/tsconfig.json` 及 `npm run build` 全部零错误通过。
- **视觉验收**：核对 5 张真实截图，浅色多标签、完整视图平铺、深色终端主题与显式重启提示、390px 窄屏适配均符合预期。
