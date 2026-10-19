import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { APIRequestError, type APIClient } from "../../api/client";
import { LocaleProvider } from "../../lib/locale";
import { preparationOperation, preparationView } from "../../test/project-preparation";
import { ProjectPreparation, ProjectPreparationPanel } from "./project-preparation";
import { V2RecoveryProvider } from "../recovery-storage";

function setup() {
  const client = { baseURL: "/api/v1", hasControl: true, hasRunExecution: true,
    getProjectPreparation: vi.fn().mockImplementation((runID: string, target: string) => Promise.resolve(preparationView({ run_id: runID, target }))),
    prepareProject: vi.fn().mockResolvedValue(preparationView({ latest: preparationOperation() })),
    getProjectPreparationOperation: vi.fn().mockImplementation((runID: string) => Promise.resolve(preparationOperation({ run_id: runID }))),
    cancelProjectPreparation: vi.fn().mockResolvedValue(preparationOperation({ state: "cancelled" })),
  };
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const ui = (runID = "run-1", working = false) => <LocaleProvider><QueryClientProvider client={queries}>
    <V2RecoveryProvider client={client} scopeID="preparation-db">
      <ProjectPreparation client={client as unknown as APIClient} runID={runID} threadID="thread-1" working={working} />
    </V2RecoveryProvider>
  </QueryClientProvider></LocaleProvider>;
  return { client, queries, ui };
}
beforeEach(() => localStorage.clear());

it("explains shared pnpm installation scope while keeping tests and consent on the selected package", async () => {
  const { client, ui } = setup();
  const initial = preparationView();
  client.getProjectPreparation.mockImplementation((runID: string, target: string) => Promise.resolve(preparationView({ run_id: runID, target,
    backend: "docker_standard_code", plan: { ...initial.plan, ecosystem: "pnpm", toolchain: "pnpm", workspace_root: ".",
      install_command: "pnpm install --offline --frozen-lockfile --ignore-scripts", test_command: "pnpm test" } })));
  render(ui());
  fireEvent.change(await screen.findByRole("textbox", { name: /项目目录/ }), { target: { value: "apps/web" } });
  fireEvent.click(screen.getByRole("button", { name: "检测目录" }));
  await screen.findByText(/将准备整个工作区的依赖/);
  expect(screen.getByText("apps/web", { selector: "code" })).toBeInTheDocument();
  expect(screen.getByText("pnpm test")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("checkbox", { name: "运行依赖的安装脚本（在Docker中）" }));
  expect(screen.getByText(/pnpm --recursive rebuild --pending/)).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "准备项目" }));
  await waitFor(() => expect(client.prepareProject).toHaveBeenCalledWith("run-1", expect.objectContaining({ target: "apps/web", run_install_scripts: true }), expect.any(String)));
});

it("canonicalizes newly selected package sources before persisting and submitting consent", async () => {
  const { client, ui } = setup();
  const sources = ["https://proxy.golang.org", "https://storage.googleapis.com"];
  const baseline = preparationView();
  client.getProjectPreparation.mockResolvedValue({ ...baseline, plan: { ...baseline.plan, ecosystem: "go", sources } });
  render(ui());
  fireEvent.click(await screen.findByRole("checkbox", { name: "允许下载：https://storage.googleapis.com" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "允许下载：https://proxy.golang.org" }));
  fireEvent.click(screen.getByRole("button", { name: "准备项目" }));
  await waitFor(() => expect(client.prepareProject).toHaveBeenCalledTimes(1));
  expect(client.prepareProject.mock.calls[0][1].allowed_sources).toEqual(sources);
  const stored = Object.keys(localStorage).find((key) => key.includes("project-preparation"));
  expect(JSON.parse(localStorage.getItem(stored!)!).value.body.allowed_sources).toEqual(sources);
});

it("replays recovered consent with its original source ordering", async () => {
  const { client, ui } = setup();
  const original = { version: "project-preparation.v1", target: ".", expected_fingerprint: "a".repeat(64),
    allowed_sources: ["https://storage.googleapis.com", "https://proxy.golang.org"],
    confirm_execution: true, run_install_scripts: false };
  const storageKey = `v2_recovery.v1:${encodeURIComponent(window.location.origin)}:${encodeURIComponent(client.baseURL)}:preparation-db:${encodeURIComponent("run:run-1:project-preparation")}`;
  localStorage.setItem(storageKey, JSON.stringify({ version: "v2_recovery.v1", value: {
    runID: "run-1", key: "project-preparation-original", body: original,
  } }));
  render(ui());
  fireEvent.click(await screen.findByRole("button", { name: "确认上次准备" }));
  await waitFor(() => expect(client.prepareProject).toHaveBeenCalledWith("run-1", original, "project-preparation-original"));
});

it("shows detected commands and submits only the selected package sources and installation script consent", async () => {
  const { client, ui } = setup();
  render(ui());
  const prepare = await screen.findByRole("button", { name: "准备项目" });
  expect(screen.getByText("Node.js 24 / npm")).toBeInTheDocument();
  expect(screen.getByText("npm ci --offline --ignore-scripts=true")).toBeInTheDocument();
  expect(screen.getByText("npm test")).toBeInTheDocument();
  const download = screen.getByRole("checkbox", { name: "允许下载：https://registry.npmjs.org" });
  const scripts = screen.getByRole("checkbox", { name: "运行依赖的安装脚本（在本地沙箱中）" });
  expect(download).not.toBeChecked();
  expect(scripts).not.toBeChecked();
  fireEvent.click(download);
  fireEvent.click(scripts);
  expect(screen.getByText("npm ci --offline --ignore-scripts=true npm rebuild --offline")).toBeInTheDocument();
  fireEvent.click(prepare);
  await screen.findByText("项目已准备");
  expect(client.prepareProject).toHaveBeenCalledWith("run-1", {
    version: "project-preparation.v1", target: ".", expected_fingerprint: "a".repeat(64),
    allowed_sources: ["https://registry.npmjs.org"], run_install_scripts: true, confirm_execution: true,
  }, expect.stringMatching(/^project-preparation-/));
  expect(screen.getByLabelText("依赖获取进度")).toHaveAttribute("value", "2");
  expect(client.getProjectPreparationOperation).toHaveBeenCalledWith("run-1", "prep-1", expect.any(AbortSignal));
});

it("keeps downloads off for cache-only preparation and allows detection on read-only connections", async () => {
  const { client, ui } = setup();
  const { rerender } = render(ui());
  fireEvent.click(await screen.findByRole("button", { name: "准备项目" }));
  await waitFor(() => expect(client.prepareProject).toHaveBeenCalledTimes(1));
  expect(client.prepareProject.mock.calls[0][1]).toMatchObject({ allowed_sources: [], run_install_scripts: false });
  await screen.findByText("项目已准备");
  client.hasControl = false;
  rerender(ui());
  expect(screen.getByRole("button", { name: "准备项目" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "检测目录" })).toBeEnabled();
});

it("preserves the original request across an unknown outcome and a changed detection result", async () => {
  const { client, ui } = setup();
  client.prepareProject.mockRejectedValueOnce(new TypeError("connection lost"));
  render(ui());
  fireEvent.click(await screen.findByRole("checkbox", { name: "允许下载：https://registry.npmjs.org" }));
  fireEvent.click(screen.getByRole("button", { name: "准备项目" }));
  await screen.findByRole("button", { name: "确认上次准备" });
  expect(screen.getByRole("button", { name: "准备项目" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "检测目录" })).toBeDisabled();
  expect(client.prepareProject).toHaveBeenCalledTimes(1);
  const original = client.prepareProject.mock.calls[0];
  client.getProjectPreparation.mockResolvedValue(preparationView({ plan: { ...preparationView().plan, fingerprint: "d".repeat(64) } }));
  fireEvent.click(screen.getByRole("button", { name: "刷新检测" }));
  await waitFor(() => expect(screen.getByRole("checkbox", { name: "允许下载：https://registry.npmjs.org" })).not.toBeChecked());
  fireEvent.click(screen.getByRole("button", { name: "确认上次准备" }));
  await screen.findByText("项目已准备");
  expect(client.prepareProject).toHaveBeenLastCalledWith(...original);
});

it("restores an unresolved request after reopening and reconciles it even when detection cannot be read", async () => {
  const { client, queries, ui } = setup();
  client.prepareProject.mockRejectedValueOnce(new TypeError("response lost"));
  const first = render(ui());
  fireEvent.click(await screen.findByRole("button", { name: "准备项目" }));
  await screen.findByRole("button", { name: "确认上次准备" });
  const original = client.prepareProject.mock.calls[0];
  first.unmount();
  queries.clear();
  client.getProjectPreparation.mockRejectedValue(new TypeError("detection unavailable"));
  render(ui());
  await screen.findByText("detection unavailable");
  expect(client.prepareProject).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "确认上次准备" }));
  await screen.findByText("项目已准备");
  expect(client.prepareProject).toHaveBeenLastCalledWith(...original);
});

it("cancels the displayed operation and makes a new request only after a terminal failure", async () => {
  const { client, ui } = setup();
  const running = preparationOperation({ state: "downloading", completed_packages: 1, can_cancel: true, job: undefined });
  client.prepareProject.mockResolvedValueOnce(preparationView({ latest: running }));
  client.getProjectPreparationOperation.mockResolvedValue(running);
  render(ui());
  fireEvent.click(await screen.findByRole("button", { name: "准备项目" }));
  const cancel = await screen.findByRole("button", { name: "取消准备" });
  expect(screen.getByRole("button", { name: "准备项目" })).toBeDisabled();
  const failed = preparationOperation({ state: "failed", error: "依赖校验失败，请刷新锁文件。", job: {
    ...preparationOperation().job!, state: "failed", exit_code: 1, stderr: "integrity mismatch",
  } });
  client.getProjectPreparationOperation.mockResolvedValue(failed);
  client.cancelProjectPreparation.mockResolvedValue(failed);
  fireEvent.click(cancel);
  await screen.findByText("integrity mismatch");
  expect(screen.getByText("依赖校验失败，请刷新锁文件。")).toBeInTheDocument();
  expect(client.cancelProjectPreparation).toHaveBeenCalledWith("run-1", "prep-1", expect.stringMatching(/^project-preparation-cancel-/));
  const originalKey = client.prepareProject.mock.calls[0][2];
  fireEvent.click(screen.getByRole("button", { name: "重新准备项目" }));
  await waitFor(() => expect(client.prepareProject).toHaveBeenCalledTimes(2));
  expect(client.prepareProject.mock.calls[1][2]).not.toBe(originalKey);
});

it("allows correcting a rejected target and prevents preparation while the agent is working", async () => {
  const { client, ui } = setup();
  client.prepareProject.mockRejectedValueOnce(new APIRequestError("锁文件已更改，请重新检测。", "CONFLICT", 409));
  const { rerender } = render(ui());
  await screen.findByRole("button", { name: "准备项目" });
  fireEvent.change(screen.getByRole("textbox", { name: /项目目录/ }), { target: { value: "./web/" } });
  fireEvent.click(screen.getByRole("button", { name: "检测目录" }));
  await waitFor(() => expect(client.getProjectPreparation).toHaveBeenCalledWith("run-1", "web", expect.any(AbortSignal)));
  fireEvent.click(await screen.findByRole("button", { name: "准备项目" }));
  await screen.findByText("锁文件已更改，请重新检测。");
  await waitFor(() => expect(screen.getByRole("button", { name: "准备项目" })).toBeEnabled());
  expect(screen.queryByRole("button", { name: "确认上次准备" })).not.toBeInTheDocument();
  rerender(ui("run-1", true));
  expect(screen.getByRole("button", { name: "准备项目" })).toBeDisabled();
});

it("keeps a late response bound to the original Run when the selected Run changes", async () => {
  const { client, ui } = setup();
  let finish!: (value: ReturnType<typeof preparationView>) => void;
  client.prepareProject.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  const { rerender } = render(ui());
  fireEvent.click(await screen.findByRole("button", { name: "准备项目" }));
  await waitFor(() => expect(client.prepareProject).toHaveBeenCalledTimes(1));
  rerender(ui("run-2"));
  await screen.findByRole("button", { name: "准备项目" });
  await act(async () => finish(preparationView({ latest: preparationOperation() })));
  expect(client.getProjectPreparationOperation.mock.calls.every(([runID]) => runID === "run-1")).toBe(true);
  expect(screen.queryByText("项目已准备")).not.toBeInTheDocument();
});

it("keeps a completed preparation attached to its directory when another directory is inspected", async () => {
  const { client, ui } = setup();
  render(ui());
  fireEvent.click(await screen.findByRole("button", { name: "准备项目" }));
  await screen.findByText("项目已准备");
  fireEvent.change(screen.getByRole("textbox", { name: /项目目录/ }), { target: { value: "web" } });
  fireEvent.click(screen.getByRole("button", { name: "检测目录" }));
  await waitFor(() => expect(client.getProjectPreparation).toHaveBeenCalledWith("run-1", "web", expect.any(AbortSignal)));
  await screen.findByRole("button", { name: "准备项目" });
  expect(screen.queryByRole("region", { name: "项目准备进度" })).not.toBeInTheDocument();
});

it("displays actionable detection failures without offering an unavailable preparation", () => {
  const onPrepare = vi.fn();
  const view = preparationView({ available: false, plan: { ...preparationView().plan, ready: false,
    issues: ["缺少 package-lock.json，请先生成锁文件。"] } });
  render(<LocaleProvider><ProjectPreparationPanel view={view} onPrepare={onPrepare}
    onCancel={vi.fn()} onReconcile={vi.fn()} onReadOperation={vi.fn()} /></LocaleProvider>);
  expect(screen.getByText("缺少 package-lock.json，请先生成锁文件。")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "准备项目" })).toBeDisabled();
  expect(screen.getByRole("checkbox", { name: "允许下载：https://registry.npmjs.org" })).toBeDisabled();
});
