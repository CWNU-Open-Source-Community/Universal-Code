# DeepSeek 连续三轮压缩与续问

2026-10-18，基于 `24162e3d`，在独立临时 SQLite 和工作区运行
`TestLiveThreadThreeCompactionsPreservePinsAndUnfinishedWork`。模型为现有配置的
`deepseek/deepseek-v4-flash`，实际 HTTPS 主机为 `api.deepseek.com`。
用户配置库以 `mode=ro` 打开，凭据通过现有环境/系统凭据存储读取；没有新增登录或替换配置。

完整验收通过，耗时 16.46 秒：

计划基础调用为 10 次（七次主模型答复、三次辅助压缩），资格探测另预留两次。
实际主模型请求为八次，辅助压缩三次，另有两次资格探测，总数为 13 次。

| 阶段 | 真实 HTTP 请求 |
| --- | ---: |
| 在隔离库完成模型资格探测 | 2 |
| 建立原始对话的四轮输入 | 4 |
| 三次手动压缩 | 3 |
| 三轮压缩后的续问 | 4 |
| 合计 | 13 |

普通 Thread 回合由现有执行链处理，因此七个普通回合产生了八次主模型请求；
报告按实际 HTTP 请求计数。每次验收硬限 16 次，实际未触及上限。
资格探测调用单列。Run 的持久记账为主模型 97,428 token、压缩 6,685 token，
合计 104,113 token；此数不包括单列的资格探测。

## 验收内容

先通过真实 `ThreadTurnService` 建立四轮对话：第一轮设置中文、保留 `LegacyLookup`
名称和禁止工具/文件/命令/联网的要求，再通过正常 pin 操作固定原始用户文本；
第二轮给出“核验 `LegacyLookup` 返回 `418` 时旧客户端的兼容性，审查仍未完成”。
该未完成事实只在早期输入中给出一次，没有放进 pin、原始 Mission goal、压缩 focus 或后续续问。
第三、四轮仅加入背景记录。

随后调用现有 `ThreadContextCompactionService.Compact`，每次压缩后通过同一 Thread
发送“请继续：用最多三句报告目前的接口、未完成工作和下一步。”，连续三轮。

| 压缩轮次 | 摘要 ID / 上一摘要 ID | 移出活跃窗口的消息 | 续答结果 |
| --- | --- | ---: | --- |
| 1 | 1 / 0 | 4 | 中文；保留 LegacyLookup、418、未完成核验 |
| 2 | 2 / 1 | 2 | 中文；保留 LegacyLookup、418、未完成核验 |
| 3 | 3 / 2 | 2 | 中文；保留 LegacyLookup、418、未完成核验 |

三次压缩均由真实模型生成，没有省略压缩输入消息。第二、三次压缩的实际 HTTP 输入含上轮摘要的生成文本哈希，
三次续问的实际 HTTP 输入均含固定 pin 和当轮摘要的生成文本哈希。
最早两条用户原文仍以原内容和摘要值保留在历史中，并已标记为 compacted。
实际工具调用为 0，工作区 README 字节和执行权限快照保持一致。

第三轮真实回复：

> 接口为 LegacyLookup，目前仅有会话记录，没有实现或验证证据。未完成工作是核验 LegacyLookup 返回 418 时旧客户端的兼容性，该审查尚未开始。下一步需等你另行安排或明确授权后才读取实现并验证；本轮未调用工具、未修改文件、未执行命令、未访问网络。

## 首次运行与判断范围

首次运行发出 9 次真实 HTTP 请求（资格 2、初始 5、压缩 1、续问 1），随后验收脚本错误地将已落盘的 assistant 公开正文按内部 RootAction JSON 解析而失败。
存储契约在 `internal/store/supervisor.go` 中先将 `response.Text` 设置为 `action.Message`；
按该契约修正读取后执行了上述完整验收。首次失败报告保留，本工作实际共发出 **22 次**真实 HTTP 请求。
该首次失败发生在脚本读取阶段，不能据此判断其未完成的语义检查。

本验收覆盖一个固定场景中三次**手动触发**的持久摘要重写、摘要继承和真实续答，
语义自动判断使用接口名、418、未完成措辞和中文字符/英文单词检查，并保留全文供检查。
原始事实也可能由提取式原文锚点、历史与上一轮模型回复共同提供；这些输出不能单独归因于生成摘要。
此次没有估计不同任务或更多轮压缩的可靠率。自动窗口触发另由现有
`TestThreadGeneratedCompactionDefaultTwoPassesKeepSourcesAndUsage` 固定协议测试覆盖。

## 复跑

默认执行会跳过真实调用。显式启用时：

```powershell
$env:TRAVERSE_CONTEXT_COMPACTION_LIVE='1'
$env:TRAVERSE_CONTEXT_COMPACTION_LIVE_REPORT='<本次输出 JSON 的绝对路径>'
go test ./internal/application -run '^TestLiveThreadThreeCompactionsPreservePinsAndUnfinishedWork$' -count=1 -v -timeout 9m
```

可用 `TRAVERSE_LIVE_MODEL_SETTINGS` 指向现有设置库，默认是用户目录下
`.cyberagent-workbench/cyberagent.db`；`TRAVERSE_LIVE_MODEL_ROUTE` 默认是上述 DeepSeek 路由。
每次在新临时库运行，缺少有效资格记录时只向临时库写资格结果。
HTTP 证据只包含阶段、主机、状态、字节数、请求哈希和包含标记；不会记录请求头或凭据。

证据：

- [完整通过的原始报告](issue366-real-model-three-compactions.json)
- [首次脚本读取失败报告](issue366-real-model-three-compactions-attempt1.json)
- 验收源码 SHA-256：`5405920a58c4886c36569c7757065c1d7f1a9269dda61910b7ca73b3cbbd768d`

本次验证命令：默认 opt-in 跳过、显式真实调用、受影响手动压缩与 pin 传递的定向测试，以及 `git diff --check`。
