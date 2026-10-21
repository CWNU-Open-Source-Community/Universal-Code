# Universal-Code Web Console

本目录包含 **Universal-Code** 的前端桌面与 Web 用户界面，基于 React 19、TypeScript 与 Vite 构建。

---

## 技术栈 (Tech Stack)

- **核心框架**：React 19 + TypeScript + Vite
- **代码与 Diff 编辑器**：Monaco Editor (本地离线打包，支持按需加载与 Proposal 恢复)
- **视觉与样式系统**：纯 CSS 苹果液态玻璃（Apple Liquid Glass Acrylic）+ Tailwind CSS 工具类
- **测试框架**：Vitest + React Testing Library
- **数据协议**：基于 OpenAPI 3.1 强类型 DTO，通过 Server-Sent Events (SSE) 接收实时流式执行日志

---

## 本地开发与联调模式 (Getting Started)

### 方式一：Vite 独立开发代理模式 (推荐日常前端开发)

该模式支持热模块替换 (HMR)，修改代码后浏览器即时更新：

```bash
# 终端 1：在仓库根目录启动 Go API 核心服务
go run ./cmd/cyberagent api serve --listen 127.0.0.1:8765

# 终端 2：启动前端 Vite 开发服务器
cd web
npm ci
npm run dev
```

开发服务器默认监听 `http://127.0.0.1:5173`，已内置代理将 `/api` 请求自动转发至 `http://127.0.0.1:8765`。

### 方式二：生产打包与 Go 统一托管模式 (验证构建与最终交付)

该模式直接由 Go 进程提供生产静态资源服务，测试最真实的桌面打包环境：

```bash
cd web
npm ci
npm run build
cd ..

# 启动 Go 服务托管前端产物
go run ./cmd/cyberagent api serve --listen 127.0.0.1:8765 --ui-dir web/dist
```

访问 `http://127.0.0.1:8765`，首次连接时输入后端打印的临时只读 token，或 `CYBERAGENT_API_TOKEN` 配置的 token。控制操作还需 control token 与对应 capability，见 [HTTP API](../docs/http-api.md)。

---

## 核心业务功能视图 (Key Views & Components)

| 核心视图 | 对应模块路径 | 功能说明 |
| :--- | :--- | :--- |
| **会话舞台 (Stage)** | `src/v2/components/conversation.tsx` | 连续任务对话流、消息卡片、工具调用状态呈现与响应流式打字机。 |
| **伴侣停靠坞 (Companion Dock)** | `src/v2/styles.css` (`.v2-companion-dock`) | 智能对齐右侧的工作流面板，支持检查点、代码审查与工具箱原生平移停靠。 |
| **任务输入舱 (Composer)** | `src/v2/components/composer.tsx` | 任务指令输入、文件/图片附件上传、模型选择器与权限档位切换。 |
| **代码 Diff 与编辑** | `src/components/unified-diff.ts` / `src/lib/monaco-local.ts` | 逐行差异对比、待审提案（Proposal）接受/驳回与本地 Monaco 编辑。 |
| **网页证据来源** | `src/components/thread-transcript.tsx` / `src/v2/components/activity-detail.tsx` | 展示网页来源、URL、获取时间、摘要标识以及可引用、部分内容和过期状态。 |
| **系统设置与外观** | `src/v2/components/settings.tsx` / `src/v2/components/provider-settings.tsx` | 多模型凭据配置、系统/浅色/深色/雾透磨砂玻璃外观实时切换。 |

---

## 目录结构 (Directory Layout)

```text
web/
├── src/
│   ├── api/             # Go 后端 API 客户端、OpenAPI 生成的 DTO (schema.d.ts)
│   ├── assets/          # 字体（HarmonyOS Sans SC）、图标与图片静态资源
│   ├── components/      # 通用基础 UI 组件（按钮、模态框、Diff 视图、状态卡片等）
│   ├── hooks/           # 通用 React Hooks（流式读取、按键绑定、焦点捕获等）
│   ├── lib/             # 格式化、剪贴板、剪裁等纯函数工具库
│   ├── v2/              # V2 双列会话舞台与伴侣停靠面板 (Companion Dock)
│   │   ├── components/  # V2 核心组件（会话流、输入区、工作台、停靠抽屉）
│   │   └── styles.css   # V2 苹果液态玻璃控件与弹性停靠样式
│   ├── App.tsx          # 前端根组件与顶层路由调度
│   └── styles.css       # 全局样式、设计令牌与主题光场定义
├── package.json
└── vite.config.ts
```

---

## 常用脚本与检查指令

| 命令 | 用途 |
| :--- | :--- |
| `npm run dev` | 启动 Vite 热重载开发服务器 |
| `npm run typecheck` | 执行 TypeScript 全量静态类型检查 (`tsc -b`) |
| `npm test` | 运行 Vitest 全量单元与集成测试套件 |
| `npm run build` | 编译打包前端生产静态资源到 `dist/` |
| `npm run check:api` | 检查前端 DTO 与 `docs/openapi.json` 是否存在接口漂移 |
| `npm run generate:api`| 基于 `docs/openapi.json` 重新生成 `src/api/schema.d.ts` |

---

## 纯 CSS 视觉与设计规范 (Design Language)

- **主题样式**：使用 CSS 渐变、半透明背景、`backdrop-filter` 和边缘阴影呈现各主题的视觉效果，修改应限定在对应主题内。
- **实际页面验证**：检查文字清晰度、长内容滚动、窄屏布局和交互状态；性能结论应记录测量环境与结果。

---

## English Overview

Universal-Code Web Console is the desktop and web operator interface for Universal-Code, built with **React 19**, **TypeScript**, and **Vite**.

### Quick Start
1. Run backend: `go run ./cmd/cyberagent api serve --listen 127.0.0.1:8765`
2. Run frontend: `npm ci && npm run dev`
3. Open `http://127.0.0.1:5173` (requests to `/api` are automatically proxied).

### Key Features
- **Fluid Companion Dock**: A docked assistant panel alongside the main conversation stage without overlapping content.
- **Monaco Diff Viewer**: Line-by-line inspection and auditable rollback.
- **CSS Themes**: Translucent surfaces, backdrop blur, gradients, and edge shadows.
