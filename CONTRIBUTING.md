# 为 Universal-Code 贡献代码 (Contributing Guide)

感谢你对 Universal-Code 的关注与支持！我们欢迎社区提交 Bug 修复、功能增强、文档优化以及用户体验改进。

为了让开发与审查流程更顺畅高效，请在提交代码前阅读本指南。

---

## 开发环境要求

在本地搭建开发环境前，请确保已安装以下基础工具：

| 工具 | 推荐版本 | 来源与用途 |
| :--- | :--- | :--- |
| **Go** | 1.26+ | 核心控制平面、CLI、TUI、HTTP API 与 SQLite 状态机 |
| **Node.js** | 24+ | 前端管理界面（`web/` 目录下的 React 18 + Vite） |
| **Git** | 2.41+ | 源码版本管理 |
| **Rust** *(可选)* | 1.97.1+ | 仅在修改或验证 `analyzers/` 目录下的 WASI 代码分析器时需要 |

> **提示**：普通的前后端功能开发不需要真实云端模型凭据；未配置外部模型时，系统将自动使用内置的 Mock Provider 供本地联调。

---

## 本地开发工作流

### 1. 检出仓库与准备依赖

```bash
# 克隆代码库
git clone https://github.com/CWNU-Open-Source-Community/Universal-Code.git
cd Universal-Code

# 安装前端依赖
cd web
npm ci
cd ..

# 校验并同步 Go 模块依赖
go mod verify
```

### 2. 启动前端热重载开发服务器

在 `web` 目录下运行：

```bash
cd web
npm run dev
# 默认开发地址为 http://127.0.0.1:5173
```

前端开发服务器已配置自动反向代理，会自动将 `/api` 请求转发到本地运行的 Go 后端服务（默认目标为 `http://127.0.0.1:8765`）。

### 3. 运行 Go 后端 API 服务

在仓库根目录下运行：

```bash
go run ./cmd/cyberagent api serve --listen 127.0.0.1:8765
```

如需配置只读访问 Token 或控制 Token，可在启动前注入环境变量：
```bash
# Windows PowerShell
$env:CYBERAGENT_API_TOKEN = "local-read-token"
$env:CYBERAGENT_API_CONTROL_TOKEN = "local-control-token"

# Linux / macOS
export CYBERAGENT_API_TOKEN="local-read-token"
export CYBERAGENT_API_CONTROL_TOKEN="local-control-token"
```

### 4. 运行桌面版客户端

- **Windows 桌面端 (需要 Edge WebView2 Runtime)**：
  ```powershell
  # 本地构建未签名测试包
  ./scripts/build-desktop.ps1

  # 运行可复现验证构建
  ./scripts/build-desktop.ps1 -VerifyReproducible
  ```
- **macOS 桌面端**：
  ```bash
  # 本地构建测试包
  ./scripts/build-desktop-darwin.sh
  ```

---

## OpenAPI 契约与 DTO 代码生成

Universal-Code 以 **Go 结构体与路由定义作为 API 的唯一真相来源 (Single Source of Truth)**。

当你修改了 Go HTTP 控制器、路由或对外 DTO 时，必须从仓库根目录重新生成接口定义并提交受测快照：

```bash
# 1. 由 Go 导出最新的 OpenAPI 3.1 规范文档
go run ./cmd/cyberagent api openapi --output docs/openapi.json

# 2. 重新生成前端 TypeScript 类型定义
cd web
npm run generate:api
cd ..

# 3. 检查是否存在类型或接口漂移
cd web
npm run check:api
cd ..
```

> **注意**：请勿手动编辑 `docs/openapi.json` 或 `web/src/api/schema.d.ts`。所有 DTO 生成物在 CI 中均会通过 `npm run check:api` 严格校验一致性。

---

## 运行测试与质量检查

在提交 Pull Request 前，请确保相关测试均能本地通过：

### 前端测试与类型检查 (web/)

```bash
cd web

# 运行 TypeScript 静态类型检查
npm run typecheck

# 运行 Vitest 单元与集成测试套件
npm test

# 验证生产打包构建
npm run build
```

### 后端单元与集成测试 (根目录)

```bash
# Go 静态分析
go vet ./...

# 运行全量单元测试
go test -count=1 ./...

# 运行桌面端专属集成测试 (Windows / macOS)
go test -tags "desktop" -count=1 ./cmd/cyberagent-desktop ./internal/desktop ./internal/webui
```

### Rust 分析器测试 (仅涉及 analyzers/ 时)

```bash
cd analyzers
cargo fmt --all -- --check
cargo test --locked
cargo clippy --locked --all-targets -- -D warnings
cd ..
```

---

## 敏感数据隔离与测试安全守则

- **严禁提交机密凭据**：绝不要将真实 API Key、Bearer/Control Token、Cookie、`.env` 文件、真实的供应商请求日志或敏感数据提交到 Git 仓库。
- **本地临时数据隔离**：请勿将本地生成的 `.cyberagent-workbench/`、`cyberagent.db`、SQLite WAL 文件或 `build/`、`dist/` 产物提交到版本库中。
- **测试环境纯净**：单元测试与 CI 流程默认使用 Mock Provider，切勿在自动化测试中对公网产生真实计费或未授权的网络流量。

---

## 设计哲学与工程规范

1. **安全第一与最小特权原则**：
   - 所有的代码读写、终端命令执行和网络访问都具备明确的审计边界。
   - 文件删除、覆盖式重命名或危险命令必须经过人工弹窗确认，绝不允许静默越权。
2. **前后端职责分明**：
   - **Go 控制面** 拥有完整的数据持久化、状态转换、权限门禁和执行调度能力。
   - **React 前端** 负责用户交互呈现与视觉效果，不私自推导或伪造未经后端确认的状态。
3. **纯 CSS 现代性能标准**：
   - 界面视觉效果优先使用现代纯 CSS 属性（如 `backdrop-filter`、多层 `box-shadow` 内阴影、`linear-gradient`）。
   - 避免引入高开销的 SVG 位移滤镜（如 `feTurbulence`）或全屏 WebGL Canvas，确保在展示大量代码和日志输出时保持 120fps 丝滑滚动。
4. **统一规范的产品命名**：
   - 所有面向用户的标题、文案、关于页面与文档中，产品官方名称一律统一为 **`Universal-Code`**。

---

## 提交 Pull Request (PR)

1. 从 `main` 分支切出具名分支，例如：
   ```bash
   git checkout -b fix/sidebar-docking
   # 或
   git checkout -b feat/support-deepseek-r1
   ```
2. 保持 Commit 粒度清晰，提交信息简明扼要。
3. 在本地完整运行并通过 `npm test` 和 `go test`。
4. 在 GitHub 上发起 Pull Request，简要说明：
   - **改动背景**：解决了什么问题或增加了什么特性。
   - **关键设计**：采用了什么样的架构思路。
   - **验证方式**：在本地如何复现并验证了修复效果。

感谢你为 Universal-Code 做出的贡献。
