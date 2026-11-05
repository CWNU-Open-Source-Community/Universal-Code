import { ArrowLeft } from "lucide-react";
import { useLocale } from "../../lib/locale";
import type { APIClient } from "../../api/client";
import { ScheduledTasksWorkspace } from "../../components/scheduled-tasks-workspace";
import "./resource-page.css";

export function V2AutomationsPage({ client, initialRunID, initialThreadID, onBack }: {
  client: APIClient;
  initialRunID?: string;
  initialThreadID?: string;
  onBack: () => void;
}) {
  const { t } = useLocale();
  return <section className="v2-resource-page">
    <header><button onClick={onBack} type="button"><ArrowLeft aria-hidden="true" size={16} />{t("返回", "Back")}</button>
      <strong>{t("自动化", "Automations")}</strong></header>
    <div className="v2-resource-body">
      <ScheduledTasksWorkspace client={client} initialRunID={initialRunID} initialThreadID={initialThreadID} />
    </div>
  </section>;
}
