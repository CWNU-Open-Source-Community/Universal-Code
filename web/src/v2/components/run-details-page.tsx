import { useLocale } from "../../lib/locale";
import type { APIClient } from "../../api/client";
import { RunWorkspace } from "../../components/run-workspace";
import { WorkbenchFrame } from "../../components/workbench-frame";
import { desktopBridgeAvailable } from "../../lib/desktop-bridge";
import type { V2RunPane } from "../navigation";
import type { V2SettingsSection } from "./sidebar";
import { SourceResourcePage } from "./source-resource-page";

export function V2RunDetailsPage({ client, resourceID, pane, threadID, onBack, onOpenSettings, onOpenRun }: {
  client: APIClient;
  resourceID: string;
  pane?: V2RunPane;
  threadID: string;
  onBack: () => void;
  onOpenSettings: (section: V2SettingsSection) => void;
  onOpenRun?: (runID: string) => void;
}) {
  const { t } = useLocale();
  const title = pane === "context" ? t("续接与记忆", "Continuation and memory") : pane === "checkpoints" ? t("工作区恢复", "Workspace recovery")
    : pane === "ui-evidence" ? t("界面观察证据", "UI observation evidence") : t("运行与工具", "Runs and tools");
  return <SourceResourcePage client={client} title={title} resourceID={resourceID} threadID={threadID}
    onBack={onBack} onOpenSettings={onOpenSettings}>
    <WorkbenchFrame client={client} desktop={desktopBridgeAvailable()} title={title}
      resourceKind="run" runID={resourceID} sessionID="">
      <RunWorkspace client={client} key={`run:${resourceID}:${pane ?? "activity"}`} runID={resourceID}
        initialTab={pane} onOpenPlugins={() => onOpenSettings("extensions")} onOpenRun={onOpenRun} />
    </WorkbenchFrame>
  </SourceResourcePage>;
}
