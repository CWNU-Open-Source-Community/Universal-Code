import { useLocale } from "../../lib/locale";
import type { APIClient } from "../../api/client";
import { SessionWorkspace } from "../../components/session-workspace";
import { WorkbenchFrame } from "../../components/workbench-frame";
import { desktopBridgeAvailable } from "../../lib/desktop-bridge";
import type { V2SettingsSection } from "./sidebar";
import { SourceResourcePage } from "./source-resource-page";

export function V2SessionDetailsPage({ client, resourceID, threadID, onBack, onOpenSettings }: {
  client: APIClient;
  resourceID: string;
  threadID: string;
  onBack: () => void;
  onOpenSettings: (section: V2SettingsSection) => void;
}) {
  const { t } = useLocale();
  const title = t("会话上下文", "Session context");
  return <SourceResourcePage client={client} title={title} resourceID={resourceID} threadID={threadID}
    onBack={onBack} onOpenSettings={onOpenSettings}>
    <WorkbenchFrame client={client} desktop={desktopBridgeAvailable()} title={title}
      resourceKind="session" runID="" sessionID={resourceID}>
      <SessionWorkspace client={client} key={`session:${resourceID}`} sessionID={resourceID}
        onOpenPlugins={() => onOpenSettings("extensions")} />
    </WorkbenchFrame>
  </SourceResourcePage>;
}
