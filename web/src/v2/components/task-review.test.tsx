import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { createRef } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { ThreadDetailView } from "../../api/types";
import { standardCodeDeliveryFixture } from "../../test/standard-code-delivery";
import { capabilityReadinessFixture, patchCapabilityReadiness } from "../../test/capability-readiness";
import { sandboxEnvironmentFixture } from "../../test/sandbox-environment";
import { V2TaskReview } from "./task-review";
import { V2ExecutionSettings } from "./execution-settings";

vi.mock("../../components/workspace-checkpoint-panel", () => ({ WorkspaceCheckpointPanel: () => <div>Checkpoint timeline</div> }));
vi.mock("../../components/code-handoff-panel", () => ({ CodeHandoffPanel: ({ runID }: { runID: string }) => <div>Ordinary Code handoff {runID}</div> }));

async function openHistory(user: ReturnType<typeof userEvent.setup>, label: string) {
  const toggle = screen.getByRole("button", { name: "记录与恢复" });
  if (toggle.getAttribute("aria-expanded") !== "true") await user.click(toggle);
  await user.click(screen.getByRole("button", { name: label }));
}

it("reviews each task execution and carries exact edit context into a correction", async () => {
  const edit = { id: "edit-1", path: "README.md", operation: "replace", status: "applied",
    original_hash: "a".repeat(64), proposed_hash: "b".repeat(64), allowed_actions: [],
    diff: "--- README.md\n+++ README.md\n-old\n+new", updated_at: "2026-09-08T00:00:00Z" };
  const fileEditQueue = vi.fn().mockResolvedValue({ items: [edit], apply_enabled: false });
  const client = { fileEditQueue, fileEditChangeSet: vi.fn().mockResolvedValue({
    applied_count: 1, returned_count: 1, proposed_count: 0, approved_count: 0, denied_count: 0,
    failed_count: 0, total_diff_bytes: 24,
  }) } as unknown as APIClient;
  const detail = { thread: { id: "thread-1", title: "检查流程", workspace_id: "workspace-1" },
    active_run: { id: "run-2", status: "running" }, last_run: { id: "run-2", status: "running" },
    runs: [{ ordinal: 1, run: { id: "run-1", status: "completed" } },
      { ordinal: 2, run: { id: "run-2", status: "running" } }],
  } as unknown as ThreadDetailView;
  const onRequestChange = vi.fn();
  const onClose = vi.fn();
  const user = userEvent.setup();
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <V2TaskReview client={client} detail={detail} working={false} onClose={onClose}
      onRequestChange={onRequestChange} returnFocusRef={createRef()} />
  </QueryClientProvider>);
  expect(within(screen.getByRole("group", { name: "交付流程" })).getAllByRole("button")).toHaveLength(5);
  expect(screen.getByRole("button", { name: "检查与交付" })).toBeVisible();
  expect(screen.queryByRole("button", { name: "编辑明细" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "查看改动" })).toHaveAttribute("aria-pressed", "true");
  await openHistory(user, "编辑明细");
  await waitFor(() => expect(fileEditQueue).toHaveBeenCalledWith("run-2", expect.any(AbortSignal)));
  await user.selectOptions(screen.getByRole("combobox", { name: "选择审阅的执行记录" }), "run-1");
  await waitFor(() => expect(fileEditQueue).toHaveBeenCalledWith("run-1", expect.any(AbortSignal)));
  await user.click(await screen.findByRole("button", { name: /README.md/ }));
  await user.click(screen.getByRole("button", { name: "引用此差异请求修改" }));
  expect(onRequestChange).toHaveBeenCalledWith(expect.stringContaining("执行记录：run-1"));
  expect(onRequestChange).toHaveBeenCalledWith(expect.stringContaining("编辑：edit-1"));
  expect(onRequestChange).toHaveBeenCalledWith(expect.stringContaining(edit.proposed_hash));
  await user.click(screen.getByRole("button", { name: "返回任务改动" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "查看改动" })).toHaveFocus());
  expect(screen.queryByRole("combobox", { name: "选择审阅的执行记录" })).not.toBeInTheDocument();
  await openHistory(user, "编辑明细");
  expect(screen.getByRole("combobox", { name: "选择审阅的执行记录" })).toHaveValue("run-1");
  await user.keyboard("{Escape}");
  expect(onClose).toHaveBeenCalledOnce();
});

it("keeps lifecycle retries bound to the original execution when another execution settles late", async () => {
  let finishResume!: (value: unknown) => void;
  const controlRunLifecycle = vi.fn((runID: string) => runID === "run-1"
    ? new Promise((resolve) => { finishResume = resolve; }) : Promise.reject(new Error("pause response lost")));
  const client = { controlRunLifecycle, hasRunLifecycle: true,
    fileEditQueue: vi.fn().mockResolvedValue({ items: [], apply_enabled: false }),
    fileEditChangeSet: vi.fn().mockResolvedValue({}) } as unknown as APIClient;
  const detail = { thread: { id: "thread-1", title: "检查流程", workspace_id: "workspace-1" },
    active_run: { id: "run-2", status: "running" }, last_run: { id: "run-2", status: "running" },
    runs: [{ ordinal: 1, run: { id: "run-1", status: "paused" } },
      { ordinal: 2, run: { id: "run-2", status: "running" } }],
  } as unknown as ThreadDetailView;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const invalidate = vi.spyOn(queryClient, "invalidateQueries");
  const user = userEvent.setup();
  render(<QueryClientProvider client={queryClient}>
    <V2TaskReview client={client} detail={detail} working={false} onClose={vi.fn()}
      onRequestChange={vi.fn()} returnFocusRef={createRef()} />
  </QueryClientProvider>);
  await openHistory(user, "撤销与恢复");
  await user.selectOptions(screen.getByRole("combobox", { name: "选择审阅的执行记录" }), "run-1");
  await user.click(screen.getByRole("button", { name: "恢复此执行" }));
  await user.selectOptions(screen.getByRole("combobox", { name: "选择审阅的执行记录" }), "run-2");
  await user.click(screen.getByRole("button", { name: "暂停此执行以预览撤销" }));
  await screen.findByText(/pause response lost/);
  invalidate.mockClear();
  await act(async () => finishResume({}));
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["run", "run-1"] });
  expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["run", "run-2"] });
  await user.click(screen.getByRole("button", { name: "暂停此执行以预览撤销" }));
  await waitFor(() => expect(controlRunLifecycle.mock.calls.filter(([runID]) => runID === "run-2")).toHaveLength(2));
  const retries = controlRunLifecycle.mock.calls.filter(([runID]) => runID === "run-2");
  expect(retries[1]).toEqual(retries[0]);
});

function reportDetail(): ThreadDetailView {
  return { thread: { id: "thread-1", title: "报告身份", workspace_id: "workspace-1" },
    active_run: { id: "run-2", status: "running", standard_code_preset_configured: true },
    last_run: { id: "run-2", status: "running", standard_code_preset_configured: true },
    runs: [{ ordinal: 1, run: { id: "run-1", status: "completed", standard_code_preset_configured: true } },
      { ordinal: 2, run: { id: "run-2", status: "running", standard_code_preset_configured: true } }],
  } as unknown as ThreadDetailView;
}
function reportFor(runID: string) {
  const report = standardCodeDeliveryFixture();
  return { ...report, binding: { ...report.binding, run_id: runID },
    observation: { observed_at: "2026-09-08T02:30:00Z", revision_sha256: report.final_checkpoint.revision_sha256 } };
}
function reportClient(recordStandardCodeDelivery: ReturnType<typeof vi.fn>) {
  return { recordStandardCodeDelivery, hasWorkspaceCheckpointControl: true, hasStandardCodePreset: true,
    standardCodeDelivery: vi.fn((runID: string) => Promise.resolve(reportFor(runID))),
    fileEditQueue: vi.fn().mockResolvedValue({ items: [], apply_enabled: false }),
    fileEditChangeSet: vi.fn().mockResolvedValue({}) };
}
function renderReview(client: APIClient, queryClient: QueryClient, detail = reportDetail()) {
  return render(<QueryClientProvider client={queryClient}><V2TaskReview client={client} detail={detail}
    working={false} onClose={vi.fn()} onRequestChange={vi.fn()} returnFocusRef={createRef()} /></QueryClientProvider>);
}

it("offers only original-request confirmation while a configured run's report result is unknown", async () => {
  const record = vi.fn().mockRejectedValueOnce(new Error("report response lost"))
    .mockResolvedValueOnce({ report: reportFor("run-2"), replayed: true });
  const client = reportClient(record);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const user = userEvent.setup();
  renderReview(client as unknown as APIClient, queryClient);
  await openHistory(user, "检查与交付");
  await user.click(screen.getByRole("button", { name: "生成当前交付报告" }));
  await screen.findByText(/report response lost/);
  expect(screen.queryByRole("button", { name: "生成当前交付报告" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "确认上次报告" })).toBeEnabled();
  expect(screen.getByText(/请先点击“确认上次报告”/)).toBeInTheDocument();
  const original = record.mock.calls[0];
  await user.click(screen.getByRole("button", { name: "确认上次报告" }));
  await waitFor(() => expect(record).toHaveBeenCalledTimes(2));
  expect(record.mock.calls[1]).toEqual(original);
  await waitFor(() => expect(screen.queryByRole("button", { name: "确认上次报告" })).not.toBeInTheDocument());
  expect(screen.getByRole("button", { name: "生成当前交付报告" })).toBeEnabled();
});

it("preserves an unknown report through modal closure and a different execution's late success", async () => {
  let finishFirst!: (value: unknown) => void;
  const recordStandardCodeDelivery = vi.fn().mockImplementationOnce(() => new Promise((resolve) => { finishFirst = resolve; }))
    .mockRejectedValueOnce(new Error("report response lost"));
  const client = reportClient(recordStandardCodeDelivery);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const invalidate = vi.spyOn(queryClient, "invalidateQueries");
  const user = userEvent.setup();
  const view = renderReview(client as unknown as APIClient, queryClient);
  await openHistory(user, "检查与交付");
  await user.selectOptions(screen.getByRole("combobox", { name: "选择审阅的执行记录" }), "run-1");
  await user.click(screen.getByRole("button", { name: "生成当前交付报告" }));
  const secondDetail = reportDetail();
  secondDetail.thread.id = "thread-2";
  secondDetail.thread.workspace_id = "workspace-2";
  secondDetail.runs = secondDetail.runs.filter(({ run }) => run.id === "run-2");
  view.rerender(<QueryClientProvider client={queryClient}><V2TaskReview
    client={client as unknown as APIClient} detail={secondDetail} working={false}
    onClose={vi.fn()} onRequestChange={vi.fn()} returnFocusRef={createRef()} /></QueryClientProvider>);
  await user.click(screen.getByRole("button", { name: "生成当前交付报告" }));
  await screen.findByText(/report response lost/);
  const originalSecond = recordStandardCodeDelivery.mock.calls[1];
  const secondIntent = queryClient.getQueryData(["run", "run-2", "standard-code-delivery-intent"]);
  expect(originalSecond[0]).toBe("run-2");
  expect(originalSecond[1]).toEqual({ operation_key: originalSecond[2], verification_job_ids: [], uncovered_items: [] });
  view.unmount();
  invalidate.mockClear();
  await act(async () => finishFirst({ report: reportFor("run-1"), replayed: false }));
  expect(queryClient.getQueryData(["run", "run-1", "standard-code-delivery-intent"])).toBeNull();
  expect(queryClient.getQueryData(["run", "run-2", "standard-code-delivery-intent"])).toEqual(secondIntent);
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["run", "run-1"] });
  expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["run", "run-2"] });
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["v2", "thread", "thread-1"] });
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["workspace", "workspace-1"] });
  expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["v2", "thread", "thread-2"] });
  expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["workspace", "workspace-2"] });

  const stale = { ...reportFor("run-2"), status: "stale", verified: false,
    observation: { observed_at: "2026-09-08T03:00:00Z", revision_sha256: "0".repeat(64),
      reason_code: "workspace_modified_after_verification" } };
  client.standardCodeDelivery.mockResolvedValue(stale as ReturnType<typeof reportFor>);
  recordStandardCodeDelivery.mockResolvedValueOnce({ report: stale, replayed: true });
  const changedDetail = secondDetail;
  changedDetail.runs[0].run.standard_code_preset_configured = false;
  renderReview(client as unknown as APIClient, queryClient, changedDetail);
  await openHistory(user, "检查与交付");
  expect(screen.queryByRole("button", { name: "生成当前交付报告" })).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "确认上次报告" }));
  await waitFor(() => expect(recordStandardCodeDelivery).toHaveBeenCalledTimes(3));
  expect(recordStandardCodeDelivery.mock.calls[2]).toEqual(originalSecond);
  await waitFor(() => expect(screen.queryByRole("button", { name: "确认上次报告" })).not.toBeInTheDocument());
  expect(queryClient.getQueryData(["run", "run-2", "standard-code-delivery-intent"])).toBeNull();
  expect(screen.getByRole("combobox", { name: "选择审阅的执行记录" })).toHaveValue("run-2");
  expect(await screen.findByText("报告已过期，当前版本未验证")).toBeInTheDocument();
});

it("requires an explicit configured preset and can read an older execution's existing detail", async () => {
  const user = userEvent.setup();
  const detail = reportDetail();
  detail.runs[1].run.standard_code_preset_configured = false;
  delete detail.runs[0].run.standard_code_preset_configured;
  const getRun = vi.fn().mockResolvedValueOnce({ run: { id: "run-2", standard_code_preset_configured: false } })
    .mockRejectedValueOnce(new Error("run detail unavailable"))
    .mockResolvedValue({ run: { id: "run-1", standard_code_preset_configured: true } });
  const get = vi.fn((path: string) => path.startsWith("/runs/") ? getRun() : Promise.reject(new Error("task review unavailable in this fixture")));
  const record = vi.fn().mockResolvedValue({ report: reportFor("run-1"), replayed: false });
  const client = { ...reportClient(record), get } as unknown as APIClient;
  renderReview(client, new QueryClient({ defaultOptions: { queries: { retry: false } } }), detail);
  expect(get.mock.calls.some(([path]) => String(path).startsWith("/runs/"))).toBe(false);
  await openHistory(user, "检查与交付");
  expect(screen.getByText(/此执行使用普通编码流程/)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "生成当前交付报告" })).not.toBeInTheDocument();
  expect(get).toHaveBeenCalledWith("/runs/run-2", {}, expect.any(AbortSignal));
  await user.selectOptions(screen.getByRole("combobox", { name: "选择审阅的执行记录" }), "run-1");
  await screen.findByRole("button", { name: "重试交付配置" });
  expect(screen.queryByRole("button", { name: "生成当前交付报告" })).not.toBeInTheDocument();
  expect(screen.queryByText(/此执行使用普通编码流程/)).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "重试交付配置" }));
  await user.click(await screen.findByRole("button", { name: "生成当前交付报告" }));
  expect(get).toHaveBeenCalledWith("/runs/run-1", {}, expect.any(AbortSignal));
  await waitFor(() => expect(record).toHaveBeenCalledOnce());
  expect(record.mock.calls[0][0]).toBe("run-1");
});

it("offers coding configuration and the exact plan within the current task while keeping history read-only", async () => {
  const user = userEvent.setup();
  const detail = reportDetail();
  for (const entry of detail.runs) entry.run.standard_code_preset_configured = false;
  const get = vi.fn((path: string) => Promise.resolve({
    run: { id: path.split("/").at(-1), status: path.endsWith("run-2") ? "created" : "completed", standard_code_preset_configured: false },
    mode: { phase: "plan", surface: "code" },
    plan_delivery: { operator_choice_needed: false, phase_change_needed: false, capability_grant: false,
      delivery_gate_enforced: true, required_checkpoints: 0, ready_checkpoints: 0, checkpoints: [] },
  }));
  const runCapabilityReadiness = vi.fn((runID: string) => Promise.resolve(patchCapabilityReadiness(
    capabilityReadinessFixture(runID), "presets", "standard_code", { selectable: true, runtime_available: true,
      blocked_by: [], remediation: [], restart_required: false })));
  const configureStandardCode = vi.fn().mockResolvedValue({ status: "blocked", run_id: "run-2", action: "configure",
    backend_intent: "local", trust_required: true, trust_digest: "a".repeat(64), next_steps: ["confirm_workspace_trust"],
    docker_readiness: { available: false }, network: "disabled", credentials: "none" });
  const client = { ...reportClient(vi.fn()), get, runCapabilityReadiness, configureStandardCode } as unknown as APIClient;
  renderReview(client, new QueryClient({ defaultOptions: { queries: { retry: false } } }), detail);
  await openHistory(user, "检查与交付");
  expect(await screen.findByText("尚无计划方案")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "生成当前交付报告" })).not.toBeInTheDocument();
  expect(screen.queryByRole("group", { name: "执行权限" })).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: /检查并配置 Local/ }));
  await screen.findByText("确认工作区来源");
  expect(configureStandardCode).toHaveBeenCalledWith("run-2", "configure", {
    version: "standard_code_preset.v1", backend_intent: "local", confirm_workspace_trust: false }, expect.any(String));
  expect(screen.getByRole("combobox", { name: "选择审阅的执行记录" })).toHaveValue("run-2");
  await user.selectOptions(screen.getByRole("combobox", { name: "选择审阅的执行记录" }), "run-1");
  expect(await screen.findByText(/当前未结束的 Code 执行；历史执行仍可审阅/)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: /检查并配置 Local/ })).toBeDisabled();
});

it("shares a nonauthorizing backend draft between actual task settings and review entrances", async () => {
  const user = userEvent.setup(); const detail = reportDetail();
  const run = { ...detail.active_run, status: "created", standard_code_preset_configured: false };
  const current = { run, mission: { workspace_id: "workspace-1" }, mode: { phase: "plan", surface: "code" },
    execution_permission: { mode: "ask" },
    execution_profile: { profile: "preview", backend: "noop", risk_tier: "minimal", approval_policy: "none", required_gate: "none" },
    execution_interaction: { mode: "preview", workspace_trust: "untrusted", command_form: "none", required_gate: "none" },
  };
  const configureStandardCode = vi.fn(); const postControl = vi.fn();
  const client = { ...reportClient(vi.fn()), configureStandardCode, postControl,
    get: vi.fn((path: string) => Promise.resolve(path.startsWith("/threads/") ? detail : current)),
    getSandboxEnvironment: vi.fn().mockResolvedValue(sandboxEnvironmentFixture({ default_backend: "sbx", sbx_enabled: true })),
    runCapabilityReadiness: vi.fn((runID: string) => Promise.resolve(patchCapabilityReadiness(capabilityReadinessFixture(runID),
      "presets", "standard_code", { selectable: true, runtime_available: true, blocked_by: [], remediation: [], restart_required: false }))),
  } as unknown as APIClient;
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const settings = () => <QueryClientProvider client={queries}><V2ExecutionSettings client={client}
    threadID="thread-1" workspaces={[]} /></QueryClientProvider>;
  const view = render(settings());
  const settingsPicker = await screen.findByRole("group", { name: "编码环境后端" });
  await user.click(within(settingsPicker).getByRole("button", { name: /^Docker Engine/u }));
  view.unmount();
  const review = renderReview(client, queries, detail);
  await openHistory(user, "检查与交付");
  const picker = await screen.findByRole("group", { name: "编码环境后端" });
  expect(within(picker).getByRole("button", { name: /^Docker Engine/u })).toHaveAttribute("aria-pressed", "true");
  await user.click(within(picker).getByRole("button", { name: /^Docker Sandboxes/u }));
  review.unmount(); render(settings());
  const reopenedPicker = await screen.findByRole("group", { name: "编码环境后端" });
  expect(within(reopenedPicker).getByRole("button", { name: /^Docker Sandboxes/u })).toHaveAttribute("aria-pressed", "true");
  expect(configureStandardCode).not.toHaveBeenCalled(); expect(postControl).not.toHaveBeenCalled();
});

function render(ui: ReactElement, options?: RenderOptions) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
