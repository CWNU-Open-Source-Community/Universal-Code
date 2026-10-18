# 长项目验证与可复用收据

2026-10-18，Windows；基于 `24162e3da1ae43f10f1dc632402a473374e90779`。

`run_tests` 保留既有省略 action 的前台调用，并提供 `start` 和 `wait`。结束前测试 Hook 使用现有后台 command Job，按原 `job_id` 观察；普通前台 `run` 的 25 秒限制保持原值。后台测试仍受 30 分钟 Job 上限、Run 预算、当前权限及 root/worker 所有权约束。

宿主将测试定义与执行前源版本保存到既有 `authority_json.project_test_source`，将终态收据保存到工具结果的 `verification_receipt`。复用要求真实 Job 完成、退出码 0、进程树已回收、输出完整，以及执行前、终态观察时和当前的源内容一致。修改源码后旧收据过期；同版本的较新失败尝试会覆盖旧成功，显式读取旧 Job 也返回 `superseded`。格式化继续使原验证过期。

首次终态观察到的源版本固定保存在收据中。测试自身写源码后，即便恢复执行前的字节，再次 wait 原 Job 也保留 `stale`，不能覆盖首次完成时的源记录。

公开的 `ReadProjectVerificationReceipt` 与 `LatestProjectVerificationReceipt` 由 CommandRuntimeService/Multiplexer 提供。调用方须提供当前普通 command authority；读取重新检查 actor、资源、后端和权限。跨 root/worker 或兄弟 worker 的普通读取不会因收据而扩大。该接口面向当前执行者；工作流宿主对已结束 worker 的历史核验需要显式的宿主范围证明。

v211 只替换既有 Job 准入触发器的两个 Plan 注册测试分支，让宿主精确准备的 `start` 使用后台上限；保留原命令指纹、actor、目标、离线、stdin、权限和租约条件。未新增收据表；安装基线由生成器更新。

## 真实本机长进程

`TestProjectVerificationLongNativeJobLive` 使用真实本机 Node `--test`，测试体实际等待 27 秒。生命周期回应由测试夹具控制，经过真实 Go Supervisor、SQLite、命令审批/所有权和进程回收链。

本轮原 Job 为 `command-job-a1893a84129dd483e6807ed4`。Hook 经 6 次原 Job 观察，在 `28.2712112s` 后成功 finish；最终只有一个 Job，前后源版本相同。产品超时上限未为测试调低。

模型 transport 由夹具控制，因此这项结果证明本机进程、持久化与 Hooks 的执行合同；没有把它计为真实模型自主规划验收。

## 已执行检查

`GOMAXPROCS=2 GOGC=50 PRAYU_PROJECT_TEST_LONG_LIVE=1 go test -p 1 ./internal/application -run '^(TestProjectHook|TestProjectVerification|TestRunTests|TestSupervisorProjectTestGuidance|TestStandardCodeSupervisorBackgroundJobOwnershipAcrossRestart)' -count=1 -timeout 5m -v` 通过，耗时 `109.644s`。覆盖 Plan/Deliver 与 Ask/Auto/Full、真实 Node start/wait、失败覆盖旧成功、源变更、测试写源码、服务重建后的收据读取、worker Plan、越 actor 读取拒绝及原后台 Job 恢复。

本机完整日志保存在 `D:/CodexProjects/long-verification-receipts-20261018/application-tests.log`。

OCR 审阅修复了终态记录可被后续 wait 覆盖的问题。修复后以相同资源限制，定向复跑 source-change、background reuse、later failure、Hook 去重、Ask 恢复、worker Plan 及普通 actor 读取拒绝，全部通过，耗时 `20.026s`。日志为 `D:/CodexProjects/long-verification-receipts-20261018/receipt-terminal-guard-tests.log`。上述 109.644 秒整批和真实长进程运行发生在这一修复之前；最终源码由这组补充回归覆盖。

补齐真实两个 worker 的收据范围验证后，单独重跑 `TestProjectVerificationWorkerCannotReadSiblingReceipt` 通过，耗时 `2.588s`。同一测试也核对 worker 无法通过普通接口读取 root 收据。日志为 `D:/CodexProjects/long-verification-receipts-20261018/receipt-sibling-scope-tests.log`。

`go test -p 1 ./internal/toolgateway ./internal/store ./internal/domain ./internal/commandruntimeadapter -run '^(TestRunTests|TestSchemaV211|TestSchemaHistoryListsEveryVersionInOrder|TestCleanInstallBaselineMatchesRepresentativeLegacyUpgrades|TestStandardCodeSupervisor|Test.*Adapter|Test.*Authority)' -count=1 -timeout 180s` 通过。包含 v210 升级后的原生调用保留、v211 准入约束、生成基线与代表性历史升级一致性、旧版 run-tests 编码及后台参数边界。各包耗时为 `1.160s / 142.282s / 0.518s / 0.496s`；同样使用 `GOMAXPROCS=2 GOGC=50`。日志为 `D:/CodexProjects/long-verification-receipts-20261018/contracts-tests.log`。

`go generate ./internal/store` 与 `git diff --check` 已执行。未运行 application 或仓库全量套件。

后续协议登记检查发现 5 个新协议标识尚未登记，以及旧 `project-hook-test.v1` 已停止写入。补齐 `process-runtime-lifecycle` 的新标识并记录旧 Hook key 的退休与历史读取证据后，按仓库命令 `go run -p 1 ./cmd/protocolregistry -write` 更新生成索引。`GOMAXPROCS=2 GOGC=50 go test -p 1 ./internal/protocolregistry -run '^(TestRepositoryRegistryIsValidSynchronizedAndNonAuthorizing|TestEvolutionRetainsDurableReaderHistory)$' -count=1 -timeout 90s -v` 通过，耗时 `1.928s`；本次没有编译 application。日志为 `D:/CodexProjects/long-verification-receipts-20261018/protocol-registration-tests.log`。
