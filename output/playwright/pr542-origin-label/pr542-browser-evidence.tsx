import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { V2AgentBrowser } from "../src/v2/components/agent-browser";
import { LocaleProvider } from "../src/lib/locale";
import { initializePrayuAppearance } from "../src/lib/appearance";
import type { APIClient } from "../src/api/client";
import "../src/styles.css";
import "../src/v2/styles.css";

const params = new URLSearchParams(location.search);
localStorage.setItem("prayu.locale.v1", params.get("locale") || "zh-CN");
localStorage.setItem("prayu.theme", params.get("theme") || "light");
initializePrayuAppearance();

const origins = params.has("long") ? [
  "https://browser-review.preview.customer-development.example.com:8443",
  "https://identity-provider.integration.team-workspace.example.org:9443",
  "https://documentation.release-candidate.platform-engineering.example.net:8443",
] : ["https://example.org:443", "http://localhost:3000"];
const fixture = {
  version: "agent_browser_status.v1", run_id: "run-browser", session_id: "agent-browser-one", generation: 1,
  state: "ready", available: true, can_start: true, product: "edge", headless: false,
  url: "https://example.org/report", title: "导航检查", document_epoch: 2,
  last_action: "browser_navigate", updated_at: "2026-11-06T10:00:00Z",
  cleanup_pending: false, tree_reaped: false, profile_removed: false,
  declared_origins: origins,
};
const client = {
  baseURL: "/component-review-fixture",
  get: async () => structuredClone(fixture),
  postControl: async () => { throw new Error("This review fixture has no backend control endpoint"); },
} as unknown as APIClient;

document.body.style.cssText = "margin:0; min-height:100dvh; box-sizing:border-box; padding:32px 16px; background:var(--prayu-panel);";
const queries = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } });
createRoot(document.getElementById("root")!).render(
  <LocaleProvider><QueryClientProvider client={queries}>
    <V2AgentBrowser client={client} runID="run-browser" running={false} />
  </QueryClientProvider></LocaleProvider>,
);
