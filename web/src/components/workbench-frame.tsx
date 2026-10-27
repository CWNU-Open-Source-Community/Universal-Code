import type { ReactNode } from "react";
import type { APIClient } from "../api/client";
import { WorkbenchDock, type WorkbenchResourceKind } from "./workbench-dock";

export function WorkbenchFrame({ title, children, client, desktop, resourceKind, runID,
  sessionID, threadID = "" }: {
  title: string;
  children: ReactNode;
  client: APIClient;
  desktop: boolean;
  resourceKind: WorkbenchResourceKind;
  runID: string;
  sessionID: string;
  threadID?: string;
}) {
  return <WorkbenchDock client={client} desktop={desktop} resourceKind={resourceKind}
    runID={runID} sessionID={sessionID} threadID={threadID} title={title}>
    {children}
  </WorkbenchDock>;
}
