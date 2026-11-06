# ADR 0195 站点级授权草案：OCR delegate 复审

日期：2026-11-05。基线：`6a10b7c8`（origin/main，含 #519–#524 与隔离区改进）。
评审对象：PR 分支 Qiyuanqiii/adr-browser-site-authorization（单提交 531580e2，docs-only）。
主题：#491 最后一片（P1 站点级授权）的设计草案 ADR——Status: Proposed，交维护者
评审后再按 4 个 Phase 实施。核心提议：操作者声明的 origin 集（1..8，TargetScope
身份规则）承载于 scoped authority 变体（ask/auto 合法），运行时以 safe-web 的
重验证纪律强制导航与重定向，越界导航以命名错误失败，`browser_origin_add` 走既有
per_call 审批账本做追加式 origin 扩充；隔离边界（一次性 profile、下载隔离、证据链、
per_call 审批）完全不变，Full 会话保持 unscoped 行为。

OCR 负责文件选择（`ocr delegate preview`：docs-only 单文件）与规则解析，主 agent 与
独立评审子代理实际阅读判断。评审子代理逐条核验 ADR 的六项技术声明与代码库现实
（双层 Full 激活门、agent 浏览器无 origin 强制、scope.go 能力、敏感 effect 枚举、
接管模式、ADR 惯例），并专项评估设计遗漏。

## 覆盖范围

纳入审查 1 个（评审记录不计入）：
- docs/adr/0195-agent-browser-site-level-authorization.md（新增，全文阅读）。

具体明细见 [coverage.json](coverage.json)。

## 发现与修复（评审子代理报告，全部已并入 ADR）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| F1，P2 | 子资源授权点被高估为"现成纪律复用"：agent 浏览器走裸 `Page.navigate` + 事件白名单 transport，**没有** `Fetch.enable/requestPaused`（该机制在 restricted transport）；实现子资源强制需扩 agent-browser transport 白名单并新写 Fetch 拦截，Phase 1 未列此改动面。 | §3 与 Phase 1 如实写明两个实现选项（agent-browser transport 加 Fetch 域，或迁移到 restricted transport）及白名单变更。 |
| F2，P2 | 会话中途 `browser_origin_add` 与指纹绑定 authority 冲突未设计：每次调用校验 `CapabilityGeneration == a.Generation`，origin 集变更必然要求世代递增；pending 审批与接管窗口下的一致性未定义。 | Phase 3 增补世代语义：origin 集变更递增 authority generation、旧世代调用以命名错误失败、旧集合的 pending 审批作废、并发语义在 Phase 3 设计后再实现。 |
| F3，P3 | `NewTargetScope` 要求 builtin registry 的 profile，而 agent 浏览器用独立的 `agentBrowserProfile`（不在 registry）——"复用 TargetScope 身份规则"需要适配点。 | §1 注明构造变体（显式携带 limits；1..8 origin 上限与 builtin `MaxOrigins` 一致）。 |
| F4，P3 | 新 effect 的真实改动面（归一化 effect 白名单与工具 schema enum 两处硬编码）未列。 | Phase 3 列出两处枚举点及 ActionClass 推导（`browser_browser_origin_add` 无 ledger 冲突）。 |
| F5，P3 | References 的 issue 无链接（0190/0191 惯例是链接）。 | 补链接。 |

评审确认（六项声明全部属实）：双层 Full 激活门（service 创建 + toolgateway 校验）、
agent 浏览器无 origin 强制（裸 Page.navigate，scope.go 机制全部在 safe-web/CDP 产品）、
scope.go 能力可复用（1..8、重验证、link-local/metadata 拒绝）、现有敏感 effect 仅两种、
接管模式与 #524 合入一致、Status: Proposed 与 0191-0193 惯例一致。**无阻塞性发现；
ADR ready for maintainer review**——F1/F2 是实现前必须补的设计缺口，已在 ADR 中如实
列为对应 Phase 的改动面。

## 验证

- docs-only 单提交（+118 行）；无代码路径变更，CI 由 docs 检查执行。
- ADR 的每一项技术声明均由评审子代理对照源码核验（含行号）；两处设计缺口
  （子资源授权实现点、origin 变更的 authority 世代语义）已并入 ADR 正文而非遗留。
