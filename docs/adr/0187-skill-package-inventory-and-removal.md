# ADR 0187: Skill 包清单与移除的操作者控制面

Date: 2026-10-15
Status: Proposed for [Issue #334](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/334).

## 决策

已安装 Skill 包的清单与移除从 CLI-only 提升为 HTTP 与 Desktop 共用的操作者控制面，
委托给既有 `SkillPackageRegistryService.List` 与 `Remove`。本决策不新增授权、不新增
执行入口、不改动存储结构。

- 只读清单 `GET /api/v1/skills/packages` 位于读 bearer 之后，与扩展清单读取同级，
  不需要控制令牌；接受 `surface`、`profile`、`include_removed` 单值查询，拒绝多余查询。
  默认不含已移除条目，`include_removed=true` 返回 tombstone 状态。
- 移除 `POST /api/v1/skills/packages/remove` 复用 `skill_installation_enabled` 同一能力
  门与 `Idempotency-Key` 头，不新增能力开关。安装与停用属于同一扩展生命周期，
  拆成两个开关会制造"看起来独立、实则同源"的伪授权面。
- Desktop 桥新增 `ListSkillPackages` 与 `RemoveSkillPackage` 两个有界方法，方向保持
  `TypeScript -> Go`，渲染器只调用，不持有清单判断或移除权限。
- 新增协议标识登记到既有 family：HTTP 投影进入 `http-openapi-contract`，
  桥投影进入 `desktop-web-presentation-state`（class `projection`）。
  `skill_package_removal.v1`、`skill_package_removal_intent.v1` 与
  `skill_package_remove_operation.v1` 已在 `extension-package-contracts` 注册，沿用不改义。

## 授权与信任边界

移除是扩展生命周期管理，不授予任何执行能力。控制令牌在场且安装能力启用只允许发出
请求；实际写入前 Go 重新校验 Run 无关的当前安装身份、actor 与确认标记。
`confirm_remove` 必须为 true，缺失或 false 直接失败关闭，界面不得代为勾选。

操作者身份沿用 `http_operator` / `desktop_operator` 固定值，请求体不接受自定义 actor，
避免把署名变成可伪造字段。仓库内容、模型输出、Skill 正文与扩展返回值都不能请求移除。

移除不追溯改变已完成 Run 的历史事实：已选入某 Run 的外部包上下文继续按其持久记录解释，
tombstone 只阻断后续选择与安装复用，不把历史收据改写成"从未授权"。

## 持久化与失败

复用 `skill_package_remove_operation.v1` 写前意图与不可变
`skill_package_removals` tombstone，无 SQLite 迁移。同一幂等键与相同意图重放返回原结果并
标记 `replayed`；同键不同意图返回冲突；目标已被其它请求移除返回冲突；
意图与 tombstone 之间崩溃由启动收敛归为待处理，不生成第二条移除。

新增 `operationreceipt` 类型 `skill_package_remove`，结局 `removed`，重试策略
`same_operation_key`，清理状态 `not_applicable`。收据沿用内容自由投影，
不外泄操作键、摘要、指纹、本地路径或包体字节。

## 备选方案与拒绝路径

- 让界面调用 CLI：拒绝。渲染器会获得安装与移除的实际 authority，违反 Go 单一控制平面。
- 只做"停用"不做"移除"：拒绝。tombstone 合同与不可变保护已存在，绕开它反而需要新机制；
  停用与移除语义不同，界面须分别如实显示。
- 为移除新增独立能力开关：拒绝。与安装同源，新增开关只会扩大配置面而不扩大真实边界。
- 把清单并入 `/api/v1/extensions` 聚合：拒绝。扩展清单按工作区投影安装代次，
  Skill 包清单按 surface/profile 资格投影，来源与过滤维度不同，合并会产生含糊 DTO。

非法查询、非 JSON 内容类型、重复字段、越界请求体、未知 `name@version`、
缺少控制令牌或能力未启用均有稳定错误，且不产生持久化副作用。

## 剩余未授权能力

本 ADR 不覆盖：把 GUI 已安装的外部包选入当前任务（现要求钉定项目建议）、
发布者信任的授予入口、URL/Git 钉定来源导入，以及任何模型侧的 Skill 管理能力。
包对象存储内容不因移除而删除；不提供绕过审查的重装路径；
不放宽 catalog 启用、phase/surface 合格检查与不可信上下文确认。

## 验证与参考

Go 侧覆盖读端点参数拒绝、移除的正常/拒绝/重放/冲突/崩溃收敛与重启恢复，
以及 OpenAPI 黄金文件与"路由必须与合同一致"的既有检查；Desktop 侧覆盖桥方法数与
字典序清单、能力聚合与只读默认；前端覆盖 client 解析、能力门与设置页交互。
治理清单只描述边界，不授予运行权限。相关实现依据见 Issue #334 正文。
