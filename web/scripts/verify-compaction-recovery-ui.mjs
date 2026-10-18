// Focused browser acceptance of the real component and production CSS, with local API fixtures.
// Run from web/: node scripts/verify-compaction-recovery-ui.mjs [--serve]
import { mkdir, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { chromium, expect } from "@playwright/test";

const webRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const artifacts = resolve(webRoot, "../output/playwright/compaction-recovery-ui");
const fixturePath = "/__compaction-recovery-fixture.tsx";
const fixture = `
import React from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { V2ThreadCompactControl } from "/src/v2/components/thread-context-controls";
import "/src/styles.css";
import "/src/v2/styles.css";
import "/src/v2/components/thread-context.css";
const params = new URLSearchParams(location.search);
document.documentElement.dataset.prayuTheme = params.get("theme") || "dark";
const now = "2026-10-18T07:10:00Z";
const result = { version: "thread_context_compaction.v1", thread_id: "thread-1", run_id: "previous-run", session_id: "session-1",
  status: "completed", summary_id: 12, removed_messages: 38, preserved_messages: 6, source_sha256: "a".repeat(64), generated: true, replayed: false };
const operation = (id, status) => ({ operation_id: id, run_id: "previous-run", session_id: "session-1", status,
  source_sha256: "a".repeat(64), provider: "openai", model: "gpt-5.4", created_at: now, usage_known: status !== "started" });
const started = params.get("state") === "started";
let operations = [operation("saved-summary-2026-10-18", started ? "started" : "generated"),
  { ...operation("previous-failed-operation", "failed"), error_message: "对话内容已有变化，未采用生成的摘要。请重新核对压缩记录。", terminal_at: now },
  { ...operation("previous-completed-operation", "completed"), result, terminal_at: now }];
const checks = { reads: [], writes: [] };
window.__compactionChecks = checks;
const client = { baseURL: "/fixture", hasControl: true,
  async get(path) {
    checks.reads.push(path);
    return path.endsWith("/compactions")
      ? { version: "thread_context_compaction_operations.v1", thread_id: "thread-1", operations }
      : { version: "thread_context_pins.v1", thread_id: "thread-1", revision: 0, pins: [], proposals: [], busy: started, effective_from: "next_request" };
  },
  async postControl(path, body, key) {
    checks.writes.push({ path, body, key });
    const previous = operations[0];
    const next = body.action === "resume" ? { ...previous, status: "completed", result, terminal_at: now }
      : { ...previous, status: "failed", terminal_at: now, error_message: "已放弃采用本次结果。" };
    operations = [next, ...operations.slice(1)]; return next;
  }
};
const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
createRoot(document.getElementById("root")).render(<QueryClientProvider client={queries}>
  <div className="v2-context-overlay"><section className="v2-thread-context" role="dialog" aria-label="任务上下文">
    <header><h2>任务上下文</h2><button type="button" aria-label="关闭任务上下文">×</button></header>
    <p className="v2-context-help">检查压缩记录，继续采用已保存的摘要或放弃未完成的操作。</p>
    <section><h3>摘要与压缩</h3><p>此会话已保存压缩摘要，历史原文可在对话记录中查看。</p>
      <V2ThreadCompactControl client={client} threadID="thread-1" runID="current-run" />
    </section>
  </section></div>
</QueryClientProvider>);
`;

let vite;
vite = await createServer({ root: webRoot, server: { host: "127.0.0.1", port: 0, strictPort: false },
  plugins: [{ name: "compaction-recovery-acceptance", resolveId(id) { if (id === fixturePath) return id; },
    load(id) { if (id === fixturePath) return fixture; },
    configureServer(server) {
      server.middlewares.use(async (req, res, next) => {
        if (req.url?.split("?")[0] !== "/__compaction-recovery") return next();
        res.setHeader("Content-Type", "text/html; charset=utf-8");
        res.end(await server.transformIndexHtml(req.url, '<!doctype html><html lang="zh-CN"><head><meta name="viewport" content="width=device-width, initial-scale=1"></head><body><div id="root"></div><script type="module" src="' + fixturePath + '"></script></body></html>'));
      });
    } }] });
await vite.listen();
const url = vite.resolvedUrls.local[0] + "__compaction-recovery";
if (process.argv.includes("--serve")) {
  console.log(url);
  await new Promise((done) => { process.once("SIGINT", done); process.once("SIGTERM", done); });
  await vite.close();
  process.exit(0);
}

await mkdir(artifacts, { recursive: true });
const browser = await chromium.launch({ channel: "msedge", headless: true });
const errors = [];
const receipts = [];
try {
  for (const [name, width, height, theme, state] of [
    ["desktop-dark-generated", 1280, 900, "dark", "generated"],
    ["mobile-light-generated", 390, 844, "light", "generated"],
    ["mobile-dark-started", 390, 844, "dark", "started"],
  ]) {
    const page = await browser.newPage({ viewport: { width, height }, deviceScaleFactor: 1, locale: "zh-CN" });
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(url + "?theme=" + theme + "&state=" + state);
    await expect(page.getByRole("button", { name: "放弃本次压缩" })).toBeEnabled();
    await expect(page.getByRole("button", { name: "压缩当前对话", exact: true })).toBeDisabled();
    await page.evaluate(() => document.fonts.ready);
    const overflow = await page.locator(".v2-thread-context").evaluate((panel) => ({ scroll: panel.scrollWidth, client: panel.clientWidth }));
    expect(overflow.scroll).toBeLessThanOrEqual(overflow.client);
    await page.screenshot({ path: resolve(artifacts, name + ".png"), fullPage: true, scale: "css" });
    if (state === "generated") {
      await page.getByRole("button", { name: "恢复已生成摘要" }).click();
      await expect(page.getByRole("status")).toHaveText("已压缩 38 条较早消息，保留 6 条近期消息。");
      await expect(page.getByRole("button", { name: "压缩当前对话", exact: true })).toBeEnabled();
    } else {
      await expect(page.getByText("放弃将停止采用本次结果；已发出的模型请求仍可能计费。")).toBeVisible();
      await page.getByRole("button", { name: "放弃本次压缩" }).click();
      await expect(page.getByText("已停止采用本次结果。", { exact: true })).toBeVisible();
      await expect(page.getByRole("button", { name: "压缩当前对话", exact: true })).toBeDisabled();
    }
    const checks = await page.evaluate(() => window.__compactionChecks);
    expect(checks.writes).toHaveLength(1);
    expect(checks.writes[0].path).toBe("/threads/thread-1/context/compactions/saved-summary-2026-10-18/recover");
    expect(checks.writes[0].body.expected_run_id).toBe("previous-run");
    expect(checks.writes[0].body.action).toBe(state === "generated" ? "resume" : "discard");
    receipts.push({ name, width, height, theme, overflow, ...checks });
    await page.close();
  }
  expect(errors).toEqual([]);
  await writeFile(resolve(artifacts, "results.json"), JSON.stringify({ receipts, errors }, null, 2));
  console.log("Browser acceptance passed: generated resume, busy-task discard, desktop/mobile themes, no horizontal overflow.");
  console.log(artifacts);
} finally {
  await browser.close();
  await vite.close();
}
