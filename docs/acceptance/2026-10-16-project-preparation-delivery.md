# #317 依赖准备与连续交付补验

2026-10-16，在 Windows 上补验 [#317](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/317)
剩余流程。Docker 的 React、Go 项目和 Docker Sandboxes 的 Go 项目均完成：
空缓存准备真实依赖、未修改原测试退出 1、真实模型提案并应用单文件修复、
模型运行原测试退出 0、操作者独立复测退出 0，以及所选计划的人工验收。

本轮保留 9 个会话尝试，其中 3 个完成所选计划，另外 6 个失败或未完成，完成率为 3/9。
这些会话共使用 124 次模型调用、2,160,210 token，Provider 资格调用另计。
这是调试和验收记录；各次尝试及模型调用数均保留在
[结构化结果](../evidence/project-preparation/issue317-delivery-result.json)。
前一轮准备、取消和恢复记录见 [#318 验收](2026-10-14-project-preparation.md)。

最后一轮 Docker Go 使用后台启动握手修复冻结后重新构建的程序，
真实模型通过后台 `start` 和两次 `wait` 完成原测试。

## 环境与执行入口

验收程序使用生产 Desktop composition root、HTTP handler、Git 工作区、SQLite、
依赖准备和模型执行服务；另建隔离应用目录、loopback 监听及独立读写令牌。
真实 Provider 为 `deepseek/deepseek-v4-flash`，通过系统凭据库读取既有授权配置。
验收程序未读取或复制密钥，也未以模拟模型代替验收。

| 环境 | 固定依赖 |
| --- | --- |
| Docker Engine | Engine `29.6.2`；镜像 `sha256:c3fbe9ca922a26b4230203d277c19651963314dce67b06e59be030b027259850`；Node `24.21.0`、Go `1.26.9` |
| Docker Sandboxes | SBX `0.47.0`；模板 `docker.io/docker/sandbox-templates@sha256:bbef0c36f33daa5da4b57b7a504e8c945cdac52bcfe94141a51aee63333fc36e`；Go `1.26.0` |
| Windows Local 限制复测 | Node `24.14.0`，真实 LPAC/AppContainer 子进程 |

本轮采用 #318 已固定、支持依赖准备的 Docker 镜像。#335 的 Go runner
`sha256:5f5fea90318d0b0a0e4bbbe66e167e59851158d96557b5b5993792b64ff5b7c1`
用于不同能力的批量执行，未用来替代准备能力。
SBX 使用已合并 #352 的模板工具路径，无临时工具路径 overlay。

包下载由产品依赖代理按锁文件和操作者授权执行；安装和测试进程均为
`network=disabled`、`credentials=none`，没有回退到宿主测试。
三个成功案例的源文件均由模型通过 `workspace_change` 和 `workspace_apply` 修改，
宿主源码补丁数为 0。

## 准备、修复与原测试

| 步骤 | Docker React | SBX Go | 最后一轮 Docker Go |
| --- | --- | --- | --- |
| 锁定依赖 | React `19.2.8`、Vite `8.2.1`，42 个条目 | `github.com/google/uuid v1.6.0`，2 个条目 | 相同 UUID 版本，2 个条目 |
| 未授权来源 | 明确拒绝，下载 0 字节 | 明确拒绝，下载 0 字节 | 明确拒绝，下载 0 字节 |
| 来源授权后 | 下载并校验 151,589,763 字节；离线 `npm ci`、已确认的 `npm rebuild` 均退出 0 | 下载并校验 32,011 字节；VM 内 `go mod download` 退出 0 | 下载并校验 32,011 字节；Docker 内 `go mod download` 退出 0 |
| 同操作键重放 | 返回同一准备 operation | 返回同一准备 operation | 返回同一准备 operation |
| 原测试 | counter 的 `-1 !== 5`，退出 1 | UUID 大小写断言失败，退出 1 | 同一 UUID 原断言失败，退出 1 |
| 缓存复用 | 42/42 命中，新增下载 0 字节，再次离线安装成功 | 2/2 命中，新增下载 0 字节，再次离线安装成功 | 2/2 命中，新增下载 0 字节，再次离线安装成功 |
| 模型改动 | `src/counter.js` 中减法改加法 | `normalize.go` 返回解析后规范 UUID | `normalize.go` 返回解析后规范 UUID |
| 模型测试 | 原 `npm test`：`node --test && vite build`，退出 0 | 原 `go test ./...`，退出 0 | 原 `go test ./...`，后台 start 后 wait 得到 0 |
| 独立复测 | 相同原 `npm test`，退出 0 | 相同原 `go test ./...`，退出 0 | 相同原 `go test ./...`，退出 0 |

React 模型测试的真实输出包含原断言通过、`pass 1 / fail 0`、Vite `8.2.1`
生产构建和生成的 `dist` 文件。完整命令 argv、输出、后端与清理状态在结构化结果内。
三个成功案例的模型均使用后台 `start` 后通过 `wait` 读取最终结果。

| 案例 | 原回归 Job | 模型启动的测试 Job | 操作者独立复测 Job |
| --- | --- | --- | --- |
| Docker React | `command-job-78737846d0d627f3c51eb0c4` | `command-job-9337736ef8d35b1d2c9653e8` | `command-job-0ea8a405abc9fbfdd593adda` |
| SBX Go | `command-job-d06c28a7b3893e218f193f77` | `command-job-ffacef155ec5925b094f98da` | `command-job-4cfcb87966a1a9046dab9193` |
| 最后一轮 Docker Go | `command-job-45441170d09c8dc40741dd85` | `command-job-e8503ca14be6ea6b2cf1bcec` | `command-job-0742e95199587e73192c0fd3` |

三条成功会话各有一个持久化 applied 提案，其原始/目标 SHA256 与实际磁盘一致。
原测试、锁文件、README 和其他源码的前后 SHA256 均一致。
源码改动见 [React 模型 diff](../evidence/project-preparation/issue317-docker-react-model-source.diff)、
[SBX Go 模型 diff](../evidence/project-preparation/issue317-sbx-go-model-source.diff)
和 [最后一轮 Docker Go 模型 diff](../evidence/project-preparation/issue317-docker-go-model-source.diff)。

## 交付状态与人工介入

Docker React 使用 17 次真实模型调用、324,707 token；SBX Go 使用 18 次、358,259 token；
最后一轮 Docker Go 使用 12 次、201,475 token。
三条成功会话都没有额外手动 Continue，也没有额外文件编辑审批。
操作者选择了明确的单模块方向，并要求人工验收。
模型测试和独立复测完成后，操作者通过正常计划控件记录验证、diff、安全和交接检查，
完成所选工作项。

三者最终均为：所选模块 `completed`，检查点 `1/1`，Run `paused`、阶段 `Deliver`，
执行租约已释放。这是产品正常的人工计划验收状态；“本轮完成”指所选模块和原测试验收完成。
Run 的状态没有变成 `completed`，本轮不把它记为自动完成整个 Run。
非所选工作项按原状态保留。

每条成功会话按可区分的操作者步骤计 17 次：任务创建、后端/信任配置和 auto 权限 3 次，
Plan 与修复消息 2 次，方向和进入 Deliver 2 次，首次拒绝/来源确认/请求重放/缓存复用 4 次，
原始测试与独立复测 2 次，暂停 1 次，模块启动/检查点/完成 3 次。
配置时另有一次信任摘要预览 POST；按控制 POST 计为 18 次。
这些是验收驱动承担的操作者步骤，不是实际鼠标点击次数。
Provider 资格核对、只读轮询、文件摘要核对及下述进程停止/启动另列，不混入计数。
Docker React 和 SBX Go 的隔离应用目录各有 2 次真实 Provider 资格调用，
以及资格确认和路由选择共 2 次控制 POST。
最后一轮 Docker Go 的首次资格请求网络失败，正常重试后通过资格检查；
保留失败请求 1 次和成功资格调用 2 次，共 3 次，资格确认/重试和路由选择共 3 次控制 POST。
Docker React 和 SBX Go 的验收服务首次启动各有一次 readiness `context deadline exceeded`，
按相同命令重试后启动；原始启动日志保留。这些服务启动重试另计，不计入 Run 内的 17 步。
最后一轮 Docker Go 的验收服务 readiness 首次通过；此前 Docker Desktop 的恢复另见下节记录。

## 停止与恢复覆盖

三个成功案例都在 `paused` 且无执行租约时，停止自有验收服务并重新打开相同应用目录：

| 案例 | 恢复后准备记录 | 恢复后模型/命令数 |
| --- | --- | --- |
| Docker React | 3 个 operation 的 ID、状态、Job ID、下载量均保持 | 模型调用仍 17 次，Job 仍 7 个 |
| SBX Go | 3 个 operation 的 ID、状态、Job ID、下载量均保持 | 模型调用仍 18 次，Job 仍 5 个 |
| 最后一轮 Docker Go | 3 个 operation 的 ID、状态、Job ID、下载量均保持 | 模型调用仍 12 次，Job 仍 5 个 |

恢复未再次调用模型、安装或测试。这次覆盖的是静止任务的服务重启和持久结果回读，
没有中断正在执行的安装或 VM 命令。下载中取消、原请求恢复及显式重试沿用
[#318 的独立记录](2026-10-14-project-preparation.md#已完成的准备与恢复检查)。

## 调试中保留的失败

| 尝试 | 实际结果与介入 |
| --- | --- |
| Docker Go 第一轮 | 原测试曾被误记为基础设施 `-1`；修复后显式重试得到真实 1。模型应用源码后，其测试在启动前因 root token projection 不一致失败；一次 Continue 未完成。独立测试为 0，整体仍记未完成。 |
| Docker React 第一轮 | Plan 文本超过字段限制；两次 Continue 后仍未完成。 |
| Docker React 第二轮 | 模型未先完成连续两批纯读取即提出计划；后续读次数达到上限。两次消息继续未绕过策略，未修改源码。 |
| SBX Go 第一轮 | 锁定 Go `1.26.9` 超过模板实际 `1.26.0`；真实模块已下载，安装失败。新 fixture 在初始 Git 提交前声明 `1.26.0`，原测试和依赖版本保持。 |
| Docker Go 新一轮 | 来源授权、安装、原测试 1、缓存复用均通过；模型 apply 和 workspace checkpoint 已持久化成功，但工具 30 秒超时覆盖结果，后续模型命令被 guard 拒绝。一次 Continue 未绕过 guard，仍记未完成。 |
| Docker Go 回执修复后 | 空缓存准备及原回归 1 均通过；模型 apply 请求到结果约 30 秒并正常记录 completed。模型选择后台 start；返回后正常进入下一工具轮次，Docker dispatch 仍检查旧 attempt，测试 Job 在进程启动前以 `-1` 失败。模型自己读取了失败；未额外 Continue 或改同步模式重试，整体未完成。 |

原失败数据保存在 `C:\p317-805ae248`；Docker 新一轮在 `C:\p317docker-f72112e9`，
SBX 成功案例在 `C:\p317sbx-6a89c429`。旧失败没有覆盖成成功。
回执修复后的 Go 数据在 `C:\p317go-final-299132e2`，后台启动的 Agent/Job/工具轮次时间线单独保存。

实测推动的五处修正都有先失败、后通过的定向回归：

- Plan 将 `dependencies` 缺省或 `null` 规范化为空数组，与 SQLite 合约一致，避免持久化失败留下待执行工具。
- Drydock 在显式 Thread successor 绑定成立时使用当前执行身份，保留物理工作区创建者和所有其他身份/内容检查。
- Docker 仅在验证过的自有清理回执明确为 `process_failed` 且退出码匹配时，把非零退出传给命令调用者；传输、I/O、超时和清理错误保持错误。
- 模型计费和 root Agent token projection 在同一 SQLite 事务同步；晚到记账按当前 Run 状态派生，保留取消状态和旧执行身份拒绝。
- `workspace_apply` 已返回合法成功结果、但可选诊断/归档跨过工具时限时，核对同一次调用的持久 ApplyResult 和完整前后 checkpoint 后保留成功结果。未完成回执、其他工具和父取消继续返回原失败；staging 清理复用已验证目录，减少重复 Git 检查。

模型计费同步这一项在原代码上对成功工具响应、带用量的失败及取消后晚到失败均复现
`root Agent token projection disagrees with the Supervisor checkpoint`。
修复后的真实 React 模型测试已从 Docker 启动并完成，独立复测另有 Job。
Docker Go 新一轮的旧失败独立保留，completion guard 没有放宽。

## 后台启动握手与最后一轮 Docker Go

第六处修复让后台 `start` 等待所选 adapter 在最终原生派发边界完成授权检查并明确回执，
再允许模型进入下一轮。相同操作键的并发重放等待原进程的同一回执；
重放请求取消不会停止原拥有者的进程。Docker、Local 和 SBX 均使用这条握手。
最终定向回归通过：真实 SQLite/生产 Docker composition 加受控 lifecycle transport 的
跨轮次测试、runner 的启动/重放/取消测试，以及 SBX 派发前替换和权限撤销测试。
这些回归没有启动真实 Docker Engine 或 SBX VM。

`C:\p317go-bg-22ad3fc1` 中的新 Go fixture 从干净的初始 Git 提交开始。
冻结后构建的验收程序 SHA256 为
`2b48860e40f5c4784f1dc3e25f824db71c53ae6ea5809deda9ce2d981c27a44f`，
manifest 与本次冻结的 8 个生产源码摘要全部一致。
真实模型会话 `run-20261016023833-b221dda42a0b` 完成了上表的全部流程。
模型后台 `start` 在 `02:41:31.680Z` 返回，晚于 manifest 准备，
下一 attempt 于 `02:41:33.285Z` 开始；两次 `wait` 均读取同一 Job，最后确认退出 0。
旧的 attempt 切换后拒绝派发错误未再出现，完整工具回执和时序保存在原始目录。

2026-10-16 10:21 和 10:26（香港时间）两次从官方入口启动 Docker Desktop，均因
`initializing Inference manager` 无法移除既有 `dockerInference` socket 而失败，
错误为 `The file cannot be accessed by the system`，Engine 随后停止。
普通官方 stop 失败，官方 `stop --force` 成功；最后已通过该入口停止本次失败实例。
没有修改 Docker 配置，也没有执行容器删除或 prune。
根任务随后通过实际窗口执行 Quit 与重新启动；在 Docker 进程退出后，
将仅含失效 socket 的 `Docker/run` 和 `docker-secrets-engine` 目录改名保留，
再让 Desktop 重建临时 socket 路径。实际窗口显示 Engine running，
`docker version` 返回 Server `29.6.2`，目录备份和动作记录均保留。
恢复后保存了 9 个既有容器的新基线；验收结束逐项核对 ID、名称、创建时间和状态均一致，
最终仍为 9 个容器，无新增遗留容器。自有验收服务已通过 stop 文件正常停止。
`sbx --app-name traverse-runtime daemon status` 返回 stopped，本轮未启动 SBX daemon，
因此新版 SBX 握手的新增覆盖为定向回归，先前的真实 SBX 成功会话保持原记录。
原始启动日志、新版 manifest、资格网络失败和恢复记录保存在该目录；
结构化结果的 `post_fix_reacceptance` 记录最终通过和这些先前的环境失败。

## Windows Local 与截图状态

本轮使用原生回归重新执行 `eval`、显式相对路径 `node --test` 和子进程私有文件/网络边界：
`eval` 与子进程边界通过，`node --test` 仍超时并返回 125，进程树和 ACL 清理成功。
没有扩大 LPAC 权限。Docker 上相同 Node 测试入口与 Vite 构建通过，提供了已实际验证的执行路径。

本轮没有新增 UI 截图。启动连接生产 API 的本地 Vite 界面服务时，自动审批返回
`blocked by policy`；未改用其他启动方法。本文的新结果来自真实 API、SQLite 和命令输出，
没有把 #318 的旧截图标成新一轮结果。

## 复现与验证

使用 PowerShell 7 和现有固定工具链创建新 fixture：

```powershell
./scripts/test-project-preparation.ps1 -Mode Prepare -IncludeReact
# 固定 SBX 模板为 Go 1.26.0 时，在初始提交前创建相容 fixture：
./scripts/test-project-preparation.ps1 -Mode Prepare -GoVersion 1.26.0
```

通过产品导入，选择环境、确认信任与执行权限；真实模型先完成连续两批纯只读检查，
再保存单模块计划。操作者确认方向并进入 Deliver，随后运行：

```powershell
./scripts/test-project-preparation.ps1 -Mode Exercise -Project react -RunID '<run-id>' -ApiURL '<loopback-api>'
# 模型通过产品提案应用最小修复，自己启动原测试并读取最终结果后：
./scripts/test-project-preparation.ps1 -Mode VerifyRepair -Project react -RunID '<run-id>' -ApiURL '<loopback-api>'
```

SBX Go 使用 `-Project go`。API 读写令牌只从当前进程环境读取，报告不包含令牌。
`Exercise` 现在要求失败原因确为未授权包源且下载为 0，不能用基础设施失败冒充来源授权检查。

本轮定向检查包括 Plan 规范化/持久化、真实 Git/SQLite 的 successor Drydock 绑定、
Docker 自然非零退出及错误保护、模型记账/重放/取消后记账、compaction 和协议修复。
相关检查均通过；完整 domain 包和 `go vet ./...` 通过。
apply 超时回执修复的 14 项相关回归通过，覆盖真实 Git/Drydock、精确回执身份、
缺失/未完成回执、父取消和既有恢复流程；换回旧超时分支的对照测试按预期失败。
覆盖所有相关 Supervisor、RunAgentUsage、RootAgent、AgentCoordinator 和 PlanDelivery 的
store 回归通过（`go test ./internal/store -run 'Test(Supervisor|RunAgentUsage|RootAgent|AgentCoordinator|PlanDelivery)' -count=1 -timeout=15m`）。
协议 registry baseline 检查与 Surfacecheck 通过。
完整 store 包触及 `go test` 默认 10 分钟总时限，当时在旧迁移测试中，未出现断言失败；
本记录不把超时记成全包通过。全量 store 仍需现有 CI 的 60 分钟分片完成。
较宽的 application 旧测试组曾在 `TestStandardCodeDockerApprovalPreferencesCheckpointAndRecovery/ask`
触及 3 分钟超时，因此本记录不声称完整 application 包通过。

可复用的生产服务驱动、工作流、严格收尾和证据导出保留在忽略目录
`output/playwright/issue317`，完整原始结果保留于上述自有目录。
