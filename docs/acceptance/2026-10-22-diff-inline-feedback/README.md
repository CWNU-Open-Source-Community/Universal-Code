# Diff 行内意见加入聊天草稿

> 历史记录：以下截图基于 2026-10-22 的旧 #445 布局。同步 #445 紧凑工具栏后的
> [2026-10-27 真实前后截图与验证](../2026-10-27-inline-feedback-current/README.md)
> 已更新，#471 当前目标分支为 main。

本次补的是多处代码审阅时反复退出、重新定位的问题。基线 `ed87d1dbdf3bc04c7935ca9c7b4fabfb67524e0e`（PR #445）已有文件、片段和行引用，但点击后立即关闭审阅窗口；没有在行旁输入意见并继续审阅的入口。核对当前 40 个待审 PR 后，没有发现已覆盖此流程的实现。

## 竞品依据与选择

对照的是二者公开文档描述的实际产品流程，不推断未公开的内部存储或组件实现。核对日期：2026-10-22。

| 产品 | 公开流程 | 本次取舍 |
| --- | --- | --- |
| [Claude Code Desktop](https://code.claude.com/docs/en/desktop#review-changes-with-diff-view) | 在 Diff 行旁输入意见，可连续添加多条，再统一提交给 Claude | 采用就地输入和连续审阅；UC 的 Enter 保留多行输入，Ctrl/⌘+Enter 将当前意见加入草稿 |
| [Codex app](https://learn.chatgpt.com/docs/code-review#inline-comments-for-feedback) | 通过行旁的 + 添加意见，完成后发送聊天消息让 Codex 处理 | 采用显式回到聊天检查并发送；添加意见本身不执行任务 |
| Universal-Code 本次实现 | 行旁 + → 写意见 → 加入现有聊天草稿 → 继续看其它行 → 返回草稿发送 | 复用现有草稿文档、附件和冲突处理；不增加评论数据库、后端接口或新的模型调用 |

每条意见携带原始来源、旧/新路径、差异片段、原始行文本和该版本的行号。输入时冻结这些来源；刷新后即使差异消失，也不会把意见重新绑定到另一行。原有引用操作和长行换行继续可用。意见加入草稿后可以编辑或删除；尚未点击“加入聊天草稿”的行内输入仅保留在当前审阅视图。

## 依赖与兼容

本 PR 叠加 #445，目标分支为 `Qiyuanqiii/review-diff-line-wrap`。`onAddComment` 是可选回调，原 `onFeedback` 与服务端合同不变。其它待审 PR 的依赖关系不变。特别核对 #437、#454、#458、#461、#465、#467、#469、#470 的对话、快捷键与草稿交互；#447/#451 使用另一套仓库 Diff 组件。

## 真实浏览器验证

使用仓库现有 `cmd/workbenchjourney`、真实 Go API 和 SQLite，在临时 Git 项目中通过 workspace import、thread、lifecycle、file-edit-proposal API 创建 `settings.ts` 提案。没有替换浏览器 API 响应。一个本机静态代理依次提供基线与候选生产包，并将 API 请求转发至同一个 Go 进程；两组截图使用同一持久化编辑记录、原始/提案哈希和 Diff。供应商使用现有离线 journey provider，审阅过程未请求模型推理。

| 视口 | 变更前 | 变更后 |
| --- | --- | --- |
| 1280 × 900 | [仅可引用](before-desktop.png) | [就地输入意见](after-desktop.png) |
| 390 × 844 | [窄屏 Diff](before-narrow.png) | [继续添加第二条意见](after-narrow.png) |

[浏览器观测记录](observations.json) 保留浏览器版本、实际 Diff、布局尺寸及操作结果：

- 两条意见连续加入同一草稿，原有空白和正文保留；审阅窗口保持打开。
- 每条意见包含正确的源编辑 ID、新文件行号和原始代码；返回后可检查完整草稿，刷新重连后仍保留两条意见。
- Ctrl+Enter 与 Meta+Enter 添加意见；Escape 只关闭当前行内输入；返回草稿聚焦编辑入口，窄屏折叠入口可展开继续编辑。
- 窄屏编辑器位于视口内，开启换行后 Diff 无横向溢出。审阅操作期间无 API 写请求，无未捕获浏览器错误。

通过：6 个相关测试文件的 68 项测试、`npm run build`（包含 TypeScript 检查）、`npm run check:api`、`go test -count=1 -tags desktop ./web ./internal/webui`。测试覆盖旧引用行为、Git 预览指纹、差异刷新、中文/英文、键盘与输入法事件、草稿冲突、保存失败、附件保留和跨任务隔离。

本次真实浏览器为 Linux Chromium。Windows/macOS 原生壳、真实中文输入法及 macOS 硬件键盘未实测；Meta 与组合输入仅验证合成事件。Git 预览由组件测试覆盖，journey harness 未启用任务 Git API。上述结果不代表完整桌面发行验收。
