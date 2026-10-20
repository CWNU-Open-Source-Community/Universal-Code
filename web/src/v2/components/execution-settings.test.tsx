import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { RunDetailView, ThreadDetailView } from "../../api/types";
import { capabilityReadinessFixture, patchCapabilityReadiness } from "../../test/capability-readiness";
import { V2ExecutionSettings } from "./execution-settings";

function runDetail(id = "run-1"): RunDetailView {
  return {
    run: { id, status: "paused", standard_code_preset_configured: false },
    mission: { id: "mission-1", workspace_id: "workspace-1" },
    mode: { surface: "code", phase: "deliver" },
    execution_permission: { mode: "ask" },
    execution_profile: { profile: "preview", backend: "noop", risk_tier: "minimal",
      approval_policy: "none", required_gate: "none", revision: 1 },
    execution_interaction: { mode: "preview", workspace_trust: "untrusted", command_form: "none",
      execution_profile: "preview", required_gate: "none", revision: 1 },
  } as RunDetailView;
}

function setup(initial = runDetail()) {
  let value = initial;
  let readiness = capabilityReadinessFixture(value.run.id);
  const get = vi.fn(async (path: string) => {
    if (path.startsWith("/threads/")) return {
      thread: { id: path.split("/").at(-1), workspace_id: "workspace-1" },
      active_run: value.run, last_run: value.run,
    } as ThreadDetailView;
    return value;
  });
  const runCapabilityReadiness = vi.fn(async () => readiness);
  const postControl = vi.fn(async (path: string, body: { profile?: string; mode?: string }) => {
    if (path.endsWith("/execution-profile")) {
      value = { ...value, execution_profile: { ...value.execution_profile,
        profile: "local", backend: "local", revision: 2 },
      execution_interaction: { ...value.execution_interaction, execution_profile: "local" } };
      readiness = patchCapabilityReadiness(readiness, "profiles", "preview", { selected: false });
      readiness = patchCapabilityReadiness(readiness, "profiles", "local", { selected: true });
      return { execution_profile: value.execution_profile, replayed: false };
    }
    value = { ...value, execution_interaction: { ...value.execution_interaction,
      mode: body.mode as "controlled", workspace_trust: "trusted", revision: 2 } };
    return { execution_interaction: value.execution_interaction, replayed: false };
  });
  const client = { get, runCapabilityReadiness, postControl } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const ui = (threadID: string) => <QueryClientProvider client={queryClient}>
    <V2ExecutionSettings client={client} threadID={threadID}
      workspaces={[{ id: "workspace-1", name: "指定验收目录", created_at: "2026-09-10T00:00:00Z" }]} />
  </QueryClientProvider>;
  return { get, runCapabilityReadiness, postControl, queryClient, ui,
    useRun: (next: RunDetailView) => { value = next; readiness = capabilityReadinessFixture(next.run.id); },
    blockLocal: () => { readiness = patchCapabilityReadiness(readiness, "profiles", "local",
      { selectable: false, blocked_by: ["run_not_quiescent"], remediation: ["pause_run"] }); } };
}

describe("V2ExecutionSettings", () => {
  it("opens existing local controls and reads back the result without skipping trust confirmation", async () => {
    const user = userEvent.setup();
    const controls = setup();
    render(controls.ui("thread-1"));
    expect(await screen.findByText("项目：指定验收目录")).toBeInTheDocument();
    const picker = screen.getByRole("group", { name: "编码环境后端" });
    expect(within(picker).getAllByRole("button")).toHaveLength(3);
    expect(within(picker).getByRole("button", { name: /^Local/u })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText(/审批偏好决定操作如何获批/u)).toBeInTheDocument();
    expect(controls.postControl).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: /本地工作区/u }));
    await waitFor(() => expect(controls.postControl).toHaveBeenCalledWith(
      "/runs/run-1/execution-profile", { profile: "local", reason: "settings execution profile selection" },
      expect.stringMatching(/^settings-execution-profile-/u),
    ));
    await waitFor(() => expect(controls.get.mock.calls.filter(([path]) => path === "/runs/run-1").length).toBeGreaterThan(1));
    await user.click(screen.getByRole("button", { name: /^Code/u }));
    expect(controls.postControl).toHaveBeenCalledTimes(1);
    const confirmation = screen.getByRole("alert");
    await user.click(within(confirmation).getByRole("button", { name: "确认" }));
    await waitFor(() => expect(controls.postControl).toHaveBeenCalledWith(
      "/runs/run-1/execution-interaction",
      { mode: "controlled", trust: "trusted", confirm_workspace_trust: true,
        reason: "settings execution interaction selection" },
      expect.stringMatching(/^settings-execution-interaction-/u),
    ));
    await waitFor(() => expect(controls.queryClient.getQueryData<RunDetailView>(["run", "run-1"])
      ?.execution_interaction.workspace_trust).toBe("trusted"));
  });

  it("preserves server disabled states and changes controls to the selected conversation", async () => {
    const user = userEvent.setup();
    const controls = setup();
    controls.blockLocal();
    const view = render(controls.ui("thread-1"));
    expect(await screen.findByRole("button", { name: /本地工作区/u })).toBeDisabled();
    controls.useRun(runDetail("run-2"));
    view.rerender(controls.ui("thread-2"));
    await waitFor(() => expect(screen.getByRole("button", { name: /本地工作区/u })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: /本地工作区/u }));
    await waitFor(() => expect(controls.postControl.mock.calls[0]?.[0]).toBe("/runs/run-2/execution-profile"));
  });

  it("makes no requests without a conversation and distinguishes an isolated workspace", async () => {
    const initial = runDetail();
    initial.run.standard_code_preset_configured = true;
    const controls = setup(initial);
    const view = render(controls.ui(""));
    expect(screen.getByText("先打开一个对话，再选择执行环境。")).toBeInTheDocument();
    expect(controls.get).not.toHaveBeenCalled();
    view.rerender(controls.ui("thread-1"));
    expect(await screen.findByText(/文件编辑与沙箱命令都在该目录中进行/u)).toBeInTheDocument();
    expect(screen.queryByText(/普通任务的文件工具使用已接入的项目目录/u)).not.toBeInTheDocument();
  });
});

function render(ui: ReactElement, options?: RenderOptions) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
