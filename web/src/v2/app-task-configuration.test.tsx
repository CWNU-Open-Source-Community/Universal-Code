import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import type { TaskBudgetSettings, ThreadView, WorkspaceView } from "../api/types";
import { normalizedTaskBudget } from "../api/task-configuration";
import { useConnectionStore } from "../state/connection";
import { LocaleProvider } from "../lib/locale";
import { V2Workbench } from "./app";

vi.mock("./components/conversation", () => ({ V2Conversation: ({ threadID }: { threadID: string }) => <div>Created task {threadID}</div> }));

const workspaces: WorkspaceView[] = ["first", "second"].map((id) => ({ id: `workspace-${id}`, name: `Project ${id}`, created_at: "2026-10-09T00:00:00Z" }));
const thread: ThreadView = { id: "thread-created", title: "Configured task", workspace_id: "workspace-first", mission_id: "mission-created",
  active_run_id: "run-created", last_run_id: "run-created", protocol_version: "thread.v1", composer_state: "ready", status: "active", version: 1,
  created_at: "2026-10-09T00:00:00Z", updated_at: "2026-10-09T00:00:00Z" };
const preview = (workspaceID: string, budget?: TaskBudgetSettings) => ({ version: "task_configuration.v1", workspace_id: workspaceID, profile: "code",
  requested_budget: normalizedTaskBudget(budget), budget: normalizedTaskBudget(budget), sources: [],
  project_disposition: "absent", fingerprint: "a".repeat(64), rejections: [], capability_grant: false });
function fixture() {
  return { baseURL: "/api/v1", hasThreadControl: true,
    listTaskSkillCandidates: vi.fn().mockResolvedValue({ items: [], unavailable_count: 0 }),
    getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? workspaces : [], page: { limit: 100 }, requestID: path })),
    availableModelRoutes: vi.fn().mockResolvedValue({ protocol_version: "model_route_catalog.v1", generation: 1,
      routes: [{ provider_id: "fixture", provider_name: "Fixture", model: "fixture", selectable: true,
        credential_status: "configured", qualification_status: "available", harness_ready: true,
        default_for_routes: ["code"], vision_capability: { state: "unknown", source: "unknown" } }] }),
    previewTaskConfiguration: vi.fn(async ({ workspace_id, budget }: { workspace_id: string; budget?: TaskBudgetSettings }) => preview(workspace_id, budget)),
    createThread: vi.fn().mockResolvedValue({ thread }), submitThreadTurn: vi.fn().mockResolvedValue({ accepted: true }),
    selectInstalledSkills: vi.fn().mockResolvedValue(undefined),
  };
}
function mount(client: ReturnType<typeof fixture>) {
  window.history.replaceState({}, "", "#/new");
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
    <LocaleProvider><V2Workbench client={client as unknown as APIClient} /></LocaleProvider></QueryClientProvider>);
}
afterEach(() => { cleanup(); useConnectionStore.getState().disconnect(); window.history.replaceState({}, "", "/"); window.localStorage.clear(); });

it("uses the composer phase for native candidates and selects their exact pin before sending the first message", async () => {
  const ref = JSON.stringify({ installation_id: "native-install", package_id: "native-package", component_id: "skill:native",
    revision: "a".repeat(64), installation_generation: 3 });
  const client = { ...fixture(), hasPlanDelivery: true };
  client.listTaskSkillCandidates.mockResolvedValue({ items: [{ ref, name: "native", token_upper_bound: 100 }], unavailable_count: 0 } as never);
  mount(client); const user = userEvent.setup();
  await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "使用手动选择的原生技能");
  await user.click(screen.getByRole("button", { name: "计划模式" }));
  await user.click(screen.getByRole("button", { name: "任务预算与项目配置" }));
  await user.click(await screen.findByRole("checkbox", { name: "native" }, { timeout: 5_000 }));
  expect(client.listTaskSkillCandidates).toHaveBeenCalledWith({ workspace_id: "workspace-first", surface: "code", phase: "plan", profile: "code" }, expect.any(AbortSignal));
  await user.click(screen.getByRole("checkbox", { name: /确认使用所选技能上下文/ }));
  await user.click(screen.getByRole("button", { name: "返回应用" }));
  await user.click(await screen.findByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(client.submitThreadTurn).toHaveBeenCalledTimes(1));
  expect(client.createThread.mock.calls[0][0]).toMatchObject({ phase: "plan" });
  expect(client.selectInstalledSkills.mock.calls[0]).toEqual([thread.last_run_id,
    { refs: [ref], confirmUntrustedContext: true, source: "installed" }, expect.stringMatching(/^v2-installed-skills-/u)]);
  expect(client.selectInstalledSkills.mock.invocationCallOrder[0]).toBeLessThan(client.submitThreadTurn.mock.invocationCallOrder[0]);
});

it("retains budget edits through settings and submits all explicit ceilings with the original task draft", async () => {
  const client = fixture(); mount(client); const user = userEvent.setup();
  await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "使用这份草稿开始任务");
  await user.click(screen.getByRole("button", { name: "任务预算与项目配置" }));
  const turns = await screen.findByRole("spinbutton", { name: "回合上限" }, { timeout: 5_000 });
  fireEvent.change(turns, { target: { value: "20" } });
  fireEvent.change(screen.getByRole("spinbutton", { name: "工具调用上限" }), { target: { value: "30" } });
  fireEvent.change(screen.getByRole("spinbutton", { name: "Token 上限" }), { target: { value: "10000" } });
  fireEvent.change(screen.getByRole("spinbutton", { name: "费用上限（USD）" }), { target: { value: "1.25" } });
  fireEvent.change(screen.getByRole("spinbutton", { name: "时长上限（秒）" }), { target: { value: "600" } });
  await user.click(screen.getByRole("button", { name: "返回应用" }));
  expect(await screen.findByRole("textbox", { name: "开始新对话" })).toHaveValue("使用这份草稿开始任务");
  expect(client.createThread).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(client.createThread).toHaveBeenCalledTimes(1));
  expect(client.createThread.mock.calls[0][0]).toEqual(expect.objectContaining({ workspace_id: "workspace-first", goal: "使用这份草稿开始任务",
    budget: { max_turns: 20, max_tool_calls: 30, max_tokens: 10000, max_cost_usd: 1.25, timeout_seconds: 600 } }));
});

it.each(["disabled", "rules", "full"])("shares the %s web selection between settings and creation without granting command network access", async (mode) => {
  const client = fixture(); mount(client); const user = userEvent.setup();
  await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "保持草稿并配置网页访问");
  await user.click(screen.getByRole("button", { name: "设置" }));
  await user.click(await screen.findByRole("button", { name: "配置" }));
  await user.selectOptions(await screen.findByRole("combobox", { name: "网页搜索模式" }), mode);
  await user.click(screen.getByRole("button", { name: "返回应用" }));
  expect(await screen.findByRole("textbox", { name: "开始新对话" })).toHaveValue("保持草稿并配置网页访问");
  const label = { disabled: "禁止搜索", rules: "规则搜索", full: "完全访问" }[mode]!;
  expect(screen.getByRole("button", { name: label })).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(client.createThread).toHaveBeenCalledTimes(1));
  expect(client.createThread.mock.calls[0][0]).toEqual(expect.objectContaining({
    network_mode: "disabled", web_access_mode: mode, web_allowed_targets: [], goal: "保持草稿并配置网页访问",
  }));
});

it("keeps invalid budget submission blocked after leaving configuration while the draft remains editable", async () => {
  const client = fixture(); mount(client); const user = userEvent.setup();
  await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "无效预算应保留草稿");
  await user.click(screen.getByRole("button", { name: "任务预算与项目配置" }));
  fireEvent.change(await screen.findByRole("spinbutton", { name: "回合上限" }), { target: { value: "0" } });
  expect(await screen.findByRole("alert")).toHaveTextContent("预算超出支持范围");
  await user.click(screen.getByRole("button", { name: "返回应用" }));
  const composer = await screen.findByRole("textbox", { name: "开始新对话" });
  expect(composer).toHaveValue("无效预算应保留草稿");
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  await user.type(composer, "，继续编辑");
  fireEvent.submit(composer.closest("form")!);
  expect(client.createThread).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "修正任务配置" }));
  expect(await screen.findByRole("spinbutton", { name: "回合上限" })).toHaveValue(0);
  await user.click(screen.getByRole("button", { name: "恢复默认预算" }));
  await screen.findByText("生效的执行上限");
  await user.click(screen.getByRole("button", { name: "返回应用" }));
  expect(await screen.findByRole("button", { name: "发送消息" })).toBeEnabled();
});

it("keeps project rejections scoped to their own workspace after preview unmount and project switching", async () => {
  const client = fixture();
  client.previewTaskConfiguration.mockImplementation(async ({ workspace_id }) => ({ ...preview(workspace_id),
    project_disposition: "rejected", rejections: [{ field: "project_config", reason: "fixture project rejected" }] } as never));
  mount(client); const user = userEvent.setup();
  await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "第一项目草稿");
  await user.click(screen.getByRole("button", { name: "任务预算与项目配置" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("fixture project rejected");
  await user.click(screen.getByRole("button", { name: "返回应用" }));
  expect(await screen.findByRole("button", { name: "发送消息" })).toBeDisabled();
  await user.selectOptions(screen.getByRole("combobox", { name: "选择工作区" }), "workspace-second");
  await user.type(screen.getByRole("textbox", { name: "开始新对话" }), "第二项目草稿");
  expect(screen.getByRole("button", { name: "发送消息" })).toBeEnabled();
  await user.selectOptions(screen.getByRole("combobox", { name: "选择工作区" }), "workspace-first");
  expect(screen.getByRole("textbox", { name: "开始新对话" })).toHaveValue("第一项目草稿");
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  expect(client.createThread).not.toHaveBeenCalled();
});

it.each(["close immediately", "edit a valid number", "preview read fails"])("retains a known project rejection when configuration reopens and %s", async (action) => {
  const client = fixture();
  client.previewTaskConfiguration.mockResolvedValueOnce({ ...preview("workspace-first"),
    project_disposition: "rejected", rejections: [{ field: "project_config", reason: "fixture project rejected" }] } as never);
  if (action === "preview read fails") client.previewTaskConfiguration.mockRejectedValue(new Error("fixture preview unavailable"));
  else client.previewTaskConfiguration.mockImplementation(() => new Promise(() => undefined));
  mount(client); const user = userEvent.setup();
  await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "已知拒绝时保留草稿");
  await user.click(screen.getByRole("button", { name: "任务预算与项目配置" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("fixture project rejected");
  await user.click(screen.getByRole("button", { name: "返回应用" }));
  expect(await screen.findByRole("button", { name: "发送消息" })).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "修正任务配置" }));
  const turns = await screen.findByRole("spinbutton", { name: "回合上限" });
  if (action === "edit a valid number") fireEvent.change(turns, { target: { value: "20" } });
  if (action === "preview read fails") expect(await screen.findByRole("alert")).toHaveTextContent("fixture preview unavailable");
  await user.click(screen.getByRole("button", { name: "返回应用" }));
  const composer = await screen.findByRole("textbox", { name: "开始新对话" });
  expect(composer).toHaveValue("已知拒绝时保留草稿");
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  fireEvent.submit(composer.closest("form")!);
  expect(client.createThread).not.toHaveBeenCalled();
  if (action === "preview read fails") {
    client.previewTaskConfiguration.mockImplementation(async ({ workspace_id, budget }) => preview(workspace_id, budget));
    await user.click(screen.getByRole("button", { name: "修正任务配置" }));
    await screen.findByText("生效的执行上限");
    await user.click(screen.getByRole("button", { name: "返回应用" }));
    expect(await screen.findByRole("button", { name: "发送消息" })).toBeEnabled();
  }
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => localStorage.setItem("universal-code.composer-plain-text", "true"));
