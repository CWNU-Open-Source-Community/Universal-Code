import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { APIRequestError, type APIClient } from "../../api/client";
import { LocaleProvider } from "../../lib/locale";
import * as desktop from "../../lib/desktop-bridge";
import { ProjectDialog, V2WorkspaceStart } from "./workspace-start";

vi.mock("../../lib/desktop-bridge", () => ({
  desktopWorkspaceImportEnabled: vi.fn(() => false),
  selectDesktopWorkspaceDirectory: vi.fn(),
  createDesktopWorkspace: vi.fn(),
}));
afterEach(cleanup);
beforeEach(() => {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  vi.clearAllMocks();
  vi.mocked(desktop.desktopWorkspaceImportEnabled).mockReturnValue(false);
});
const workspace = { id: "workspace-import", name: "我的项目", created_at: "2026-09-08T00:00:00Z" };
function show(client: Partial<APIClient>, editing = false) {
  const onSelect = vi.fn();
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <LocaleProvider>{editing ? <ProjectDialog workspace={workspace} client={client as APIClient} onClose={vi.fn()} onSaved={onSelect} />
      : <V2WorkspaceStart client={client as APIClient} onSelect={onSelect} />}</LocaleProvider>
  </QueryClientProvider>);
  return onSelect;
}

it("explains unavailable import and restores keyboard focus without allowing creation", async () => {
  const importer = vi.fn();
  show({ hasWorkspaceImport: false, importWorkspace: importer });
  const user = userEvent.setup();
  const trigger = screen.getByRole("button", { name: "接入项目" });
  await user.click(trigger);
  const dialog = screen.getByRole("dialog", { name: "创建项目" });
  expect(within(dialog).getByRole("status")).toHaveTextContent("--enable-workspace-import");
  expect(within(dialog).getByRole("button", { name: "创建项目" })).toBeDisabled();
  await user.keyboard("{Escape}");
  expect(trigger).toHaveFocus();
  expect(importer).not.toHaveBeenCalled();
});

it("creates with an explicit name and source folder only after the final confirmation", async () => {
  localStorage.setItem("prayu.locale.v1", "en-US");
  const importer = vi.fn().mockResolvedValue({ workspace });
  const onSelect = show({ hasWorkspaceImport: true, importWorkspace: importer });
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Connect project" }));
  const dialog = screen.getByRole("dialog", { name: "Create project" });
  const name = within(dialog).getByRole("textbox", { name: "Project name" });
  expect(name).toHaveFocus();
  await user.type(name, "我的项目");
  await user.click(within(dialog).getByRole("button", { name: "Add" }));
  await user.type(within(dialog).getByRole("textbox", { name: "Project folder path" }), "D:\\我的项目");
  await user.click(within(dialog).getByRole("button", { name: "Add" }));
  expect(importer).not.toHaveBeenCalled();
  await user.click(within(dialog).getByRole("button", { name: "Create project" }));
  await waitFor(() => expect(onSelect).toHaveBeenCalledExactlyOnceWith(workspace));
  expect(importer).toHaveBeenCalledExactlyOnceWith("D:\\我的项目", undefined, "我的项目");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
});

it("keeps invalid source and name for correction, then submits the corrected path", async () => {
  const importer = vi.fn().mockRejectedValueOnce(new APIRequestError("rejected", "INVALID_ARGUMENT", 400, "fixture"))
    .mockResolvedValueOnce({ workspace });
  const onSelect = show({ hasWorkspaceImport: true, importWorkspace: importer });
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "接入项目" }));
  await user.click(screen.getByRole("button", { name: "添加" }));
  await user.type(screen.getByRole("textbox", { name: "项目文件夹路径" }), "D:\\missing project");
  await user.click(screen.getByRole("button", { name: "添加" }));
  await user.click(screen.getByRole("button", { name: "创建项目" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("目录必须已存在");
  expect(screen.getByText("D:\\missing project")).toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: "项目名称" })).toHaveValue("missing project");
  expect(screen.getByRole("textbox", { name: "项目名称" })).toHaveFocus();
  expect(onSelect).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "更换" }));
  const path = screen.getByRole("textbox", { name: "项目文件夹路径" });
  await user.clear(path);
  await user.type(path, "D:\\existing project");
  await user.click(screen.getByRole("button", { name: "添加" }));
  await user.click(screen.getByRole("button", { name: "创建项目" }));
  await waitFor(() => expect(onSelect).toHaveBeenCalledWith(workspace));
  expect(importer).toHaveBeenLastCalledWith("D:\\existing project", undefined, "missing project");
});

it("keeps Escape working after confirming the folder with Enter", async () => {
  const importer = vi.fn();
  show({ hasWorkspaceImport: true, importWorkspace: importer });
  const user = userEvent.setup();
  const trigger = screen.getByRole("button", { name: "接入项目" });
  await user.click(trigger);
  await user.click(screen.getByRole("button", { name: "添加" }));
  await user.type(screen.getByRole("textbox", { name: "项目文件夹路径" }), "D:\\design{Enter}");
  expect(screen.getByRole("textbox", { name: "项目名称" })).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(trigger).toHaveFocus();
  expect(importer).not.toHaveBeenCalled();
});

it("keeps the path editor open while an IME candidate is still composing", async () => {
  const importer = vi.fn();
  const onSelect = show({ hasWorkspaceImport: true, importWorkspace: importer });
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "接入项目" }));
  await user.click(screen.getByRole("button", { name: "添加" }));
  const path = screen.getByRole("textbox", { name: "项目文件夹路径" });
  await user.type(path, "D:\\文档");
  fireEvent.keyDown(path, { key: "Enter", isComposing: true });
  fireEvent.keyDown(path, { key: "Enter", keyCode: 229 });
  expect(path).toHaveValue("D:\\文档");
  expect(screen.getByRole("textbox", { name: "项目名称" })).toHaveValue("");
  fireEvent.keyDown(path, { key: "Enter" });
  expect(screen.queryByRole("textbox", { name: "项目文件夹路径" })).not.toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: "项目名称" })).toHaveValue("文档");
  expect(onSelect).not.toHaveBeenCalled();
});

it("selects a native directory without registering a project on cancel", async () => {
  vi.mocked(desktop.desktopWorkspaceImportEnabled).mockReturnValue(true);
  vi.mocked(desktop.selectDesktopWorkspaceDirectory).mockResolvedValue({ handle: "native-handle", directory_path: "D:\\design", name: "design" });
  vi.mocked(desktop.createDesktopWorkspace).mockResolvedValue(workspace);
  const onSelect = show({});
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "接入项目" }));
  await user.click(screen.getByRole("button", { name: "添加" }));
  expect(await screen.findByText("D:\\design")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "取消" }));
  expect(desktop.createDesktopWorkspace).not.toHaveBeenCalled();
  expect(onSelect).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "接入项目" }));
  await user.click(screen.getByRole("button", { name: "添加" }));
  await screen.findByText("D:\\design");
  await user.click(screen.getByRole("button", { name: "创建项目" }));
  await waitFor(() => expect(onSelect).toHaveBeenCalledWith(workspace));
  expect(desktop.createDesktopWorkspace).toHaveBeenCalledExactlyOnceWith("native-handle", "design");
});

it("edits only the project name and displays its unchanged source folder", async () => {
  const rename = vi.fn().mockResolvedValue({ ...workspace, name: "新名称" });
  const onSelect = show({ hasControl: true, renameWorkspace: rename,
    getWorkspaceProject: vi.fn().mockResolvedValue({ ...workspace, directory_path: "D:\\original", thread_count: 12, active_thread_count: 1 }) }, true);
  const user = userEvent.setup();
  expect(await screen.findByText("D:\\original")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "更换" })).not.toBeInTheDocument();
  await user.clear(screen.getByRole("textbox", { name: "项目名称" }));
  await user.type(screen.getByRole("textbox", { name: "项目名称" }), "新名称");
  await user.click(screen.getByRole("button", { name: "保存更改" }));
  await waitFor(() => expect(onSelect).toHaveBeenCalled());
  expect(rename).toHaveBeenCalledExactlyOnceWith(workspace.id, "新名称");
});
