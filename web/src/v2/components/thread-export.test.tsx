import { useRef, useState } from "react";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { ThreadTranscriptItemView } from "../../api/types";
import { LocaleProvider } from "../../lib/locale";
import { downloadTextFile } from "../../lib/download";
import { V2ThreadExport } from "./thread-export";

vi.mock("../../lib/download", () => ({ downloadTextFile: vi.fn() }));
const message = { id: "public-1", run_id: "run-1", run_ordinal: 1, sequence: 1, activity_type: "message", kind: "operator_input",
  source: "operator", title: "用户消息", detail: "preview", durable: true, provisional: false, created_at: "2026-10-22T00:00:01Z" } as ThreadTranscriptItemView;
const page = { items: [message], page: { limit: 100 }, requestID: "request-1" };
function fixture(get = vi.fn(async () => ({ message_id: "public-1", content: "完整正文\n```js\nrun();\n```" }))) {
  return { get, getPage: vi.fn(async () => page) } as unknown as APIClient;
}
function Fixture({ client }: { client: APIClient }) {
  const trigger = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  return <><button ref={trigger} onClick={() => setOpen(true)}>Export</button>
    {open && <V2ThreadExport client={client} threadID="thread-1" title="导出验证" onClose={() => setOpen(false)} returnFocusRef={trigger} />}</>;
}
afterEach(() => { cleanup(); vi.clearAllMocks(); localStorage.clear(); });

it("downloads Markdown and text, copies the complete content and restores focus on Escape", async () => {
  const user = userEvent.setup();
  const copy = vi.spyOn(navigator.clipboard, "writeText");
  render(<LocaleProvider><Fixture client={fixture()} /></LocaleProvider>);
  await user.click(screen.getByRole("button", { name: "Export" }));
  await screen.findByText("已准备 1 条消息。");
  expect(screen.getByRole("button", { name: "关闭导出窗口" })).toHaveFocus();
  await user.click(screen.getByRole("button", { name: "下载文件" }));
  expect(downloadTextFile).toHaveBeenLastCalledWith("conversation-导出验证.md", "text/markdown;charset=utf-8", expect.stringContaining("完整正文\n```js\nrun();\n```"));
  await user.selectOptions(screen.getByRole("combobox", { name: "文件格式" }), "text");
  await user.click(screen.getByRole("button", { name: "下载文件" }));
  expect(downloadTextFile).toHaveBeenLastCalledWith("conversation-导出验证.txt", "text/plain;charset=utf-8", expect.stringContaining("导出验证\n\n用户"));
  await user.click(screen.getByRole("button", { name: "复制文本" }));
  expect(copy).toHaveBeenCalledWith(expect.stringContaining("完整正文"));
  expect(screen.getByText("已复制对话。")).toBeVisible();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Export" })).toHaveFocus();
});

it("keeps save controls disabled after a failed body read and supports a complete retry", async () => {
  const get = vi.fn().mockRejectedValueOnce(new Error("body unavailable")).mockResolvedValue({ message_id: "public-1", content: "Recovered full body" });
  render(<Fixture client={fixture(get)} />);
  fireEvent.click(screen.getByRole("button", { name: "Export" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("body unavailable");
  expect(screen.getByRole("button", { name: "Download file" })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "Download file" }));
  expect(downloadTextFile).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Read again" }));
  await screen.findByText("1 message ready.");
  fireEvent.click(screen.getByRole("button", { name: "Download file" }));
  expect(downloadTextFile).toHaveBeenCalledExactlyOnceWith(expect.any(String), expect.any(String), expect.stringContaining("Recovered full body"));
});

it("reports clipboard failure while retaining the downloadable content", async () => {
  const user = userEvent.setup();
  vi.spyOn(navigator.clipboard, "writeText").mockRejectedValueOnce(new Error("Clipboard denied"));
  render(<Fixture client={fixture()} />);
  await user.click(screen.getByRole("button", { name: "Export" }));
  await screen.findByText("1 message ready.");
  await user.click(screen.getByRole("button", { name: "Copy text" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Clipboard denied");
  expect(screen.getByRole("button", { name: "Download file" })).toBeEnabled();
});

it("aborts unfinished reads when closed without writing a file", async () => {
  const getPage = vi.fn((_path: string, _query: unknown, _cursor: string, signal: AbortSignal) => new Promise((_resolve, reject) => {
    signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")));
  }));
  render(<Fixture client={{ getPage, get: vi.fn() } as unknown as APIClient} />);
  fireEvent.click(screen.getByRole("button", { name: "Export" }));
  await waitFor(() => expect(getPage).toHaveBeenCalledTimes(1));
  expect(screen.getByRole("button", { name: "Download file" })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "Close export" }));
  expect(getPage.mock.calls[0][3].aborted).toBe(true);
  expect(downloadTextFile).not.toHaveBeenCalled();
});
