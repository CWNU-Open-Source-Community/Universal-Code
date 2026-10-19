import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { ProjectlessDirectory } from "../../api/types";
import { LocaleProvider } from "../../lib/locale";
import { V2Settings } from "./settings";
import { V2ProjectlessDirectorySettings } from "./projectless-directory-settings";

const native = vi.hoisted(() => ({ available: false, enabled: false, choose: vi.fn() }));
vi.mock("../../lib/desktop-bridge", async (load) => ({ ...await load<typeof import("../../lib/desktop-bridge")>(),
  desktopBridgeAvailable: () => native.available,
  desktopProjectlessDirectoryEnabled: () => native.enabled,
  chooseDesktopProjectlessDirectory: native.choose,
}));

const initial: ProjectlessDirectory = { version: "projectless_directory.v1", directory_path: "C:\\Documents\\Universal-Code", default_directory_path: "C:\\Documents\\Universal-Code" };
const next: ProjectlessDirectory = { ...initial, directory_path: "D:\\Chat files" };

beforeEach(() => { native.available = false; native.enabled = false; native.choose.mockReset(); });

function fixture(overrides: Partial<APIClient> = {}, general = false) {
  const getProjectlessDirectory = vi.fn().mockResolvedValue(initial);
  const importWorkspace = vi.fn().mockResolvedValue({ workspace: { id: "ws-import-root" } });
  const setProjectlessDirectory = vi.fn().mockResolvedValue(next);
  const client = { hasProjectlessWorkspaces: true, hasWorkspaceImport: true,
    getProjectlessDirectory, importWorkspace, setProjectlessDirectory, ...overrides } as unknown as APIClient;
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<LocaleProvider><QueryClientProvider client={queries}>{general
    ? <V2Settings client={client} section="general" threadID="" workspaces={[]} onOpenInspector={vi.fn()} onSelectSection={vi.fn()} />
    : <V2ProjectlessDirectorySettings client={client} />}</QueryClientProvider></LocaleProvider>);
  return { getProjectlessDirectory, importWorkspace, setProjectlessDirectory, queries };
}

it("shows the configured directory in General and saves the server directory without adding a project", async () => {
  const user = userEvent.setup();
  const controls = fixture({}, true);
  expect(await screen.findByText(initial.directory_path)).toBeVisible();
  expect(screen.getByText(/每个新建本地聊天使用独立文件夹/)).toBeVisible();
  await user.click(screen.getByRole("button", { name: "更改目录" }));
  await user.type(screen.getByLabelText("服务电脑上的目录"), next.directory_path);
  await user.click(screen.getByRole("button", { name: "使用此目录" }));
  expect(controls.importWorkspace).not.toHaveBeenCalled();
  expect(controls.setProjectlessDirectory).toHaveBeenCalledExactlyOnceWith(next.directory_path);
  expect(await screen.findByText(next.directory_path)).toBeVisible();
  expect(await screen.findByRole("status")).toHaveTextContent("默认目录已更新");
});

it("keeps the current root and entered path visible when a save fails", async () => {
  const setProjectlessDirectory = vi.fn().mockRejectedValue(new Error("selected directory is unavailable"));
  fixture({ setProjectlessDirectory });
  const user = userEvent.setup();
  await screen.findByText(initial.directory_path);
  await user.click(screen.getByRole("button", { name: "更改目录" }));
  await user.type(screen.getByLabelText("服务电脑上的目录"), next.directory_path);
  await user.click(screen.getByRole("button", { name: "使用此目录" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("selected directory is unavailable");
  expect(screen.getByText(initial.directory_path)).toBeVisible();
  expect(screen.getByLabelText("服务电脑上的目录")).toHaveValue(next.directory_path);
  expect(screen.queryByText(next.directory_path)).not.toBeInTheDocument();
});

it("uses the native picker without exposing a path input and preserves cancellation", async () => {
  native.available = true; native.enabled = true;
  native.choose.mockResolvedValueOnce(null).mockResolvedValueOnce(next);
  const controls = fixture();
  const user = userEvent.setup();
  await screen.findByText(initial.directory_path);
  await user.click(screen.getByRole("button", { name: "更改目录" }));
  expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  expect(screen.getByText(initial.directory_path)).toBeVisible();
  await waitFor(() => expect(screen.getByRole("button", { name: "更改目录" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "更改目录" }));
  expect(await screen.findByText(next.directory_path)).toBeVisible();
  expect(native.choose.mock.calls).toEqual([[], []]);
  expect(controls.importWorkspace).not.toHaveBeenCalled();
  expect(controls.setProjectlessDirectory).not.toHaveBeenCalled();
});

it("does not offer renderer path input in desktop when native selection is disabled", async () => {
  native.available = true;
  fixture();
  await screen.findByText(initial.directory_path);
  expect(screen.getByRole("button", { name: "更改目录" })).toBeDisabled();
  expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
});

it("keeps the setting hidden for a server without the capability", () => {
  const controls = fixture({ hasProjectlessWorkspaces: false });
  expect(screen.queryByRole("heading", { name: "本地聊天目录" })).not.toBeInTheDocument();
  expect(controls.getProjectlessDirectory).not.toHaveBeenCalled();
});
