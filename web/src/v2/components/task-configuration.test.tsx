import { act, fireEvent, render as renderComponent, screen, waitFor } from "@testing-library/react";
import { useLayoutEffect, useState, type ReactElement } from "react";
import type { APIClient } from "../../api/client";
import type { RunView, TaskBudgetSettings, TaskConfigurationView } from "../../api/types";
import type { ProjectSkillChoice } from "../../api/project-skills";
import { TaskConfiguration } from "./task-configuration";
import { LocaleProvider } from "../../lib/locale";

function render(ui: ReactElement) { return renderComponent(ui, { wrapper: LocaleProvider }); }
beforeEach(() => { localStorage.removeItem("prayu.locale.v1"); });

const view = (workspaceID = "workspace-1"): TaskConfigurationView => ({ version: "task_configuration.v1", workspace_id: workspaceID, profile: "code", requested_budget: { max_turns: 30, max_tool_calls: 40 }, budget: { max_turns: 12, max_tool_calls: 8 }, sources: [{ field: "budget.max_turns", source: "project" }, { field: "budget.max_tool_calls", source: "project" }], project_disposition: "applied", project: { protocol: "project_config.v1", read_only: false, allowed_profiles: [], excluded_path_count: 2, skill_suggestion_count: 1 }, project_fingerprint: "a".repeat(64), fingerprint: "b".repeat(64), rejections: [], capability_grant: false });
const clientFixture = () => ({ previewTaskConfiguration: vi.fn().mockResolvedValue(view()), getRunTaskConfiguration: vi.fn().mockResolvedValue(view()), createThread: vi.fn() });

describe("TaskConfiguration presentation fixtures", () => {
  it("displays native Skill metadata while selecting and confirming its exact component pin", async () => {
    const ref = JSON.stringify({ installation_id: "native-install", package_id: "native-package", component_id: "skill:native",
      revision: "a".repeat(64), installation_generation: 3 });
    const client = { ...clientFixture(), hasThreadControl: true, listTaskSkillCandidates: vi.fn().mockResolvedValue({
      items: [{ ref, name: "native", token_upper_bound: 100 }], unavailable_count: 0,
    }) }, changed = vi.fn(), validity = vi.fn();
    function Host() {
      const [choice, setChoice] = useState<ProjectSkillChoice>({ refs: [], confirmUntrustedContext: false });
      return <TaskConfiguration client={client as unknown as APIClient} workspaceID="workspace-1" phase="plan" projectSkills={choice}
        onProjectSkillsChange={(next) => { changed(next); setChoice(next); }} onValidityChange={validity} />;
    }
    render(<Host />);
    fireEvent.click(await screen.findByRole("checkbox", { name: "native" }));
    expect(changed).toHaveBeenLastCalledWith({ refs: [ref], confirmUntrustedContext: false, source: "installed" });
    await waitFor(() => expect(validity).toHaveBeenLastCalledWith(false));
    fireEvent.click(screen.getByRole("checkbox", { name: /确认使用所选技能上下文/ }));
    expect(changed).toHaveBeenLastCalledWith({ refs: [ref], confirmUntrustedContext: true, source: "installed" });
    expect(client.listTaskSkillCandidates).toHaveBeenCalledWith({ workspace_id: "workspace-1", surface: "code", phase: "plan", profile: "code" }, expect.any(AbortSignal));
    await waitFor(() => expect(validity).toHaveBeenLastCalledWith(true));
    expect(client.createThread).not.toHaveBeenCalled();
  });

  it.each(["workspace", "profile", "phase", "client"])("hides old candidates immediately when the %s changes and blocks the saved choice while loading", async (change) => {
    const read = vi.fn().mockResolvedValueOnce({ items: [{ ref: "external@1.0.0", name: "external", version: "1.0.0", token_upper_bound: 100 }], unavailable_count: 0 })
      .mockImplementation(() => new Promise(() => {}));
    const first = { ...clientFixture(), hasThreadControl: true, listTaskSkillCandidates: read };
    const second = { ...clientFixture(), hasThreadControl: true, listTaskSkillCandidates: vi.fn().mockImplementation(() => new Promise(() => {})) };
    const validity = vi.fn(), displayed: boolean[] = [];
    type Props = { client: APIClient; workspaceID: string; profile: "code" | "review"; phase: "plan" | "deliver" };
    function Host(props: Props) {
      useLayoutEffect(() => { displayed.push(Boolean(screen.queryByRole("checkbox", { name: "external@1.0.0" }))); }, [props]);
      return <TaskConfiguration {...props} projectSkills={{ refs: ["external@1.0.0"], confirmUntrustedContext: true, source: "installed" }} onProjectSkillsChange={() => {}} onValidityChange={validity} />;
    }
    const props: Props = { client: first as unknown as APIClient, workspaceID: "workspace-1", profile: "code", phase: "deliver" };
    const result = render(<Host {...props} />);
    await screen.findByRole("checkbox", { name: "external@1.0.0" });
    await waitFor(() => expect(validity).toHaveBeenLastCalledWith(true));
    const signal = read.mock.calls[0][1] as AbortSignal;
    result.rerender(<Host {...props} {...(change === "workspace" ? { workspaceID: "workspace-2" } : change === "profile" ? { profile: "review" as const }
      : change === "phase" ? { phase: "plan" as const } : { client: second as unknown as APIClient })} />);
    expect(displayed.at(-1)).toBe(false);
    expect(signal.aborted).toBe(true);
    await waitFor(() => expect(validity).toHaveBeenLastCalledWith(false));
  });

  it("ignores a late old inventory, keeps budget edits from rereading Skills, and refreshes current candidates", async () => {
    let finish!: (value: unknown) => void;
    const list = vi.fn().mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }))
      .mockResolvedValue({ items: [{ ref: "new@1.0.0", name: "new", version: "1.0.0", token_upper_bound: 100 }], unavailable_count: 0 });
    const client = { ...clientFixture(), hasThreadControl: true, listTaskSkillCandidates: list };
    const props = { client: client as unknown as APIClient, workspaceID: "workspace-1", onProjectSkillsChange: () => {} };
    const result = render(<TaskConfiguration {...props} />);
    await waitFor(() => expect(list).toHaveBeenCalledTimes(1));
    result.rerender(<TaskConfiguration {...props} phase="plan" />);
    await screen.findByRole("checkbox", { name: "new@1.0.0" });
    await act(async () => finish({ items: [{ ref: "old@1.0.0", name: "old", version: "1.0.0", token_upper_bound: 100 }], unavailable_count: 0 }));
    expect(screen.queryByRole("checkbox", { name: "old@1.0.0" })).not.toBeInTheDocument();
    result.rerender(<TaskConfiguration {...props} phase="plan" budget={{ max_turns: 20 }} onProjectSkillsChange={() => {}} />);
    await screen.findByText("生效的执行上限");
    expect(list).toHaveBeenCalledTimes(2);
    fireEvent.click(screen.getByRole("button", { name: "重新读取" }));
    await waitFor(() => expect(list).toHaveBeenCalledTimes(3));
    result.rerender(<TaskConfiguration {...props} run={{ id: "existing-run" } as RunView} />);
    await screen.findByText("已保存的执行上限");
    expect(list).toHaveBeenCalledTimes(3);
  });

  it("lets the operator choose exact recommended versions and confirms context before creation", async () => {
    const client = clientFixture(), changed = vi.fn(), validity = vi.fn();
    client.previewTaskConfiguration.mockResolvedValue({ ...view(), project: { ...view().project!, skill_suggestions: ["code@1.0.0"] } });
    function Host() {
      const [choice, setChoice] = useState({ refs: [] as string[], confirmUntrustedContext: false });
      return <TaskConfiguration client={client as unknown as APIClient} workspaceID="workspace-1" projectSkills={choice}
        onProjectSkillsChange={(next) => { changed(next); setChoice(next); }} onValidityChange={validity} />;
    }
    render(<Host />);
    fireEvent.click(await screen.findByRole("checkbox", { name: "code@1.0.0" }));
    expect(changed).toHaveBeenLastCalledWith({ refs: ["code@1.0.0"], confirmUntrustedContext: false });
    await waitFor(() => expect(validity).toHaveBeenLastCalledWith(false));
    fireEvent.click(screen.getByRole("checkbox", { name: /确认使用所选技能上下文/ }));
    expect(changed).toHaveBeenLastCalledWith({ refs: ["code@1.0.0"], confirmUntrustedContext: true });
    await waitFor(() => expect(validity).toHaveBeenLastCalledWith(true));
    expect(client.createThread).not.toHaveBeenCalled();
  });
  it("offers installed packages as a second admission and routes a mixed choice to it", async () => {
    const client = { ...clientFixture(), hasThreadControl: true, listTaskSkillCandidates: vi.fn().mockResolvedValue({
      items: [{ ref: "code@1.0.0", name: "code", version: "1.0.0", token_upper_bound: 20 },
        { ref: "review@1.2.0", name: "review", version: "1.2.0", token_upper_bound: 20 }], unavailable_count: 0,
    }) };
    const changed = vi.fn(), validity = vi.fn();
    client.previewTaskConfiguration.mockResolvedValue({ ...view(), project: { ...view().project!, skill_suggestions: ["code@1.0.0"] } });
    function Host() {
      const [choice, setChoice] = useState<{ refs: string[]; confirmUntrustedContext: boolean; source?: "project" | "installed" }>({ refs: [], confirmUntrustedContext: false });
      return <TaskConfiguration client={client as unknown as APIClient} workspaceID="workspace-1" projectSkills={choice}
        onProjectSkillsChange={(next) => { changed(next); setChoice(next); }} onValidityChange={validity} />;
    }
    render(<Host />);
    // A pinned suggestion is never offered twice; Go filters unavailable refs.
    expect(await screen.findAllByRole("checkbox", { name: "code@1.0.0" })).toHaveLength(1);
    expect(screen.queryByRole("checkbox", { name: "gone@1.0.0" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("checkbox", { name: "review@1.2.0" }));
    expect(changed).toHaveBeenLastCalledWith({ refs: ["review@1.2.0"], confirmUntrustedContext: false, source: "installed" });
    await waitFor(() => expect(validity).toHaveBeenLastCalledWith(false));
    fireEvent.click(screen.getByRole("checkbox", { name: "code@1.0.0" }));
    expect(changed).toHaveBeenLastCalledWith({ refs: ["review@1.2.0", "code@1.0.0"], confirmUntrustedContext: false, source: "installed" });
    fireEvent.click(screen.getByRole("checkbox", { name: /确认使用所选技能上下文/ }));
    expect(changed).toHaveBeenLastCalledWith({ refs: ["review@1.2.0", "code@1.0.0"], confirmUntrustedContext: true, source: "installed" });
    await waitFor(() => expect(validity).toHaveBeenLastCalledWith(true));
    expect(screen.getByText(/已安装技能不来自项目建议/)).toBeInTheDocument();
    expect(client.createThread).not.toHaveBeenCalled();
  });

  it("reports a failed Registry read instead of silently offering no packages", async () => {
    const client = { ...clientFixture(), hasThreadControl: true, listTaskSkillCandidates: vi.fn().mockRejectedValue(new Error("Skill candidates response is invalid")) };
    const validity = vi.fn();
    render(<TaskConfiguration client={client as unknown as APIClient} workspaceID="workspace-1" onValidityChange={validity}
      projectSkills={{ refs: [], confirmUntrustedContext: false }} onProjectSkillsChange={() => undefined} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Skill candidates response is invalid");
    await waitFor(() => expect(validity).toHaveBeenLastCalledWith(true));
  });

  it("refuses a saved choice whose admission no longer offers the package", async () => {
    const client = { ...clientFixture(), hasThreadControl: true, listTaskSkillCandidates: vi.fn().mockResolvedValue({ items: [], unavailable_count: 0 }) };
    const changed = vi.fn(), validity = vi.fn();
    render(<TaskConfiguration client={client as unknown as APIClient} workspaceID="workspace-1"
      projectSkills={{ refs: ["review@1.2.0"], confirmUntrustedContext: true, source: "installed" }}
      onProjectSkillsChange={changed} onValidityChange={validity} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("保存的技能已不在当前可选列表中");
    await waitFor(() => expect(validity).toHaveBeenLastCalledWith(false));
    fireEvent.click(screen.getByRole("button", { name: "清除技能选择" }));
    expect(changed).toHaveBeenLastCalledWith({ refs: [], confirmUntrustedContext: false });
  });

  it("edits the bounded draft, resets defaults and displays Go-owned effective sources", async () => {
    const client = clientFixture(), changed = vi.fn();
    function Host() {
      const [budget, setBudget] = useState<TaskBudgetSettings | undefined>({ max_turns: 30, max_tool_calls: 40 });
      return <TaskConfiguration client={client as unknown as APIClient} workspaceID="workspace-1" budget={budget} onBudgetChange={(next) => { changed(next); setBudget(next); }} />;
    }
    render(<Host />);
    await screen.findByText("生效的执行上限");
    expect(screen.getByText(/实际费用请查看供应商账单/)).toBeInTheDocument();
    expect(screen.getAllByText("项目收窄").length).toBeGreaterThan(0);
    fireEvent.change(screen.getByRole("spinbutton", { name: "回合上限" }), { target: { value: "50" } });
    expect(changed).toHaveBeenLastCalledWith({ max_turns: 50, max_tool_calls: 40 });
    fireEvent.click(screen.getByRole("button", { name: "恢复默认预算" }));
    expect(changed).toHaveBeenLastCalledWith(undefined);
    expect(client.createThread).not.toHaveBeenCalled();
  });

  it("reports invalid numeric drafts and whole-project rejections without partial effective values", async () => {
    const client = clientFixture(), validity = vi.fn();
    const { rerender } = render(<TaskConfiguration client={client as unknown as APIClient} workspaceID="workspace-1" budget={{ max_turns: -1 }} onValidityChange={validity} />);
    expect(screen.getByRole("alert")).toHaveTextContent("预算超出支持范围");
    expect(validity).toHaveBeenLastCalledWith(false);
    expect(client.previewTaskConfiguration).not.toHaveBeenCalled();
    client.previewTaskConfiguration.mockResolvedValue({ ...view(), project_disposition: "rejected", project: undefined, fingerprint: undefined, project_fingerprint: undefined, sources: [], rejections: [{ field: "budget.max_turns", reason: "must narrow" }] });
    rerender(<TaskConfiguration client={client as unknown as APIClient} workspaceID="workspace-1" onValidityChange={validity} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("项目配置已拒绝");
    await waitFor(() => expect(validity).toHaveBeenLastCalledWith(false));
    expect(screen.queryByText("生效的执行上限")).not.toBeInTheDocument();
  });

  it("discards a late preview after the selected workspace changes", async () => {
    const client = clientFixture(); let finish!: (value: TaskConfigurationView) => void;
    client.previewTaskConfiguration.mockImplementationOnce(() => new Promise<TaskConfigurationView>((resolve) => { finish = resolve; })).mockResolvedValueOnce(view("workspace-2"));
    const { rerender } = render(<TaskConfiguration client={client as unknown as APIClient} workspaceID="workspace-1" />);
    await waitFor(() => expect(client.previewTaskConfiguration).toHaveBeenCalledTimes(1));
    const firstSignal = client.previewTaskConfiguration.mock.calls[0][1] as AbortSignal;
    rerender(<TaskConfiguration client={client as unknown as APIClient} workspaceID="workspace-2" />);
    expect(firstSignal.aborted).toBe(true);
    await screen.findByText("生效的执行上限");
    finish({ ...view(), project_disposition: "rejected", rejections: [{ field: "project_config", reason: "stale rejection" }] });
    await waitFor(() => expect(screen.queryByText(/stale rejection/)).not.toBeInTheDocument());
  });

  it("keeps known rejection validity through pending and failed refreshes", async () => {
    const client = clientFixture(), validity = vi.fn();
    client.previewTaskConfiguration.mockResolvedValueOnce({ ...view(), project_disposition: "rejected", project: undefined, fingerprint: undefined, project_fingerprint: undefined, sources: [], rejections: [{ field: "project_config", reason: "must fix configuration" }] });
    render(<TaskConfiguration client={client as unknown as APIClient} workspaceID="workspace-1" onValidityChange={validity} />);
    expect(validity).not.toHaveBeenCalled();
    await screen.findByText("项目配置已拒绝，无法创建任务。");
    await waitFor(() => expect(validity).toHaveBeenLastCalledWith(false));
    validity.mockClear();
    client.previewTaskConfiguration.mockRejectedValueOnce(new Error("transport unavailable"));
    fireEvent.click(screen.getByRole("button", { name: "重新读取" }));
    expect(screen.getByRole("status")).toHaveTextContent("正在读取配置");
    expect(validity).not.toHaveBeenCalled();
    await screen.findByText("transport unavailable");
    expect(validity).not.toHaveBeenCalled();
    client.previewTaskConfiguration.mockResolvedValueOnce(view());
    fireEvent.click(screen.getByRole("button", { name: "重新读取" }));
    await screen.findByText("生效的执行上限");
    await waitFor(() => expect(validity).toHaveBeenLastCalledWith(true));
  });

  it("reads an existing Run's pinned snapshot without offering edits or loading the live project", async () => {
    const client = clientFixture();
    render(<TaskConfiguration client={client as unknown as APIClient} workspaceID="workspace-1" run={{ id: "run-pinned" } as RunView} onBudgetChange={vi.fn()} />);
    await screen.findByText("已保存的执行上限");
    expect(client.getRunTaskConfiguration).toHaveBeenCalledWith("run-pinned", expect.any(AbortSignal));
    expect(client.previewTaskConfiguration).not.toHaveBeenCalled();
    expect(screen.queryByRole("spinbutton")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "恢复默认预算" })).not.toBeInTheDocument();
  });

  it("hides the previous connection snapshot on the first render with a different client", async () => {
    const first = clientFixture(), second = clientFixture(), displayed: boolean[] = [];
    second.getRunTaskConfiguration.mockImplementation(() => new Promise<TaskConfigurationView>(() => {}));
    function Host({ client }: { client: APIClient }) {
      useLayoutEffect(() => { displayed.push(Boolean(screen.queryByText("已保存的执行上限"))); }, [client]);
      return <TaskConfiguration client={client} workspaceID="workspace-1" run={{ id: "run-pinned" } as RunView} />;
    }
    const { rerender } = render(<Host client={first as unknown as APIClient} />);
    await screen.findByText("已保存的执行上限");
    rerender(<Host client={second as unknown as APIClient} />);
    expect(displayed.at(-1)).toBe(false);
    expect(screen.queryByText("已保存的执行上限")).not.toBeInTheDocument();
    await waitFor(() => expect(second.getRunTaskConfiguration).toHaveBeenCalledTimes(1));
  });

  it("localizes draft fields, help, saved legacy limits and errors in English", async () => {
    localStorage.setItem("prayu.locale.v1", "en-US");
    const client = clientFixture();
    const { rerender, container } = render(<TaskConfiguration client={client as unknown as APIClient} workspaceID="workspace-1" onBudgetChange={vi.fn()} />);
    await screen.findByText("Effective execution limits");
    expect(screen.getByRole("region", { name: "Task budget and project configuration" })).toBeInTheDocument();
    expect(screen.getByRole("spinbutton", { name: "Tool call limit" })).toHaveAttribute("placeholder", "Default 100");
    expect(screen.getByRole("spinbutton", { name: "Cost limit (USD)" })).toHaveAttribute("placeholder", "0 means unlimited");
    expect(screen.getByText(/check your provider's bill for actual charges/)).toBeInTheDocument();
    expect(screen.getByText("Applied to file-tool reads, searches and edits")).toBeInTheDocument();
    expect(container.textContent).not.toMatch(/[\u3400-\u9fff]/u);
    client.getRunTaskConfiguration.mockResolvedValue({ ...view(), budget: { max_turns: 30, max_tool_calls: 0 }, sources: view().sources.map((source) => ({ ...source, source: "snapshot" })) });
    rerender(<TaskConfiguration client={client as unknown as APIClient} workspaceID="workspace-1" run={{ id: "run-legacy" } as RunView} />);
    await screen.findByText("Saved execution limits");
    expect(screen.getAllByText("Existing run snapshot").length).toBeGreaterThan(0);
    expect(screen.getByText(/zero or omitted tool limit means unlimited/)).toBeInTheDocument();
    expect(screen.getAllByText("Unlimited").length).toBeGreaterThan(0);
    expect(container.textContent).not.toMatch(/[\u3400-\u9fff]/u);
    rerender(<TaskConfiguration client={client as unknown as APIClient} workspaceID="workspace-1" budget={{ max_turns: -1 }} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Budget exceeds the supported range.");
  });
});
