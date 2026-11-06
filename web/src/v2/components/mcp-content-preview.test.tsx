import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { cleanup, fireEvent, render as renderComponent, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { APIClient } from "../../api/client";
import type { ThreadActivityToolDetailView } from "../../api/types";
import { ThreadActivityToolDetailPanel } from "./activity-detail";
import { MCPContentPreview } from "./mcp-content-preview";
import { installDesktopNavigationGuard } from "../../lib/desktop-navigation";

afterEach(() => { cleanup(); delete window.go; vi.restoreAllMocks(); vi.unstubAllGlobals(); });

const pngData = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/l9sAAAAASUVORK5CYII=";
function decodeImage() {
  const image = screen.getByRole("img");
  Object.defineProperties(image, { naturalWidth: { value: 1 }, naturalHeight: { value: 1 } });
  fireEvent.load(image);
}

it("opens saved MCP image bytes at full size, copies them, and permits a desktop download without fetching", async () => {
  const url = `blob:${window.location.origin}/saved-mcp-image`;
  vi.stubGlobal("URL", class extends URL { static createObjectURL = vi.fn(() => url); static revokeObjectURL = vi.fn(); });
  vi.stubGlobal("fetch", vi.fn());
  const clipboard: Record<string, Blob | Promise<Blob>>[] = [];
  vi.stubGlobal("ClipboardItem", class { constructor(data: Record<string, Blob | Promise<Blob>>) { clipboard.push(data); } });
  vi.stubGlobal("navigator", Object.create(navigator, { clipboard: { value: { write: vi.fn(async () => {}) } } }));
  window.go = { desktop: { DesktopBridge: { Bootstrap: vi.fn(), InstallSkillPackage: vi.fn(), SelectSkillPackage: vi.fn(), PreviewSkillPackage: vi.fn() } } };
  const removeGuard = installDesktopNavigationGuard();
  try {
    render(<MCPContentPreview contents={[{ kind: "image", mime_type: "image/png", name: "result.png", data_base64: pngData,
      byte_size: atob(pngData).length, width: 1, height: 1 }]} />);
    const trigger = screen.getByRole("button", { name: "查看原图 result.png" });
    expect(trigger).toBeDisabled();
    decodeImage();
    expect(trigger).toBeEnabled();
    fireEvent.click(trigger);
    const dialog = screen.getByRole("dialog", { name: "图片预览 result.png" });
    const view = within(dialog);
    fireEvent.click(view.getByRole("button", { name: "100%" }));
    expect(view.getByRole("img")).toHaveStyle({ width: "1px", height: "1px" });
    const download = view.getByRole("link", { name: "下载原图 result.png" });
    expect(download).toHaveAttribute("href", url);
    expect(download).toHaveAttribute("download", "result.png");
    const allowedDownload = vi.fn((event: MouseEvent) => { expect(event.defaultPrevented).toBe(false); event.preventDefault(); });
    download.addEventListener("click", allowedDownload);
    fireEvent.click(download);
    expect(allowedDownload).toHaveBeenCalledTimes(1);
    fireEvent.click(view.getByRole("button", { name: "复制图片" }));
    expect(await screen.findByText("已复制图片")).toBeInTheDocument();
    const copied = await clipboard[0]["image/png"];
    expect(new Uint8Array(await copied.arrayBuffer())).toEqual(Uint8Array.from(atob(pngData), (character) => character.charCodeAt(0)));
    expect(fetch).not.toHaveBeenCalled();
    fireEvent.keyDown(dialog, { key: "Escape" });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
    expect(URL.revokeObjectURL).toHaveBeenCalledWith(url);
  } finally { removeGuard(); }
});

it("keeps GIF downloads in their original format and closes the old viewer when saved bytes change", () => {
  const revoke = vi.fn();
  vi.stubGlobal("URL", class extends URL { static createObjectURL() { return "blob:original-gif"; } static revokeObjectURL = revoke; });
  const gif = { kind: "image" as const, mime_type: "image/gif", data_base64: "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7", byte_size: 42, width: 1, height: 1 };
  const page = render(<MCPContentPreview contents={[gif]} />);
  decodeImage();
  fireEvent.click(screen.getByRole("button", { name: "查看原图 mcp-image.gif" }));
  expect(screen.getByRole("link", { name: "下载原图 mcp-image.gif" })).toHaveAttribute("download", "mcp-image.gif");
  page.rerender(<MCPContentPreview contents={[{ ...gif, mime_type: "image/png", data_base64: pngData }]} />);
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(revoke).toHaveBeenCalledWith("blob:original-gif");
  expect(screen.getByRole("button", { name: "查看原图 mcp-image.png" })).toBeDisabled();
});

it("renders saved content in the actual activity panel without fetching resource links or executing HTML", () => {
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  const tool: ThreadActivityToolDetailView = { name: "mcp_tool_call", label: "MCP", agent_id: "agent-media", agent_role: "root", agent_label: "Root Agent", status: "completed",
    started_at: "2026-10-14T16:00:00Z", duration_milliseconds: 12,
    detail: { kind: "mcp", mcp: { operation: "call", server: "media-server", tool: "resources.read", arguments: [],
      result: { type: "object", count: 3, summary: "Saved SDK content", fields: [] }, omitted_contents: 2, content_truncated: true,
      contents: [
        { kind: "resource_text", uri: "notes://guide", text: "<script>window.mcpPreviewExecuted=true</script>", mime_type: "text/html" },
        { kind: "image", name: "Preview image", mime_type: "image/png", data_base64: "aW1hZ2U=", byte_size: 5, width: 96, height: 54 },
        { kind: "audio", name: "Preview audio", mime_type: "audio/wav", data_base64: "YXVkaW8=", byte_size: 5 },
        { kind: "resource_link", uri: "https://docs.example.com/guide", name: "Reference" },
      ], boundary: { authorization: "policy_checked", error_code: "", failure_reason: "", truncated: false, untrusted: true } } } };
  const view = render(<ThreadActivityToolDetailPanel client={{} as APIClient} tool={tool} activityRef="call-media" runID="run-media" threadID="thread-media" />);
  expect(screen.getByText("<script>window.mcpPreviewExecuted=true</script>").tagName).toBe("PRE");
  expect(screen.getByText("notes://guide").tagName).toBe("CODE");
  expect(screen.getByRole("link", { name: "https://docs.example.com/guide" })).toHaveAttribute("rel", "noopener noreferrer");
  const audio = screen.getByLabelText("Preview audio");
  expect(audio).toHaveAttribute("controls");
  expect(audio).toHaveAttribute("preload", "none");
  expect(audio).not.toHaveAttribute("autoplay");
  expect(view.container.querySelector("script")).toBeNull();
  expect(fetchMock).not.toHaveBeenCalled();
  expect(screen.getByText(/2 项内容超出上限/u)).toBeInTheDocument();
  expect(screen.getByText(/返回内容超过预览上限/u)).toBeInTheDocument();
  fireEvent.error(screen.getByRole("img", { name: "Preview image" }));
  expect(screen.getByText("已保存的图片无法显示。")).toBeInTheDocument();
  fireEvent.error(audio);
  expect(screen.getByText("已保存的音频无法播放。")).toBeInTheDocument();
});

function render(ui: ReactElement, options?: RenderOptions) {
 localStorage.setItem("prayu.locale.v1", "zh-CN");
 const Wrapper=options?.wrapper;
 return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
