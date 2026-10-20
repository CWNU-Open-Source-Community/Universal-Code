import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { fireEvent, render as renderComponent, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { APIClient } from "../../api/client";
import type { ThreadActivityToolDetailView } from "../../api/types";
import { ThreadActivityToolDetailPanel } from "./activity-detail";

afterEach(() => vi.unstubAllGlobals());

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
