# 执行环境选择

普通桌面启动默认选择 **Local**。新任务先进入预览，点击「开始编码」后，
才按所选环境准备独立工作目录和执行权限。环境选择与 Ask / Auto / Full
审批档位分别保存。

| 选项 | 执行位置 | 当前接入状态 |
| --- | --- | --- |
| Local | 本机操作系统沙箱 | 使用现有 Local 后端；当前 Windows 有实现，其他平台由就绪检测提示可用环境 |
| Docker Engine | 用户本机 Engine 的受限容器 | 已接入现有 Standard Code Docker 执行链，要求本地固定镜像 |
| Docker Sandboxes（sbx） | 官方 sbx 管理的本地微虚拟机 | Windows sbx 0.47.0 已接正式辅助服务、执行和条件清理；通过就绪检测后可用 |

选择不可用的环境后，界面保留选择并给出检测结果。旧客户端的 `auto`
继续指向 Local。当前任务沿用自己的环境，修改默认值影响后续准备的任务。

## 在应用中配置

1. 打开侧栏「连接与环境」，选择「执行环境设置」。
2. 选择默认环境，按需启用 Docker Engine 或 Docker Sandboxes。
3. 填写固定镜像，保存并核对「已保存」和「当前进程」的差异。
4. 点击重启，在本机确认窗口确认。应用停止当前工作并清理拥有的执行资源，
   再加载保存的设置。
5. 回到任务选择环境，确认工作区来源，再开始编码。

配置保存在应用目录的 `sandbox-environment.json`，带修订号和原请求重放。
保存结果不明确时，页面保留原请求供重试。修改设置不会切换正在执行的任务，
重启仍需重新建立运行时权限。显式命令行启动继续使用原有启动参数。

## 准备项目依赖

开始编码后，在「审阅改动 → 检查与交付 → 准备项目」检测 npm 或 Go
项目，核对锁文件和命令，选择本次允许下载的包源，再在所选沙箱中
离线安装。依赖下载与任务的网页搜索分别授权，npm 安装脚本另行选择。
工具链应提前安装在本机环境、固定镜像或模板中。

缓存复用、安装输出、失败重试和中断恢复的具体操作见
[准备项目依赖](project-preparation.md)。

Batch 交付的 Go/npm 验证也使用任务当前选择的 Command Runtime 后端，目标是
已记录的子工作区或合并工作区。仍需显式 Batch 验证开关及当前 Full 权限；
后端未就绪时拒绝执行。收据标明实际 backend、网络策略与 readiness，历史
验收记录不代表这条新路径已真机通过。见 [Batch 验证边界](batch-delivery.md#验证执行边界--validation-execution-boundary)。

高级 Git 的 `bisect_run` 固定 Go/npm 测试也复用 Run 所选后端。Git 审批与
检查点继续绑定原仓库，每步只将该仓库交给已授权的后端，收据绑定实际测试提交、
Job、后端代际与清理状态。Ask / Auto / Full 继续沿用高级 Git 的精确操作审批，
未就绪、权限变化或退出结果不确定时停止，不将提交误标为好或坏。
详见 [有界 bisect](git-advanced.md#bounded-bisect--有界-bisect)。

## Docker Engine 准备

由用户安装并启动 Docker；Windows 使用 Docker Desktop 的 Linux Engine。
设置中的摘要是本地镜像 ID，格式为 `sha256:<64 位小写十六进制>`。
镜像需要包含项目的 Standard Code runner，构建方式见
[Standard Code Docker](standard-code-docker.md)。普通桌面启动从设置页读取该值；
独立命令行和显式启动模式继续支持原有环境变量。

应用检测固定本地 Engine 与镜像，运行时使用现有网络关闭、凭据隔离、
Drydock 和审批机制。Docker 安装、登录、镜像构建与下载由用户管理。

## Docker Sandboxes 准备

当前产品适配器支持 **Windows、sbx 0.47.0 和 daemon API 0.38.0**。
按[官方安装文档](https://docs.docker.com/ai/sandboxes/install/)准备虚拟化组件并安装
该版本。所有准备使用独立的 `traverse-runtime` 环境，它有自己的 daemon、登录和
模板缓存；[第一方测试脚本](https://github.com/docker/sbx-kits-contrib/blob/main/scripts/test-kit-e2e.sh)
说明了 `--app-name` 的隔离范围。

在 PowerShell 中首次[启动该环境](https://docs.docker.com/reference/cli/sbx/daemon/start/)、
完成浏览器登录，并设置 SSH 与 MCP：

```powershell
sbx --app-name traverse-runtime daemon start --detach --policy deny-all
sbx --app-name traverse-runtime login
sbx --app-name traverse-runtime settings set ssh.agentForwardingEnabled false
sbx --app-name traverse-runtime settings set mcp.forceLocalGateway true
sbx --app-name traverse-runtime daemon restart
sbx --app-name traverse-runtime settings get ssh.agentForwardingEnabled
sbx --app-name traverse-runtime settings get mcp.forceLocalGateway
```

最后两项应分别返回 `false` 和 `true`。设置的生效时机见
[sbx settings](https://docs.docker.com/ai/sandboxes/configuration/settings/)。
按[官方模板准备流程](https://docs.docker.com/ai/sandboxes/usage/#load-a-template)
在同一环境缓存包含 shell 和所需工具链的 OCI 模板；已有镜像 tar 可用
[template load](https://docs.docker.com/reference/cli/sbx/template/load/)
`sbx --app-name traverse-runtime template load <文件路径>` 导入。
在应用中填写 `仓库名@sha256:<64 位小写十六进制>`，启用 Docker Sandboxes，保存并
重启应用。产品创建使用 `--pull never`，因此所填摘要对应的模板必须已在该环境缓存。

应用重启后会自动准备内置辅助服务，再检测 daemon、模板配置、SSH 和 MCP。
「重新检测」只读取状态。就绪检测通过后，可以为新任务选择 Docker Sandboxes。

## Docker Sandboxes 执行边界

原生启动的 `Prepare` 会注册固定零工具 MCP 辅助服务。CLI 和桌面程序均在读取
应用数据库、凭据或启动界面前处理专用 helper 参数。服务身份绑定可执行文件
SHA-256、绝对路径和固定参数；正式创建使用 `--static-mcp` 选择这个集合。
应用直接核对完整存储记录，包括工作目录、环境覆盖与凭据字段；已有登记无需运行
`mcp ls` 或 `mcp inspect`，只有缺失登记时才调用 `mcp add`。
辅助服务只提供空的工具、资源、模板和提示列表。静态集合机制见
[MCP gateway](https://docs.docker.com/ai/sandboxes/mcp-gateway/)。

执行使用新生成的随机名称，并在派发前复核保存的 UUID 和精确挂载。宿主 OS
账户及 sbx 管理员属于可信控制面，来宾命令和项目文件属于不可信输入。本地名称
执行依赖这个边界；它不提供针对宿主管理员故意替换资源的原子 UUID 选择保证。
合作的产品写入者共享全账号 namespace 锁。恢复先严格读取自有日志，只有存在
待清理记录才获取该锁；空日志和已完成日志的读取不会占用 SBX。

取消和恢复通过固定 daemon 端点发送 `expected_id` 条件删除，直接删除整台 VM。
成功响应和删除后 UUID、名称均消失，才形成清理收据。创建结果不明确、UUID 未保存
或删除未确认时，日志保留待处理状态，Command Runtime 保留不确定清理结果。
合同与版本范围见 [ADR 0172](adr/0172-sbx-owned-namespace-execution.md)。

每个 VM 使用 deny-all 网络、关闭共享技能、工作区挂载和只读 `.git` 挂载。
普通 Run/Batch 使用 linked worktree 的 `.git` 文件；原生 bisect 也支持原仓库的
`.git` 目录，整个目录只读。测试可以读取其中的历史和配置，不能修改 Git 元数据；
切换提交和标记结果仍由已审批的原生 Git 操作执行。
来宾固定检查先确认 SSH 转发套接字不可用、凭据模式为 `none`，再以 `env -i`
派发显式命令环境。创建前扫描会拒绝多链接文件、符号链接、Windows reparse point
和特殊文件。sbx 的可执行身份摘要表示「固定模板与来宾路径绑定」，界面与输出会
标明该含义。

宿主 CLI 使用官方 `SBX_NO_TELEMETRY=1` 关闭 usage analytics，并固定
`DOCKER_CI=true`。在 sbx 0.47 中，后者只跳过更新检查与交互式诊断同意流程，
不授权诊断上传；调用方的默认模板覆盖变量仍会过滤。版本合同见
[ADR 0172](adr/0172-sbx-owned-namespace-execution.md#fixed-cli-environment)。

就绪检测通过固定 Windows named pipe 直接读取 daemon health、清单和隔离设置，
每次重新观察可变状态。成功确认的 CLI 版本按可执行摘要缓存，每次使用仍重新核对
文件 SHA-256；短 CLI 调用共享可取消的 gate，避免同一进程反复并发启动 CLI。

## 验证范围

设置保存、权限分离、原请求恢复、后端显式选择、桌面固定重启、SQLite v188
升级与旧数据保留均有回归测试。v189 只补充持久化 Job 的 `docker_sandboxes` backend
与 `sbx` profile 映射，保留原有授权约束及历史数据。SBX 受控 transport 测试覆盖辅助服务身份、
版本不兼容、跨进程 namespace 排斥、执行前身份复核和不确定清理。
界面截图使用标明模拟数据的真实 React 组件。

开发机的正式辅助服务 `Prepare` 与就绪检测已实测通过；新版 readiness 观察为
0.761 秒与 0.262 秒。最新直接 API 生产路径的真实条件删除复跑通过（13.62 秒）：
错误 UUID 保留原 VM，同名重建后旧 UUID 保留新 VM，
两个正确 UUID 均完成删除，随后恢复没有重复动作；来宾 `none` 模式成功，模拟
`apikey` 模式在 payload 前返回 125 且 stdout 为空。本地证据在
`build/sandbox-backend-selection/sbx-product-live/conditional-removal-1055017084/result.json`，
运行日志为 `build/sandbox-backend-selection/sbx-product-live/conditional-removal-direct-api-attempt2.log`。
完整应用链路第六次验收通过（115.11 秒）：成功命令退出 0、失败命令退出 7，取消
用例记录 `cancelled` 和 CLI 实际退出码 1。三个用例各派发一次 backend，stdout/stderr
各保留 23 bytes 中文输出，清理收据、checkpoint 与 exact replay 均通过。取消后继续
观察 21 秒，没有新增 post-cleanup marker，detached 子进程 heartbeat 已停止。
各用例证据在 `build/sandbox-backend-selection/sbx-product-live/sbx-product-587455417/<case>/acceptance-summary.json`，
日志为 `build/sandbox-backend-selection/sbx-product-live/application-acceptance-attempt6.log`。
这些结果适用于本次固定模板与开发机配置；全量回归测试范围以验收记录为准。

该过程观察到 CLI 初始化等待耗尽就绪检测预算，并行与后续串行调用都曾出现等待；
当前实现已改用直接 API 读取与版本缓存。现有记录尚未证明 telemetry 是这些超时的根因。
既有固定模板上的挂载、网络、静态 MCP 正反例与 Docker Engine 实测范围，见
[验收记录](acceptance/2026-10-10-sandbox-selection.md)。
