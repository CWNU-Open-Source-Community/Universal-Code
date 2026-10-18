import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider, useLocale } from "../../lib/locale";
import { act, fireEvent, render as renderComponent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIRequestError, type APIClient } from "../../api/client";
import type { WorkspaceView } from "../../api/types";
import { V2Composer, v2ComposerNotSubmitted } from "./composer";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { V2RecoveryProvider } from "../recovery-storage";
import type { ComponentProps } from "react";

const client = {} as APIClient;
const workspaces: WorkspaceView[] = [
  { id: "workspace-1", name: "Traverse Board", created_at: "2026-08-29T00:00:00Z" },
];

type ComposerSubmit = ComponentProps<typeof V2Composer>["onSubmit"];

function renderComposer(onSubmit: ComposerSubmit = vi.fn(async () => undefined), workspaceID = "workspace-1") {
  const onWorkspaceChange = vi.fn();
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <V2Composer client={client} onSubmit={onSubmit}
    onWorkspaceChange={onWorkspaceChange} threadID="" workspaceID={workspaceID}
    workspaces={workspaces} /></QueryClientProvider>);
  return { onSubmit, onWorkspaceChange };
}

describe("V2Composer", () => {
  it.each([false, true])("respects a newer focus choice after switching editor mode (moved=%s)", (moved) => {
    localStorage.setItem("universal-code.composer-plain-text", "false");
    renderComposer();
    const frames: FrameRequestCallback[] = [];
    const raf = vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
      frames.push(callback); return frames.length;
    });
    try {
      const toggle = screen.getByRole("button", { name: "切换为纯文本编辑" });
      act(() => toggle.focus());
      fireEvent.click(toggle);
      const workspace = screen.getByRole("combobox", { name: "选择工作区" });
      if (moved) act(() => workspace.focus());
      act(() => { for (const frame of frames.splice(0)) frame(performance.now()); });
      expect(moved ? workspace : screen.getByRole("textbox", { name: "开始新对话" })).toHaveFocus();
    } finally { raf.mockRestore(); }
  });

  it("accepts a different follow-up while pending and never clears a newer draft on completion", async () => {
    let finishFirst!: () => void;
    let finishSecond!: () => void;
    const onSubmit = vi.fn().mockImplementationOnce(() => new Promise<void>((resolve) => { finishFirst = resolve; }))
      .mockImplementationOnce(() => new Promise<void>((resolve) => { finishSecond = resolve; }));
    renderComposer(onSubmit);
    const user = userEvent.setup();
    const input = screen.getByRole("textbox", { name: "开始新对话" });
    await user.type(input, "第一条");
    await user.click(screen.getByRole("button", { name: "发送消息" }));
    expect(input).toBeEnabled();
    expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
    fireEvent.change(input, { target: { value: "第二条" } });
    await user.click(screen.getByRole("button", { name: "发送消息" }));
    fireEvent.change(input, { target: { value: "尚未提交的第三条草稿" } });
    await act(async () => { finishSecond(); finishFirst(); });
    expect(onSubmit.mock.calls).toEqual([["第一条"], ["第二条"]]);
    expect(input).toHaveValue("尚未提交的第三条草稿");
  });

  it("trims and submits a ready message with Enter, then clears and refocuses", async () => {
    const user = userEvent.setup();
    const controls = renderComposer();
    const textarea = screen.getByRole("textbox", { name: "开始新对话" });

    await user.type(textarea, "  检查归档与权限流程  ");
    await user.keyboard("{Enter}");

    await waitFor(() => expect(controls.onSubmit)
      .toHaveBeenCalledWith("检查归档与权限流程"));
    expect(textarea).toHaveValue("");
    expect(textarea).toHaveFocus();
  });

  it("keeps a legacy draft when submission is explicitly deferred", async () => {
    const onSubmit = vi.fn<ComposerSubmit>(async () => v2ComposerNotSubmitted);
    renderComposer(onSubmit);
    const user = userEvent.setup();
    const textarea = screen.getByRole("textbox", { name: "开始新对话" });
    await user.type(textarea, "配置模型后仍要发送的原草稿");
    await user.click(screen.getByRole("button", { name: "发送消息" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    expect(textarea).toHaveValue("配置模型后仍要发送的原草稿");
  });

  it("keeps a managed draft version when submission is explicitly deferred", async () => {
    const onSubmit = vi.fn<ComposerSubmit>(async () => v2ComposerNotSubmitted);
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <V2RecoveryProvider client={{ baseURL: "/api/v1" }} scopeID="composer-deferred-draft">
        <V2Composer client={client} onSubmit={onSubmit} onWorkspaceChange={() => undefined}
          threadID="" workspaceID="workspace-managed" workspaces={[
            { id: "workspace-managed", name: "Managed", created_at: "2026-09-21T00:00:00Z" },
          ]} />
      </V2RecoveryProvider>
    </QueryClientProvider>);
    const user = userEvent.setup();
    const textarea = screen.getByRole("textbox", { name: "开始新对话" });
    await user.type(textarea, "持久草稿也不能被预检吞掉");
    await user.click(screen.getByRole("button", { name: "发送消息" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    expect(onSubmit.mock.calls[0]?.[3]).toEqual(expect.objectContaining({
      scope: expect.objectContaining({ workspaceID: "workspace-managed" }),
      ref: expect.objectContaining({ seq: expect.any(Number) }),
    }));
    expect(textarea).toHaveValue("持久草稿也不能被预检吞掉");
  });

  it("keeps failed input visible, reports the error, and clears it on the next edit", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn().mockRejectedValue(new Error("控制平面暂时不可用"));
    renderComposer(onSubmit);
    const textarea = screen.getByRole("textbox", { name: "开始新对话" });

    await user.type(textarea, "保留这条消息");
    await user.click(screen.getByRole("button", { name: "发送消息" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("控制平面暂时不可用");
    expect(textarea).toHaveValue("保留这条消息");
    await user.type(textarea, "。");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it.each([
    ["CANCELLED", true, "本轮已停止，输入和已完成的工作已保留。可发送新消息继续。"],
    ["FAILED_PRECONDITION", true, "本轮执行失败，输入和已完成的工作已保留。可发送新消息继续。"],
    ["CANCELLED", undefined, "Unconfirmed cancellation: retain this exact request"],
  ] as const)("uses concise text only for a confirmed failed turn (%s, %s)", async (code, turnFailed, expected) => {
    const reason = new APIRequestError("Unconfirmed cancellation: retain this exact request", code,
      499, "request-original", undefined, undefined, turnFailed);
    renderComposer(vi.fn().mockRejectedValue(reason));
    const input = screen.getByRole("textbox", { name: "开始新对话" });
    fireEvent.change(input, { target: { value: "保留原要求" } });
    fireEvent.click(screen.getByRole("button", { name: "发送消息" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(expected);
    expect(input).toHaveValue("保留原要求");
    expect(reason.message).toBe("Unconfirmed cancellation: retain this exact request");
  });

  it("requires a workspace before enabling submission", () => {
    const missing = renderComposer(vi.fn(async () => undefined), "");
    fireEvent.change(screen.getByRole("textbox", { name: "开始新对话" }),
      { target: { value: "有效文字" } });
    expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
    expect(missing.onSubmit).not.toHaveBeenCalled();
  });

  it("rejects content whose UTF-8 payload exceeds 16 KiB", () => {
    renderComposer();
    const oversized = "界".repeat(5_500);
    fireEvent.change(screen.getByRole("textbox", { name: "开始新对话" }),
      { target: { value: oversized } });
    expect(screen.getByText("消息不能超过 16 KiB")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  });
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => localStorage.setItem("universal-code.composer-plain-text", "true"));

it("switches composer controls and the open add menu without changing the draft or workspace name", async () => {
  function LanguageControl() {
    const { locale, setLocale } = useLocale();
    return <select aria-label="Test language" value={locale}
      onChange={(event) => setLocale(event.target.value as "zh-CN" | "en-US")}>
      <option value="zh-CN">中文</option><option value="en-US">English</option>
    </select>;
  }
  const onSubmit = vi.fn(async () => undefined);
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={queries}><LanguageControl />
    <V2Composer client={{ hasThreadControl: true } as APIClient} onSubmit={onSubmit}
      onWorkspaceChange={vi.fn()} threadID="" workspaceID="workspace-1"
      workspaces={[{ ...workspaces[0], name: "用户项目名称" }]} />
  </QueryClientProvider>);
  const user = userEvent.setup();
  const input = screen.getByRole("textbox", { name: "开始新对话" });
  expect(input).toHaveAttribute("placeholder", "输入消息…");
  await user.type(input, "保留中文草稿和代码 const value = 1;");
  await user.click(screen.getByRole("button", { name: "添加附件" }));
  expect(screen.getByRole("menuitem", { name: "添加图片或文件" })).toBeInTheDocument();

  fireEvent.change(screen.getByRole("combobox", { name: "Test language" }), { target: { value: "en-US" } });
  expect(screen.getByRole("textbox", { name: "Start a new conversation" })).toBe(input);
  expect(input).toHaveAttribute("placeholder", "Type a message…");
  expect(input).toHaveValue("保留中文草稿和代码 const value = 1;");
  expect(screen.getByRole("button", { name: "Send message" })).toBeEnabled();
  expect(screen.getByRole("combobox", { name: "Select workspace" })).toHaveDisplayValue("用户项目名称");
  expect(screen.getByRole("button", { name: "Switch to Markdown editing" })).toBeInTheDocument();
  expect(screen.getByRole("menu", { name: "Add content" })).toBeInTheDocument();
  expect(screen.getByRole("menuitem", { name: "Add images or files" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Add attachments" })).toHaveAttribute("aria-expanded", "true");
  expect(onSubmit).not.toHaveBeenCalled();

  fireEvent.change(screen.getByRole("combobox", { name: "Test language" }), { target: { value: "zh-CN" } });
  expect(screen.getByRole("textbox", { name: "开始新对话" })).toBe(input);
  expect(screen.getByRole("menuitem", { name: "添加图片或文件" })).toBeInTheDocument();
  expect(input).toHaveValue("保留中文草稿和代码 const value = 1;");
  queries.clear();
});

function render(ui: ReactElement, options?: RenderOptions) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  return renderComponent(ui, { wrapper: LocaleProvider, ...options });
}
