import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { useRef, useState } from "react";
import { act, fireEvent, render as renderComponent, screen, waitFor } from "@testing-library/react";
import { MarkdownInput, type MarkdownInputHandle } from "./markdown-input";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import { V2Composer } from "./composer";
import { V2RecoveryProvider } from "../recovery-storage";

function Harness({ initial = "", scope = "one", submit = vi.fn() }: { initial?: string; scope?: string; submit?: (text: string) => void }) {
  const [value, setValue] = useState(initial);
  const [plain, setPlain] = useState(false);
  const ref = useRef<MarkdownInputHandle>(null);
  return <><MarkdownInput value={value} onChange={setValue} onSubmit={() => submit(value)} plainText={plain}
    scope={scope} label="测试输入" placeholder="输入消息…" disabled={false} inputRef={ref} />
    <button onClick={() => setPlain(!plain)}>切换</button><button onClick={() => setValue("")}>清空</button>
    <output data-testid="markdown">{value}</output></>;
}

function paste(text: string) {
  fireEvent.paste(screen.getByRole("textbox"), { clipboardData: { getData: (type: string) => type === "text/plain" ? text : "" } });
}

beforeAll(() => {
  // jsdom has no layout; ProseMirror asks for caret geometry when scrolling.
  Object.defineProperties(Range.prototype, {
    getClientRects: { configurable: true, value: () => [] },
    getBoundingClientRect: { configurable: true, value: () => new DOMRect() },
  });
});
beforeEach(() => localStorage.clear());

it("renders common Markdown and GFM without rewriting a restored draft or a mode switch", () => {
  const original = "# 标题\n\n**重点**与 `inline`\n\n> 引用\n\n- 项目\n\n- [x] 已完成\n\n| 项 | 值 |\n| --- | --- |\n| 中文 | 1 |\n\n```text\n可复制文字\n第二行\n```";
  const { container } = render(<Harness initial={original} />);
  expect(screen.getByRole("heading", { name: "标题" })).toBeInTheDocument();
  expect(container.querySelector("strong")).toHaveTextContent("重点");
  expect(container.querySelector("blockquote")).toHaveTextContent("引用");
  expect(container.querySelector("table")).toHaveTextContent("中文");
  expect(screen.getByRole("checkbox")).toBeChecked();
  expect(container.querySelector("pre")).toHaveTextContent("可复制文字");
  fireEvent.click(screen.getByText("切换"));
  expect(screen.getByRole("textbox")).toHaveValue(original);
  fireEvent.click(screen.getByText("切换"));
  expect(screen.getByTestId("markdown").textContent).toBe(original);
});

it("pastes formatted Markdown, sends serialized text, and preserves code contents", () => {
  const submit = vi.fn();
  render(<Harness submit={submit} />);
  paste("## 中文标题\n\n**粗体**\n\n```ts\nconst value = '<b>原文</b>';\n```\n\n尾段");
  expect(screen.getByRole("heading", { name: "中文标题" })).toBeInTheDocument();
  expect(screen.getByRole("textbox").querySelector("strong")).toHaveTextContent("粗体");
  fireEvent.keyDown(screen.getByRole("textbox"), { key: "Enter", ctrlKey: true });
  expect(submit).toHaveBeenCalledWith(expect.stringContaining("```ts\nconst value = '<b>原文</b>';\n```"));
  expect(submit.mock.calls[0][0]).toContain("**粗体**");
});

it("protects IME confirmation and gives code blocks a real newline", () => {
  const submit = vi.fn();
  render(<Harness submit={submit} />);
  paste("中文输入");
  const input = screen.getByRole("textbox");
  fireEvent.keyDown(input, { key: "Enter", isComposing: true });
  fireEvent.keyDown(input, { key: "Enter", keyCode: 229 });
  expect(submit).not.toHaveBeenCalled();
  fireEvent.click(screen.getByText("清空"));
  paste("```text\n第一行\n```");
  fireEvent.keyDown(input, { key: "Enter" });
  expect(submit).not.toHaveBeenCalled();
  expect(input.querySelector("pre")?.textContent).toBe("第一行\n");
  fireEvent.keyDown(input, { key: "Enter", ctrlKey: true });
  expect(submit).toHaveBeenCalledOnce();
});

it("undoes edits but never restores a cleared draft through Undo", async () => {
  render(<Harness />);
  paste("**暂存**");
  expect(screen.getByRole("textbox").querySelector("strong")).toBeInTheDocument();
  fireEvent.keyDown(screen.getByRole("textbox"), { key: "z", ctrlKey: true });
  await waitFor(() => expect(screen.getByTestId("markdown").textContent).toBe(""));
  paste("新草稿");
  fireEvent.click(screen.getByText("清空"));
  fireEvent.keyDown(screen.getByRole("textbox"), { key: "z", ctrlKey: true });
  expect(screen.getByTestId("markdown").textContent).toBe("");
});

it("copies the selected block as Markdown, not flattened text", () => {
  render(<Harness initial={"**重要**\n\n```text\n两行\n原文\n```"} />);
  const input = screen.getByRole("textbox");
  act(() => input.focus());
  fireEvent.keyDown(input, { key: "a", ctrlKey: true });
  const setData = vi.fn();
  fireEvent.copy(input, { clipboardData: { clearData: vi.fn(), setData } });
  expect(setData).toHaveBeenCalledWith("text/plain", expect.stringContaining("**重要**"));
  expect(setData).toHaveBeenCalledWith("text/plain", expect.stringContaining("```text\n两行\n原文\n```"));
});

it("keeps Markdown punctuation literal when using Ctrl+Shift+V", () => {
  render(<Harness />);
  fireEvent.keyDown(screen.getByRole("textbox"), { key: "v", ctrlKey: true, shiftKey: true });
  paste("**literal**");
  expect(screen.getByRole("textbox")).toHaveTextContent("**literal**");
  expect(screen.getByRole("textbox").querySelector("strong")).toBeNull();
  fireEvent.click(screen.getByText("切换"));
  fireEvent.click(screen.getByText("切换"));
  expect(screen.getByRole("textbox")).toHaveTextContent("**literal**");
});

function ComposerHarness({ submit = vi.fn(async () => undefined) }: { submit?: (text: string) => Promise<void> }) {
  return <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <V2RecoveryProvider client={{ baseURL: "/api/v1" }} scopeID="rich-composer-test">
      <V2Composer client={{} as APIClient} workspaceID="markdown-workspace" threadID=""
        workspaces={[{ id: "markdown-workspace", name: "测试项目", created_at: "" }]}
        onWorkspaceChange={() => undefined} onSubmit={submit} />
    </V2RecoveryProvider>
  </QueryClientProvider>;
}

it("restores the real composer's Markdown draft and remembers the source mode", async () => {
  const view = render(<ComposerHarness />);
  expect(screen.getByRole("textbox")).toHaveAttribute("contenteditable", "true");
  paste("# 恢复标题\n\n**不能丢失**\n\n```text\n原始内容\n```");
  const expected = screen.getByRole("textbox").textContent;
  view.unmount();
  const restored = render(<ComposerHarness />);
  await screen.findByRole("heading", { name: "恢复标题" });
  expect(screen.getByRole("textbox").textContent).toBe(expected);
  fireEvent.click(screen.getByRole("button", { name: "切换为纯文本编辑" }));
  const source = (screen.getByRole("textbox") as HTMLTextAreaElement).value;
  expect(source).toContain("**不能丢失**");
  restored.unmount();
  render(<ComposerHarness />);
  expect(screen.getByRole("textbox")).toHaveValue(source);
});

it("retains a failed rich-text submission and submits the same Markdown on retry", async () => {
  const submit = vi.fn().mockRejectedValueOnce(new Error("离线，请重试")).mockResolvedValue(undefined);
  render(<ComposerHarness submit={submit} />);
  paste("**准确的请求**\n\n```ts\nconst id = 1;\n```");
  fireEvent.click(screen.getByRole("button", { name: "发送消息" }));
  await screen.findByText("离线，请重试");
  expect(screen.getByRole("textbox").querySelector("strong")).toHaveTextContent("准确的请求");
  fireEvent.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(submit).toHaveBeenCalledTimes(2));
  expect(submit.mock.calls[1][0]).toBe(submit.mock.calls[0][0]);
  expect(submit.mock.calls[0][0]).toContain("```ts\nconst id = 1;\n```");
});

it("does not let a late submission clear a newer rich-text draft", async () => {
  let finish!: () => void;
  const submit = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
  // The fallback draft path also serves embedded/standalone composers.
  render(<QueryClientProvider client={new QueryClient()}><V2Composer client={{} as APIClient}
    workspaceID="one" threadID="" workspaces={[{ id: "one", name: "项目", created_at: "" }]}
    onWorkspaceChange={() => undefined} onSubmit={submit} /></QueryClientProvider>);
  paste("第一条");
  fireEvent.click(screen.getByRole("button", { name: "发送消息" }));
  fireEvent.keyDown(screen.getByRole("textbox"), { key: "a", ctrlKey: true });
  paste("**新的草稿**");
  await act(async () => finish());
  expect(screen.getByRole("textbox").querySelector("strong")).toHaveTextContent("新的草稿");
});

function render(ui: ReactElement, options?: RenderOptions) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  return renderComponent(ui, { wrapper: LocaleProvider, ...options });
}
