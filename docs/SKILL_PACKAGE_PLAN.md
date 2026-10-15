# CyberAgent Workbench Custom Skill Package Plan

状态：`skill_package.v1` 纯内存校验、schema v69 内容寻址本地 Registry、schema v70 外部 Skill Run 固定选择与最小上下文、schema v71 安全只读来源投影、schema v104 签名/固定来源团队 Catalog、schema v111 外部模式元数据账本，以及 schema v112 不可信生成候选人工审查均已完成。Desktop/Wails 与 HTTP 继续提供显式确认的惰性安装；Marketplace 与安装时执行没有开放。

## 当前能力

现有 Go `skill.v1` 已严格定义名称、语义版本、描述、兼容 Profile、工具前置声明、Markdown 内容路径、UTF-8 字节数、保守 token 上界和 SHA-256。当前内置 Manifest 还显式声明 `surfaces`（Code/Cyber）、`phases`（Plan/Deliver）、`roles`（root/Specialist）以及 `user_invocable`、`model_invocable`、`explicit_only` 调用策略；四维兼容必须同时命中，调用许可与正文交付分别判断，任何字段都不授予能力。`internal/skills.LoadFS` 可以从受控 `fs.FS` 构造不可变 Registry，当前编译进二进制的十四项为 `code`、`review`、`learn`、`script`、`plan-delivery`、`doctor`、`debug`、`run-verify`、`focused-checks`、`simplify`、`security-review`、`run-skill-generator`、`loop-monitor` 和独立的 `frontend-design`，另支持测试夹具。分类与真实验收边界见 [内置技能](builtin-skills.md)。

Profile Skill 中 `code/learn` 仅支持 Code，增强后的 `review@1.5.0` 支持 Code 与 Review Profile，`script` 支持 Code 与 Cyber；它们支持 Plan/Deliver、root/Specialist 和 user/model 来源。`plan-delivery` 跨全部 Profile 与 Surface，但仅支持 root 且必须由操作者显式选择。六项新增通用能力均为 root 可组合指导：`doctor` 仅 Plan，`run-verify`、`focused-checks` 和 `simplify` 仅 Deliver，`debug` 与 `security-review` 跨两阶段。`review@1.5.0` 和 `focused-checks@1.2.0` 可消费当前 `code-intel-lsp.v1` 与 GitHub Review evidence graph，但不会把语义或远端内容当成能力授权；旧版已进入有界内嵌归档以保持既有 Run 可恢复。schema v110 按当前 mode 固定每轮兼容子集并允许审计空交付。schema v111 让外部安装账本保存同一模式字段且保持 legacy 指纹；schema v112 的 `run-skill-generator` 只在 Code/Deliver/root 显式选择后开放候选提案，生成、人工审批、惰性安装与 Run 选择互不替代。ADR 0160/schema v173 增加内置 `skill_read` 目录与读取入口：普通 root 对话可读取当前兼容且 model-invocable 的精确版本，成功收据驱动后续请求重建指导。该路径不修改操作者选择，不包含 explicit-only 条目，不启用外部包自主发现、安装或授权。

当前产品入口包括：

- `cyberagent skill list`
- `cyberagent skill show`
- `cyberagent skill validate`
- `cyberagent skill package validate <package.zip>`
- `cyberagent skill import <package.zip> --surface code|cyber --operation-key <stable-key> --confirm-untrusted-skill`
- `cyberagent skill installed [--surface code|cyber] [--profile <profile>] [--include-removed]`
- `cyberagent skill installed show <name>@<version>`
- `cyberagent skill remove <name>@<version> --operation-key <stable-key> --confirm-remove`
- `GET /api/v1/skills/packages` 与桌面/Web 的“Skill 包”设置页：列出历史登记项，并逐包给出真实的包对象回读结果；`include_removed` 才显示 tombstone
- `POST /api/v1/skills/packages/remove` 与桌面同一入口：显式确认后为精确 `name@version` 追加不可变 tombstone，复用同一操作键与恢复语义
- `cyberagent skill select-external <run-id> <name>@<version>... --operation-key <stable-key> --confirm-untrusted-skill-context [--specialist <name>@<version>]`
- `cyberagent skill external-selection <run-id>`
- `cyberagent skill select`
- `cyberagent skill selection`
- `cyberagent skill candidates [--run <run-id>]`
- `cyberagent skill candidate show|approve|reject|import ...`

其中 `skill package validate` 只通过有界普通文件读取和纯内存 parser 返回 metadata-only 风险预览，不创建数据库、不落盘、不安装、不执行正文，也不访问网络、Provider 或工具。schema v69 的 `skill import` 在显式确认后只增加不可变元数据与原始包对象，仍不执行正文、联网、调用 Provider/工具或授予能力；schema v111 将模式元数据绑定到同一安装意图。schema v70 再要求独立 Run 级确认，固定精确 active 版本，并只把脱敏正文作为用户角色的非可信工作流指导交付；包内工具声明不授权。schema v112 的候选正文是单独的、有界不可信审查数据，只有 `show --show-content` 显式显示；批准与二次确认后的导入仍不自动选择。签名、URL/Git 固定来源和团队 Catalog 见 schema v104，在线 Marketplace 仍未开放。

## 第一版包边界

- `skill_package.v1` 第一版只允许一个严格 `skill.v1` Manifest 和一份 UTF-8 Markdown 指导正文。
- ADR 0024 已固定确定性 ZIP：根目录按顺序只能包含 `manifest.json`、`SKILL.md`，两项都使用 Deflate、零时间戳、无 extra/comment/属性/前缀/间隙/尾随数据，并使用固定 ZIP 2.0 data-descriptor profile。
- archive 最大 64 KiB，文件数固定为 2；解压总量、单项大小与压缩比均有硬上限。`archive_sha256` 标识原始容器，`package_fingerprint` 通过协议版本、规范 Manifest 和正文的长度分帧标识语义内容。
- 禁止绝对路径、`..`、反斜杠歧义、重复/大小写碰撞条目、symlink、hardlink、设备、FIFO、嵌套压缩包、尾随数据和 ZIP bomb。
- 限制压缩包字节、解压字节、文件数、目录深度、单文件大小和压缩比；所有限制在读取正文前执行。
- 第一版不接受可执行脚本、二进制、动态库、任意资源下载或安装钩子。后续脚本资源仍只能作为 Artifact，由 Go Tool Gateway、Policy、Scope 和审批独立授权。

## 信任与权限

- 包内容是操作者安装的工作流指导，不是系统策略，也不能覆盖 Policy、Scope、预算、Approval、Sandbox 或来源隔离。
- `tool_dependencies` 仍只是前置声明，永不授予工具能力。
- 导入不会执行正文、脚本、命令、网络请求或 Provider 调用。
- 外部包固定标记为 `operator_installed_untrusted`；安装不会自动影响 Run，schema v70 另需操作者显式确认精确 Run 选择。
- 上下文交付会重新执行版本/hash/bytes/Profile/对象语义精确复核、secret redaction、独立 token 预算和 capability=false 来源记录；正文只进入当前 Provider request 的用户角色信封。
- schema v71 只读投影固定 `tool_capability_grant=false`，只报告声明工具数量，不返回工具名称，更不把声明提升为授权；投影按 Run 显式关联并限制最多四个条目、一个 Specialist。
- Code 与 Cyber Catalog 分离。Cyber 默认不继承 Code Skill；Script Profile 只能使用明确兼容的窄化脚本指导。

## 本地 Registry

- Go 在用户数据目录的 `skill-registry/objects/sha256/<prefix>/<digest>.zip` 维护 content-addressed 对象；正文不进入仓库、SQLite 或 Run 事件。
- SQLite 安装 Registry 只保存包摘要、完整模式元数据、信任状态、安装/完成操作和移除 tombstone；schema v112 另在候选表中保存最多 4096 字节、带精确摘要的不可信待审正文。候选、审查和导入收据均不可更新或删除。
- 相同对象并发发布收敛；同名同版本已有不可变安装时冲突；Registry 最多 64 个历史包身份，同名最多 8 个版本。
- 已被 Run 精确固定的版本不能移除。移除只追加 tombstone 且保留对象；恢复/重装需要未来显式协议，不静默推断。
- 导入先写安装意图，再使用同目录独占临时文件、file sync、原子 hard link 和完整回读验证发布对象，最后写完成结果；崩溃后以同一 operation key 恢复。目录 sync 在 Windows 上为 best effort，不作超出平台证据的持久性声明。
- 写入接口只有 `Put` 与 `Verify`，另有与写权限分离的只读 `PackageObjectLoader`；它没有执行或删除方法。每次完成重放、列表、详情和 Run 上下文加载都重新验证普通文件身份、字节数、archive SHA-256、ZIP 结构与语义指纹。

## 产品入口

CLI 第一阶段：

```text
cyberagent skill package validate <package.zip>
cyberagent skill import <package.zip> --surface code|cyber --operation-key <stable-key> --confirm-untrusted-skill
cyberagent skill installed [--surface code|cyber] [--profile <profile>]
cyberagent skill installed show <name>@<version>
cyberagent skill select-external <run-id> <name>@<version> --operation-key <stable-key> --confirm-untrusted-skill-context
cyberagent skill external-selection <run-id>
cyberagent skill remove <name>@<version> --operation-key <stable-key> --confirm-remove
cyberagent skill candidates [--run <run-id>]
cyberagent skill candidate show <candidate-id> --show-content
cyberagent skill candidate approve|reject|import <candidate-id> ...
```

HTTP 入口覆盖上面两条 Run 相关命令，并在一次调用里合并它们的准入：
`GET /api/v1/runs/{run_id}/external-skills` 读取既有投影，
`POST /api/v1/runs/{run_id}/external-skills/select` 用控制 bearer 与
`Idempotency-Key` 选择任意 `name@version`，内置名仍要求精确 active 版本，外部名要求
已安装、具资格并显式确认不可信上下文；任务草稿界面据此提供勾选。
`skill installed`/`skill remove` 对应 ADR 0185 的清单与移除端点。固定项目建议的
`POST /api/v1/runs/{run_id}/project-skills/select` 保留更窄的 pin 准入，两条路由
的差别只在准入规则，见 ADR 0187。

Desktop D1 阶段：

- ADR 0033 已完成 Go 预览边界：原生选择器只把路径交给 Go closure；TypeScript 只得到五分钟过期、单次消费的不透明句柄，不解压、不校验，也不能提交任意路径。
- D1-B1 新增第四个窄 Wails 方法 `InstallSkillPackage`。它只消费 Go 发放的确认句柄并调用 Registry service；renderer 仍不接触路径或 archive bytes。
- `POST /api/v1/skills/packages/install` 使用独立 capability/control token、严格 Origin/Host/Content-Type、64 KiB archive 上限、canonical base64、幂等 operation key 和 metadata-only 结果。普通 read token 无权调用。
- 两条安装路径都要求明确确认 `operator_installed_untrusted`，并固定 command/hook/network/Provider/tool/Run-selection/context-delivery authority 为 false。
- Web 远程上传、URL 安装、Git 仓库安装和在线 Marketplace 均后置，不允许第一版自动下载并信任内容。

## 签名与分发

- 本地未签名包可以在明确警告后安装，但保持 untrusted 标记。
- 签名包需要独立 publisher identity、签名算法、证书轮换、吊销和透明来源设计；签名只证明发布者和完整性，不授予能力。
- 官方内置 Skill 与用户安装 Skill 使用不同 trust class 和 Catalog，不允许用户包覆盖内置名称/版本。

## 实施切片

1. [x] 固定 `skill_package.v1` 和威胁模型，完成纯内存 parser/validator/fuzzer 与 metadata-only CLI 校验，不写磁盘。
2. [x] schema v69 增加 content-addressed 本地 Registry、不可变安装/移除账本、原子导入/恢复和 CLI；导入保持惰性。
3. [x] schema v70 将用户 Skill 纳入 Run 的精确版本选择、root/Specialist 最小上下文和 Code/Cyber 分离测试；安装与 Run 上下文使用两次独立确认。
4. [x] schema v71 增加按 Run 隔离的有界只读来源/交付投影，并接入 HTTP/OpenAPI、TUI 与 React；所有内容、摘要和私有身份保持在边界内。
5. [x] Desktop D1-A 增加路径隔离的 Go-owned 本地验证预览桥，D0-A 再把原生 selector 接入 Wails `.zip` 对话框和只读 UI；renderer 永不接收路径/正文。
6. [x] Desktop D1-B1 增加 handle-only Wails 确认安装和 canonical-base64 HTTP control；两者复用内容寻址惰性 Registry，独立 capability 默认关闭，安装不执行正文或授予能力。
7. [x] schema v111 为外部安装保存规范化 Surface/Phase/Role 与调用策略，mode-aware 意图升级为 v2 指纹并精确保留 legacy v1。
8. [x] schema v112 增加 `run-skill-generator`、真实工具来源绑定的候选、exact-fingerprint 人工审批/拒绝、二次确认导入和崩溃恢复；导入后不自动选择。
9. [x] schema v104 已提供签名包、团队 Catalog 与固定 URL/Git 来源；在线 Marketplace 和安装时执行继续不属于基础版本。

## 已验证基线

- v69 最终全仓普通/race 测试分别通过于 259.7 秒/275.3 秒；vet、零告警 staticcheck、module verify/tidy diff 与零可达漏洞 govulncheck 通过。
- parser 的既有 20 秒约 2645 万次 fuzz 基线保持有效；新增对象发布、Store、Application 和 CLI 定向测试覆盖三轮 race、双 Store 收敛、崩溃恢复、腐坏/symlink/取消、伪造收据和 SQL 旁路；真实双 Service 独立生成候选身份的导入/移除收敛另通过 20 轮普通与 10 轮 race。首次 Linux CI run `29556933994` 暴露并发嵌套目录准备失败；逐级创建并验证真实目录后，12 个独立 Store 通过 100 轮普通与 20 轮 race。
- TypeScript/OpenAPI/production build、9 个文件 22 项前端测试与零漏洞 npm audit 已在 v71 定向基线通过；v71 新增一个只读 GET path 和三个安全 DTO schema。
- v71 最终全仓普通/race 分别通过于 227.1 秒/301.1 秒；vet、零告警 staticcheck、module verify/tidy diff、零可达漏洞 govulncheck、确定性生成、仓库隐私/diff 扫描与隔离真实 CLI schema-v71 smoke 全绿。空选择 Run 的 detail 省略/endpoint 404 普通与 race 回归也已通过。
- Desktop D1-A 最终全仓普通/race 分别通过于 255.8 秒/314.0 秒；Desktop 普通 100 轮/race 25 轮、Skill 文件边界 100 轮、CLI 包链 10 轮与 32 方并发消费测试通过，vet、零告警 staticcheck、module verify/tidy diff、零可达漏洞 govulncheck、22 项前端测试、OpenAPI drift、production build、零漏洞 npm audit、60 份 Markdown/79 条相对链接及仓库隐私扫描全绿。GitHub Actions run `29578985787` 已通过实现提交 `45c047c`（Go/Linux 3 分 13 秒，TypeScript 27 秒）。该切片不改变 schema v71。
- 审计修复旧 schema 夹具未先移除 v69 trigger、错误文本静态规则、冗余临时清理状态、对象收据接口绑定、发布前取消点、并发请求因独立 ID/时间被误判为改意图，以及 Manifest 自由文本 description 未脱敏进入 SQLite；当前无已知未解决高/中风险。
- 首次 Linux CI run `29556933994` 暴露并发嵌套目录准备失败；修复提交 `d28b100` 的 GitHub Actions run `29557803407` 已全绿（Go/Linux 3 分 21 秒，TypeScript 23 秒）。

## 验收标准

- 恶意 ZIP、路径逃逸、链接、特殊文件、超大包、重复项、Unicode/大小写碰撞和内容 hash 漂移全部失败关闭。
- 导入前后不会执行命令、访问网络、调用模型或改变工具权限。
- 并发导入、重启恢复和相同 operation 重放收敛到同一不可变版本。
- v69 已保证安装与 tombstone 跨重启恢复；v70 已完成外部 Run 固定版本、Go/SQL 移除门禁、root/Specialist 上下文恢复与首次模型调用原子提交；v71 已完成安全只读投影；v111 保证模式字段/指纹回读；v112 保证未经 exact-fingerprint 人工批准和独立不可信确认的候选不能导入，导入收据也不能伪装成 Run selection。
- CLI、HTTP 与 Desktop 对同一包复用同一 parser/Registry 语义并产生相同 digest、信任与风险事实；Desktop 不暴露路径/bytes，HTTP 拒绝非规范编码，跨入口重放收敛到同一不可变安装结果。
