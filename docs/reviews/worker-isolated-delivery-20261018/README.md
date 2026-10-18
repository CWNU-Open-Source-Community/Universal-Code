# Worker 隔离交付与资源续跑审阅

日期：2026-10-18。关联 #374，基于 #376 的 `7fb0b0c0a88f6ded3bca49a64fda38bd74233f8a`。

本次让工作流显式选择共享工作区或已有 Batch 隔离交付，并把通用 worker 工具接入 Batch。命令、浏览器和 MCP 资源可由同一 worker 续跑使用；每次调用仍检查当前 attempt、权限、工作区和预算。Batch 完成、取消及恢复路径统一释放 worker。LSP 按物理根目录管理进程，查询保留拥有路径限制。

## OCR delegate 覆盖

执行了 `ocr delegate preview --format json` 和 `ocr delegate rule --format json`。主 Agent 与独立 Astra 子 Agent 阅读差异、调用链和新增文件，由 OCR 负责选文件与解析规则。

最终实现共 77 个文件；OCR 选出 49 个候选，其中 47 个经人工审阅，2 个生成文件排除人工全文审阅。OCR 默认排除的 28 个文件中另有 19 个补充审阅。总计 16 个全文、50 个差异与调用链审阅；3 个生成文件及 8 个未独立人工审阅的测试文件均明确列出。逐文件记录见 [coverage.json](coverage.json)。生成文件通过生成器、类型检查或协议校验核对。本报告和覆盖账本是审阅产物，未计入实现文件数。

原始选文件、规则、补丁、独立报告和测试日志保存在本机 `D:/CodexProjects/worker-capabilities-20261018/`，包括 `ocr-isolated-final-preview.json`、`ocr-isolated-final-rules.json`、`isolated-final-reviewed.patch`、`ocr-batch-workflow-review.json`、`batch-lsp-independent-review.json`。早期报告保留修复前发现，以下记录最终处理结果。

## 已修正的问题

- owner 恢复后发送 Question 会使工作流继续等待：恢复到 Dispatched 时使用 Ack，Working 时使用 Progress，保留旧操作键重放。
- 取消审批等待中的 Batch worker 只关闭资源，未释放子节点和预算：在持久取消之后事务结束 attempt、归档 Session、释放未用分配。
- 合并中断后的 Reconcile 绕过完成收尾：合并、恢复、完成重放与启动恢复共用清理入口。
- LSP 关闭超时后丢失进程引用，重试可能假成功：保留待退出的进程，等待确认退出后才释放对应根目录；期间不重新启动同根服务。
- 配置 LSP 后，公共清理会拒绝原有非 execution 的 Batch 子节点：验证 Run 和角色后跳过这些没有 worker LSP 资源的子节点，保留父服务。

跨 PR 整合还发现历史回读不能识别 Skill/Batch 收据的授权格式；修复及回归测试已放入依赖 #376，按各自不可变身份校验历史读取。

最终审阅无未解决发现。共享根目录 LSP 不随单个 worker 关闭；隔离根目录清理已通过公共 `closeWorkerResources` 入口验证。

## 已执行验证

- 整合后的 application 定向测试通过（224.710 秒），覆盖 Skill、历史、权限收窮信息、Batch 执行与复核等待、审批原 attempt 恢复、取消、合并中断恢复及资源续跑。日志 `isolated-application-final.log`。
- 同一整合测试实际运行 Node、Windows Edge、MCP HTTP 传输及 gopls。验证续跑复用、结束释放、兄弟隔离、LSP 越界输入拒绝和越界返回位置过滤；共享/legacy Batch 清理保留父 LSP 服务。
- domain、store、toolgateway 的相关合同测试通过，历史可见范围与工作流定义检查通过。日志 `isolated-contract-tests.log`；其中 MCP 没有匹配用例，另行运行并通过 `TestWorkerCatalogDefersIdleNotificationWithoutRestoringAuthority`。
- codeintel 全包通过（11.312 秒），包括真实 gopls、并发物理根及清理失败重试；TypeScript language-server smoke 未配置环境，明确跳过。linked-worktree repository 和 gateway 测试通过。日志 `batch-lsp-manager-real-final-test.log`、`batch-lsp-linked-worktree-final-test.log`、`batch-lsp-gateway-test.log`。
- 前端类型检查和 3 个相关组件文件的 34 项测试通过。使用真实组件与静态 API 数据检查中文 390px、700px 布局，已查看截图并确认无水平溢出；该检查覆盖组件布局，没有替代真实后端验收。
- OpenAPI/TypeScript 声明已重新生成，协议目录与基线演化检查通过，`git diff --check` 通过。

模型响应由测试程序控制，工具与存储走真实执行链；本次没有把这些测试算作真实模型自主规划验收。CI 状态以对应 PR 的最新提交检查为准。
