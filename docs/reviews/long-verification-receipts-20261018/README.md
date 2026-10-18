# 长项目验证收据审阅

2026-10-18，工作分支 `Qiyuanqiii/long-verification-receipts`，基于 `24162e3da1ae43f10f1dc632402a473374e90779`。

使用 OCR delegate 做确定性的文件选择与规则解析，由宿主完成代码审阅。没有调用 OCR 的模型 endpoint。原始 preview/rule JSON 保存于 `D:/CodexProjects/long-verification-receipts-20261018/ocr-preview.json` 和 `ocr-rules.json`。

审阅单位为 preview 的 `(path,status)`。20 个选中文件的变更均已审阅，覆盖率 100%；逐项范围见 [coverage.json](coverage.json)。已有文件审阅变更及其调用上下文，新生产文件全文审阅。生成 SQL 基线只审阅本次 trigger 变更，生成 Go 元数据审阅全部变更；没有逐行审阅完整生成基线或声称仓库全文覆盖。另行读取了 OCR 默认排除的 4 个测试文件、验收记录及迁移历史行。本目录的审阅产物显式排除在递归审阅之外。

规则为 Go 文件的系统 Go 规则，以及 SQL 基线的默认正确性、权限、资源与测试规则。重点追踪了 source pin → 最终 dispatch check → 原生 Job → 首次终态结果 → 新鲜读取这条链，并核对 Hook 恢复、最新失败优先、backend identity、Plan 的两个准入分支及普通 actor 资源范围。

审阅中修复了两个相关缺陷：终态第二次捕获必须重新比较源版本；后续 wait 必须保留首次终态的 `CompletedSourceVersion` 和 stale 状态。回归测试实际运行一个会写源码的 Node 测试，恢复原源码后再次 wait，确认原 Job 仍不可复用。修复后 7 项 application 定向测试通过，详见 [验收记录](../../acceptance/2026-10-18-long-project-verification.md)。没有保留未修复的确认缺陷。

另行补齐真实两个 worker 的越权读取断言，单独运行通过，耗时 `2.588s`。所有最终测试结束后生成本次文件清单与覆盖记录；未运行全量套件。

普通 `ReadProjectVerificationReceipt` / `LatestProjectVerificationReceipt` 只接受当前原生 actor authority。工作流主控验收委派任务使用的窄宿主入口由另一工作流任务负责，不能把普通 actor 的读取范围当作工作流主控验收范围。

后续复审发现协议注册遗漏；单独补齐 5 个新标识和旧 Hook v1 的退休记录，并重新生成协议索引。该增量按相同 delegate 规则审阅，详见 [protocol-registration.json](protocol-registration.json)。协议发现、索引同步和读者历史定向检查通过；没有重新编译 application。
