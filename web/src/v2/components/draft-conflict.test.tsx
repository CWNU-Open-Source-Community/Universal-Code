import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { fireEvent, render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { WorkspaceImageAttachment } from "../../api/image-attachments";
import type { DraftHead, DraftState } from "../draft-document";
import { V2DraftConflict } from "./draft-conflict";

const workspaceID = "workspace-original";
const image = (suffix: string): WorkspaceImageAttachment => ({ id: `image-${suffix}`, workspace_id: workspaceID,
  name: `${suffix}.png`, sha256: suffix.repeat(64), mime_type: "image/png", byte_size: 100, width: 60, height: 40 });
const heads: DraftHead[] = [
  { ref: { branchID: "branch-one", seq: 3 }, ownerID: "window-one", snapshot: {
    text: "  第一份原文\n\n保留尾部空白。  ", files: [{ id: "first-file", path: "src/same.ts", digest: "a".repeat(64), partial: false, redacted: false }], images: [image("a")] } },
  { ref: { branchID: "branch-two", seq: 5 }, ownerID: "window-two", snapshot: {
    text: `第二份完整正文\n${"中间内容".repeat(160)}\n最后一行`, files: [{ id: "second-file", path: "src/same.ts", digest: "b".repeat(64), partial: true, redacted: true }], images: [image("b")] } },
  { ref: { branchID: "branch-three", seq: 1 }, ownerID: "window-three", snapshot: { text: "", files: [], images: [] } },
];
function state(overrides: Partial<DraftState> = {}): DraftState {
  return { snapshot: heads[0].snapshot, ref: heads[0].ref, heads, conflict: true, headToken: "all-heads-token-1", error: null, persisted: true, ...overrides };
}
function client() {
  return { downloadWorkspaceImage: vi.fn(async () => new Blob(["png"], { type: "image/png" })),
    submitThreadTurn: vi.fn(), postControl: vi.fn() } as unknown as APIClient;
}
function setup(initial = state()) {
  const api = client(); const resolve = vi.fn();
  const view = render(<V2DraftConflict state={initial} client={api} workspaceID={workspaceID} onResolve={resolve} />);
  return { ...view, api, resolve, update: (next: DraftState) => view.rerender(
    <V2DraftConflict state={next} client={api} workspaceID={workspaceID} onResolve={resolve} />) };
}

beforeEach(() => {
  vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: vi.fn(() => "blob:verified-draft-image"), revokeObjectURL: vi.fn() }));
});
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("compares every complete version and selects its exact ref only after explicit confirmation", async () => {
  const { api, resolve } = setup(); const user = userEvent.setup();
  expect(screen.getByRole("status")).toHaveTextContent("3 个待核对版本");
  expect(screen.queryByRole("radio")).not.toBeInTheDocument();
  expect(api.downloadWorkspaceImage).not.toHaveBeenCalled();
  expect(resolve).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "查看版本" }));
  expect(screen.getAllByRole("radio")).toHaveLength(3);
  expect(screen.getByLabelText("版本 1完整正文").textContent).toBe(heads[0].snapshot.text);
  expect(screen.getByLabelText("版本 2完整正文").textContent).toBe(heads[1].snapshot.text);
  expect(screen.getAllByText(/src\/same\.ts/)).toHaveLength(2);
  expect(screen.getByText("SHA256 " + "a".repeat(64), { selector: ".v2-draft-conflict-version > ul code" })).toBeInTheDocument();
  expect(screen.getByText("SHA256 " + "b".repeat(64), { selector: ".v2-draft-conflict-version > ul code" })).toBeInTheDocument();
  expect(screen.getByText("此版本没有文字。")).toBeInTheDocument();
  expect(screen.getByText(/整份文字、文件引用、图片和附件/)).toBeVisible();
  await waitFor(() => expect(api.downloadWorkspaceImage).toHaveBeenCalledTimes(2));
  expect(api.downloadWorkspaceImage).toHaveBeenCalledWith(heads[0].snapshot.images[0], expect.any(AbortSignal));
  await screen.findByRole("img", { name: "a.png" });
  expect(screen.queryByRole("button", { name: /移除图片/ })).not.toBeInTheDocument();
  const confirm = screen.getByRole("button", { name: "确认选用此版本" });
  expect(confirm).toBeDisabled();
  await user.click(screen.getByRole("radio", { name: "版本 2" }));
  expect(resolve).not.toHaveBeenCalled();
  await user.click(confirm);
  expect(resolve).toHaveBeenCalledExactlyOnceWith("all-heads-token-1", heads[1].ref);
  expect(confirm).toBeDisabled();
  expect(api.submitThreadTurn).not.toHaveBeenCalled();
  expect(api.postControl).not.toHaveBeenCalled();
});

it("invalidates a selected version when the head token changes even if that branch remains", async () => {
  const { update, resolve } = setup(); const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "查看版本" }));
  await user.click(screen.getByRole("radio", { name: "版本 3" }));
  const laterHead: DraftHead = { ...heads[0], ref: { ...heads[0].ref, seq: 4 }, snapshot: { ...heads[0].snapshot, text: "原窗口又新增一行" } };
  update(state({ heads: [laterHead, heads[1], heads[2]], headToken: "all-heads-token-2" }));
  expect(screen.getByRole("radio", { name: "版本 3" })).not.toBeChecked();
  expect(screen.getByRole("button", { name: "确认选用此版本" })).toBeDisabled();
  expect(screen.getByText(/版本列表已变化/)).toBeVisible();
  expect(resolve).not.toHaveBeenCalled();
  await user.click(screen.getByRole("radio", { name: "版本 3" }));
  await user.click(screen.getByRole("button", { name: "确认选用此版本" }));
  expect(resolve).toHaveBeenCalledExactlyOnceWith("all-heads-token-2", heads[2].ref);
});

it("does not autofocus or resolve on arrival and keeps the selected version through manual disclosure", async () => {
  const api = client(); const resolve = vi.fn(); const user = userEvent.setup();
  const view = render(<><textarea aria-label="当前草稿" defaultValue="继续编辑" />
    <V2DraftConflict state={state({ conflict: false, heads: [heads[0]] })} client={api} workspaceID={workspaceID} onResolve={resolve} /></>);
  const editor = screen.getByRole("textbox", { name: "当前草稿" }); editor.focus();
  view.rerender(<><textarea aria-label="当前草稿" defaultValue="继续编辑" />
    <V2DraftConflict state={state()} client={api} workspaceID={workspaceID} onResolve={resolve} /></>);
  expect(editor).toHaveFocus();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "查看版本" }));
  await user.click(screen.getByRole("radio", { name: "版本 1" }));
  await user.click(screen.getByRole("button", { name: "收起版本" }));
  expect(screen.getByRole("status")).toHaveTextContent("3 个待核对版本");
  await user.click(screen.getByRole("button", { name: "查看版本" }));
  expect(screen.getByRole("radio", { name: "版本 1" })).toBeChecked();
  expect(resolve).not.toHaveBeenCalled();
});

it("copies the exact untrimmed full text and reports denied clipboard access without resolving", async () => {
  const { resolve } = setup(); const user = userEvent.setup();
  const copy = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
  await user.click(screen.getByRole("button", { name: "查看版本" }));
  await user.click(screen.getByRole("button", { name: "复制版本 1正文" }));
  expect(copy).toHaveBeenCalledExactlyOnceWith(heads[0].snapshot.text);
  await screen.findByText("已复制版本 1的完整正文。");
  copy.mockRejectedValueOnce(new Error("denied"));
  await user.click(screen.getByRole("button", { name: "复制版本 2正文" }));
  await screen.findByText("未能复制，请选中正文后手动复制；版本内容未改变。");
  expect(screen.getByLabelText("版本 2完整正文").textContent).toBe(heads[1].snapshot.text);
  expect(resolve).not.toHaveBeenCalled();
});

it("shows save failure separately without promising reload safety or locking the local editor", async () => {
  const api = client(); const resolve = vi.fn();
  render(<><textarea aria-label="当前草稿" defaultValue="尚未保存的内容" />
    <V2DraftConflict state={state({ conflict: false, error: "设备存储空间不足", persisted: false })}
      client={api} workspaceID={workspaceID} onResolve={resolve} /></>);
  const alert = screen.getByRole("alert");
  expect(alert).toHaveTextContent("草稿尚未安全保存");
  expect(alert).toHaveTextContent("刷新或关闭可能丢失未保存的修改");
  expect(alert).toHaveTextContent("设备存储空间不足");
  expect(screen.queryByRole("button", { name: "查看版本" })).not.toBeInTheDocument();
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "本页继续写下的内容" } });
  expect(screen.getByRole("textbox")).toHaveValue("本页继续写下的内容");
  expect(resolve).not.toHaveBeenCalled();
});

it("retains all versions when exact resolution fails and never substitutes another branch", async () => {
  const { resolve } = setup(); const user = userEvent.setup();
  resolve.mockImplementation(() => { throw new Error("版本已变化，请重新核对。"); });
  await user.click(screen.getByRole("button", { name: "查看版本" }));
  await user.click(screen.getByRole("radio", { name: "版本 2" }));
  await user.click(screen.getByRole("button", { name: "确认选用此版本" }));
  expect(screen.getByRole("alert")).toHaveTextContent("版本已变化，请重新核对。");
  expect(screen.getAllByRole("radio")).toHaveLength(3);
  expect(screen.getByRole("radio", { name: "版本 2" })).toBeChecked();
  expect(resolve).toHaveBeenCalledExactlyOnceWith("all-heads-token-1", heads[1].ref);
});

it("does not fetch a thumbnail for an image outside the current workspace", async () => {
  const outside = { ...heads[0], snapshot: { ...heads[0].snapshot,
    images: [{ ...image("c"), workspace_id: "workspace-other" }] } };
  const { api } = setup(state({ heads: [outside, heads[2]] }));
  await userEvent.setup().click(screen.getByRole("button", { name: "查看版本" }));
  expect(screen.getByText(/其他项目的图片，未读取其预览/)).toBeVisible();
  expect(api.downloadWorkspaceImage).not.toHaveBeenCalled();
  const version = screen.getByRole("radio", { name: "版本 1" }).closest("article")!;
  expect(within(version).getByText("c.png · 60 × 40")).toBeInTheDocument();
});

function render(ui: ReactElement, options?: RenderOptions) {
 window.localStorage.setItem("prayu.locale.v1", "zh-CN");
 const Wrapper=options?.wrapper;
 return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
