# ADR 0193: 内置终端授权偏好与默认 shell

Date: 2026-10-27
Status: Proposed

## 用户行为

设置 → 执行环境增加“内置终端”。用户可选择禁止 Agent 操作、手动限时授权（15–900 秒），或完全允许。默认保持手动授权 300 秒。完全允许仅在任务已有 Full 权限、工作区和真实终端仍有效时自动授权；普通 Agent 命令执行工具继续使用自己的执行路径。

保存使用既有 Go 原生确认对话框，不再对完全允许模式的每次控制或续期弹窗。禁止 Agent 后仍可手动输入命令。终端中的“停止 Agent 控制”阻止当前会话自动恢复，用户可以再次允许；修改控制模式清除此前的授权和暂停记录。结束会话、任务权限降级和应用退出使授权失效。没有管理员终端或 UAC 功能。

Windows 新终端可以选择 PowerShell 7、Windows PowerShell 5.1 或 CMD。自动选择优先 PowerShell 7。设置显示检测到的程序路径和文件资源版本；终端显示实际启动的 shell。修改默认 shell 保留现有会话，在下一次新建终端时生效。Linux/macOS 保留 Bash。设置页面的 environment 路由同时支持直接进入和刷新。

## 后端约束

`desktop_terminal_settings.v1` 保存到应用目录的 `desktop-terminal.json`，包含 revision、控制模式、手动时长和 shell ID。文件使用既有受限目录、跨进程锁、严格解码及原子替换；保存需匹配当前 revision，已提交的相同请求可以重试。HTTP 和 Agent 工具均没有修改偏好的入口。

控制器在保存和 Agent 读写之间串行检查。完全允许模式复用已有最长 15 分钟的进程内租约，在临近到期或操作预算耗尽前续期；每次仍检查真实 Run、权限和会话。配置文件没有令牌、Run 身份或持久授权。读取配置失败撤销租约并返回错误，不能用默认许可替代损坏的配置。

普通产品启动加载偏好；明确指定细粒度 CLI 参数的启动继续采用原有手动模式，界面显示只读设置。旧应用忽略新增文件，重新升级继续读取；回退保留文件，不迁移或覆盖未知格式。

## Windows PowerShell 7 交付

完整构建目录可通过以下方式附带应用自有运行时：

```powershell
./scripts/build-desktop.ps1 -OutputDirectory build/desktop -BundlePowerShell7
```

也可以对已有构建目录单独运行 `scripts/prepare-powershell-runtime.ps1`。脚本只下载微软官方 PowerShell 7.6.8 win-x64 ZIP，先核对固定 SHA-256，再检查 pwsh.exe 的 Microsoft Authenticode 签名，保留 LICENSE 和 ThirdPartyNotices。它不安装到系统、不修改 PATH，已有目录要求操作者明确处理后才重建。

交付时必须保留 EXE 旁的 `runtimes/powershell7` 完整目录。现有正式 Release ZIP、MSIX 和单 EXE 发布流程没有在此变更中改为携带运行时；单独复制 EXE 不会包含 PowerShell。包来源写入运行时目录的 `runtime-source.json`。本地验证只证明该 Windows x64 目录可用，正式发行仍按现有签名和 Windows 兼容性要求验收。

用户终端和 Agent PowerShell 命令运行器都优先识别同一应用目录中的运行时；受信任宿主的 `CYBERAGENT_POWERSHELL_PATH` 仍可覆盖选择。前端只能选择枚举 ID，不能提供程序路径。命令运行器的镜像检查、工作区排除和 Windows 沙箱必须使用 pwsh 的限制保持原样。

## 关闭与验证

真实交互测试发现：结束 Windows 终端时，先关闭输出管道会等待尚未结束的同步 ReadFile，造成界面一直忙碌。现在由独立读线程排空 ConPTY 最后一帧，关闭伪控制台并等待读取完成后再释放管道。对应微软的 [ConPTY 会话关闭说明](https://learn.microsoft.com/en-us/windows/console/creating-a-pseudoconsole-session) 和 [ClosePseudoConsole 说明](https://learn.microsoft.com/en-us/windows/console/closepseudoconsole)。

测试覆盖设置保存/取消/冲突、权限降级、连续命令与续期、撤销后不恢复、禁止 Agent 仍能手动输入，以及三个 Windows shell 的启动、命令、主动结束。真实截图、来源提交和平台限制记录于 `docs/acceptance/2026-10-27-terminal-preferences/README.md`。
