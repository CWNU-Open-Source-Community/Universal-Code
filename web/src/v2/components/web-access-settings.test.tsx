import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { WebAccessMode } from "../../api/types";
import { V2Settings } from "./settings";

function setup(threadID = "", initialMode: WebAccessMode = "rules", initialTargets: string[] = []) {
  const onChange = vi.fn(); const onSelect = vi.fn(); const get = vi.fn(); const setRunWebAccess = vi.fn();
  const client = { get, setRunWebAccess } as unknown as APIClient;
  const queries = new QueryClient();
  const tree = (mode: WebAccessMode, targets: string[]) => <QueryClientProvider client={queries}>
    <V2Settings client={client} section="configuration" threadID={threadID} workspaces={[]}
      defaultWebAccessMode={mode} defaultWebAccessTargets={targets} onDefaultWebAccessChange={onChange}
      onSelectSection={onSelect} onOpenInspector={vi.fn()} />
  </QueryClientProvider>;
  const view = render(tree(initialMode, initialTargets));
  return { ...view, onChange, onSelect, get, setRunWebAccess,
    redraw: (mode: WebAccessMode, targets: string[] = []) => view.rerender(tree(mode, targets)) };
}

it("configures new conversations without requiring an open task and exposes all five modes", async () => {
  const fixture = setup(); const user = userEvent.setup();
  expect(screen.getByRole("heading", { name: "配置", level: 1 })).toBeInTheDocument();
  const select = screen.getByRole("combobox", { name: "网页搜索模式" });
  expect(select).toBeEnabled(); expect(select).toHaveValue("rules");
  expect(select).toHaveAccessibleDescription(/使用已配置的搜索服务/u);
  expect(within(select).getAllByRole("option").map((item) => item.textContent)).toEqual([
    "禁止搜索", "规则搜索", "缓存搜索（待支持）", "索引搜索（待支持）", "完全访问",
  ]);
  expect(within(select).getByRole("option", { name: "缓存搜索（待支持）" })).toBeDisabled();
  expect(within(select).getByRole("option", { name: "索引搜索（待支持）" })).toBeDisabled();
  expect(screen.getByText("缓存搜索、索引搜索：待支持")).toBeVisible();
  await user.selectOptions(select, "cached"); await user.selectOptions(select, "indexed");
  expect(fixture.onChange).not.toHaveBeenCalled();
  expect(fixture.get).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "管理" })).toBeDisabled();
});

it("saves full and disabled defaults without changing the current task", async () => {
  const fixture = setup("current-thread", "rules", ["docs.example.org"]); const user = userEvent.setup();
  const select = screen.getByRole("combobox", { name: "网页搜索模式" });
  await user.selectOptions(select, "full");
  expect(fixture.onChange).toHaveBeenLastCalledWith("full", []);
  fixture.redraw("full");
  expect(screen.getByText(/直接读取公网 HTTPS 网页/u)).toBeVisible();
  expect(screen.queryByRole("button", { name: "编辑域名" })).not.toBeInTheDocument();
  await user.selectOptions(select, "disabled");
  expect(fixture.onChange).toHaveBeenLastCalledWith("disabled", []);
  fixture.redraw("disabled");
  expect(screen.getByText("关闭新的网页搜索与抓取，已保存的网页仍可阅读。")).toBeVisible();
  expect(fixture.get).not.toHaveBeenCalled(); expect(fixture.setRunWebAccess).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "管理" }));
  await user.click(screen.getByRole("button", { name: "设置环境" }));
  expect(fixture.onSelect.mock.calls).toEqual([["permissions"], ["environment"]]);
});

it("confirms canonical exact domains before saving the new-conversation rules", async () => {
  const fixture = setup(); const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "编辑域名" }));
  const editor = screen.getByRole("textbox", { name: "新对话允许的 HTTPS 域名" });
  await user.type(editor, "HTTPS://Docs.Example.COM:443/\ndocs.example.com");
  await user.click(screen.getByRole("button", { name: "保存域名" }));
  expect(fixture.onChange).not.toHaveBeenCalled();
  const dialog = screen.getByRole("dialog", { name: "保存新对话网页规则？" });
  await user.click(within(dialog).getByRole("button", { name: "保存域名" }));
  expect(fixture.onChange).toHaveBeenCalledExactlyOnceWith("rules", ["docs.example.com"]);
  await waitFor(() => expect(screen.getByRole("button", { name: "编辑域名" })).toHaveFocus());
});

it("rejects broad or path-bearing default rules and supports keyboard focus", async () => {
  setup(); const user = userEvent.setup();
  await user.tab(); expect(screen.getByRole("combobox", { name: "网页搜索模式" })).toHaveFocus();
  await user.tab(); await user.keyboard("{Enter}");
  const editor = screen.getByRole("textbox", { name: "新对话允许的 HTTPS 域名" });
  await user.type(editor, "*.example.com\nhttps://example.net/path\nlocalhost");
  expect(screen.getByRole("alert")).toHaveTextContent("只接受无路径、查询或通配符");
  expect(screen.getByRole("button", { name: "保存域名" })).toBeDisabled();
});
