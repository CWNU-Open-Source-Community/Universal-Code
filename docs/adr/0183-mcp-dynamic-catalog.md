# ADR 0183: MCP 任务会话与动态目录

Date: 2026-10-15
Status: Accepted for [Issue #330](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/330).

已启用服务器的目录变化自动更新，不再把服务器隔离或要求重新审批整份目录。首次登记、
发现和启用仍由操作员审核。目录替换通过现有 generation CAS 原子写入；停用或撤销同时
发生时，旧发现结果不能重新启用服务器。刷新失败保留旧目录供设置查看，并记录健康
错误；模型目录入口继续返回其他健康服务器。
单个服务故障不会中断模型轮次；当前任务授权失效或存储故障仍返回错误。

每个 Run、Workspace、服务器组合按需建立一个 SDK 连接。连接保留到任务授权围栏撤销、
服务器停用或服务关闭；描述符、凭据修订和原生启动配置变化会更换连接。每条连接只允许
一个发现或调用租约，其他任务不共享。每轮 Supervisor 读取目录会实际刷新一次；工具搜索
读取最新已发布目录。目录通知合并后在同一连接租约下刷新，无全服务轮询。

后台刷新使用当前任务授权检查，逐次重读 Run、Workspace、permission snapshot/revision、
执行 lease 和进程内授权 fence。它不捕获单次 ActorAttempt 的工具许可。每次刷新冻结自己
的检查函数，后来的模型轮次不能让一个已经失效的刷新继续执行。每次工具调用仍重新走
现有 Ask、Auto、Full 权限与实际发送检查；租约结束即清除工具许可和 elicitation 处理器。

2026-07-28 使用 Go SDK 1.8 的 tools/resources/prompts list_changed 回调和单次
subscriptions/listen。SDK 为此使用 Background context；适配器仅向这个精确订阅传递当前
发现预算，并绑定一次取消到实际 request ID。资源订阅和任意后台发送不在此许可内。
legacy HTTP 使用初始化返回的 session ID，在同一精确 endpoint 上建立一个通知专用 GET。
GET 保留 origin、header、凭据保护，启动前后验证任务授权，不跟随重定向；只接受有界目录
通知，不提供 server 请求入口。流结束记录不可用状态，下次目录使用可以建立新连接。
stdio 的 legacy 通知仍由 SDK 接收。

目录指纹或通知状态变化会使尚未发送的旧调用明确 stale，要求取得新目录并准备新调用。
已发调用沿用已有结果/unknown receipt，目录更新和连接恢复不会重放它。连接内 native
指纹还绑定完整发现页；凭据只驻留已有运行时/OS credential store，不增加数据库字段。

验证覆盖真实 SDK legacy 与 modern HTTP 的工具、资源和提示词通知、连接复用、旧调用零
发送、刷新失败保留旧目录、GET redirect/提前 EOF 恢复、Run fence 撤销及服务器停用释放，
并沿用现有撤权、输入交互、未知执行结果和工具调用授权测试。

无需数据库迁移。回滚时关闭任务连接并恢复按操作连接；已保存的目录和审核记录继续使用
现有格式。Codex/Claude 的会话内按需连接启发本生命周期，具体授权与错误处理沿用本项目
的 Run/Workspace 和执行许可模型。
