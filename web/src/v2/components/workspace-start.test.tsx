import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { APIRequestError, type APIClient } from "../../api/client";
import { LocaleProvider } from "../../lib/locale";
import { V2WorkspaceStart } from "./workspace-start";

afterEach(cleanup);
beforeEach(() => localStorage.setItem("prayu.locale.v1", "zh-CN"));

function show(client: Partial<APIClient>) {
  const onSelect = vi.fn();
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <LocaleProvider><V2WorkspaceStart client={client as APIClient} onSelect={onSelect} /></LocaleProvider>
  </QueryClientProvider>);
  return onSelect;
}

it("explains the unavailable import capability and returns keyboard focus without a fake submit", async () => {
  const importer = vi.fn();
  show({ hasWorkspaceImport: false, importWorkspace: importer });
  const user = userEvent.setup();
  const trigger = screen.getByRole("button", { name: "接入项目" });
  await user.click(trigger);
  const dialog = screen.getByRole("dialog", { name: "接入已有项目" });
  expect(within(dialog).getByRole("status")).toHaveTextContent("--enable-workspace-import");
  expect(within(dialog).queryByRole("textbox")).not.toBeInTheDocument();
  expect(within(dialog).queryByRole("button", { name: "接入此目录" })).not.toBeInTheDocument();
  await user.keyboard("{Escape}");
  expect(trigger).toHaveFocus();
  expect(importer).not.toHaveBeenCalled();
});

it("connects the exact project path in English while preserving the project name", async () => {
  localStorage.setItem("prayu.locale.v1", "en-US");
  const workspace = { id: "workspace-import", name: "我的项目", created_at: "2026-09-08T00:00:00Z" };
  const importer = vi.fn().mockResolvedValue({ protocol_version: "workspace_import.v1", workspace,
    directory_content_modified: false, agent_authority_granted: false });
  const onSelect = show({ hasWorkspaceImport: true, importWorkspace: importer });
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Connect project" }));
  const dialog = screen.getByRole("dialog", { name: "Connect an existing project" });
  await user.type(within(dialog).getByRole("textbox", { name: "Project folder path" }), "D:\\我的项目");
  await user.click(within(dialog).getByRole("button", { name: "Connect this directory" }));
  await waitFor(() => expect(onSelect).toHaveBeenCalledExactlyOnceWith(workspace));
  expect(importer).toHaveBeenCalledExactlyOnceWith("D:\\我的项目");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
});

it("preserves an invalid directory for correction and only selects after successful registration", async () => {
  const workspace = { id: "workspace-import", name: "Imported", created_at: "2026-09-08T00:00:00Z" };
  const importer = vi.fn().mockRejectedValueOnce(new APIRequestError("rejected", "INVALID_ARGUMENT", 400, "fixture"))
    .mockResolvedValueOnce({ protocol_version: "workspace_import.v1", workspace,
      directory_content_modified: false, agent_authority_granted: false });
  const onSelect = show({ hasWorkspaceImport: true, importWorkspace: importer });
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "接入项目" }));
  const path = screen.getByRole("textbox", { name: "项目文件夹路径" });
  await waitFor(() => expect(path).toHaveFocus());
  await user.type(path, "D:\\missing project");
  await user.click(screen.getByRole("button", { name: "接入此目录" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("已存在的文件夹完整路径");
  expect(path).toHaveFocus();
  expect(path).toHaveValue("D:\\missing project");
  expect(onSelect).not.toHaveBeenCalled();
  await user.clear(path);
  await user.type(path, "D:\\existing project");
  await user.click(screen.getByRole("button", { name: "接入此目录" }));
  await waitFor(() => expect(onSelect).toHaveBeenCalledWith(workspace));
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
});
