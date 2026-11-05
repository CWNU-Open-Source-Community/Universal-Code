import { cleanup, render, screen } from "@testing-library/react";
import type { APIClient } from "../../api/client";
import { LocaleProvider } from "../../lib/locale";
import { V2AutomationsPage } from "./automations-page";

const scheduledWorkspace = vi.hoisted(() => vi.fn());
vi.mock("../../components/scheduled-tasks-workspace", () => ({
  ScheduledTasksWorkspace: (props: { client: APIClient; initialRunID?: string; initialThreadID?: string }) => {
    scheduledWorkspace(props);
    return <div aria-label="Scheduled tasks workspace" />;
  },
}));

afterEach(() => { cleanup(); scheduledWorkspace.mockClear(); });

it("opens the independent automation workspace with the exact initial run and conversation", () => {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const threadExecution = vi.fn();
  const client = { hasThreadExecutionRead: true, threadExecution } as unknown as APIClient;
  const view = render(<LocaleProvider><V2AutomationsPage client={client} initialRunID="run-exact"
    initialThreadID="thread-exact" /></LocaleProvider>);
  expect(scheduledWorkspace).toHaveBeenLastCalledWith({ client, initialRunID: "run-exact", initialThreadID: "thread-exact" });
  expect(screen.getByLabelText("Scheduled tasks workspace")).toBeVisible();
  expect(screen.queryByText("来源与设置")).not.toBeInTheDocument();
  expect(threadExecution).not.toHaveBeenCalled();
  view.rerender(<LocaleProvider><V2AutomationsPage client={client} /></LocaleProvider>);
  expect(scheduledWorkspace).toHaveBeenLastCalledWith({ client, initialRunID: undefined, initialThreadID: undefined });
});
