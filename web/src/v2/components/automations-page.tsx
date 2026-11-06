import type { APIClient } from "../../api/client";
import { ScheduledTasksWorkspace } from "../../components/scheduled-tasks-workspace";
import "./resource-page.css";

export function V2AutomationsPage({ client, initialRunID, initialThreadID }: {
  client: APIClient;
  initialRunID?: string;
  initialThreadID?: string;
}) {
  return <section className="v2-resource-page">
    <div className="v2-resource-body">
      <ScheduledTasksWorkspace client={client} initialRunID={initialRunID} initialThreadID={initialThreadID} />
    </div>
  </section>;
}
