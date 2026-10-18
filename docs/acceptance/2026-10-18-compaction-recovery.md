# 压缩异常恢复与连续压缩验收

基线：`24162e3da1ae43f10f1dc632402a473374e90779`。本次完成 B 分工第一轮：手动 Thread 压缩的核对、恢复、放弃，以及真实模型连续压缩后的要求保留。#377、#378 在开始本轮时已合并。没有改动工作流解释器，也没有新增迁移。

## 实现与验证

页面、HTTP 和 CLI 共用同一恢复服务。按原 operation/Thread/Run 核对；已保存的摘要在来源匹配且任务空闲时恢复，不再次请求模型。放弃未发送的请求释放费用预留；放弃已发送但用量未知的请求保留预算占用；迟到响应只记账，不能安装摘要。使用说明见 [THREAD_CONTEXT_RECOVERY.md](../THREAD_CONTEXT_RECOVERY.md)。

已运行并通过：

- `go test ./internal/application ./internal/httpapi ./internal/app -run 'TestManualThreadCompact|TestThreadContextCompaction|TestThreadCompactCLI|TestLiveThreadThreeCompactions' -count=1`。常规运行的 live test 默认跳过；此命令覆盖重启、已生成候选复用、来源变化、Thread/Run 绑定、读取/控制 bearer、CLI 重放、prepared/started/generated 的实际金额预留及迟到结算。
- `go test ./internal/httpapi -run 'TestThreadContextCompaction|TestOpenAPI' -count=1`。
- `go vet ./internal/application ./internal/store ./internal/httpapi ./internal/app`。
- 协议登记相对上述基线的演进检查、surfacecheck、`git diff --check`。
- Web 的 API/React 定向测试 28 项，以及生产构建。构建保留既有大 chunk 提示。
- `node web/scripts/verify-compaction-recovery-ui.mjs`：真实组件与生产 CSS，API 使用明确的本地响应数据；1280×900 深色、390×844 浅色、390×844 深色。检查生成摘要恢复、普通任务忙碌时放弃旧压缩、按钮状态及横向溢出。三张截图均为 deviceScaleFactor=1 的原比例截图，已逐张查看。HTTP 真服务另由上述 Go 集成测试覆盖。

真实模型另以 opt-in 执行：现有 `deepseek/deepseek-v4-flash` 连续三次手动压缩、三次续问，均保留中文、`LegacyLookup` 和早期“418 兼容性核验尚未完成”的事实。完整运行实际 13 次 HTTP；首次验收脚本读取错误另有 9 次，共 22 次。工具调用为 0，原文、工作区文件及权限保持不变。详见 [报告与复跑方式](../evidence/context-memory/issue366-real-model-three-compactions.md)，其中保留失败和成功的原始报告。

该样本证明本次三轮持久压缩与续答链的实际行为，没有测量不同任务或更多轮压缩的可靠率。早期事实可能通过生成摘要、原文锚点及前轮回复共同保留。

## OCR delegate 复审

OCR 负责文件选择和规则解析，主 agent 与独立子 agent 实际阅读判断。对 `24162e3d..767eb602` 的 31 个变更文件全部登记：26 个手写文件已审阅，5 个生成文件按下表排除逐行审阅；`coverage_rate=26/31=83.87%`，手写文件覆盖 100%。本记录是另一个已检查的说明文件。新手写文件全文阅读；已有大文件检查变更及相关调用链，不声称全文审阅旧文件。

| 文件范围 | 覆盖 |
| --- | --- |
| domain 的 compaction 与 recovery；application 的 compaction 与 recovery 测试；store 的 compaction 与 recovery | 6 个，后端子 agent 与主 agent |
| HTTP compaction、对应测试、server 路由、OpenAPI 定义及其测试 | 5 个，后端子 agent 与主 agent |
| CLI compaction、对应测试、thread 分派 | 3 个，后端子 agent 与主 agent |
| Web API、API 测试、组件、组件测试、CSS、浏览器验收脚本 | 6 个，主 agent |
| opt-in live test；protocols/registry.json | 2 个，主 agent |
| 恢复说明、设计说明改动、旧验收边界修订、真实模型验收说明 | 4 个，主 agent |
| docs/openapi.json、web/src/api/schema.d.ts、docs/convergence/protocol-registry.md | 3 个生成文件；通过生成器、契约/登记检查及 Web 构建核对 |
| 两份真实模型 JSON 报告 | 2 个生成证据；核对调用计数、各轮回答、检查值及成功/失败标记，未按手写代码逐行审阅 |

复审中修复了查询端点遗漏读取 bearer 检查，以及前端两处状态问题：放弃与成功提交竞争时错误显示放弃成功；明确来源冲突后仍重用已结束的旧请求身份。相应回归通过。最终没有未处理的 review finding。
