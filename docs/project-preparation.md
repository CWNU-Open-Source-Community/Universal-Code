# 准备项目依赖

在任务中打开「审阅改动 → 检查与交付」，使用「准备项目」检测工具链、
项目文件、锁文件、安装命令和测试命令。准备结果、安装输出和测试结果
分别保留在该执行的记录中。

## 开始准备

1. 为任务选择 Local、Docker Engine 或 Docker Sandboxes，确认环境就绪，
   点击「开始编码」准备工作目录。
2. 打开「准备项目」，填写项目内的相对目录，点击「检测目录」。留空或
   填写 `.` 使用根目录；例如 `web` 表示其中的独立 npm 项目。
3. 核对检测出的配置与锁文件、项目要求的工具版本、安装命令和测试命令。
4. 按需勾选本次允许下载的依赖源，确认安装脚本选项，再点击「准备项目」。
5. 安装完成后，下方「项目测试与格式化」会选中刚准备的目录。点击「运行测试」，
   查看实际退出码和输出。也可以填写其他测试目录，等待该目录的检测结果后运行。

准备动作需要当前任务的命令执行权限和可写工作目录。代理正在执行时，
先暂停并等待当前操作释放，再手动准备项目。环境配置见
[执行环境选择](sandbox-environments.md)。

## 检测范围与工具链

| 项目 | 检测文件 | 安装与测试 |
| --- | --- | --- |
| npm | `package.json`、`package-lock.json` v2/v3，以及已有的 Node 版本配置 | 离线 `npm ci`；存在 `scripts.test` 时检测 npm 测试动作 |
| pnpm | `package.json`、`pnpm-lock.yaml` v9；多包项目同时读取 `pnpm-workspace.yaml` 和成员清单 | Docker 镜像固定 pnpm 10.28.1；离线安装整个工作区，在选中包内执行 `pnpm test` |
| Yarn Classic | `package.json`、`yarn.lock` v1 | 固定 Yarn 1.22.22，离线、冻结锁文件且关闭安装脚本；支持单包项目 |
| Bun | `package.json`、文本 `bun.lock` v2/config v1 | Docker 镜像固定 Bun 1.4.3；经校验的 registry 缓存用于离线、冻结锁文件且关闭安装脚本的安装 |
| Go | `go.mod`、`go.sum` 和声明的 Go 工具链版本 | 离线准备模块；检测 `go test -mod=readonly ./...` |
| Rust / Cargo | `Cargo.toml`、`Cargo.lock` v3/v4；workspace 成员及本地依赖清单 | 准备经校验的 crates.io vendor，执行 `cargo fetch --offline --locked`；测试复用同一 vendor |
| Python | `pylock.toml` 1.0 和可选 `pyproject.toml` | Docker 中使用 pip >=25.3 将锁定 wheel 离线安装到任务私有目录；检测原生 unittest 动作 |

使用项目已有的锁文件。缺少锁文件、清单与锁文件不一致、依赖校验信息
缺失时，先生成并审阅锁文件，再点击「刷新检测」。选择同时包含
`package.json`、`go.mod`、`Cargo.toml` 或 Python 清单/锁文件的混合项目目录时，应分别选择对应的独立项目目录。
npm workspaces、Yarn workspace/Berry 和本地 Go 模块替换仍不在内置安装范围内，
检测会显示具体原因。

### Bun 文本锁文件

选择声明 `packageManager: bun@1.4.3` 的单包项目，使用文本 `bun.lock`
v2/config v1。当前支持默认 npm registry 中具有精确稳定版本与 SHA-256/SHA-512
完整性值的包。二进制锁文件、workspace、自定义 registry、Git/本地来源、
patch、catalog 及会改变安装配置的文件会显示具体不可用原因。

准备先复验 tarball，生成任务私有的 Bun 缓存，再通过固定 Docker 工具链执行
`--offline --frozen-lockfile --ignore-scripts --force --backend=copyfile`。
原清单和锁文件保持不变；已有安装包从缓存重新复制。存在 `scripts.test` 时，
测试执行 `bun run --no-install test`。镜像需包含 Bun 1.4.3 及对应版本标签，
构建步骤见 [Docker 镜像说明](standard-code-docker.md#fixed-image)。
真实原生验证、界面截图及未运行的容器路径见
[Bun 准备记录](acceptance/2026-10-20-bun-project-preparation.md)。

### Cargo 项目与 workspace

选择包含共享 `Cargo.lock` 的 workspace 根目录，或独立 Cargo 项目目录。
准备会绑定成员与仓库内本地依赖清单，从已授权的 crates.io 源下载锁定归档，
校验 SHA-256 后生成任务私有的 vendor。原有清单和锁文件保持不变。
Git、自定义 registry、外部本地依赖及会改变来源或工具链的配置会显示不可用原因。

Windows Local 支持离线 fetch；编译测试需要所选运行环境具有可用的 Rust
标准库及链接工具。当前 Local 的 LPAC 限制会在测试前明确提示。
实际运行及 Full 测试结果见 [Cargo 准备记录](acceptance/2026-10-20-cargo-project-preparation.md)。
Yarn Classic 的验证范围见 [Yarn 准备记录](acceptance/2026-10-20-yarn-project-preparation/README.md)。

### Python wheel 依赖

选择包含 `pylock.toml` 的目录，使用
[pylock.toml 1.0](https://packaging.python.org/en/latest/specifications/pylock-toml/)
的默认安装集合。每个包需有精确版本、wheel 的公共 HTTPS URL 和小写 SHA-256；
可同时提供 `pyproject.toml` 的 `requires-python`。当前支持默认组，
`extras` 和公开 `dependency-groups` 须为空。源码构建、VCS、本地路径、
editable 安装，以及 requirements/Poetry/uv 专有锁文件需要先导出为此格式。

准备复用现有依赖源授权与校验缓存。缓存包含锁定的 wheel 候选，安装时由
实际解释器检查 `requires-python`、环境与包 markers、默认组和 wheel
平台/ABI 标签，选择兼容 wheel；无法选择或存在冲突包时明确失败。
声明的 wheel 字节数也会核对。安装使用本地文件、`--no-index --no-deps
--require-hashes --only-binary=:all: --target`，不执行源码构建。

通用联网使用的 pip、证书包及既有镜像应按[运行时安全更新](ci.md#october-2026-runtime-security-updates)升级；下述版本是离线安装的兼容性下限。

此入口当前使用 Standard Code Docker Engine。镜像需已包含 pip >=25.3；
旧版本在安装记录中给出明确错误。依赖只写入任务的 `.traverse-preparation`
私有目录，安装成功后才发布；解释器、兼容标签、环境或锁文件变化后需要
重新准备。检测到的 unittest，以及已配置的 `test.python` / `test.pytest`，
通过 `PYTHONPATH` 使用匹配的私有目录。未提供此锁文件的已配置测试保持原有命令。
实际验证范围见 [Python 准备记录](acceptance/2026-10-20-python-dependency-preparation/README.md)。

### pnpm 多包项目

例如工作区包含 `apps/web` 和 `packages/shared-ui`，并使用 `workspace:*`
引用内部包时，可以直接选择 `apps/web`。检测会在任务目录内向上找到共享锁文件，
列出根目录及各成员的配置，并显示「依赖安装目录」。准备会安装整个工作区，
测试仍只在 `apps/web` 中执行。成员清单或锁文件变化后，需要重新检测和确认。

先按[Docker 镜像构建说明](standard-code-docker.md#fixed-image)安装包含
pnpm 10.28.1 的镜像。旧镜像会提示重建；Windows Local 的目录链接限制与
原生 pnpm workspace 不兼容，当前 pnpm 准备入口仅支持 Docker Engine。

准备先下载并校验锁定的 tarball，再由镜像中的 pnpm 填充独立缓存，最后使用
原锁文件执行离线、冻结锁文件的安装。项目锁文件不会被改写成临时下载路径。
支持常用 `*`、`**`、`?` 与 `!` 排除形式的成员目录匹配，以及
`workspace:*`、`workspace:^`、`workspace:~` 和精确成员版本。
当前最多 60 个成员、64 个需要确认的清单及配置文件。成员使用 `package.json`；
`package.yaml` / `package.json5`、catalog、patch、外部本地文件/Git 依赖、
自定义安装目录和切换 Node 版本等配置会给出不可用原因。
项目声明其他 pnpm 版本时，需要先核对并更新项目版本及锁文件。

工具链应提前安装在所选环境中。Local 使用可读取的本机 Node.js/npm 或
Go 安装目录；Docker Engine 镜像和 Docker Sandboxes 模板应包含所需工具。
项目声明的版本用于核对要求，准备流程使用该环境已经安装的工具链。

Local 安装输出出现工具链目录 `WRITE_DAC` 或访问拒绝时，使用当前账户
拥有、可供沙箱读取的工具链目录，重新检测后再准备。工具链位置、镜像
或模板调整后，按执行环境设置的提示重启应用。

Windows Local 使用 LPAC 隔离。Node 工具链需要包含 libuv 的
[AppContainer 管道修复](https://github.com/libuv/libuv/pull/5181)，才能为
`node --test` 和构建工具创建子进程输出管道。实测 Node.js 24.14.0 的
默认测试运行器会卡在管道创建并超时；只升级 npm 无法修复此问题。
遇到相同现象时，使用带该修复的 Node 工具链，或选择已配置好工具链的
Docker 执行环境，再重新准备和测试项目。

## 独立的依赖下载授权

依赖源列表来自本次检测到的锁文件。勾选的 HTTPS 源只用于获取锁定的
依赖包；每次准备都重新绑定锁文件内容与所选源。npm 包按锁文件的
完整性值校验，Go 模块按 `go.sum` 校验。下载重定向也需要对应的源授权。
Python wheel 按锁定的 SHA-256 校验。
Go 代理将模块归档重定向到 Google Storage 时，需要同时勾选列表中的
`https://proxy.golang.org` 和 `https://storage.googleapis.com`。

网页搜索在任务中单独设置。准备依赖后，沙箱中的安装、安装脚本和项目
测试继续使用关闭的网络。需要联网的测试应先检查其配置和运行要求。

依赖源全部留空时，准备使用已有缓存。缺少缓存的依赖会显示失败原因，
勾选该项目需要的源后可以重新准备。

## 复用缓存与安装脚本

「依赖缓存」显示已缓存包的数量；准备进度显示实际复用的包数和下载
字节数。应用共享缓存保存经过校验的下载内容，每个任务在自己的工作
目录安装依赖。再次使用前会核对校验值，损坏内容需要重新获取。

npm/pnpm 的「运行依赖的安装脚本」默认关闭。首次安装先关闭脚本完成依赖
安装，生成项目命令入口；勾选后，npm 在当前沙箱中执行离线 `npm rebuild`，
pnpm 执行独立的 `pnpm --recursive rebuild --pending`。需要生成原生组件的
依赖应核对其安装要求，再选择此项。pnpm 继续遵守项目的
依赖构建白名单；需要构建的包应先在项目中明确允许。
这项选择与包源下载授权分别保存。pnpmfile 配置钩子始终关闭。

「项目已准备」表示本次安装成功。测试通过需要运行测试，并核对其
终态与退出码；测试失败时可将结果放回对话，让代理修复后再次运行。

## 失败、取消与恢复

| 界面状态或操作 | 下一步 |
| --- | --- |
| 准备失败 | 展开「安装命令输出」，核对退出码和标准错误；修复工具链、锁文件或源选择后点击「重新准备项目」 |
| 测试失败 | 查看测试输出，使用「将失败结果放回对话」修复源码，再运行原测试 |
| 取消准备 | 点击正在运行记录中的「取消准备」，等待取消结果后再开始新的准备 |
| 准备已中断 | 核对保留的记录，修复环境后显式重新准备；已缓存且校验通过的下载内容可以复用 |
| 准备请求结果尚未确认 | 点击「确认上次准备」，使用原请求核对结果 |
| 无法读取进度 | 点击「重试读取进度」，继续读取已有准备记录 |

关闭面板或浏览器后，服务继续处理已接收的准备操作。重新打开同一任务
可以查看记录；服务重启后，未完成的操作按保存的状态恢复为可核对的
记录。确认原请求会读取已有结果，新的准备需要再次点击执行。
服务异常退出留下的过期准备记录，会在同一任务的新准备开始前重新核对；
确认已无执行者后结束旧记录，因此可以改选另一个项目目录继续准备。

## 运行范围

准备工作流通过所选 Local、Docker Engine 或 Docker Sandboxes 执行。
各后端的平台与版本要求见[执行环境选择](sandbox-environments.md)，
具体运行结果以当前安装记录、测试记录和对应验收记录为准。
初版 npm/Go 的真实运行与截图见
[项目依赖准备验收](acceptance/2026-10-14-project-preparation.md)；pnpm 的验证范围见
[pnpm 项目准备验证记录](acceptance/2026-10-19-pnpm-project-preparation.md)。
检测、授权、缓存和中断恢复合同见
[ADR 0176](adr/0176-project-dependency-preparation.md)。
