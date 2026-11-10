import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import { browserNetworkQueryKey, readBrowserNetwork, saveBrowserNetwork,
  type BrowserNetworkAccess, type BrowserNetworkTarget, type BrowserNetworkView } from "../../api/browser-network";
import { agentBrowserQueryKey } from "../../api/agent-browser";
import type { RunView, ThreadDetailView } from "../../api/types";
import { useLocale } from "../../lib/locale";
import { v2QueryKeys } from "../query-keys";
import "./browser-network-control.css";

export function browserNetworkRunFinished(status: RunView["status"] | undefined): boolean {
  return status === "completed" || status === "failed" || status === "cancelled";
}

export function V2BrowserNetworkSettings({ client, threadID, draftWorkspaceID = "" }: {
  client: APIClient; threadID: string; draftWorkspaceID?: string;
}) {
  const { t } = useLocale();
  const thread = useQuery({ queryKey: v2QueryKeys.thread(threadID), enabled: Boolean(threadID),
    queryFn: ({ signal }) => client.get<ThreadDetailView>(`/threads/${encodeURIComponent(threadID)}`, {}, signal) });
  const detail = thread.data?.thread.id === threadID ? thread.data : undefined;
  const run = detail?.active_run ?? detail?.last_run;
  const workspaceID = threadID ? detail?.thread.workspace_id ?? "" : draftWorkspaceID;
  const target: BrowserNetworkTarget | undefined = run && !browserNetworkRunFinished(run.status)
    ? { kind: "runs", id: run.id } : workspaceID ? { kind: "workspaces", id: workspaceID } : undefined;
  return <section className="v2-settings-section"><h2>{t("浏览器", "Browser")}</h2>
    {target ? <div className="v2-settings-card"><V2BrowserNetworkControl client={client} target={target} /></div>
      : threadID && thread.isError ? <p role="alert">{t("无法读取浏览器设置", "Could not read browser settings")}</p>
        : threadID && thread.isPending ? <p role="status">{t("正在读取浏览器设置…", "Loading browser settings…")}</p>
          : <p className="v2-settings-empty">{t("先选择一个项目。", "Select a project first.")}</p>}
  </section>;
}

export function V2BrowserNetworkControl(props: {
  client: APIClient; target: BrowserNetworkTarget; finished?: boolean;
}) {
  return <BrowserNetworkControl key={JSON.stringify([props.client.baseURL, props.target.kind, props.target.id, props.finished])} {...props} />;
}

function BrowserNetworkControl({ client, target, finished = false }: {
  client: APIClient; target: BrowserNetworkTarget; finished?: boolean;
}) {
  const { t } = useLocale();
  const queries = useQueryClient();
  const [restricted, setRestricted] = useState(false);
  const [origins, setOrigins] = useState("");
  const [formError, setFormError] = useState("");
  const query = useQuery({ queryKey: browserNetworkQueryKey(client.baseURL, target),
    queryFn: ({ signal }) => readBrowserNetwork(client, target, signal), retry: false, staleTime: 5_000 });
  const workspaceTarget: BrowserNetworkTarget | undefined = target.kind === "runs" && finished && query.data?.workspace_id
    ? { kind: "workspaces", id: query.data.workspace_id } : undefined;
  const project = useQuery({ queryKey: workspaceTarget ? browserNetworkQueryKey(client.baseURL, workspaceTarget)
    : ["browser-network-project", client.baseURL, target.kind, target.id],
    queryFn: ({ signal }) => readBrowserNetwork(client, workspaceTarget!, signal), enabled: Boolean(workspaceTarget),
    retry: false, staleTime: 5_000 });
  const effectiveTarget = workspaceTarget ?? target;
  const current = workspaceTarget ? project.data : query.data;
  const mutation = useMutation({ retry: false,
    mutationFn: ({ mode, sites }: { mode: BrowserNetworkAccess; sites: string[] }) => saveBrowserNetwork(client, effectiveTarget, {
      version: "browser_network_access.v1", expected_revision: current!.revision,
      browser_network_access: mode, browser_origins: sites,
      ...(effectiveTarget.kind === "runs" ? { remember_for_project: true } : {}),
    }, `v2-browser-network-${globalThis.crypto.randomUUID()}`),
    onSuccess: (result) => {
      queries.setQueryData(browserNetworkQueryKey(client.baseURL, effectiveTarget), result);
      if (result.project_preference) {
        queries.setQueryData<BrowserNetworkView>(browserNetworkQueryKey(client.baseURL,
          { kind: "workspaces", id: result.workspace_id }), {
          version: "browser_network_access.v1", ...result.project_preference,
        });
      }
      if (target.kind === "runs") void queries.invalidateQueries({ queryKey: agentBrowserQueryKey(client.baseURL, target.id) });
      setRestricted((result.browser_origins?.length ?? 0) > 0);
      setOrigins(result.browser_origins?.join("\n") ?? "");
      setFormError("");
    },
    onError: () => {
      void queries.invalidateQueries({ queryKey: browserNetworkQueryKey(client.baseURL, effectiveTarget) });
    },
  });
  const enabled = current !== undefined && current.browser_network_access !== "off";
  const loading = query.isPending || Boolean(workspaceTarget && project.isPending);
  const failed = query.isError || Boolean(workspaceTarget && project.isError);
  const disabled = !client.hasControl || !current || mutation.isPending;
  const save = () => {
    const sites = restricted ? [...new Set(origins.split(/[\n,]+/u).map((site) => site.trim()).filter(Boolean))] : [];
    if (restricted && sites.length === 0) {
      setFormError(t("请添加至少一个网站。", "Add at least one site.")); return;
    }
    setFormError(""); mutation.mutate({ mode: enabled ? restricted ? "restricted" : "internet" : "off", sites });
  };
  return <div className="v2-browser-network-control" aria-busy={loading || mutation.isPending}>
    <div className="v2-browser-network-row"><strong>{t("允许联网", "Allow internet")}</strong>
      <button aria-checked={enabled} aria-label={t("允许联网", "Allow internet")} className="v2-cdp-switch"
        disabled={disabled} role="switch" type="button" onClick={() => {
          const sites = current?.browser_origins ?? [];
          setFormError(""); mutation.mutate({ mode: enabled ? "off" : sites.length ? "restricted" : "internet", sites });
        }}><i aria-hidden="true" /></button>
    </div>
    {loading && <p role="status">{t("正在读取浏览器设置…", "Loading browser settings…")}</p>}
    {failed && <p role="alert">{t("无法读取浏览器设置。", "Could not read browser settings.")} <button type="button"
      onClick={() => { void (workspaceTarget ? project.refetch() : query.refetch()); }}>{t("重试", "Retry")}</button></p>}
    {current && <details onToggle={(event) => {
      if (!event.currentTarget.open) return;
      setRestricted((current.browser_origins?.length ?? 0) > 0);
      setOrigins(current.browser_origins?.join("\n") ?? ""); setFormError("");
    }}><summary>{t("高级设置", "Advanced settings")}</summary>
      <div className="v2-browser-network-advanced"><label className="v2-browser-network-restricted">
        <input type="checkbox" checked={restricted} disabled={disabled}
          onChange={(event) => setRestricted(event.target.checked)} />{t("仅指定网站", "Only specified sites")}</label>
        {restricted && <label className="v2-browser-network-sites">{t("网站白名单", "Site allowlist")}
          <textarea value={origins} disabled={disabled} rows={3} placeholder="https://example.com"
            onChange={(event) => setOrigins(event.target.value)} /></label>}
        <button type="button" disabled={disabled} onClick={save}>{t("保存", "Save")}</button>
        {formError && <p role="alert">{formError}</p>}
      </div>
    </details>}
    {mutation.isError && <p role="alert">{mutation.error instanceof Error ? mutation.error.message
      : t("无法更新浏览器设置。", "Could not update browser settings.")}</p>}
  </div>;
}
