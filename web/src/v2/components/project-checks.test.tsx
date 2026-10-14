import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import { LocaleProvider } from "../../lib/locale";
import { preparationOperation, preparationView } from "../../test/project-preparation";
import { V2RecoveryProvider } from "../recovery-storage";
import { ProjectChecks } from "./project-checks";

beforeEach(() => localStorage.clear());

it("selects the prepared subdirectory for tests only after preparation becomes ready, and resets it on Run change", async () => {
  let finish!: (value: ReturnType<typeof preparationOperation>) => void;
  const running = preparationOperation({ target: "web", state: "installing", can_cancel: true });
  const client = { baseURL: "/api/v1", hasControl: true, hasRunExecution: true,
    getProjectPreparation: vi.fn().mockImplementation((runID: string, target: string) => Promise.resolve(preparationView({ run_id: runID, target }))),
    prepareProject: vi.fn().mockResolvedValue(preparationView({ target: "web", latest: running })),
    getProjectPreparationOperation: vi.fn().mockImplementation(() => new Promise((resolve) => { finish = resolve; })),
    cancelProjectPreparation: vi.fn(),
    listProjectActions: vi.fn().mockImplementation((runID: string, target: string) => Promise.resolve({
      protocol_version: "project-actions.v1", run_id: runID, target, backend: "docker_sandboxes", capability_grant: false,
      actions: [
        { slot: "test", command_id: target === "web" ? "test.npm" : "", configured: false, available: target === "web", target_kind: "directory", reason: "" },
        { slot: "format", command_id: "", configured: false, available: false, target_kind: "go_file", reason: "" },
      ],
    })),
    runProjectAction: vi.fn().mockResolvedValue({ protocol_version: "project-actions.v1", run_id: "run-1", slot: "test", command_id: "test.npm",
      backend: "docker_sandboxes", replayed: false, job: { ...preparationOperation().job!, id: "test-job" } }),
    getProjectActionJob: vi.fn().mockResolvedValue({ protocol_version: "project-actions.v1", run_id: "run-1", slot: "test", command_id: "test.npm",
      backend: "docker_sandboxes", replayed: false, job: { ...preparationOperation().job!, id: "test-job" } }),
    stopThreadApplicationService: vi.fn(),
  };
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const ui = (runID = "run-1") => <LocaleProvider><QueryClientProvider client={queries}>
    <V2RecoveryProvider client={client} scopeID="project-checks-db">
      <ProjectChecks client={client as unknown as APIClient} runID={runID} threadID="thread-1" />
    </V2RecoveryProvider>
  </QueryClientProvider></LocaleProvider>;
  const { rerender } = render(ui());
  await screen.findByRole("button", { name: "准备项目" });
  fireEvent.change(screen.getByRole("textbox", { name: /^项目目录/ }), { target: { value: "./web/" } });
  fireEvent.click(screen.getByRole("button", { name: "检测目录" }));
  await waitFor(() => expect(client.getProjectPreparation).toHaveBeenCalledWith("run-1", "web", expect.any(AbortSignal)));
  fireEvent.click(await screen.findByRole("button", { name: "准备项目" }));
  await waitFor(() => expect(client.getProjectPreparationOperation).toHaveBeenCalledWith("run-1", "prep-1", expect.any(AbortSignal)));
  expect(screen.getByRole("textbox", { name: /测试目录/ })).toHaveValue("");
  await act(async () => finish(preparationOperation({ target: "web" })));
  await waitFor(() => expect(screen.getByRole("textbox", { name: /测试目录/ })).toHaveValue("web"));
  await waitFor(() => expect(screen.getByRole("button", { name: "运行测试" })).toBeEnabled());
  expect(client.listProjectActions).toHaveBeenCalledWith("run-1", "web", expect.any(AbortSignal));
  fireEvent.click(screen.getByRole("button", { name: "运行测试" }));
  await waitFor(() => expect(client.runProjectAction).toHaveBeenCalledWith("run-1", "test", "web", expect.any(String)));
  rerender(ui("run-2"));
  expect(screen.getByRole("textbox", { name: /测试目录/ })).toHaveValue("");
  await waitFor(() => expect(client.listProjectActions).toHaveBeenCalledWith("run-2", ".", expect.any(AbortSignal)));
  expect(screen.getByRole("button", { name: "运行测试" })).toBeDisabled();
});
