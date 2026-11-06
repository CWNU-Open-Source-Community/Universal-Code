import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { BrowserNetworkAccess, BrowserNetworkTarget } from "../../api/browser-network";
import { LocaleProvider } from "../../lib/locale";
import { V2BrowserNetworkControl, V2BrowserNetworkSettings } from "./browser-network-control";

const runTarget: BrowserNetworkTarget = { kind: "runs", id: "run-1" };
function policy(mode: BrowserNetworkAccess = "off", runID = "run-1") {
  return { version: "browser_network_access.v1", workspace_id: "workspace-1", run_id: runID, revision: 1,
    browser_network_access: mode, browser_origins: mode === "restricted" ? ["https://docs.example.com"] : [],
    project_preference: { workspace_id: "workspace-1", revision: 0, browser_network_access: "off" } };
}
function setup(mode: BrowserNetworkAccess = "off") {
  const get = vi.fn(async (path: string): Promise<unknown> => path.startsWith("/workspaces/")
    ? { ...policy(), run_id: undefined, revision: 0 } : policy(mode));
  const putControl = vi.fn(async (path: string, body: Record<string, unknown>) => ({ ...policy(), ...body,
    run_id: path.startsWith("/workspaces/") ? undefined : "run-1", revision: 2, replayed: false }));
  const client = { baseURL: "/api/v1", hasControl: true, get, putControl } as unknown as APIClient;
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const wrapper = ({ children }: { children: React.ReactNode }) => <LocaleProvider><QueryClientProvider client={queries}>{children}</QueryClientProvider></LocaleProvider>;
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  return { client, get, putControl, wrapper };
}
afterEach(() => localStorage.removeItem("prayu.locale.v1"));

it("enables browser internet and remembers the project with one gesture, without a Full confirmation", async () => {
  const { client, putControl, wrapper } = setup();
  render(<V2BrowserNetworkControl client={client} target={runTarget} />, { wrapper });
  const toggle = await screen.findByRole("switch", { name: "允许联网" });
  await waitFor(() => expect(toggle).toBeEnabled());
  await userEvent.click(toggle);
  await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "true"));
  expect(putControl).toHaveBeenCalledExactlyOnceWith("/runs/run-1/browser-network", {
    version: "browser_network_access.v1", expected_revision: 1, browser_network_access: "internet",
    browser_origins: [], remember_for_project: true,
  }, expect.stringMatching(/^v2-browser-network-/u));
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.queryByRole("textbox", { name: "网站白名单" })).not.toBeInTheDocument();
  expect(screen.queryByText(/Full|完全访问/u)).not.toBeInTheDocument();
});

it("disables browser internet independently of approvals", async () => {
  const { client, putControl, wrapper } = setup("internet");
  render(<V2BrowserNetworkControl client={client} target={runTarget} />, { wrapper });
  const toggle = await screen.findByRole("switch", { name: "允许联网" });
  await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "true"));
  await userEvent.click(toggle);
  await waitFor(() => expect(putControl).toHaveBeenCalledWith("/runs/run-1/browser-network", expect.objectContaining({
    browser_network_access: "off", browser_origins: [], remember_for_project: true,
  }), expect.any(String)));
});

it("restores the specified sites after switching browser internet off and on", async () => {
  const { client, putControl, wrapper } = setup("restricted");
  render(<V2BrowserNetworkControl client={client} target={runTarget} />, { wrapper });
  const toggle = await screen.findByRole("switch", { name: "允许联网" });
  await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "true"));
  await userEvent.click(toggle);
  await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "false"));
  expect(putControl.mock.calls[0][1]).toMatchObject({ browser_network_access: "off", browser_origins: ["https://docs.example.com"] });
  await userEvent.click(toggle);
  await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "true"));
  expect(putControl.mock.calls[1][1]).toMatchObject({ browser_network_access: "restricted", browser_origins: ["https://docs.example.com"] });
});

it("configures specified sites while offline and enables only those sites", async () => {
  const { client, putControl, wrapper } = setup();
  render(<V2BrowserNetworkControl client={client} target={runTarget} />, { wrapper });
  await userEvent.click(await screen.findByText("高级设置"));
  await userEvent.click(screen.getByRole("checkbox", { name: "仅指定网站" }));
  fireEvent.change(screen.getByRole("textbox", { name: "网站白名单" }), { target: { value: "https://docs.example.com" } });
  await userEvent.click(screen.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(putControl).toHaveBeenCalledOnce());
  expect(putControl.mock.calls[0][1]).toMatchObject({ browser_network_access: "off", browser_origins: ["https://docs.example.com"] });
  const toggle = screen.getByRole("switch", { name: "允许联网" });
  await waitFor(() => expect(toggle).toBeEnabled());
  expect(toggle).toHaveAttribute("aria-checked", "false");
  await userEvent.click(toggle);
  await waitFor(() => expect(putControl).toHaveBeenCalledTimes(2));
  expect(putControl.mock.calls[1][1]).toMatchObject({ browser_network_access: "restricted", browser_origins: ["https://docs.example.com"] });
});

it("keeps the optional allowlist inside collapsed advanced settings and saves exact sites", async () => {
  const { client, putControl, wrapper } = setup("internet");
  render(<V2BrowserNetworkControl client={client} target={runTarget} />, { wrapper });
  const advanced = await screen.findByText("高级设置");
  expect(advanced.closest("details")).not.toHaveAttribute("open");
  await userEvent.click(advanced);
  await userEvent.click(screen.getByRole("checkbox", { name: "仅指定网站" }));
  fireEvent.change(screen.getByRole("textbox", { name: "网站白名单" }), { target: { value: "https://docs.example.com\nhttps://app.example.com" } });
  await userEvent.click(screen.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(putControl).toHaveBeenCalledWith("/runs/run-1/browser-network", expect.objectContaining({
    browser_network_access: "restricted", browser_origins: ["https://docs.example.com", "https://app.example.com"],
  }), expect.any(String)));
});

it("saves the project preference before its first task", async () => {
  const { client, get, putControl, wrapper } = setup();
  render(<V2BrowserNetworkSettings client={client} threadID="" draftWorkspaceID="workspace-1" />, { wrapper });
  const toggle = await screen.findByRole("switch", { name: "允许联网" });
  await waitFor(() => expect(toggle).toBeEnabled());
  await userEvent.click(toggle);
  expect(get).toHaveBeenCalledWith("/workspaces/workspace-1/browser-network", {}, expect.any(AbortSignal));
  await waitFor(() => expect(putControl).toHaveBeenCalledExactlyOnceWith("/workspaces/workspace-1/browser-network", {
    version: "browser_network_access.v1", expected_revision: 0, browser_network_access: "internet", browser_origins: [],
  }, expect.any(String)));
});

it("saves the next-run selection through the current finished run endpoint", async () => {
  const { client, get, putControl, wrapper } = setup();
  get.mockImplementation(async (path) => path.startsWith("/threads/")
    ? { thread: { id: "thread-1", workspace_id: "workspace-1" }, last_run: { id: "run-1", status: "completed" } }
    : policy());
  putControl.mockImplementation(async (_path, body) => ({ ...policy(), ...body, revision: 2, replayed: false, for_next_run: true }));
  render(<V2BrowserNetworkSettings client={client} threadID="thread-1" />, { wrapper });
  const toggle = await screen.findByRole("switch", { name: "允许联网" });
  await waitFor(() => expect(toggle).toBeEnabled());
  expect(toggle).toHaveAttribute("aria-checked", "false");
  await userEvent.click(toggle);
  await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "true"));
  expect(putControl).toHaveBeenCalledExactlyOnceWith("/runs/run-1/browser-network", expect.objectContaining({
    expected_revision: 1, browser_network_access: "internet", remember_for_project: true,
  }), expect.any(String));
  expect(get.mock.calls.some(([path]) => path.startsWith("/workspaces/"))).toBe(false);
  await userEvent.click(toggle);
  await waitFor(() => expect(putControl).toHaveBeenCalledTimes(2));
  expect(putControl.mock.calls[1]).toEqual(["/runs/run-1/browser-network", expect.objectContaining({
    expected_revision: 2, browser_network_access: "off",
  }), expect.any(String)]);
});

it("displays a saved next-run selection returned by the run endpoint after reopening", async () => {
  const { client, get, putControl, wrapper } = setup();
  get.mockResolvedValue({ ...policy("internet"), revision: 2, for_next_run: true });
  render(<V2BrowserNetworkControl client={client} target={runTarget} />, { wrapper });
  const toggle = await screen.findByRole("switch", { name: "允许联网" });
  await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "true"));
  await userEvent.click(toggle);
  await waitFor(() => expect(putControl).toHaveBeenCalledExactlyOnceWith("/runs/run-1/browser-network", expect.objectContaining({
    expected_revision: 2, browser_network_access: "off", remember_for_project: true,
  }), expect.any(String)));
});

it("keeps a failed change visible and does not claim the network is enabled", async () => {
  const { client, putControl, wrapper } = setup();
  putControl.mockRejectedValue(new Error("Network preference could not be saved"));
  render(<V2BrowserNetworkControl client={client} target={runTarget} />, { wrapper });
  const toggle = await screen.findByRole("switch", { name: "允许联网" });
  await waitFor(() => expect(toggle).toBeEnabled());
  await userEvent.click(toggle);
  expect(await screen.findByRole("alert")).toHaveTextContent("Network preference could not be saved");
  expect(toggle).toHaveAttribute("aria-checked", "false"); expect(putControl).toHaveBeenCalledOnce();
});
