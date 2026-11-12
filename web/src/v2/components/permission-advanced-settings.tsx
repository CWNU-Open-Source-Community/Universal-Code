import { useCallback, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import type { WorkspaceView } from "../../api/types";
import { useLocale } from "../../lib/locale";
import { v2QueryKeys } from "../query-keys";
import { V2BrowserNetworkSettings } from "./browser-network-control";
import { V2CommandServiceSettings } from "./command-service-settings";
import { V2ExecutionSettings } from "./execution-settings";
import { V2RunNetworkAuthorityControl } from "./run-network-authority-control";
import { V2RuntimeCapabilityControl } from "./runtime-capability-control";

type AdvancedPermissionProps = {
  client: APIClient;
  threadID: string;
  workspaces: WorkspaceView[];
  draftWorkspaceID?: string;
  onOpenModelSettings: () => void;
};

export function V2AdvancedPermissionSettings(props: AdvancedPermissionProps) {
  return <AdvancedPermissionSettings key={props.threadID} {...props} />;
}

function AdvancedPermissionSettings({ client, threadID, workspaces, draftWorkspaceID,
  onOpenModelSettings }: AdvancedPermissionProps) {
  const { t } = useLocale();
  const [open, setOpen] = useState(false);
  const confirmationOpen = useRef(false);
  const onConfirmationOpenChange = useCallback((value: boolean) => {
    confirmationOpen.current = value;
  }, []);
  const permission = useQuery({
    queryKey: v2QueryKeys.permission(threadID),
    queryFn: ({ signal }) => client.getThreadExecutionPermission(threadID, signal),
    enabled: open && Boolean(threadID), staleTime: 10_000,
  });
  const runID = permission.data?.current_run_id ?? "";
  return <details className="v2-permission-advanced" onToggle={(event) => {
    if (!event.currentTarget.open && confirmationOpen.current) {
      event.currentTarget.open = true;
      return;
    }
    setOpen(event.currentTarget.open);
  }}>
    <summary>{t("高级设置", "Advanced settings")}</summary>
    {open && <>
      <V2ExecutionSettings client={client} threadID={threadID} workspaces={workspaces} />
      <section className="v2-settings-section"><h2>{t("网页与搜索限制", "Web and search restrictions")}</h2>
        {threadID && permission.isError ? <p role="alert">{t("无法读取当前任务的网络设置。", "Could not read this task's network settings.")}
          <button type="button" onClick={() => { void permission.refetch(); }}>{t("重试", "Retry")}</button></p>
          : threadID && permission.isPending ? <p role="status">{t("正在读取网络设置…", "Loading network settings…")}</p>
            : <V2RunNetworkAuthorityControl key={`${threadID}:${runID}`} client={client} threadID={threadID}
              runID={runID} onOpenModelSettings={onOpenModelSettings}
              onConfirmationOpenChange={onConfirmationOpenChange} />}
      </section>
      <V2BrowserNetworkSettings client={client} threadID={threadID} draftWorkspaceID={draftWorkspaceID} />
      <V2CommandServiceSettings client={client} threadID={threadID} />
      <section className="v2-settings-section"><V2RuntimeCapabilityControl /></section>
    </>}
  </details>;
}
