import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { act, cleanup, fireEvent, render as renderComponent, renderHook, screen, waitFor } from "@testing-library/react";
import { File as NodeFile } from "node:buffer";
import { webcrypto } from "node:crypto";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { APIRequestError, type APIClient } from "../../api/client";
import type { AvailableModelRouteView } from "../../api/types";
import type { WorkspaceImageAttachment } from "../../api/image-attachments";
import { V2Composer } from "./composer";
import { v2ImageReferenceKey } from "./image-input";
import type { V2PendingModelRoute } from "./model-route-control";
import { V2RecoveryProvider, useV2RecoveryStore } from "../recovery-storage";
import { recoveryImagesKey, recoveryTurnKey, settleRecoveryTurn, validRecoveryTurn } from "../recovery-session";
import type { V2TurnInput } from "../use-thread-turn";

const image: WorkspaceImageAttachment = { id: "image-original", workspace_id: "workspace-images", sha256: "a".repeat(64),
  mime_type: "image/png", byte_size: 128, width: 64, height: 64, name: "布局.png" };
const nextImage = { ...image, id: "image-later", sha256: "b".repeat(64), name: "后续.png" };
const fixture = (state = "supported") => ({ baseURL: "/api/v1", hasThreadControl: true, hasModelControl: true,
  availableModelRoutes: vi.fn(async () => ({ routes: [{ provider_id: "fixture", model: "vision", selectable: true, default_for_routes: ["code"],
    vision_capability: { state, source: "operator_declared" } }] })),
  downloadWorkspaceImage: vi.fn(async () => new Blob(["png"], { type: "image/png" })),
  uploadWorkspaceImage: vi.fn(async () => image),
  uploadWorkspaceFile: vi.fn(async (workspace: string, file: File) => ({ id: "stored-file", workspace_id: workspace,
    name: file.name, mime_type: file.type, sha256: "d".repeat(64), byte_size: file.size,
    readability: "stored_only", text_bytes: 0, redacted: false })),
} as unknown as APIClient);

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal("File", NodeFile);
  vi.stubGlobal("crypto", webcrypto);
  vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: vi.fn(() => "blob:verified-image"), revokeObjectURL: vi.fn() }));
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

function mount(client: APIClient, initial: WorkspaceImageAttachment[] = [], submit = vi.fn(async () => {}), pendingModelRoute?: V2PendingModelRoute) {
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  queries.setQueryData(v2ImageReferenceKey(image.workspace_id, ""), initial);
  const view = render(<QueryClientProvider client={queries}><V2Composer client={client} workspaceID={image.workspace_id}
    workspaces={[]} threadID="" pendingModelRoute={pendingModelRoute} onWorkspaceChange={() => {}} onSubmit={submit} /></QueryClientProvider>);
  return { ...view, queries, submit };
}

it("sends an image-only message with the exact original reference and keeps later attachments", async () => {
  let finish!: () => void;
  const submit = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
  const page = mount(fixture(), [image], submit);
  await waitFor(() => expect(screen.getByRole("button", { name: "发送消息" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "发送消息" }));
  expect(submit).toHaveBeenCalledWith("", [], [image]);
  act(() => page.queries.setQueryData(v2ImageReferenceKey(image.workspace_id, ""), [image, nextImage]));
  fireEvent.change(screen.getByRole("textbox", { name: "开始新对话" }), { target: { value: "新的要求" } });
  await act(async () => finish());
  expect(screen.getByRole("textbox", { name: "开始新对话" })).toHaveValue("新的要求");
  expect(page.queries.getQueryData(v2ImageReferenceKey(image.workspace_id, ""))).toEqual([nextImage]);
});

const visionRoute: AvailableModelRouteView = { provider_id: "ready-provider", provider_name: "Ready Provider", model: "vision",
  definition_revision: 1, enabled: true, credential_status: "configured", qualification_status: "available",
  harness_ready: true, selectable: true, unavailable_reason: "", default_for_routes: [],
  vision_capability: { state: "supported", source: "operator_declared" } };
const textRoute: AvailableModelRouteView = { ...visionRoute, provider_id: "text-provider", model: "text",
  vision_capability: { state: "unsupported", source: "operator_declared" } };

it.each([
  { name: "a custom vision route without a named default", routes: [visionRoute], allowed: true },
  { name: "an unavailable text default and a ready vision fallback", routes: [
    { ...textRoute, enabled: false, selectable: false, unavailable_reason: "provider_disabled" as const, default_for_routes: ["code"] }, visionRoute,
  ], allowed: true },
  { name: "an unavailable vision default and a ready text fallback", routes: [
    { ...visionRoute, enabled: false, selectable: false, unavailable_reason: "provider_disabled" as const, default_for_routes: ["code"] }, textRoute,
  ], allowed: false },
  { name: "a selectable named default after another ready route", routes: [
    textRoute, { ...visionRoute, default_for_routes: ["code"] },
  ], allowed: true },
])("checks image sending against the creation model for $name", async ({ routes, allowed }) => {
  const client = fixture();
  vi.mocked(client.availableModelRoutes).mockResolvedValue({ protocol_version: "model_route_catalog.v1", generation: 1, routes });
  const page = mount(client, [image]);
  await screen.findByText(allowed ? /将发送原始图片/ : /当前模型不支持图片/);
  const send = screen.getByRole("button", { name: "发送消息" });
  if (allowed) {
    expect(send).toBeEnabled();
    fireEvent.click(send);
    expect(page.submit).toHaveBeenCalledWith("", [], [image]);
  } else {
    expect(send).toBeDisabled();
    fireEvent.click(send);
    expect(page.submit).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "移除图片 布局.png" })).toBeInTheDocument();
  }
});

it.each(["text", "missing"])("keeps the explicit %s model identity instead of using a vision fallback", async (model) => {
  const client = fixture();
  vi.mocked(client.availableModelRoutes).mockResolvedValue({ protocol_version: "model_route_catalog.v1", generation: 1,
    routes: [{ ...visionRoute, default_for_routes: ["code"] }, textRoute] });
  const page = mount(client, [image], vi.fn(async () => {}), { provider: textRoute.provider_id, model });
  await screen.findByText(model === "text" ? /当前模型不支持图片/ : /当前模型尚未声明图片能力/);
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  expect(page.submit).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "移除图片 布局.png" })).toBeInTheDocument();
});

it.each(["unknown", "unsupported"])("keeps %s model images visible and blocks silent text-only fallback", async (state) => {
  const page = mount(fixture(state), [image]);
  fireEvent.change(screen.getByRole("textbox", { name: "开始新对话" }), { target: { value: "分析布局" } });
  await screen.findByText(state === "unknown" ? /当前模型尚未声明图片能力/ : /当前模型不支持图片/);
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  expect(page.submit).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "移除图片 布局.png" })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "移除图片 布局.png" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "发送消息" })).toBeEnabled());
});

it("pastes actual images and leaves ordinary text paste unclaimed", async () => {
  const client = fixture(); mount(client);
  const input = screen.getByRole("textbox", { name: "开始新对话" });
  const ordinary = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(ordinary, "clipboardData", { value: { files: [] } });
  fireEvent(input, ordinary);
  expect(ordinary.defaultPrevented).toBe(false);
  const file = new File(["real test bytes"], "截图.png", { type: "image/png" });
  fireEvent.paste(input, { clipboardData: { files: [file] } });
  await waitFor(() => expect(client.uploadWorkspaceImage).toHaveBeenCalledWith(image.workspace_id, file, expect.stringMatching(/^v2-image-upload-/)));
  await screen.findByRole("button", { name: "移除图片 布局.png" });
});

it("isolates pending uploads and late failures when the same composer changes workspace", async () => {
  const client = fixture();
  let failUpload!: (failure: Error) => void;
  vi.mocked(client.uploadWorkspaceImage).mockImplementationOnce(() => new Promise<WorkspaceImageAttachment>((_resolve, reject) => {
    failUpload = reject;
  }));
  const page = mount(client);
  const input = screen.getByRole("textbox", { name: "开始新对话" });
  fireEvent.change(input, { target: { value: "切换项目时仍在输入的草稿" } });
  const file = new File(["pending image bytes"], "原项目截图.png", { type: "image/png" });
  fireEvent.paste(input, { clipboardData: { files: [file] } });
  await waitFor(() => expect(client.uploadWorkspaceImage).toHaveBeenCalledWith(image.workspace_id, file, expect.stringMatching(/^v2-image-upload-/)));
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  // The shared menu stays available; only its upload action is busy.
  fireEvent.click(screen.getByRole("button", { name: "添加附件" }));
  const upload = screen.getByRole("menuitem", { name: "添加图片或文件" });
  expect(upload).toBeDisabled();
  fireEvent.click(upload);
  expect(client.uploadWorkspaceImage).toHaveBeenCalledTimes(1);
  expect(screen.getByText("正在保存图片，完成后可发送…")).toBeInTheDocument();
  expect(client.uploadWorkspaceImage).toHaveBeenCalledWith(image.workspace_id, file, expect.stringMatching(/^v2-image-upload-/));

  page.rerender(<QueryClientProvider client={page.queries}><V2Composer client={client} workspaceID="workspace-new"
    workspaces={[]} threadID="" onWorkspaceChange={() => {}} onSubmit={page.submit} /></QueryClientProvider>);
  expect(screen.getByRole("textbox", { name: "开始新对话" })).toBe(input);
  expect(input).toHaveValue("切换项目时仍在输入的草稿");
  expect(screen.getByRole("button", { name: "添加附件" })).toBeEnabled();
  expect(screen.queryByRole("menu", { name: "添加内容" })).not.toBeInTheDocument();
  expect(screen.queryByText("正在保存图片，完成后可发送…")).not.toBeInTheDocument();
  await waitFor(() => expect(screen.getByRole("button", { name: "发送消息" })).toBeEnabled());

  await act(async () => failUpload(new Error("原项目图片上传失败")));
  expect(screen.queryByText("原项目图片上传失败")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "添加附件" })).toBeEnabled();
  expect(screen.getByRole("button", { name: "发送消息" })).toBeEnabled();
  expect(input).toHaveValue("切换项目时仍在输入的草稿");
  expect(client.uploadWorkspaceImage).toHaveBeenCalledTimes(1);
  expect(page.submit).not.toHaveBeenCalled();
});

it.each([true, false])("allows correcting a known rejection but locks an unknown request (known: %s)", async (known) => {
  const page = mount(fixture(), [image]);
  const input = { threadID: "", workspaceID: image.workspace_id, content: "", images: [image],
    operationKey: "original-image-intent", createdAt: new Date().toISOString() };
  const error = known ? new APIRequestError("模型不支持图片", "FAILED_PRECONDITION", 409, "rejected", false) : new Error("响应丢失");
  act(() => page.queries.getMutationCache().build(page.queries, { mutationKey: ["v2", "submit-turn"], gcTime: Infinity }, {
    context: undefined, data: undefined, variables: input, status: "error", isPaused: false,
    failureCount: 1, failureReason: error, error, submittedAt: Date.now(),
  }));
  await waitFor(() => known ? expect(screen.getByRole("button", { name: "移除图片 布局.png" })).toBeEnabled()
    : expect(screen.getByRole("button", { name: "移除图片 布局.png" })).toBeDisabled());
  if (known) {
    fireEvent.click(screen.getByRole("button", { name: "移除图片 布局.png" }));
    await waitFor(() => expect(page.queries.getQueryData(v2ImageReferenceKey(image.workspace_id, ""))).toEqual([]));
  }
});

it("keeps SVG as a stored file without image decoding and rejects an over-limit image before upload", async () => {
  const client = fixture(); mount(client);
  const picker = screen.getByLabelText("选择图片或文件");
  fireEvent.change(picker, { target: { files: [new File(["<svg/>"], "image.svg", { type: "image/svg+xml" })] } });
  await screen.findByRole("button", { name: "移除文件 image.svg" });
  expect(screen.getByText("6 B")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "下载文件 image.svg" })).toBeInTheDocument();
  expect(client.uploadWorkspaceFile).toHaveBeenCalledTimes(1);
  const large = new File(["tiny"], "large.png", { type: "image/png" });
  Object.defineProperty(large, "size", { value: 5 * 1024 * 1024 + 1 });
  fireEvent.change(picker, { target: { files: [large] } });
  expect(await screen.findByRole("alert")).toHaveTextContent("每张不超过 5 MiB");
  expect(client.uploadWorkspaceImage).not.toHaveBeenCalled();
});

it("recovers an unknown image-only request and clears only original image identities", () => {
  const input: V2TurnInput = { threadID: "thread-images", workspaceID: image.workspace_id, content: "", draft: "",
    images: [image], operationKey: "original-image-key", createdAt: new Date().toISOString() };
  expect(validRecoveryTurn(input)).toBe(true);
  expect(validRecoveryTurn({ ...input, images: [{ ...image, workspace_id: "another-project" }] })).toBe(false);
  const wrapper = ({ children }: { children: ReactNode }) => <V2RecoveryProvider client={{ baseURL: "/api/v1" }} scopeID="images-test">{children}</V2RecoveryProvider>;
  const first = renderHook(() => useV2RecoveryStore(), { wrapper });
  first.result.current!.write(recoveryTurnKey(input), input);
  first.result.current!.write(recoveryImagesKey(input.workspaceID, input.threadID), [image, nextImage]);
  first.result.current!.write(`draft:thread:${input.threadID}`, "后续修改");
  first.unmount();
  const restored = renderHook(() => useV2RecoveryStore(), { wrapper });
  expect(restored.result.current!.read(recoveryTurnKey(input), null)).toEqual(input);
  settleRecoveryTurn(restored.result.current, input, true);
  expect(restored.result.current!.read(recoveryImagesKey(input.workspaceID, input.threadID), [])).toEqual([nextImage]);
  expect(restored.result.current!.read(`draft:thread:${input.threadID}`, "")).toBe("后续修改");
  expect(restored.result.current!.read(recoveryTurnKey(input), null)).toBeNull();
  expect([...Array(localStorage.length)].map((_, index) => localStorage.getItem(localStorage.key(index)!)).join("")).not.toContain("base64");
});

it("merges the image capability and local draft hints into one caption line", async () => {
  const client = fixture();
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={queries}>
    <V2RecoveryProvider client={{ baseURL: "/api/v1" }} scopeID="composer-caption-merge">
      <V2Composer client={client} workspaceID={image.workspace_id}
        workspaces={[]} threadID="" onWorkspaceChange={() => {}} onSubmit={vi.fn(async () => {})} />
    </V2RecoveryProvider>
  </QueryClientProvider>);
  const input = screen.getByRole("textbox", { name: "开始新对话" });
  const file = new File(["caption merge bytes"], "合并提示.png", { type: "image/png" });
  fireEvent.paste(input, { clipboardData: { files: [file] } });
  await screen.findByRole("button", { name: "移除图片 布局.png" });
  fireEvent.change(input, { target: { value: "看图说话" } });
  const caption = await screen.findByText(/将发送原始图片/);
  expect(caption).toHaveTextContent("草稿与附件保存在此设备");
  const hintLines = screen.getAllByRole("status").filter((el) =>
    el.classList.contains("v2-composer-caption") && /图片|草稿/.test(el.textContent ?? ""));
  expect(hintLines).toHaveLength(1);
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => localStorage.setItem("universal-code.composer-plain-text", "true"));

function render(ui: ReactElement, options?: RenderOptions) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  return renderComponent(ui, { wrapper: LocaleProvider, ...options });
}
