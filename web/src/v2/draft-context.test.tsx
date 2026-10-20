import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../lib/locale";
import { useRef, useState } from "react";
import { act, cleanup, fireEvent, render as renderComponent, within, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { APIClient } from "../api/client";
import type { WorkspaceImageAttachment } from "../api/image-attachments";
import { V2RecoveryProvider, useV2RecoveryStore } from "./recovery-storage";
import { requireV2DraftVersion, useV2DraftDocument } from "./draft-context";
import { recoveryTurnKey, settleRecoveryTurn } from "./recovery-session";
import { useV2FileReferences, type V2FileReference } from "./components/file-context";
import { useV2ImageInput } from "./components/image-input";
import { V2DraftConflict } from "./components/draft-conflict";
import type { DraftSnapshot, DraftState } from "./draft-document";
import type { V2TurnInput } from "./use-thread-turn";

const workspaceID = "workspace-one";
const threadID = "thread-one";
const backendID = `ds1_${"a".repeat(64)}`;
const selectedFile = (name: string): V2FileReference => ({ id: `file-${name}`, path: `${name}.md`, digest: name.repeat(64), partial: false, redacted: false });
const selectedImage = (name: string): WorkspaceImageAttachment => ({ id: `image-${name}`, workspace_id: workspaceID,
  sha256: name.repeat(64), mime_type: "image/png", byte_size: 12, width: 2, height: 3, name: `${name}.png` });
const noAttachments = (text: string): DraftSnapshot => ({ text, files: [], images: [] });

function client() {
  return {
    baseURL: "/api/v1",
    uploadWorkspaceImage: vi.fn().mockResolvedValue(selectedImage("c")),
    // Image pixel transport is not under test here. Failure leaves the actual
    // component's metadata and per-version selection available for inspection.
    downloadWorkspaceImage: vi.fn().mockRejectedValue(new Error("fixture preview unavailable")),
    submitThreadTurn: vi.fn(),
  };
}
type TestClient = ReturnType<typeof client>;

function Probe({ name, api, initialSubmission, onSubmission }: {
  name: string; api: TestClient; initialSubmission?: V2TurnInput; onSubmission?: (input: V2TurnInput) => void;
}) {
  const draft = useV2DraftDocument(workspaceID, threadID)!;
  const files = useV2FileReferences(workspaceID, threadID);
  const images = useV2ImageInput({ client: api as unknown as APIClient, workspaceID, threadID, disabled: false });
  const recovery = useV2RecoveryStore()!;
  const sent = useRef<V2TurnInput | undefined>(initialSubmission);
  const [notice, setNotice] = useState("");
  const snapshot = { text: draft.state.snapshot.text, files: files.files, images: images.images };
  return <section aria-label={`window-${name}`}>
    <textarea aria-label="草稿正文" value={snapshot.text} onChange={(event) => draft.changeText(event.target.value)} />
    <button onClick={() => files.update(() => [selectedFile(name)])}>选择文件</button>
    <button onClick={() => images.update(() => [selectedImage(name)])}>选择图片引用</button>
    {images.button}
    <output data-testid="snapshot">{JSON.stringify(snapshot)}</output>
    <output data-testid="state">{JSON.stringify(draft.state)}</output>
    <output data-testid="uploading">{String(images.uploading)}</output>
    <button onClick={() => {
      try {
        const draftVersion = requireV2DraftVersion(draft, snapshot);
        const input: V2TurnInput = { workspaceID, threadID, content: snapshot.text.trim(), draft: snapshot.text,
          files: snapshot.files, images: snapshot.images, draftVersion,
          operationKey: `submission-${crypto.randomUUID()}`, createdAt: new Date().toISOString() };
        recovery.write(recoveryTurnKey(input), input);
        sent.current = input; onSubmission?.(input); setNotice("原版本已记录");
      } catch (error) { setNotice((error as Error).message); }
    }}>记录待核对原版本</button>
    <button onClick={() => {
      if (!sent.current) return;
      try { settleRecoveryTurn(recovery, sent.current, true); setNotice("原结果已核对"); }
      catch (error) { setNotice((error as Error).message); }
    }}>收到原请求已接收结果</button>
    <output data-testid="notice">{notice}</output>
    <V2DraftConflict state={draft.state} client={api as unknown as APIClient} workspaceID={workspaceID}
      onResolve={(token, ref) => { draft.document.resolve(draft.scope, token, ref); }} />
  </section>;
}

function openWindow(name: string, api = client(), initialSubmission?: V2TurnInput, onSubmission?: (input: V2TurnInput) => void) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const rendered = render(<QueryClientProvider client={queryClient}>
    <V2RecoveryProvider client={api} scopeID={backendID}>
      <Probe name={name} api={api} initialSubmission={initialSubmission} onSubmission={onSubmission} />
    </V2RecoveryProvider>
  </QueryClientProvider>);
  const ui = within(rendered.getByRole("region", { name: `window-${name}` }));
  return { ...rendered, ui, api, snapshot: () => JSON.parse(ui.getByTestId("snapshot").textContent!) as DraftSnapshot,
    state: () => JSON.parse(ui.getByTestId("state").textContent!) as DraftState };
}

// Native cross-document storage notifications are modeled explicitly. Each
// window has a separate RecoveryProvider, QueryClient and document instance.
function notifyOtherWindows() {
  act(() => {
    for (const key of Object.keys(localStorage)) window.dispatchEvent(new StorageEvent("storage", {
      key, newValue: localStorage.getItem(key), storageArea: localStorage,
    }));
  });
}

function type(window: ReturnType<typeof openWindow>, text: string) {
  fireEvent.change(window.ui.getByRole("textbox", { name: "草稿正文" }), { target: { value: text } });
}
function chooseBundle(window: ReturnType<typeof openWindow>, text: string) {
  type(window, text);
  fireEvent.click(window.ui.getByRole("button", { name: "选择文件" }));
  fireEvent.click(window.ui.getByRole("button", { name: "选择图片引用" }));
}
function chooseVersion(window: ReturnType<typeof openWindow>, text: string) {
  fireEvent.click(window.ui.getByRole("button", { name: "查看版本" }));
  const article = window.ui.getAllByRole("article").find((entry) => within(entry).queryByText(text, { exact: true }));
  expect(article).toBeDefined();
  fireEvent.click(within(article!).getByRole("radio"));
  fireEvent.click(window.ui.getByRole("button", { name: "确认选用此版本" }));
}

beforeEach(() => localStorage.clear());
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe("React draft documents across independent windows", () => {
  it("synchronizes an idle window's text, real file hook and image hook as one complete version", () => {
    const first = openWindow("a");
    const second = openWindow("b");
    chooseBundle(first, "中文草稿\n保留换行");
    notifyOtherWindows();
    expect(second.snapshot()).toEqual({ text: "中文草稿\n保留换行", files: [selectedFile("a")], images: [selectedImage("a")] });
    expect(second.state().conflict).toBe(false);
    expect(first.api.submitThreadTurn).not.toHaveBeenCalled();
    expect(second.api.submitThreadTurn).not.toHaveBeenCalled();
  });

  it("keeps genuinely same-render-base writes separate and restores both full bundles after remount", () => {
    const first = openWindow("a");
    const second = openWindow("b");
    expect(first.state().ref).toBeNull(); expect(second.state().ref).toBeNull();
    // React flushes after the outer act: neither handler is given the other's
    // new snapshot before both windows have edited their displayed base.
    act(() => { chooseBundle(first, "窗口 A 的限制"); chooseBundle(second, "窗口 B 的修正"); });
    notifyOtherWindows();
    const expected = [
      { text: "窗口 A 的限制", files: [selectedFile("a")], images: [selectedImage("a")] },
      { text: "窗口 B 的修正", files: [selectedFile("b")], images: [selectedImage("b")] },
    ];
    expect(first.state().conflict).toBe(true);
    expect(second.state().heads.map(({ snapshot }) => snapshot)).toEqual(expect.arrayContaining(expected));
    expect(second.state().heads).toHaveLength(2);
    first.unmount(); second.unmount();
    const reopened = openWindow("c");
    expect(reopened.state().heads.map(({ snapshot }) => snapshot)).toEqual(expect.arrayContaining(expected));
    expect(reopened.state().conflict).toBe(true);
    chooseVersion(reopened, "窗口 B 的修正");
    expect(reopened.snapshot()).toEqual(expected[1]);
    expect(reopened.state().conflict).toBe(false);
    reopened.unmount();
    expect(openWindow("d").snapshot()).toEqual(expected[1]);
  });

  it("invalidates a UI version choice when another window changes its head before confirmation", () => {
    const first = openWindow("a");
    const second = openWindow("b");
    act(() => { type(first, "A"); type(second, "B"); });
    notifyOtherWindows();
    fireEvent.click(first.ui.getByRole("button", { name: "查看版本" }));
    fireEvent.click(first.ui.getAllByRole("radio")[0]);
    expect(first.ui.getByRole("button", { name: "确认选用此版本" })).toBeEnabled();
    type(second, "B 新版本");
    notifyOtherWindows();
    expect(first.ui.getByText("版本列表已变化，请重新核对并选择要使用的版本。")).toBeVisible();
    expect(first.ui.getByRole("button", { name: "确认选用此版本" })).toBeDisabled();
    expect(first.state().heads.map(({ snapshot }) => snapshot.text)).toEqual(expect.arrayContaining(["A", "B 新版本"]));
  });

  it("routes a real deferred upload completion to its captured branch after choosing another window's version", async () => {
    let finishUpload!: (image: WorkspaceImageAttachment) => void;
    const api = client();
    api.uploadWorkspaceImage.mockReturnValue(new Promise((resolve) => { finishUpload = resolve; }));
    const first = openWindow("a", api);
    const second = openWindow("b");
    act(() => { type(first, "A 图片说明"); type(second, "B 独立正文"); });
    fireEvent.change(first.ui.getByLabelText("选择图片文件"), {
      target: { files: [new File([new Uint8Array([1, 2, 3])], "late.png", { type: "image/png" })] },
    });
    expect(api.uploadWorkspaceImage).toHaveBeenCalledTimes(1);
    expect(first.ui.getByTestId("uploading")).toHaveTextContent("true");
    notifyOtherWindows();
    chooseVersion(first, "B 独立正文");
    expect(first.snapshot()).toEqual(noAttachments("B 独立正文"));
    await act(async () => { finishUpload(selectedImage("c")); });
    await waitFor(() => expect(first.ui.getByTestId("uploading")).toHaveTextContent("false"));
    expect(first.snapshot()).toEqual(noAttachments("B 独立正文"));
    expect(first.state().conflict).toBe(true);
    expect(first.state().heads.map(({ snapshot }) => snapshot)).toEqual(expect.arrayContaining([
      { text: "A 图片说明", files: [], images: [selectedImage("c")] }, noAttachments("B 独立正文"),
    ]));
    first.unmount(); second.unmount();
    expect(openWindow("d").state().heads.map(({ snapshot }) => snapshot)).toEqual(expect.arrayContaining([
      { text: "A 图片说明", files: [], images: [selectedImage("c")] }, noAttachments("B 独立正文"),
    ]));
  });

  it("keeps a newer same-text/different-image draft when the older exact submission is accepted", () => {
    let submission: V2TurnInput | undefined;
    const first = openWindow("a", client(), undefined, (input) => { submission = input; });
    const second = openWindow("b");
    chooseBundle(first, "同一段正文");
    fireEvent.click(first.ui.getByRole("button", { name: "记录待核对原版本" }));
    expect(submission?.draftVersion).toBeDefined();
    notifyOtherWindows();
    fireEvent.click(second.ui.getByRole("button", { name: "选择文件" }));
    fireEvent.click(second.ui.getByRole("button", { name: "选择图片引用" }));
    const expected = { text: "同一段正文", files: [selectedFile("b")], images: [selectedImage("b")] };
    expect(second.snapshot()).toEqual(expected);
    fireEvent.click(first.ui.getByRole("button", { name: "收到原请求已接收结果" }));
    notifyOtherWindows();
    expect(first.snapshot()).toEqual(expected);
    expect(second.snapshot()).toEqual(expected);
    first.unmount(); second.unmount();
    expect(openWindow("c").snapshot()).toEqual(expected);
  });

  it("settles a recorded exact version after full remount without clearing a later local draft", () => {
    let submission: V2TurnInput | undefined;
    const first = openWindow("a", client(), undefined, (input) => { submission = input; });
    chooseBundle(first, "已发送原稿");
    fireEvent.click(first.ui.getByRole("button", { name: "记录待核对原版本" }));
    expect(submission).toBeDefined();
    first.unmount();
    const reopened = openWindow("b", client(), submission);
    chooseBundle(reopened, "新稿仍保留");
    fireEvent.click(reopened.ui.getByRole("button", { name: "收到原请求已接收结果" }));
    expect(reopened.ui.getByTestId("notice")).toHaveTextContent("原结果已核对");
    const expected = { text: "新稿仍保留", files: [selectedFile("b")], images: [selectedImage("b")] };
    expect(reopened.snapshot()).toEqual(expected);
    reopened.unmount();
    const final = openWindow("c");
    expect(final.snapshot()).toEqual(expected);
    expect(final.api.submitThreadTurn).not.toHaveBeenCalled();
  });

  it("selects the full file/image bundle when both visible versions have identical text", () => {
    const first = openWindow("a");
    const second = openWindow("b");
    act(() => { chooseBundle(first, "相同正文"); chooseBundle(second, "相同正文"); });
    notifyOtherWindows();
    fireEvent.click(first.ui.getByRole("button", { name: "查看版本" }));
    expect(first.ui.getAllByRole("article")).toHaveLength(2);
    const chosen = first.ui.getAllByRole("article").find((entry) => within(entry).queryByText("b.md"));
    expect(chosen).toBeDefined();
    fireEvent.click(within(chosen!).getByRole("radio"));
    fireEvent.click(first.ui.getByRole("button", { name: "确认选用此版本" }));
    const expected = { text: "相同正文", files: [selectedFile("b")], images: [selectedImage("b")] };
    expect(first.snapshot()).toEqual(expected);
    notifyOtherWindows();
    expect(second.snapshot()).toEqual(expected);
    expect(first.api.submitThreadTurn).not.toHaveBeenCalled();
    expect(second.api.submitThreadTurn).not.toHaveBeenCalled();
  });

  it("clears an unchanged exact recorded version after restart, and repeated observation creates no new draft", () => {
    let submission: V2TurnInput | undefined;
    const first = openWindow("a", client(), undefined, (input) => { submission = input; });
    chooseBundle(first, "等待原结果的完整输入");
    fireEvent.click(first.ui.getByRole("button", { name: "记录待核对原版本" }));
    expect(submission?.draftVersion).toBeDefined();
    first.unmount();
    const reopened = openWindow("b", client(), submission);
    expect(reopened.snapshot().images).toEqual([selectedImage("a")]);
    fireEvent.click(reopened.ui.getByRole("button", { name: "收到原请求已接收结果" }));
    expect(reopened.snapshot()).toEqual(noAttachments(""));
    const saved = Object.entries(localStorage);
    fireEvent.click(reopened.ui.getByRole("button", { name: "收到原请求已接收结果" }));
    expect(Object.entries(localStorage)).toEqual(saved);
    reopened.unmount();
    const final = openWindow("c");
    expect(final.snapshot()).toEqual(noAttachments(""));
    expect(final.state().conflict).toBe(false);
    expect(final.api.submitThreadTurn).not.toHaveBeenCalled();
  });
});

function render(ui: ReactElement, options?: RenderOptions) {
 localStorage.setItem("prayu.locale.v1", "zh-CN");
 const Wrapper=options?.wrapper;
 return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
