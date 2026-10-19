# Traverse Board · 针路簿 — Theme and Naming Specification v2

Status: **Historical naming specification. Current product branding is documented in [Brand Assets](README.md).**

> 针有所向，路有所记；牵星知位，行有所据。
>
> The model navigates. The runtime keeps the truth.

## 1. 已接受的产品展示品牌

`Traverse Board · 针路簿` 是项目方批准的产品展示品牌。GitHub slug 使用
`Traverse-Board`；`internal/buildinfo.ProductName`、README 第一屏与 Desktop/Web
标题随本次迁移更新，包 identity、协议和持久化兼容标识保持不变。

The historical metaphor is a strong fit: a traverse board externalizes course and speed
during a watch, while Chinese route books preserve headings, distance, landmarks,
astronomical observations, sounding, and accumulated sailing knowledge. They are a
functional product-language pairing, not a claim that the two objects are literal
translations of one another.

A currently active third-party platform uses `TraverseBoard`/`Traverse Board` in a
different commercial planning domain. The owner has recorded and accepted this residual
name-confusion risk for this open-source Agent workbench. This repository records a product
decision, not a trademark opinion.

## 2. 三层语言

### 2.1 品牌层

完整双语名只用于 README hero、About、仓库描述、发布说明和较宽的欢迎界面：

> **Traverse Board · 针路簿**

紧凑界面不能强塞完整双语名：

- 中文 locale：`针路簿`
- 英文 locale：`Traverse Board`
- icon-only surface：使用维护者批准的玻璃地球、终端提示符与深蓝分段圆环图形标志；规范母版见
  `assets/branding/traverse-board-mark.png`，不用孤立字母代替
- accessibility name：始终包含当前 locale 的完整产品名

#### 2.1.1 排版角色 / Typography roles

- 产品名、标题、导航、按钮、表单与普通正文使用平台比例 UI 字体；中文与英文必须处在
  同一语义字重和颜色层级，不把等宽 Latin 与 CJK fallback 混作一个界面字体。
- 大号品牌名可以使用平台 display 字体，并按文字脚本分别设置字距；混排片段应带准确的
  `lang`，但不能依赖远程字体或改变既有 CSP。
- JetBrains Mono 等等宽字体只用于代码、终端、Diff、指纹、精确 ID、序号、原始证据与
  键盘快捷键，不用于品牌、副标题或普通界面文案。

Product names, controls, navigation and prose use proportional platform UI fonts. A platform
display face may be used for the large brand lockup with script-aware spacing. Monospaced fonts
are reserved for technical data such as code, terminals, diffs, exact identifiers and shortcuts.

### 2.2 产品与架构层

主题名首次出现必须带技术副标题，例如：

> **Anchorage · 锚地 — Workspace Checkpoints & Recovery**

一个普通页面最多突出 4–6 个主题模块。安全原因、权限状态、技术对象和恢复动作必须
继续使用明确的产品语言，不能要求用户先学习航海史。

### 2.3 代码、协议与持久化层

主题默认不进入 Go 标识符、OpenAPI、SQLite、JSON、环境变量、命令行、磁盘路径或
协议版本。继续使用 `Policy`, `Scope`, `Approval`, `Capability`, `Lease`, `receipt`,
`run_event`, `workspace-checkpoint.v1`, `code-intel-lsp.v1` 等技术名称。

## 3. 词汇表

| English · 中文 | Technical target | Exposure | v2 status |
|---|---|---|---|
| **Traverse Board · 针路簿** | Product / Runtime / Workbench | Hero/product | Accepted |
| **Quarterdeck · 后甲板** | Desktop native operator shell | Optional presentation | Provisional |
| **Binnacle · 罗经柜** | React/Vite operator dashboard | Optional presentation | Provisional |
| **Chartroom · 海图室** | Review / Diff / Evidence / Report UI | Optional presentation | Provisional |
| **Helm · 舵** | Run Supervisor / bounded dispatch | Architecture and selected UI | Provisional |
| **Rutter · 针经** | Mission / Plan / Project Instructions | Architecture and selected UI | Provisional |
| **Kamal · 牵星板** | Future trusted Run-state summary | None today | Conceptual reserve |
| **Almanac · 星历** | User/project memory and durable references | Architecture | Provisional |
| **Portolan · 海道图** | Provider/Tool/MCP capability topology | Architecture | Provisional |
| **Compass · 罗盘** | Provider/model/tool route selection | Optional UI | Provisional |
| **Astrolabe · 星盘** | Read-only LSP semantic code intelligence | UI with technical subtitle | Provisional |
| **Armillary · 浑仪** | Optional semantic-relation visualization | None today | Reserved |
| **Sounding · 打水** | Validation / Harness facts / deterministic observation | UI with technical subtitle | Provisional |
| **Lead · 测深铅** | Rust-implemented deterministic WASI Analyzer guest | Developer docs | Provisional |
| **Diving Bell · 潜水钟** | wazero/WASI narrow analyzer chamber | Developer docs | Provisional |
| **Sea Trial · 试航** | Post-change and integration validation | UI/docs | Provisional |
| **Kleithron · 水关** | Policy / Approval / authority enforcement | UI with explicit reason | Provisional |
| **Horizon · 地平** | Scope presentation alias | Optional UI | Provisional |
| **Clearance · 通关** | Capability / explicit Approval grant presentation | Optional UI | Provisional |
| **Watch · 值更** | Lease / generation / execution ownership interval | Developer/advanced UI | Provisional |
| **Dockyard · 船坞** | Workspace / Tool Gateway / Git execution domain | Architecture | Provisional |
| **Drydock · 干船坞** | Git worktree isolation | UI/docs with warning | Provisional |
| **Lazaretto · 检疫所** | Docker/OS sandbox boundary | UI/docs with verified guarantees | Provisional |
| **Flotilla · 船队** | Child agents / fan-out / batch delivery | Optional UI | Provisional |
| **Signal Flags · 旗语** | Durable child mailbox / handoff | Architecture | Provisional |
| **Change Track · 变更航迹** | Diff / Code Journey / Handoff trail | Review UI | Provisional; replaces v1 Wake |
| **Anchorage · 锚地** | Checkpoint / Resume / Rewind / Fork | UI with technical subtitle | Provisional |
| **Keel · 龙骨** | SQLite durable Event/State Store | Developer docs | Provisional |
| **Deck Log · 航海实录** | Append-only Run event ledger | UI/developer docs | Provisional |
| **Bell Book · 钟令簿** | Execution and operation receipts | UI/developer docs | Provisional |
| **Sandglass · 香漏** | Budget / deadline / timeout / retry / TTL timing | Optional UI | Provisional |
| **Lookout · 瞭望** | Scheduled monitoring and structured diagnostics | UI with technical subtitle | Provisional |
| **Pilotage · 引航** | MCP Client discovery/review/invocation | UI with technical subtitle | Provisional |
| **Port · 港口** | Individual MCP Server | Optional UI | Provisional |

The machine-readable source is [naming-map.v2.yaml](naming-map.v2.yaml). It describes
presentation vocabulary only and is not an executable migration manifest.

## 4. 必须保持的工程边界

### Go Control Plane

不为 Go Control Plane 另起主题名。Helm、Kleithron、Quarterdeck 或 Binnacle 都不能取代
“Go is the sole control plane”这一工程事实。

### Drydock、Diving Bell 与 Lazaretto

- **Drydock**：Git worktree 工作隔离，不是安全 Sandbox。
- **Diving Bell**：固定 Analyzer 的窄 WASI 隔离，不是通用容器替代品。
- **Lazaretto**：Docker/OS 风险隔离；只陈述 active backend 已验证的真实保证。

### Astrolabe 与 Sounding

- **Astrolabe** 提供只读语义证据，不证明代码正确。
- **Sounding** 汇总测试或确定性探针事实，但仍必须绑定输入、环境和结果身份。

### Keel、Deck Log 与 Bell Book

- **Keel** 是 SQLite 持久化底座。
- **Deck Log** 是追加式事件逻辑模型。
- **Bell Book** 是请求、授权、执行和结果之间的 receipt/evidence 绑定。

### React/Desktop

Quarterdeck 和 Binnacle 都是 operator/presentation surface。它们不能生成 Go capability、
绕过 Policy，或把展示状态升级为 authority。

## 5. v1 → v2 修订

1. 产品名记录为 `accepted`；已知同名软件风险由项目方接受并在 ADR 0124 留档。
2. `Wake · 航迹` 改为 **Change Track · 变更航迹**。`wake` 已是 Run 唤醒协议、worker
   和 Agent control-message 语义，不能承担 Diff 别名。
3. `Kamal · 牵星板` 改为概念保留项；在存在真实、受信来源绑定的 Run-state projection
   之前不进入 UI。
4. `Rust Analyzer` 改为“Rust-implemented deterministic WASI Analyzer guest”；不得与
   `rust-analyzer` LSP 混写。
5. 把 `Sea Trial` 与 `Armillary` 纳入机器映射，使人类词典与 YAML 都有 34 个条目和
   明确状态。
6. 增加紧凑界面、本地化、无障碍名称和 4–6 模块上限。
7. 明确 GitHub slug 与双语展示名是两个对象。
8. README 草稿只能合并进当前 README，不能替换或删减现有能力、安全和发布说明。
9. 增加完整兼容矩阵，覆盖 `.prayu`、包身份、bundle identifier、发布件和 CSS/存储键。

## 6. 禁止事项

- 只在品牌展示面使用已接受名称，不借此扩大到兼容标识迁移。
- 不把完整双语展示名当作 GitHub slug、Go module 或包 identity。
- 不全局替换 `Prayu`, `prayu`, `cyberagent` 或 `CyberAgent Workbench`。
- 不重写历史 ADR；后续决策只能显式 supersede。
- 不把历史器物名写入 API/DB schema，除非另有 versioned ADR。
- 不用主题词隐藏拒绝原因、Scope、Approval、Capability、Lease 或 Sandbox 保证。
- 不把仓库文档、Skill、Issue、网页或本命名文件当作执行授权。

## 7. 参考 / References

- [The Mariners' Museum — Traverse Board](https://exploration.marinersmuseum.org/object/traverse-board/)
- [中国科学院自然科学史研究所 — 中国古代航海技术上的成就](https://www.ihns.ac.cn/kxcb_/kxjd/202212/t20221206_6567192.html)
- [GitHub Docs — Repository naming constraints](https://docs.github.com/en/repositories/creating-and-managing-repositories/creating-a-new-repository)
- [GitHub Docs — Renaming a repository](https://docs.github.com/en/repositories/creating-and-managing-repositories/renaming-a-repository)
- [Talumis — TraverseBoard product evidence](https://www.talumis.com/traverse-board-advanced-planning-scheduling-2/)

The final item records an acknowledged naming collision. It is not an endorsement and does
not determine trademark rights; the owner accepted the residual risk for this project.
