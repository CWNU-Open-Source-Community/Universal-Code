import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import type { APIClient } from "../../api/client";
import { LocaleProvider } from "../../lib/locale";
import { TaskConfigurationSettings } from "./task-configuration-settings";

const configuration = { version: "task_configuration.v1", workspace_id: "workspace-source", profile: "code",
  requested_budget: { max_turns: 20, max_tool_calls: 30 }, budget: { max_turns: 20, max_tool_calls: 30 },
  sources: [], project_disposition: "absent", rejections: [], fingerprint: "a".repeat(64), capability_grant: false };
function fixture() {
  return { hasThreadControl: true, get: vi.fn().mockResolvedValue({ thread: { id: "thread-source", workspace_id: "workspace-source" },
    active_run: { id: "run-current" }, last_run: { id: "run-current" }, runs: [{ run: { id: "run-history" } }, { run: { id: "run-current" } }] }),
    getRunTaskConfiguration: vi.fn().mockResolvedValue(configuration), previewTaskConfiguration: vi.fn() };
}
function mount(client: ReturnType<typeof fixture>, sourceRunID = "run-history", threadID = "thread-source") {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <LocaleProvider><TaskConfigurationSettings client={client as unknown as APIClient} threadID={threadID} sourceRunID={sourceRunID}
      draftWorkspaceID="workspace-unrelated" draftBudget={{ max_turns: 5 }} onDraftBudgetChange={vi.fn()} /></LocaleProvider>
  </QueryClientProvider>);
}

it("reads the explicit historical Run snapshot without draft controls or live project preview", async () => {
  const client = fixture();
  mount(client);
  await screen.findByText("已保存的执行上限");
  expect(screen.getAllByRole("heading", { name: "任务预算与项目配置" })).toHaveLength(1);
  expect(screen.getByRole("heading", { name: "本次执行配置", level: 2 })).toBeInTheDocument();
  expect(client.getRunTaskConfiguration).toHaveBeenCalledWith("run-history", expect.any(AbortSignal));
  expect(client.previewTaskConfiguration).not.toHaveBeenCalled();
  expect(screen.queryByRole("spinbutton")).not.toBeInTheDocument();
});

it("keeps a missing requested Run unavailable instead of reading the current Run or draft project", async () => {
  const client = fixture();
  mount(client, "run-missing");
  expect(await screen.findByRole("alert")).toHaveTextContent("所选执行或其工作区尚未找到");
  expect(client.getRunTaskConfiguration).not.toHaveBeenCalled();
  expect(client.previewTaskConfiguration).not.toHaveBeenCalled();
  expect(screen.queryByRole("spinbutton")).not.toBeInTheDocument();
});

it("leaves a snapshot read failure explicit without falling back to the current project configuration", async () => {
  const client = fixture();
  client.getRunTaskConfiguration.mockRejectedValue(new Error("固定配置暂不可用"));
  mount(client);
  expect(await screen.findByRole("alert")).toHaveTextContent("固定配置暂不可用");
  expect(client.previewTaskConfiguration).not.toHaveBeenCalled();
  expect(screen.queryByRole("spinbutton")).not.toBeInTheDocument();
});

it("reads an unbound historical Run only after checking that Run's own mission and workspace", async () => {
  const client = fixture();
  client.get.mockResolvedValue({ run: { id: "run-history", mission_id: "mission-source" }, mission: { id: "mission-source", workspace_id: "workspace-source" } } as never);
  mount(client, "run-history", "");
  await screen.findByText("已保存的执行上限");
  expect(client.get).toHaveBeenCalledWith("/runs/run-history", {}, expect.any(AbortSignal));
  expect(client.getRunTaskConfiguration).toHaveBeenCalledWith("run-history", expect.any(AbortSignal));
  expect(client.previewTaskConfiguration).not.toHaveBeenCalled();
});
