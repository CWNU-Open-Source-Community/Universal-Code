# pnpm 项目准备验证记录

日期：2026-10-19。范围：固定 pnpm 10.28.1、v9 锁文件、单包与 workspace。

## 已验证

- Windows 上使用真实 Node.js 24 与 pnpm 10.28.1，运行产品检测器和下载 broker，
  然后离线 fetch、冻结锁文件安装、在选定子包执行测试。外部包与 `workspace:*`
  内部包都可加载；原锁文件不变；根、成员及依赖的 postinstall 均未运行。
- 同一份已校验缓存可在新工作目录中复用，不授予新的包源下载权限。
- 原生 pnpm 验证自动安装 peer、关闭自动安装 peer、devDependency 覆盖 peer
  三种情况。命令使用与产品一致的目标目录下 store/cache/state/global 布局。
- 应用服务配合真实 SQLite 与受控 Docker transport，验证正常准备、单独授权
  rebuild、fetch 失败、阶段间清单变化和取消。失败或取消后没有派发安装；重放
  同一请求不会创建新任务；准备后通过界面使用的项目动作服务运行子包测试，
  测试结果可读取且可重放。该检查没有启动真实容器。
- 迁移 215：从 214 升级后旧准备记录仍可读取及更新；新库将缓存、安装、脚本
  分别绑定到准确的目录、Run、权限版本和用途；失败 fetch 不能形成 ready 记录。
- HTTP/OpenAPI、前端相关测试、类型检查与生产构建通过。浏览器实际渲染了
  项目准备组件的浅色和深色页面；480px、900px 下检查了目录说明和命令换行。
  预览使用检测器结果，Docker 未就绪时准备按钮禁用。

## 复现命令

```powershell
go test ./internal/projectprep -count=1
# 以下两项需填写本机真实工具的绝对路径。
$env:TRAVERSE_PNPM_TEST_NODE = '<absolute-path-to-node.exe>'
$env:TRAVERSE_PNPM_TEST_CLI = '<absolute-path-to-pnpm-10.28.1/bin/pnpm.cjs>'
go test ./internal/dependencycache -count=1
go test ./internal/application -run 'PNPMPreparation|TestProjectPreparation|TestPNPMGuest|TestDockerPreparationWrites|TestDetectedProjectChecksWorkspaceRoot' -count=1 -timeout=5m
go test ./internal/store -run 'ProjectPreparation|PNPMPreparation|CleanInstallBaseline' -count=1 -timeout=4m
go test ./internal/httpapi -run 'TestOpenAPIGoldenDocumentMatchesGoDTOs|TestProjectPreparationHTTP' -count=1
npm --prefix web test -- src/api/project-preparation.test.ts src/v2/components/project-preparation.test.tsx
npm --prefix web run build
```

应用服务检查按场景完成。三阶段 scripts 场景使用一分钟等待上限单独复跑通过；
原先的 30 秒等待不足。状态、任务数量和重放断言均保留。

## 尚未验证

本机 Docker Desktop 初始化失败，未完成真实 Docker 容器中的安装。仓库提供
`TestDockerPNPMWorkspacePreparationRealDaemonOptIn`；按
[镜像说明](../standard-code-docker.md#fixed-image)构建并配置固定镜像后运行：

```powershell
$env:CYBERAGENT_STANDARD_CODE_DOCKER_TEST_IMAGE_DIGEST = 'sha256:<digest>'
go test ./internal/sandbox -run TestDockerPNPMWorkspacePreparationRealDaemonOptIn -v -count=1
```

CI 在选中 dependencycache 包时安装经过 SHA-512 校验的固定 pnpm，并运行原生
集成测试。真实 Docker 检查仍需上述镜像，不能用原生测试结果替代。
