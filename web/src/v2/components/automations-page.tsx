import type { APIClient } from "../../api/client";
import { ScheduledTasksWorkspace } from "../../components/scheduled-tasks-workspace";
import "./secondary-page.css";
import "./automations-page.css";

export function V2AutomationsPage({ client, initialRunID, initialThreadID }: {
  client: APIClient;
  initialRunID?: string;
  initialThreadID?: string;
}) {
  return <main className="v2-automations-page v2-page-scroll">
    <div className="v2-page-column">
      <ScheduledTasksWorkspace client={client} initialRunID={initialRunID} initialThreadID={initialThreadID} />
    </div>
  </main>;
}
