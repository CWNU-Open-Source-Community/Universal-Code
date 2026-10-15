# ADR 0183: 插件发布者信任的操作者控制面

Date: 2026-10-15
Status: Proposed for [Issue #334](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/334).

## 决策

签名插件的发布者信任从 CLI-only 提升为与撤销对称的操作者控制面：新增
`POST /api/v1/extensions/plugins/{installation_id}/publisher-trust`，沿用
`plugin-lifecycle.v1`、控制 bearer 与既有安装作用域路由匹配。请求继续携带
`expected_publisher_fingerprint` 与 `expected_publisher_generation`，并由界面从
`GET .../history` 的 publisher 投影取值；`confirm` 必须为 true。操作者身份固定为
`http_extension_operator`，请求体不接受自定义 actor。

Go 侧新增有界核心 `Service.TrustInstallationPublisher(installationID,
expectedFingerprint, expectedGeneration, actor)`，与
`RevokeInstallationPublisher` 同形：安装必须签名有效，其
`PublisherFingerprint` 必须等于操作者看到的期望指纹，信任记录代次必须等于期望代次
（当前无记录时期望必须为 0），随后以 CAS 写入 `State=trusted` 并递增代次。

现有 `Service.TrustPublisher(installationID, actor)` 改为读取当前安装与信任记录后
委派同一核心，CLI 行为与参数保持不变。这样授权只存在一条有绑定语义的写入路径，
不会出现"界面点一下信任到了没看过的记录"。

## 授权与信任边界

信任记录绑定公钥指纹、显示名与公钥三者；正向信任要求后两者也与记录一致，
撤销状态只绑公钥指纹并跨显示名继续生效。改名重签不能洗掉撤销。

信任不授予任何能力。逐包的 `skills`、`mcp_servers`、`ui_metadata`、`hooks`
仍由 `Review` 的具体动作与能力清单批准；被撤销的发布者解除信任后，
`approve`/`enable` 才重新可用，但仍各自需要确认。安装内容签名无效时既不能授予也不能撤销。

远端清单、包内文本、模型输出与界面状态都不构成交互证据；授予动作只能由操作者在
已核对指纹与代次的界面上发起。

## 持久化与失败

无 SQLite 迁移：`plugin_publisher_trust` 与其代次约束已存在，写入沿用
`SetPluginPublisherTrust` 的 expected-generation CAS。同一期望代次的并发第二次请求
返回冲突，不产生第二条记录，也不静默覆盖并发方的审查者身份。

失败均无副作用：未知安装、签名无效、期望指纹不符、期望代次过期、缺少确认、
无控制令牌或非 POST。重启后信任记录保持原代次；本项不引入操作收据，因为信任记录
自身携带代次、审查者与时间戳，重复提交由 CAS 收敛而不是靠幂等键重放。

## 备选方案与拒绝路径

- 直接暴露现有 `TrustPublisher(installationID, actor)`：拒绝。它按当前存储状态自行推导代次，
  控制面拿不到操作者实际看到的绑定值，陈旧点击会信任到另一条记录。
- 为信任新增幂等键与操作收据：拒绝。信任记录已有代次与 CAS，再加一层只会产生两套
  互相可能漂移的恢复语义；冲突即是需要界面重新读取的信号。
- 让撤销与信任共用一条 `publisher-state` 路由：拒绝。两者前置条件不同
  （撤销要求已存在记录，授予允许首次创建），合并会产生含糊校验与更难解释的失败。
- 在信任时顺带批准能力：拒绝。信任回答"这是谁"，能力审查回答"允许它做什么"。

## 剩余未授权能力

Skill Catalog 的发布者信任（`skill catalog trust <name> <public-key>`）仍只有 CLI，
本 ADR 不覆盖；不提供批量授予/撤销、不提供按显示名的模糊匹配授权、不提供自动重新信任，
也不改变插件导入来源、签名校验与 Hook 执行边界。

## 验证与参考

Go 覆盖授予的正常路径、期望代次过期、期望指纹不符、未签名安装、缺少确认、
方法与令牌拒绝，以及 CLI 兼容行为；界面覆盖按钮可达性、绑定值取自真实历史投影、
撤销后重新信任可解除阻塞但启用仍需单独批准。HTTP 契约由 OpenAPI 黄金文件与
"路由必须真实可达"检查约束。实现依据见 Issue #334 正文与
[ADR 0135](0135-pre-1-0-product-convergence.md) 的扩展边界。
