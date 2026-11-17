import { isBuiltinComputerUseServer } from "../api/computer-use";
import type {
  CodeIntelConfigurationView,
  CodeIntelServerView,
  ExtensionMCPServerView,
  ExtensionPluginInstallationView,
  SkillPackageItemView,
} from "../api/types";

export type PluginToolRole =
  | "all"
  | "frontend_ui"
  | "backend_data"
  | "qa_browser"
  | "review_security"
  | "docs_search"
  | "devops_automation";

export type SpecificPluginToolRole = Exclude<PluginToolRole, "all">;

export interface PluginRoleDefinition {
  id: PluginToolRole;
  label: { zh: string; en: string };
  shortLabel: { zh: string; en: string };
  description: { zh: string; en: string };
}

export const PLUGIN_TOOL_ROLES: ReadonlyArray<PluginRoleDefinition> = [
  {
    id: "all",
    label: { zh: "全部", en: "All" },
    shortLabel: { zh: "全部", en: "All" },
    description: {
      zh: "查看全部已安装的 Plugin、MCP Server、LSP 与 Skill 工具",
      en: "Browse all configured Plugins, MCP servers, LSPs, and Skills",
    },
  },
  {
    id: "frontend_ui",
    label: { zh: "前端与设计", en: "Frontend & UI" },
    shortLabel: { zh: "前端与设计", en: "Frontend & UI" },
    description: {
      zh: "设计稿解析、UI 组件、样式系统、可访问性与前端语言智能",
      en: "Design-to-code, UI components, styling systems, a11y, and frontend LSPs",
    },
  },
  {
    id: "backend_data",
    label: { zh: "后端与数据", en: "Backend & Data" },
    shortLabel: { zh: "后端与数据", en: "Backend & Data" },
    description: {
      zh: "数据库查询、Schema 演进、API 契约与服务端语言分析",
      en: "Database queries, schema inspection, API contracts, and backend LSPs",
    },
  },
  {
    id: "qa_browser",
    label: { zh: "测试与浏览器", en: "QA & Browser" },
    shortLabel: { zh: "测试与浏览器", en: "QA & Browser" },
    description: {
      zh: "浏览器自动化、Playwright/CDP 交互验收、截图取证与回归测试",
      en: "Browser automation, Playwright/CDP verification, screenshots, and E2E QA",
    },
  },
  {
    id: "review_security",
    label: { zh: "代码审查与安全", en: "Review & Security" },
    shortLabel: { zh: "代码审查与安全", en: "Review & Security" },
    description: {
      zh: "静态安全审计、漏洞与密钥扫描、代码规范与 PR 审阅技能",
      en: "Security auditing, vulnerability & secret scanning, linting, and PR review skills",
    },
  },
  {
    id: "docs_search",
    label: { zh: "文档与检索", en: "Docs & Search" },
    shortLabel: { zh: "文档与检索", en: "Docs & Search" },
    description: {
      zh: "外部技术文档检索、知识库连接、API 手册与上下文增强",
      en: "External documentation lookup, knowledge bases, and search MCP connectors",
    },
  },
  {
    id: "devops_automation",
    label: { zh: "运维与自动化", en: "DevOps & Automation" },
    shortLabel: { zh: "运维与自动化", en: "DevOps & Automation" },
    description: {
      zh: "CI/CD 流水线、容器与云环境巡检、监控告警与自然语言定时任务",
      en: "CI/CD pipelines, container ops, observability, and scheduled automations",
    },
  },
];

export interface RoleTemplatePreset {
  id: string;
  role: SpecificPluginToolRole;
  name: { zh: string; en: string };
  kindLabel: string;
  summary: { zh: string; en: string };
  suggestedServerID: string;
  suggestedTargetHint: string;
}

export const ROLE_TEMPLATE_PRESETS: ReadonlyArray<RoleTemplatePreset> = [
  {
    id: "preset-figma-ui",
    role: "frontend_ui",
    name: { zh: "Figma / UI 设计协同 MCP", en: "Figma / UI Design MCP" },
    kindLabel: "MCP",
    summary: {
      zh: "读取设计节点、色板 Token 与布局约束，直接对齐前端组件实现。",
      en: "Inspect design frames, tokens, and layout specs directly inside tasks.",
    },
    suggestedServerID: "figma-mcp",
    suggestedTargetHint: "http://127.0.0.1:3845/mcp",
  },
  {
    id: "preset-postgres-db",
    role: "backend_data",
    name: { zh: "PostgreSQL / SQLite 数据结构 MCP", en: "PostgreSQL / SQLite Schema MCP" },
    kindLabel: "MCP",
    summary: {
      zh: "受控读取数据库表结构、索引与只读诊断查询，辅助编写迁移与 SQL。",
      en: "Inspect table schemas, indexes, and read-only diagnostic queries safely.",
    },
    suggestedServerID: "postgres-mcp",
    suggestedTargetHint: "stdio://npx @modelcontextprotocol/server-postgres",
  },
  {
    id: "preset-playwright-qa",
    role: "qa_browser",
    name: { zh: "Playwright / Chrome DevTools 浏览器工具", en: "Playwright / Chrome DevTools MCP" },
    kindLabel: "MCP",
    summary: {
      zh: "驱动真实浏览器完成页面交互、控制台报错排查、无障碍审计与截图验收。",
      en: "Automate browser flows, inspect console/network logs, and capture visual proof.",
    },
    suggestedServerID: "playwright-mcp",
    suggestedTargetHint: "stdio://npx @playwright/mcp@latest",
  },
  {
    id: "preset-security-review",
    role: "review_security",
    name: { zh: "代码审查与安全扫描 Skill", en: "Security & Code Review Skill" },
    kindLabel: "Skill / Plugin",
    summary: {
      zh: "按 OWASP 清单核查鉴权边界、注入风险、敏感凭据暴露与并发缺陷。",
      en: "Audit auth boundaries, injection risks, secret exposure, and concurrency bugs.",
    },
    suggestedServerID: "security-audit",
    suggestedTargetHint: "SKILL.md / plugin.v1",
  },
  {
    id: "preset-docs-search",
    role: "docs_search",
    name: { zh: "Context7 / 开发者文档检索 MCP", en: "Context7 / Docs Search MCP" },
    kindLabel: "MCP",
    summary: {
      zh: "按版本精确拉取最新第三方 SDK 文档与官方 API 示例，消除过时接口幻觉。",
      en: "Fetch version-accurate SDK documentation and official API examples on demand.",
    },
    suggestedServerID: "docs-search-mcp",
    suggestedTargetHint: "https://mcp.context7.com/mcp",
  },
  {
    id: "preset-devops-ci",
    role: "devops_automation",
    name: { zh: "CI 巡检与自然语言自动化", en: "CI Patrol & Automations Studio" },
    kindLabel: "Automation / MCP",
    summary: {
      zh: "结合容器/CI 状态查询与自然语言定时工作台，持续守护构建与线上健康。",
      en: "Combine CI/container MCP tools with natural-language scheduled automations.",
    },
    suggestedServerID: "devops-ci-mcp",
    suggestedTargetHint: "#/automations",
  },
];

export type ClassifiedToolKind = "plugin" | "mcp" | "lsp" | "skill";

export interface ClassifiedToolEntry {
  id: string;
  kind: ClassifiedToolKind;
  name: string;
  subtitle: string;
  description: string;
  state: string;
  enabled: boolean;
  needsSetup: boolean;
  roles: SpecificPluginToolRole[];
  tags: string[];
}

const ROLE_KEYWORDS: Record<SpecificPluginToolRole, ReadonlyArray<string>> = {
  frontend_ui: [
    "figma",
    "ui",
    "ux",
    "css",
    "scss",
    "tailwind",
    "design",
    "storybook",
    "react",
    "vue",
    "svelte",
    "html",
    "dom",
    "component",
    "frontend",
    "front-end",
    "a11y",
    "accessibility",
    "canvas",
    "theme",
    "typescript-language-server",
    "vtsls",
    "volar",
    "eslint",
    "前端",
    "设计",
    "样式",
    "界面",
    "组件",
  ],
  backend_data: [
    "postgres",
    "postgresql",
    "mysql",
    "sqlite",
    "sql",
    "database",
    "db",
    "redis",
    "mongodb",
    "supabase",
    "prisma",
    "drizzle",
    "graphql",
    "openapi",
    "swagger",
    "grpc",
    "schema",
    "migration",
    "backend",
    "gopls",
    "pyright",
    "rust-analyzer",
    "jdtls",
    "clangd",
    "数据库",
    "后端",
    "数据",
    "表结构",
    "接口",
  ],
  qa_browser: [
    "playwright",
    "puppeteer",
    "chrome",
    "devtools",
    "browser",
    "cdp",
    "selenium",
    "cypress",
    "vitest",
    "jest",
    "e2e",
    "qa",
    "test",
    "testing",
    "screenshot",
    "lighthouse",
    "computer_use",
    "computer-use",
    "测试",
    "浏览器",
    "截图",
    "端到端",
    "验收",
  ],
  review_security: [
    "security",
    "audit",
    "semgrep",
    "codeql",
    "snyk",
    "trivy",
    "owasp",
    "cve",
    "vulnerability",
    "vuln",
    "secret",
    "review",
    "lint",
    "sonar",
    "auth",
    "compliance",
    "sandbox",
    "安全",
    "审查",
    "审阅",
    "审计",
    "漏洞",
    "鉴权",
  ],
  docs_search: [
    "doc",
    "docs",
    "documentation",
    "context7",
    "search",
    "tavily",
    "exa",
    "brave",
    "searxng",
    "perplexity",
    "confluence",
    "notion",
    "wiki",
    "knowledge",
    "rag",
    "fetch",
    "markdown",
    "readme",
    "文档",
    "检索",
    "搜索",
    "知识库",
    "手册",
  ],
  devops_automation: [
    "docker",
    "kubernetes",
    "k8s",
    "helm",
    "terraform",
    "github",
    "gitlab",
    "ci",
    "cd",
    "actions",
    "pipeline",
    "deploy",
    "sentry",
    "prometheus",
    "grafana",
    "datadog",
    "cron",
    "schedule",
    "automation",
    "workflow",
    "ops",
    "devops",
    "terminal",
    "shell",
    "运维",
    "自动化",
    "部署",
    "监控",
    "流水线",
    "定时",
  ],
};

export function matchTextToRoles(
  tokens: ReadonlyArray<string>,
  fallbackRole: SpecificPluginToolRole = "devops_automation",
): SpecificPluginToolRole[] {
  const joined = tokens.join(" ").toLowerCase();
  const wordSet = new Set(joined.split(/[^a-z0-9_\u4e00-\u9fa5-]+/u).filter(Boolean));
  const matched: SpecificPluginToolRole[] = [];

  for (const role of Object.keys(ROLE_KEYWORDS) as SpecificPluginToolRole[]) {
    const keywords = ROLE_KEYWORDS[role];
    const hit = keywords.some((kw) => {
      if (/^[\u4e00-\u9fa5]+$/u.test(kw) || kw.includes("-") || kw.includes("_") || kw.length >= 4) {
        return joined.includes(kw);
      }
      return wordSet.has(kw);
    });
    if (hit) matched.push(role);
  }

  return matched.length > 0 ? matched : [fallbackRole];
}

export function classifyExtensionCatalog(input: {
  mcpServers?: ExtensionMCPServerView[];
  plugins?: ExtensionPluginInstallationView[];
  codeIntelServers?: CodeIntelServerView[];
  codeIntelConfigurations?: CodeIntelConfigurationView[];
  skills?: SkillPackageItemView[];
}): ClassifiedToolEntry[] {
  const entries: ClassifiedToolEntry[] = [];

  for (const plugin of input.plugins ?? []) {
    const enabled = plugin.state === "enabled" && plugin.enabled_capabilities.length > 0;
    const needsSetup = ["staged", "approved", "quarantined"].includes(plugin.state);
    const tokens = [
      plugin.manifest.name,
      plugin.manifest.id,
      plugin.manifest.description,
      plugin.manifest.publisher,
      ...plugin.manifest.capabilities,
      ...plugin.enabled_capabilities,
      plugin.source.uri,
    ];
    entries.push({
      id: `plugin:${plugin.id}`,
      kind: "plugin",
      name: plugin.manifest.name || plugin.id,
      subtitle: [plugin.manifest.publisher, plugin.manifest.version ? `v${plugin.manifest.version}` : ""]
        .filter(Boolean)
        .join(" · "),
      description: plugin.manifest.description || plugin.source.uri,
      state: plugin.state,
      enabled,
      needsSetup,
      roles: matchTextToRoles(tokens, "review_security"),
      tags: plugin.manifest.capabilities,
    });
  }

  for (const server of input.mcpServers ?? []) {
    const builtin = isBuiltinComputerUseServer(server);
    const toolNames = (server.capabilities?.tools ?? []).map((tool) => String(tool));
    const enabled = server.state === "enabled";
    const needsSetup = [
      "staged",
      "discovery_approved",
      "capabilities_pending",
      "quarantined",
    ].includes(server.state);
    const roles: SpecificPluginToolRole[] = builtin
      ? ["qa_browser", "devops_automation"]
      : matchTextToRoles(
          [
            server.name,
            server.id,
            server.target,
            server.source?.uri ?? "",
            ...(server.declared_capabilities ?? []),
            ...toolNames,
          ],
          "docs_search",
        );

    entries.push({
      id: `mcp:${server.id}`,
      kind: "mcp",
      name: server.name || server.id,
      subtitle: `${server.transport} · ${toolNames.length} tools`,
      description: builtin
        ? "Universal-Code Host Computer Use"
        : toolNames.slice(0, 5).join(", ") || server.target,
      state: server.state,
      enabled,
      needsSetup,
      roles,
      tags: toolNames.slice(0, 6),
    });
  }

  for (const lsp of input.codeIntelServers ?? []) {
    const langs = lsp.languages ?? [];
    const roles = matchTextToRoles(
      [lsp.server_name, lsp.server_id, ...langs, ...(lsp.model_visible_tools ?? [])],
      langs.some((l) => ["typescript", "javascript", "tsx", "jsx", "css", "html", "vue"].includes(l.toLowerCase()))
        ? "frontend_ui"
        : "backend_data",
    );
    entries.push({
      id: `lsp:${lsp.workspace_id}/${lsp.server_id}`,
      kind: "lsp",
      name: lsp.server_name || lsp.server_id,
      subtitle: langs.join(", ") || lsp.server_id,
      description: (lsp.model_visible_tools ?? []).join(", ") || lsp.source_label,
      state: lsp.health,
      enabled: ["healthy", "ready"].includes(lsp.health),
      needsSetup: ["degraded", "failed", "stopped"].includes(lsp.health),
      roles,
      tags: langs,
    });
  }

  for (const cfg of input.codeIntelConfigurations ?? []) {
    const alreadyListed = entries.some(
      (item) => item.id === `lsp:${cfg.workspace_id}/${cfg.server_id}`,
    );
    if (alreadyListed) continue;
    const langIDs = (cfg.languages ?? []).map((lang) => lang.id);
    entries.push({
      id: `lsp-cfg:${cfg.workspace_id}/${cfg.server_id}`,
      kind: "lsp",
      name: cfg.server_name || cfg.server_id,
      subtitle: langIDs.join(", ") || cfg.server_id,
      description: cfg.source_label,
      state: cfg.review_state,
      enabled: cfg.review_state === "enabled" || cfg.review_state === "reviewed",
      needsSetup: cfg.review_state === "staged" || cfg.review_state === "pending_review",
      roles: matchTextToRoles([cfg.server_name, cfg.server_id, ...langIDs], "backend_data"),
      tags: langIDs,
    });
  }

  for (const skill of input.skills ?? []) {
    if (skill.status === "removed") continue;
    const tokens = [
      skill.skill,
      ...(skill.profiles ?? []),
      ...(skill.roles ?? []),
      ...(skill.surfaces ?? []),
      ...(skill.tool_dependencies ?? []),
    ];
    entries.push({
      id: `skill:${skill.skill}`,
      kind: "skill",
      name: skill.skill,
      subtitle: (skill.profiles ?? []).join(", ") || skill.surface,
      description: (skill.tool_dependencies ?? []).join(", ") || skill.trust_class,
      state: skill.status,
      enabled: skill.status === "installed",
      needsSetup: !skill.operator_confirmed,
      roles: matchTextToRoles(tokens, "review_security"),
      tags: [...(skill.profiles ?? []), ...(skill.tool_dependencies ?? [])].slice(0, 6),
    });
  }

  return entries;
}

export function countToolsByRole(
  entries: ReadonlyArray<ClassifiedToolEntry>,
): Record<PluginToolRole, number> {
  const counts: Record<PluginToolRole, number> = {
    all: entries.length,
    frontend_ui: 0,
    backend_data: 0,
    qa_browser: 0,
    review_security: 0,
    docs_search: 0,
    devops_automation: 0,
  };
  for (const entry of entries) {
    for (const role of entry.roles) {
      counts[role] += 1;
    }
  }
  return counts;
}

export function filterClassifiedTools(
  entries: ReadonlyArray<ClassifiedToolEntry>,
  role: PluginToolRole,
  query = "",
): ClassifiedToolEntry[] {
  const normalized = query.trim().toLowerCase();
  return entries.filter((entry) => {
    if (role !== "all" && !entry.roles.includes(role)) return false;
    if (!normalized) return true;
    const haystack = [
      entry.name,
      entry.subtitle,
      entry.description,
      entry.kind,
      ...entry.tags,
    ]
      .join(" ")
      .toLowerCase();
    return haystack.includes(normalized);
  });
}

// Navigation pre-filter handoff between in-chat recommendation card and Plugins settings page
const ROLE_FILTER_STORAGE_KEY = "uc.v2.pending-plugin-role.v1";
let pendingRoleFilterMemory: PluginToolRole | null = null;
const roleFilterListeners = new Set<(role: PluginToolRole) => void>();

export function requestPluginRoleFilter(role: PluginToolRole): void {
  pendingRoleFilterMemory = role;
  try {
    window.sessionStorage?.setItem(ROLE_FILTER_STORAGE_KEY, role);
  } catch {
    // Ignore storage failures
  }
  for (const listener of roleFilterListeners) {
    listener(role);
  }
}

export function consumePluginRoleFilter(): PluginToolRole | null {
  let value = pendingRoleFilterMemory;
  pendingRoleFilterMemory = null;
  try {
    const stored = window.sessionStorage?.getItem(ROLE_FILTER_STORAGE_KEY);
    if (stored) {
      window.sessionStorage.removeItem(ROLE_FILTER_STORAGE_KEY);
      if (!value && PLUGIN_TOOL_ROLES.some((r) => r.id === stored)) {
        value = stored as PluginToolRole;
      }
    }
  } catch {
    // Ignore storage failures
  }
  return value;
}

export function subscribePluginRoleFilter(listener: (role: PluginToolRole) => void): () => void {
  roleFilterListeners.add(listener);
  return () => {
    roleFilterListeners.delete(listener);
  };
}

// Per-thread dismiss persistence
const DISMISS_STORAGE_KEY = "uc.v2.dismissed-plugin-recommendations.v1";
const dismissMemoryFallback = new Map<string, Set<string>>();

function readDismissMap(): Record<string, string[]> {
  try {
    const raw = window.localStorage?.getItem(DISMISS_STORAGE_KEY);
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const result: Record<string, string[]> = {};
    for (const [k, v] of Object.entries(parsed)) {
      if (Array.isArray(v)) {
        result[k] = v.filter((item): item is string => typeof item === "string");
      }
    }
    return result;
  } catch {
    return {};
  }
}

export function isRecommendationDismissed(threadID: string, recommendationID: string): boolean {
  if (!threadID) return false;
  if (dismissMemoryFallback.get(threadID)?.has(recommendationID)) return true;
  const map = readDismissMap();
  const list = map[threadID];
  return Boolean(list && (list.includes(recommendationID) || list.includes("*")));
}

export function dismissRecommendation(threadID: string, recommendationID: string): void {
  if (!threadID) return;
  const set = dismissMemoryFallback.get(threadID) ?? new Set<string>();
  set.add(recommendationID);
  dismissMemoryFallback.set(threadID, set);
  try {
    const map = readDismissMap();
    const existing = new Set(map[threadID] ?? []);
    existing.add(recommendationID);
    map[threadID] = [...existing];
    window.localStorage?.setItem(DISMISS_STORAGE_KEY, JSON.stringify(map));
  } catch {
    // Keep memory state when localStorage is unavailable
  }
}

export function clearDismissedRecommendationsForTests(): void {
  dismissMemoryFallback.clear();
  pendingRoleFilterMemory = null;
  try {
    window.localStorage?.removeItem(DISMISS_STORAGE_KEY);
    window.sessionStorage?.removeItem(ROLE_FILTER_STORAGE_KEY);
  } catch {
    // Ignore
  }
}

export interface ProactiveToolRecommendation {
  id: string;
  role: SpecificPluginToolRole;
  actionKind: "plugin_or_mcp" | "automation";
  status: "disabled_installed" | "missing_tool" | "automation_candidate";
  title: { zh: string; en: string };
  reason: { zh: string; en: string };
  matchedToolName?: string;
  draftHint: { zh: string; en: string };
  suggestedSchedule?: string;
}

interface RecommendationRule {
  id: string;
  role: SpecificPluginToolRole;
  actionKind: "plugin_or_mcp" | "automation";
  patterns: RegExp[];
  failurePatterns?: RegExp[];
  missingTitle: { zh: string; en: string };
  missingReason: { zh: string; en: string };
  disabledReason: (toolName: string) => { zh: string; en: string };
  draftHint: (toolName?: string) => { zh: string; en: string };
}

const RECOMMENDATION_RULES: ReadonlyArray<RecommendationRule> = [
  {
    id: "rec-automation-studio",
    role: "devops_automation",
    actionKind: "automation",
    patterns: [
      /(?:每天|每日|每周[一二三四五六日天]?|每\s*\d+\s*(?:分钟|小时)|每小时|工作日).*(?:巡检|检查|汇总|运行|执行|构建|早报|监控|同步)/u,
      /(?:定时|周期性|自动巡检|cron)/iu,
      /\b(?:every\s+(?:day|hour|\d+\s*(?:minutes?|hours?)|monday|tuesday|wednesday|thursday|friday)|weekdays\s+at|daily\s+(?:check|brief|build)|recurring\s+task)\b/iu,
    ],
    missingTitle: {
      zh: "检测到周期性任务需求 · 推荐使用自动化工作台",
      en: "Recurring task detected · Try Automations Studio",
    },
    missingReason: {
      zh: "当前对话包含定时或周期巡检诉求，无需手写 Cron，可直接转为自然语言定时计划并持续追踪历史记录。",
      en: "This conversation describes a recurring check. Convert it into a natural-language schedule with zero Cron syntax.",
    },
    disabledReason: () => ({
      zh: "可将此周期任务保存为自然语言定时自动化。",
      en: "Save this recurring workflow in Automations Studio.",
    }),
    draftHint: () => ({
      zh: "请先梳理本次巡检的标准执行步骤与输出模板，便于我保存为定时自动化任务。",
      en: "Outline the repeatable inspection steps and report format so I can schedule this as an automation.",
    }),
  },
  {
    id: "rec-qa-browser",
    role: "qa_browser",
    actionKind: "plugin_or_mcp",
    patterns: [
      /(?:网页|页面|浏览器|前端交互|端到端|截图|视觉回归|控制台报错|无障碍|点击测试)/u,
      /\b(?:playwright|puppeteer|browser\s+test|e2e\s+test|screenshot|dom\s+inspection|lighthouse|chrome\s+devtools|visual\s+regression)\b/iu,
    ],
    failurePatterns: [/\b(?:browser_cdp|ui_evidence|playwright|headless)\b/iu],
    missingTitle: {
      zh: "智能体推荐：启用浏览器与端到端测试工具（Playwright / CDP）",
      en: "Recommended Tool: Enable Browser & QA MCP (Playwright / CDP)",
    },
    missingReason: {
      zh: "当前任务涉及页面交互验证或截图排查，接入“测试与浏览器”工具后智能体可直接驱动真实页面并采集证据。",
      en: "This task involves UI interaction or visual verification. Enabling a Browser/Playwright tool lets the agent inspect live pages.",
    },
    disabledReason: (toolName) => ({
      zh: `检测到已登记的「${toolName}」尚未启用，开启后智能体即可直接驱动浏览器执行页面验收。`,
      en: `"${toolName}" is registered but not yet enabled. Enable it so the agent can run live browser checks.`,
    }),
    draftHint: (toolName) => ({
      zh: `请结合${toolName ? `「${toolName}」` : "浏览器自动化测试工具"}验证关键页面交互流程、检查控制台异常并记录验收结论。`,
      en: `Verify the key UI interactions${toolName ? ` using ${toolName}` : " with browser automation"}, inspect console errors, and summarize QA results.`,
    }),
  },
  {
    id: "rec-backend-data",
    role: "backend_data",
    actionKind: "plugin_or_mcp",
    patterns: [
      /(?:数据库|表结构|慢查询|数据迁移|外键|索引优化|接口契约)/u,
      /\b(?:postgres|postgresql|mysql|sqlite|sql\s+query|database\s+schema|prisma\s+migrate|graphql\s+schema|openapi\s+spec|redis)\b/iu,
    ],
    failurePatterns: [/\b(?:connection\s+refused.*5432|sqlstate|schema\s+mismatch)\b/iu],
    missingTitle: {
      zh: "智能体推荐：接入数据库与 Schema 诊断 MCP 工具",
      en: "Recommended Tool: Connect Backend & Database Schema MCP",
    },
    missingReason: {
      zh: "当前对话涉及 SQL / 表结构或接口 Schema 分析，启用“后端与数据”插件可让智能体安全核对真实表结构。",
      en: "This conversation references SQL, database schemas, or API specs. Connect a Backend & Data MCP for accurate schema inspection.",
    },
    disabledReason: (toolName) => ({
      zh: `已安装的「${toolName}」当前未启用，前往插件页启用后可直接核对真实数据库 Schema。`,
      en: `"${toolName}" is installed but disabled. Enable it in Plugins to inspect live schemas safely.`,
    }),
    draftHint: (toolName) => ({
      zh: `请先核对相关数据表 Schema、索引与迁移兼容性${toolName ? `（通过 ${toolName}）` : ""}，再给出实现方案。`,
      en: `Verify the database schema, indexes, and migration compatibility${toolName ? ` via ${toolName}` : ""} before applying changes.`,
    }),
  },
  {
    id: "rec-review-security",
    role: "review_security",
    actionKind: "plugin_or_mcp",
    patterns: [
      /(?:安全审计|漏洞扫描|密钥泄露|权限绕过|代码审查|注入风险|合规检查)/u,
      /\b(?:security\s+audit|vulnerability\s+scan|owasp|secret\s+leak|sql\s+injection|xss|cve-\d+|semgrep|codeql)\b/iu,
    ],
    missingTitle: {
      zh: "智能体推荐：启用代码审查与安全审计工具",
      en: "Recommended Tool: Enable Security & Code Review Tools",
    },
    missingReason: {
      zh: "当前任务聚焦安全边界或深度代码审查，搭配“代码审查与安全”Skill/插件可执行系统化风险核查。",
      en: "This task focuses on security or code auditing. Enable Review & Security skills/plugins for structured vulnerability checks.",
    },
    disabledReason: (toolName) => ({
      zh: `检测到安全/审查工具「${toolName}」处于未启用状态，启用后可用于当前审计任务。`,
      en: `Security/review tool "${toolName}" is currently disabled. Enable it for this audit.`,
    }),
    draftHint: (toolName) => ({
      zh: `请按安全审计规范${toolName ? `（结合 ${toolName}）` : ""}重点检查鉴权边界、输入校验、敏感凭据与潜在漏洞，并分级列出修复建议。`,
      en: `Run a structured security review${toolName ? ` with ${toolName}` : ""} covering auth boundaries, input validation, and secret handling.`,
    }),
  },
  {
    id: "rec-docs-search",
    role: "docs_search",
    actionKind: "plugin_or_mcp",
    patterns: [
      /(?:查阅官方文档|最新\s*SDK\s*文档|外部知识库|检索手册|API\s*参考文档)/u,
      /\b(?:official\s+documentation|external\s+docs|sdk\s+reference|context7|confluence|notion\s+wiki|search\s+docs)\b/iu,
    ],
    failurePatterns: [/\b(?:search_diagnostics.*not_configured|no_usable_results)\b/iu],
    missingTitle: {
      zh: "智能体推荐：接入文档与知识库检索 MCP",
      en: "Recommended Tool: Connect Docs & Knowledge Search MCP",
    },
    missingReason: {
      zh: "当前对话需要查阅外部 SDK 文档或知识库，接入“文档与检索”MCP 可按版本精准拉取权威参考。",
      en: "This task references external docs or knowledge bases. Connect a Docs & Search MCP for up-to-date references.",
    },
    disabledReason: (toolName) => ({
      zh: `检测到文档检索工具「${toolName}」尚未启用，开启后可直接在对话中引用最新文档。`,
      en: `Docs search tool "${toolName}" is not enabled yet. Enable it to pull live documentation.`,
    }),
    draftHint: (toolName) => ({
      zh: `请先对齐目标依赖版本的官方 API 规范${toolName ? `（使用 ${toolName}）` : ""}，避免使用已废弃接口。`,
      en: `Cross-check the target SDK version's official API docs${toolName ? ` via ${toolName}` : ""} to avoid deprecated patterns.`,
    }),
  },
  {
    id: "rec-frontend-ui",
    role: "frontend_ui",
    actionKind: "plugin_or_mcp",
    patterns: [
      /(?:设计稿|Figma|还原界面|视觉走查|设计\s*Token|Storybook)/iu,
      /\b(?:figma|design\s+tokens?|storybook|design-to-code)\b/iu,
    ],
    missingTitle: {
      zh: "智能体推荐：启用前端与设计协同工具（Figma / UI）",
      en: "Recommended Tool: Enable Frontend & UI Design MCP",
    },
    missingReason: {
      zh: "检测到设计稿还原或 UI 组件规范诉求，接入“前端与设计”工具可直接读取设计节点与样式参数。",
      en: "Detected design-to-code or UI token work. Enable Frontend & UI tools to inspect design specs directly.",
    },
    disabledReason: (toolName) => ({
      zh: `已登记的前端设计工具「${toolName}」尚未启用，前往插件页开启即可接入。`,
      en: `Frontend/UI tool "${toolName}" is registered but disabled. Enable it in Plugins.`,
    }),
    draftHint: (toolName) => ({
      zh: `请严格对齐现有设计变量、间距层级与响应式断点${toolName ? `（结合 ${toolName}）` : ""}完成界面实现。`,
      en: `Align spacing, design tokens, and responsive breakpoints${toolName ? ` using ${toolName}` : ""}.`,
    }),
  },
];

function extractScheduleHint(corpus: string): string | undefined {
  const zhMatch =
    /(每天(?:早上|上午|下午|晚上)?\s*[\d零一二两三四五六七八九十:：点半]+|每周[一二三四五六日天]\s*[\d:：点半]*|工作日(?:早上|上午|下午)?\s*[\d:：点半]+|每\s*\d+\s*(?:分钟|小时)|每小时)/u.exec(
      corpus,
    );
  if (zhMatch) return zhMatch[1]!.trim();
  const enMatch =
    /\b(every\s+(?:day|monday|tuesday|wednesday|thursday|friday|saturday|sunday)(?:\s+at\s+\d{1,2}(?::\d{2})?\s*(?:am|pm)?)?|every\s+\d+\s*(?:minutes?|hours?)|weekdays\s+at\s+\d{1,2}(?::\d{2})?\s*(?:am|pm)?)\b/iu.exec(
      corpus,
    );
  return enMatch ? enMatch[1]!.trim() : undefined;
}

export function contextualIntentDetected(context: {
  threadTitle?: string;
  missionGoal?: string;
  recentMessages?: string[];
  failureHints?: string[];
  draft?: string;
}): boolean {
  const corpus = [
    context.threadTitle ?? "",
    context.missionGoal ?? "",
    ...(context.recentMessages ?? []),
    context.draft ?? "",
  ].join("\n");
  const failureCorpus = (context.failureHints ?? []).join("\n");
  if (!corpus.trim() && !failureCorpus.trim()) return false;
  return RECOMMENDATION_RULES.some(
    (rule) =>
      rule.patterns.some((re) => re.test(corpus)) ||
      Boolean(failureCorpus && rule.failurePatterns?.some((re) => re.test(failureCorpus))),
  );
}

export function evaluateProactiveToolRecommendations(input: {
  threadID?: string;
  threadTitle?: string;
  missionGoal?: string;
  recentMessages?: string[];
  failureHints?: string[];
  draft?: string;
  catalog: ReadonlyArray<ClassifiedToolEntry>;
}): ProactiveToolRecommendation[] {
  const corpus = [
    input.threadTitle ?? "",
    input.missionGoal ?? "",
    ...(input.recentMessages ?? []),
    input.draft ?? "",
  ]
    .filter(Boolean)
    .join("\n");
  const failureCorpus = (input.failureHints ?? []).filter(Boolean).join("\n");

  if (!corpus.trim() && !failureCorpus.trim()) return [];

  const results: ProactiveToolRecommendation[] = [];

  for (const rule of RECOMMENDATION_RULES) {
    const matchedContext = rule.patterns.some((re) => re.test(corpus));
    const matchedFailure = Boolean(
      failureCorpus && rule.failurePatterns?.some((re) => re.test(failureCorpus)),
    );
    if (!matchedContext && !matchedFailure) continue;

    if (input.threadID && isRecommendationDismissed(input.threadID, rule.id)) {
      continue;
    }

    if (rule.actionKind === "automation") {
      results.push({
        id: rule.id,
        role: rule.role,
        actionKind: "automation",
        status: "automation_candidate",
        title: rule.missingTitle,
        reason: rule.missingReason,
        draftHint: rule.draftHint(),
        suggestedSchedule: extractScheduleHint(corpus),
      });
      continue;
    }

    const matchingTools = input.catalog.filter(
      (entry) =>
        entry.roles.includes(rule.role) &&
        (entry.kind === "mcp" || entry.kind === "plugin" || entry.kind === "skill"),
    );
    const enabledTool = matchingTools.find((entry) => entry.enabled);

    // Do not nag when an appropriate tool for this role is already enabled (unless a failure hint specifically fired)
    if (enabledTool && !matchedFailure) {
      continue;
    }

    const disabledOrPendingTool = matchingTools.find((entry) => !entry.enabled);
    if (disabledOrPendingTool) {
      results.push({
        id: rule.id,
        role: rule.role,
        actionKind: "plugin_or_mcp",
        status: "disabled_installed",
        title: {
          zh: `智能体推荐：启用已安装的「${disabledOrPendingTool.name}」`,
          en: `Recommended: Enable installed "${disabledOrPendingTool.name}"`,
        },
        reason: rule.disabledReason(disabledOrPendingTool.name),
        matchedToolName: disabledOrPendingTool.name,
        draftHint: rule.draftHint(disabledOrPendingTool.name),
      });
    } else {
      results.push({
        id: rule.id,
        role: rule.role,
        actionKind: "plugin_or_mcp",
        status: "missing_tool",
        title: rule.missingTitle,
        reason: rule.missingReason,
        draftHint: rule.draftHint(),
      });
    }
  }

  return results;
}
