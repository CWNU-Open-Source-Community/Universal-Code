# ADR 0187: 已安装 Skill 的按任务选择入口

Date: 2026-10-15
Status: Proposed for [Issue #334](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/334).

## 决策

新增 `POST /api/v1/runs/{run_id}/external-skills/select`，让操作者把任意**已安装**
Skill 精确绑定到一个尚未开始的 Run，而不必先改 `.prayu/config.yaml` 的
`skill_suggestions`。请求体沿用固定项目的形状：控制 bearer、`Idempotency-Key` 与严格
JSON `{"refs":["review@1.2.0"],"confirm_untrusted_context":true}`，操作者身份固定为
`http_installed_skill_operator`。

Go 侧把 `SelectProjectSkills` 的公共流程抽成 `selectSkills(ctx, store, request,
subject, admitted)`：固定项目入口传入"必须在 pinned suggestions 中"的准入闭包，
`SelectInstalledSkills` 传入 `nil` 准入，其余检查一行不改。两个入口共用
`ProjectSkillSelectionResult`，因此响应结构、选择 ID 与 `capability_grant=false`
语义完全一致。

选择仍只走既有的两个服务：内置名交给 `SkillSelectionService.Select`，外部
`name@version` 交给 `ExternalSkillSelectionService.Select`。安装存在性、资格、
Token 上限、Run 必须仍为 `Created`、execution mode 快照绑定、untrusted-context
确认与同 key 重放全部由这两个服务持有；HTTP 层不复制任何一条。

## 授权与信任边界

这条路由放开的是"**选哪个已装好的包**"，不是"能否装包"。安装动作仍只在
CLI 与 [ADR 0185](0185-skill-package-inventory-and-removal.md) 的移除控制面之外，
本入口不导入、不下载、不安装，也不授予任何能力。内置 Skill 仍要求与编译进
二进制的版本精确相等，否则冲突。

外部 Skill 的正文是不可信指令来源。确认位必须为 true 才允许选择，且确认只解除
选择阻塞，不改变注入时的 untrusted 标记与 Token 预算上限。仓库内容、远端清单与
模型输出都不能替操作者点击确认。

与固定项目入口的差别只有一处：项目文件是"仓库作者建议"，操作者选择是"操作者
指名"。两者都只能指向已经通过安装审查的包，因此不存在"界面能选到 CLI 装不了的东西"。

## 持久化与失败

无 SQLite 迁移：内置与外部选择记录、幂等账本与模式快照均已存在。失败顺序沿用
既有实现——外部优先提交，未安装或不具资格的外部引用留下内置选择不变；任一步失败
都不推进 Run，`Created` 任务保持可用同 key 恢复。

缺 refs、超上限、refs 重复、格式非法、未确认外部上下文、Run 已离开 `Created`、
无控制令牌、非 POST 与 query 参数一律无副作用拒绝。同一 key 换 refs 返回冲突，
同 key 同意图重放原选择 ID 并标记 `replayed`。

## 备选方案与拒绝路径

- 让界面写 `.prayu/config.yaml` 再走固定项目入口：拒绝。该文件是仓库作者的窄化
  配置，由产品界面按任务写入会把"我的这台机器选了哪个 Skill"混进版本控制内容，
  并且需要文件写权限与一次额外的解析。
- 复用 `/project-skills/select` 并放宽它的准入：拒绝。它当前的契约是"refs 必须
  精确匹配 pinned suggestions"，放宽会静默改变已发布路由的语义，也让固定项目
  预检与人工指名无法分别审计。
- 新增 `/api/v1/skills/packages/select` 之类的包作用域路由：拒绝。选择是**按 Run**
  的绑定，包作用域路由会让人以为存在跨任务的全局启用开关。
- 提供"一次选择、后续任务默认带上"：拒绝。既有约束是每个 Run 绑定自己的模式
  快照与选择记录，持久默认值会绕过逐任务确认。

## 剩余未授权能力

不提供卸载后自动清理历史选择、不提供运行中改选、不提供批量应用到多个 Run，也不
改变 Skill 内容注入预算与 Hook 执行边界。Skill Catalog 发布者信任仍只有 CLI
（见 [ADR 0186](0186-plugin-publisher-trust-control.md) 的同一遗留项）。

## 验证与参考

Go 覆盖已安装外部 Skill 的正常选择、未确认外部上下文拒绝、Run 已离开 `Created`
的拒绝、未安装引用拒绝、重放与换 refs 冲突、方法与令牌拒绝，并断言固定项目入口
在未被 pin 时仍然拒绝同一 ref。HTTP 契约由 OpenAPI 黄金文件与"路由必须真实可达"
检查约束；界面覆盖入口可达性、确认位与响应投影。依据见 Issue #334 正文、
[ADR 0170](0170-task-budget-and-project-configuration.md) 的任务配置边界与
[ADR 0106](0106-narrowing-only-prayu-project-config.md) 的项目配置窄化。
