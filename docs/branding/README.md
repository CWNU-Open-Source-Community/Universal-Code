# 品牌资产与历史迁移 / Brand Assets and Migration History

当前产品名称为 **Universal-Code**，正式图标见下方「官方图形标志」。
以下命名迁移文档保留此前的决策记录。

项目方曾批准 **Traverse Board · 针路簿** 作为产品展示品牌，并批准
`Traverse-Board` 作为 GitHub 仓库 slug。此次迁移只改变产品展示与规范仓库地址；
`.prayu`、`cyberagent`、协议、持久化 schema、安装 identity 等兼容接口继续保留。

The owner previously approved **Traverse Board · 针路簿** as the product display brand and
`Traverse-Board` as the GitHub repository slug. This migration changes presentation and
canonical repository URLs while preserving compatibility identifiers.

## 文件 / Files

| 文档 | 用途 |
|---|---|
| [Theme and Naming v2](theme-and-naming-v2.md) | 已接受品牌、展示规则、34 项词汇表和 v1 修订说明 |
| [Machine-readable naming map](naming-map.v2.yaml) | 状态明确的机器可读展示映射；不授权兼容标识迁移 |
| [Compatibility and migration matrix](compatibility-matrix.md) | 展示名、仓库地址、协议、数据目录、包身份和发布件的迁移边界 |
| [ADR 0124](../adr/0124-traverse-board-branding-migration.md) | 品牌决策、仓库 slug、后果与兼容边界 |
| [ADR 0125](../adr/0125-traverse-board-windows-executable-name.md) | `v0.1.0-rc.2` Windows 对外 EXE 文件名迁移 |
| [ADR 0145](../adr/0145-windows-two-deliverable-release-contract.md) | Windows Store 包、直发 EXE 与内部验证件边界 |

## 官方图形标志 / Official Mark

维护者提供的玻璃地球、终端提示符与深蓝分段圆环图形是 Universal-Code 的正式图标。规范母版位于
[`assets/branding/traverse-board-mark.png`](../../assets/branding/traverse-board-mark.png)，
尺寸为 1254×1254，保留原始白色圆角承托；SHA-256 为
`a97e71e5f373d5d48ce24e39360cd390be2beabfe731037ac114ec38f12407d6`。

Web、Windows 与 macOS 资产只允许从该母版进行确定性的缩放、圆角透明遮罩和容器
格式转换，不得用字母 `T`、旧 `P` 标志或生成式重绘替代。小尺寸界面使用图形标志
本身，并由周围的可访问名称提供完整产品名。运行
`pwsh ./scripts/generate-brand-assets.ps1` 可一次重建所有受版本控制的平台派生件。

The maintainer-supplied glass globe, terminal prompt, and segmented navy ring are the official Universal-Code icon.
Platform assets are deterministic resize, rounded-alpha-mask, and container derivatives of
the exact supplied master above; generative redraws and letter placeholders are not canonical.

| Surface | Derived assets |
|---|---|
| React/Web | `web/src/assets/traverse-board-mark.png`, 32px favicon, 180px touch icon |
| Windows | MSIX Store/44/150px PNGs, 300px Partner Center listing icon, multi-resolution ICO, amd64/arm64 executable resources |
| macOS | Complete 16px–1024px `TraverseBoard.icns` copied into the signed app bundle |

## 历史迁移结论 / Historical Migration Decision

- 历史航海主题与当前 Go-owned、可恢复、可审计架构相符。
- 项目方确认本项目是方向不同的开源 Agent 工作台，接受市场上存在同名商业软件这一
  已知残余风险；该判断是项目命名决策，不构成商标法律意见。
- GitHub 仓库名不能包含空格、中点或中文，因此 slug 使用 `Traverse-Board`；完整双语名
  用于 README、仓库描述和产品界面。
- v2 使用 **Change Track · 变更航迹** 表示 Diff/Code Journey/Handoff trail，避免与
  已有 `run_wake_intent.v1`、Run Wake worker 和 Agent `wake` 控制消息冲突。
- **Kamal · 牵星板** 只保留为未来 Run-state summary 概念；当前没有独立的
  `Trusted Run Fix` 领域对象或 authority source。
- “Rust Analyzer”必须写成“Rust-implemented deterministic WASI Analyzer guest”，
  避免与 `rust-analyzer` 语言服务器混淆。
- 从 `v0.1.0-rc.2` 开始，Windows 对外可执行文件名为 `TraverseBoard.exe`；内部 Go
  入口、数据与安装 identity 继续遵循兼容边界，详见 ADR 0125。
- 当前 Windows 用户成品仅指 Microsoft Store 包与可直接双击的 `TraverseBoard.exe`；
  便携 ZIP/sidecar 属于内部验证或证据，源码与新 Release 不再提供 Start 脚本，详见
  ADR 0145。该边界不表示 Store 认证或正式签名已经完成。

## 授权边界 / Authorization Boundary

ADR 0124 授权展示品牌与 GitHub slug 迁移，但不授权发布版本、迁移用户数据、替换
兼容标识或执行全局字符串替换。实施变更仍须经过精确 diff、验证和可回退审查。

ADR 0124 authorizes the display-brand and GitHub-slug migration. It does not authorize a
release, user-data migration, compatibility-ID replacement, or global search-and-replace.
