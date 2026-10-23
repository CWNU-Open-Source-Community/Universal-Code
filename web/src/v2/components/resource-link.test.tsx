import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { cleanup, fireEvent, render as renderComponent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import type { NarrativeEntry } from "../projection/narrative";
import { installDesktopNavigationGuard } from "../../lib/desktop-navigation";
import { parseProjectFileLink, webURL } from "../../lib/chat-resources";
import { V2Narrative } from "./narrative";
import { V2FileAttachments } from "./file-input";
import type { WorkspaceFileAttachment } from "../../api/file-attachments";

function native() {
  const bridge = {
    Bootstrap: vi.fn(async () => ({})), InstallSkillPackage: vi.fn(async () => ({})), SelectSkillPackage: vi.fn(async () => ({})), PreviewSkillPackage: vi.fn(async () => ({})),
    OpenExternalURL: vi.fn(async () => undefined), ChatResourceAction: vi.fn(async () => "done"),
    ReadChatResource: vi.fn(async () => ({ version: "desktop_chat_resource.v1", name: "report.md", path: "D:/project/report.md", relative_path: "report.md", mime_type: "text/markdown", data_base64: btoa("# done") })),
    PreviewExternalURL: vi.fn(async (url: string) => ({ url, title: "SignPath Foundation", description: "Free code signing", image: "" })),
    SaveExternalURL: vi.fn(async () => "done"),
  };
  window.go = { desktop: { DesktopBridge: bridge } }; return bridge;
}
function messages(content: string, openFile = vi.fn()) {
  const entries: NarrativeEntry[] = [
    { kind: "user", id: "question", text: content, createdAt: "", provisional: false },
    { kind: "assistant", id: "answer", text: content, createdAt: "", runId: "historical-run", provisional: false },
  ];
  const query = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={query}><div className="v2-shell"><V2Narrative client={{} as APIClient} entries={entries} threadID="thread" workspaceID="workspace" onOpenFile={openFile} /></div></QueryClientProvider>);
  return openFile;
}
afterEach(() => { cleanup(); delete window.go; vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("opens both user and Agent web links through the desktop browser while retaining navigation protection", async () => {
  const bridge = native(), remove = installDesktopNavigationGuard();
  try {
    messages("[签名服务](https://signpath.org/)");
    for (const link of screen.getAllByRole("link", { name: "签名服务" })) fireEvent.click(link);
    await waitFor(() => expect(bridge.OpenExternalURL).toHaveBeenCalledTimes(2));
    expect(bridge.OpenExternalURL).toHaveBeenCalledWith("https://signpath.org/");
    expect(window.open("https://example.com/")).toBeNull();
  } finally { remove(); }
});

it("keeps root-level file:line links working under the desktop guard and resolves the exact run", async () => {
  const bridge = native(), remove = installDesktopNavigationGuard();
  try {
    const open = messages("[查看报告](report.md:42)");
    fireEvent.click(screen.getAllByRole("link", { name: "查看报告" })[1]);
    await waitFor(() => expect(open).toHaveBeenCalledWith(expect.objectContaining({ path: "report.md", line: 42, runID: "historical-run" })));
    expect(bridge.ReadChatResource).toHaveBeenCalledWith(expect.objectContaining({ workspace_id: "workspace", run_id: "historical-run", path: "report.md" }));
    expect(bridge.OpenExternalURL).not.toHaveBeenCalled();
  } finally { remove(); }
});

it("preserves descriptive Markdown labels and shortens bare URLs and inline file paths", () => {
  messages("[前端 CI 已通过](https://github.com/org/repo/pull/405)\n\nhttps://github.com/org/repo/pull/405\n\n`web/src/styles.css:598`\n\n`1.0`\n\n```sh\ncat web/src/styles.css\n```");
  expect(screen.getAllByRole("link", { name: "前端 CI 已通过" })).toHaveLength(2);
  expect(screen.getAllByRole("link", { name: "PR #405" })).toHaveLength(2);
  expect(screen.getAllByRole("link", { name: "styles.css (line 598)" })).toHaveLength(2);
  expect(document.querySelector("pre a")).toBeNull();
  expect(screen.queryByRole("link", { name: "1.0" })).not.toBeInTheDocument();
});

it("renders generated images from the historical workspace and opens their copyable viewer", async () => {
  const bridge = native();
  bridge.ReadChatResource.mockResolvedValue({ version: "desktop_chat_resource.v1", name: "result.png", path: "D:/project/result.png", relative_path: "result.png", mime_type: "image/png", data_base64: "aW1hZ2U=" });
  vi.stubGlobal("createImageBitmap", vi.fn(async () => ({ width: 12, height: 8, close: vi.fn() })));
  vi.stubGlobal("URL", class extends URL { static createObjectURL() { return "blob:generated-image"; } static revokeObjectURL() {} });
  messages("![生成的图片](result.png)");
  await waitFor(() => expect(screen.getAllByRole("img", { name: "生成的图片" })).toHaveLength(2));
  const images = screen.getAllByRole("img", { name: "生成的图片" });
  expect(images[1]).toHaveAttribute("src", "data:image/png;base64,aW1hZ2U=");
  fireEvent.click(images[1]);
  await screen.findByRole("dialog", { name: "图片预览 result.png" });
  expect(screen.getByRole("button", { name: "复制图片" })).toBeEnabled();
  expect(bridge.ReadChatResource).toHaveBeenCalledWith(expect.objectContaining({ workspace_id: "workspace", run_id: "historical-run", path: "result.png" }));
});

it("loads a hover preview on demand and offers browser, copy and save operations", async () => {
  const bridge = native(); messages("[SignPath](https://signpath.org/)");
  expect(bridge.PreviewExternalURL).not.toHaveBeenCalled();
  const link = screen.getAllByRole("link", { name: "SignPath" })[0]; fireEvent.mouseEnter(link);
  await screen.findByText("SignPath Foundation");
  expect(screen.getByRole("dialog", { name: "链接预览" })).toHaveTextContent("signpath.org");
  fireEvent.contextMenu(link, { clientX: 500, clientY: 700 });
  expect(screen.getByRole("menuitem", { name: "复制链接" })).toBeInTheDocument();
  expect(screen.getByRole("menuitem", { name: "链接另存为…" })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("menuitem", { name: "在浏览器中打开" }));
  await waitFor(() => expect(bridge.OpenExternalURL).toHaveBeenCalledWith("https://signpath.org/"));
});

it("supports keyboard file menus and propagates native failures instead of claiming a copy", async () => {
  const bridge = native(); bridge.ChatResourceAction.mockRejectedValueOnce(new Error("Clipboard busy"));
  messages("[报告](report.md)");
  const link = screen.getAllByRole("link", { name: "报告" })[1];
  link.focus(); fireEvent.keyDown(link, { shiftKey: true, key: "F10" });
  expect(screen.getByRole("menuitem", { name: "打开方式…" })).toBeInTheDocument();
  expect(screen.getByRole("menuitem", { name: "复制文件内容" })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("menuitem", { name: "复制文件" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Clipboard busy");
  expect(bridge.ChatResourceAction).toHaveBeenCalledWith(expect.objectContaining({ workspace_id: "workspace", run_id: "historical-run", path: "report.md", action: "copy_file" }));
  expect(screen.queryByText("已复制文件")).not.toBeInTheDocument();
});

it("keeps the selected link action focused while the assistant continues streaming", () => {
  native();
  const query = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const content = (suffix: string) => <QueryClientProvider client={query}>
    <V2Narrative client={{} as APIClient} entries={[{
      kind: "assistant", id: "answer", text: `[报告](report.md)\n\n${suffix}`,
      createdAt: "", runId: "run", provisional: true,
    }]} threadID="thread" workspaceID="workspace" />
  </QueryClientProvider>;
  const view = render(content("正在检查。"));
  const link = screen.getByRole("link", { name: "报告" });
  fireEvent.keyDown(link, { shiftKey: true, key: "F10" });
  const first = screen.getByRole("menuitem", { name: "打开文件" });
  expect(first).toHaveFocus();
  fireEvent.keyDown(first, { key: "ArrowDown" });
  const selected = screen.getByRole("menuitem", { name: "在资源管理器中显示" });
  expect(selected).toHaveFocus();
  view.rerender(content("正在检查。已读取项目说明。"));
  expect(selected).toHaveFocus();
  fireEvent.keyDown(selected, { key: "Escape" });
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  expect(link).toHaveFocus();
});

it("keeps a tall preview above its link in a short window so the link remains clickable", async () => {
  native(); vi.stubGlobal("innerHeight", 600);
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(518);
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    return this.getAttribute("role") === "dialog"
      ? { x: 20, y: 0, left: 20, right: 400, top: 0, bottom: 520, width: 380, height: 520, toJSON: () => ({}) }
      : { x: 20, y: 420, left: 20, right: 200, top: 420, bottom: 440, width: 180, height: 20, toJSON: () => ({}) };
  });
  messages("[SignPath](https://signpath.org/)");
  fireEvent.mouseEnter(screen.getAllByRole("link", { name: "SignPath" })[0]);
  const preview = await screen.findByRole("dialog", { name: "链接预览" });
  expect(preview).toHaveStyle({ top: "12px", maxHeight: "400px" });
  fireEvent.scroll(preview);
  expect(preview).toBeInTheDocument();
  fireEvent.scroll(document);
  expect(screen.queryByRole("dialog", { name: "链接预览" })).not.toBeInTheDocument();
});

it("copies a sent file using its stored attachment identity", async () => {
  const bridge = native();
  const file: WorkspaceFileAttachment = { id: "saved-file", workspace_id: "workspace", name: "notes.md", mime_type: "text/markdown", byte_size: 6, sha256: "a".repeat(64), readability: "text", text_bytes: 6, redacted: false };
  render(<V2FileAttachments client={{} as APIClient} attachments={[file]} />);
  fireEvent.click(screen.getByRole("button", { name: "复制文件 notes.md" }));
  await screen.findByText("已复制文件，可粘贴到输入框");
  expect(bridge.ChatResourceAction).toHaveBeenCalledWith({ version: "desktop_chat_resource.v1", workspace_id: "workspace", attachment_id: "saved-file", action: "copy_file" });
});

it("rejects executable URL schemes and preserves absolute local paths for native workspace validation", () => {
  for (const url of ["javascript:alert(1)", "data:text/html,test", "https://name:password@example.com", "https://example.com\\@evil.test/"]) expect(webURL(url)).toBeNull();
  expect(parseProjectFileLink("file:///D:/project/report.md#L12")).toEqual({ path: "D:/project/report.md", line: 12 });
  expect(parseProjectFileLink("/home/me/project/report.md:8")).toEqual({ path: "/home/me/project/report.md", line: 8 });
  expect(parseProjectFileLink("../outside.txt")).toBeNull();
  expect(parseProjectFileLink("vscode://open-file")).toBeNull();
});

it("keeps web links with line suffixes out of workspace file parsing", () => {
  expect(parseProjectFileLink("https://example.com/report.md:12")).toBeNull();
  expect(parseProjectFileLink("mailto:user@example.com:12")).toBeNull();
  expect(parseProjectFileLink("src/report.md:12")).toEqual({ path: "src/report.md", line: 12 });
  expect(parseProjectFileLink("C:/project/report.md:12")).toEqual({ path: "C:/project/report.md", line: 12 });
});

function render(ui: ReactElement, options?: RenderOptions) {
 localStorage.setItem("prayu.locale.v1", "zh-CN");
 const Wrapper=options?.wrapper;
 return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
