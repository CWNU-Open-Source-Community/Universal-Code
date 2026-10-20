import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { act, cleanup, fireEvent, render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import type { APIClient } from "../../api/client";
import type { WorkspaceImageAttachment } from "../../api/image-attachments";
import { V2ImagePreview } from "./image-input";

const original: WorkspaceImageAttachment = { id: "original-image", workspace_id: "workspace-one", sha256: "a".repeat(64),
  mime_type: "image/png", byte_size: 128, width: 2400, height: 800, name: "完整长图.png" };
const png = new Blob(["original PNG bytes"], { type: "image/png" });
const fixture = (blob = png) => ({ downloadWorkspaceImage: vi.fn(async () => blob), uploadWorkspaceImage: vi.fn() } as unknown as APIClient);

beforeEach(() => {
  let next = 0;
  vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: vi.fn(() => `blob:verified-${++next}`), revokeObjectURL: vi.fn() }));
  // The real page permits authenticated same-origin requests, not fetch(blob:).
  vi.stubGlobal("fetch", vi.fn(async () => { throw new TypeError("Blocked by connect-src 'self'"); }));
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

async function open(client = fixture(), image = original) {
  const page = render(<V2ImagePreview client={client} images={[image]} />);
  const trigger = screen.getByRole("button", { name: `预览图片 ${image.name}` });
  await waitFor(() => expect(trigger).toBeEnabled());
  trigger.focus();
  fireEvent.click(trigger);
  const dialog = screen.getByRole("dialog", { name: `图片预览 ${image.name}` });
  return { ...page, client, trigger, dialog };
}

function clipboard(write: (items: ClipboardItem[]) => Promise<void>) {
  const contents: Record<string, Blob | Promise<Blob>>[] = [];
  vi.stubGlobal("navigator", Object.create(navigator, { clipboard: { value: { write: vi.fn(write) } } }));
  vi.stubGlobal("ClipboardItem", class { constructor(data: Record<string, Blob | Promise<Blob>>) { contents.push(data); } });
  return contents;
}

it("views the original ratio, zooms without changing the download, and restores thumbnail focus", async () => {
  const { client, trigger, dialog, container } = await open();
  const view = within(dialog);
  const image = view.getByRole("img", { name: original.name });
  expect(view.getByRole("button", { name: "关闭图片预览" })).toHaveFocus();
  expect(container).toHaveAttribute("inert");
  expect(Number.parseFloat(image.style.width) / Number.parseFloat(image.style.height)).toBe(3);
  expect(view.getByRole("button", { name: "适应窗口" })).toHaveAttribute("aria-pressed", "true");
  fireEvent.click(view.getByRole("button", { name: "100%" }));
  expect(image).toHaveStyle({ width: "2400px", height: "800px" });
  fireEvent.click(view.getByRole("button", { name: "放大图片" }));
  expect(image).toHaveStyle({ width: "3000px", height: "1000px" });
  expect(view.getByLabelText("图片缩放比例")).toHaveTextContent("125%");
  fireEvent.keyDown(dialog, { key: "0" });
  expect(view.getByRole("button", { name: "适应窗口" })).toHaveAttribute("aria-pressed", "true");
  const download = view.getByRole("link", { name: `下载原图 ${original.name}` });
  expect(download).toHaveAttribute("href", "blob:verified-1");
  expect(download).toHaveAttribute("download", original.name);
  view.getByRole("button", { name: "100%" }).focus();
  fireEvent.keyDown(dialog, { key: "Tab" });
  expect(view.getByRole("button", { name: "复制图片" })).toHaveFocus();
  fireEvent.keyDown(dialog, { key: "Escape" });
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(trigger).toHaveFocus();
  expect(container).not.toHaveAttribute("inert");
  expect(client.downloadWorkspaceImage).toHaveBeenCalledTimes(1);
  expect(client.downloadWorkspaceImage).toHaveBeenCalledWith(original, expect.any(AbortSignal));
  expect(client.uploadWorkspaceImage).not.toHaveBeenCalled();
});

it("copies the verified original PNG only after a click and reports success only after clipboard acceptance", async () => {
  let complete!: () => void;
  const contents = clipboard(() => new Promise<void>((resolve) => { complete = resolve; }));
  const { client, dialog } = await open();
  expect(fetch).not.toHaveBeenCalled();
  expect(navigator.clipboard.write).not.toHaveBeenCalled();
  fireEvent.click(within(dialog).getByRole("button", { name: "复制图片" }));
  expect(await screen.findByText("正在复制图片…")).toHaveAttribute("role", "status");
  expect(screen.queryByText("已复制图片")).not.toBeInTheDocument();
  expect(fetch).not.toHaveBeenCalled();
  expect(await contents[0]["image/png"]).toBe(png);
  await act(async () => complete());
  expect(screen.getByText("已复制图片")).toHaveAttribute("role", "status");
  expect(client.uploadWorkspaceImage).not.toHaveBeenCalled();
  expect(client.downloadWorkspaceImage).toHaveBeenCalledTimes(1);
});

it("converts JPEG only for the clipboard and keeps the original download and dimensions", async () => {
  const jpeg = new Blob(["original JPEG bytes"], { type: "image/jpeg" });
  const image: WorkspaceImageAttachment = { ...original, mime_type: "image/jpeg", name: "原图.jpg" };
  const contents = clipboard(async () => {});
  const bitmap = { width: image.width, height: image.height, close: vi.fn() };
  vi.stubGlobal("createImageBitmap", vi.fn(async () => bitmap));
  const drawImage = vi.fn();
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({ drawImage } as unknown as CanvasRenderingContext2D);
  vi.spyOn(HTMLCanvasElement.prototype, "toBlob").mockImplementation(function (this: HTMLCanvasElement, callback, type) {
    expect([this.width, this.height, type]).toEqual([2400, 800, "image/png"]);
    callback(png);
  });
  const { client, dialog } = await open(fixture(jpeg), image);
  expect(createImageBitmap).not.toHaveBeenCalled();
  fireEvent.click(within(dialog).getByRole("button", { name: "复制图片" }));
  expect(await contents[0]["image/png"]).toBe(png);
  expect(await screen.findByText("已复制图片")).toHaveAttribute("role", "status");
  expect(createImageBitmap).toHaveBeenCalledWith(jpeg);
  expect(drawImage).toHaveBeenCalledWith(bitmap, 0, 0);
  expect(bitmap.close).toHaveBeenCalledTimes(1);
  expect(fetch).not.toHaveBeenCalled();
  expect(within(dialog).getByRole("link", { name: "下载原图 原图.jpg" })).toHaveAttribute("href", "blob:verified-1");
  expect(within(dialog).getByRole("link", { name: "下载原图 原图.jpg" })).toHaveAttribute("download", "原图.jpg");
  expect(client.uploadWorkspaceImage).not.toHaveBeenCalled();
});

it.each(["unavailable", "denied"])("keeps download available and reports a %s clipboard instead of claiming copied", async (reason) => {
  if (reason === "unavailable") vi.stubGlobal("navigator", Object.create(navigator, { clipboard: { value: undefined } }));
  else clipboard(async () => { throw new DOMException("Denied by browser", "NotAllowedError"); });
  const { dialog } = await open();
  fireEvent.click(within(dialog).getByRole("button", { name: "复制图片" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(reason === "unavailable" ? "当前浏览器不支持复制图片" : "浏览器未允许写入剪贴板");
  expect(screen.queryByText("已复制图片")).not.toBeInTheDocument();
  expect(fetch).not.toHaveBeenCalled();
  expect(within(dialog).getByRole("link", { name: `下载原图 ${original.name}` })).toHaveAttribute("href", "blob:verified-1");
});

it("closes an old viewer on image identity change and never reuses its URL for the new reference", async () => {
  const contents = clipboard(async () => {});
  const client = fixture();
  let finish!: (value: Blob) => void;
  const page = await open(client);
  vi.mocked(client.downloadWorkspaceImage).mockImplementationOnce(() => new Promise<Blob>((resolve) => { finish = resolve; }));
  const next = { ...original, workspace_id: "workspace-two", sha256: "b".repeat(64) };
  page.rerender(<V2ImagePreview client={client} images={[next]} />);
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:verified-1");
  expect(screen.getByRole("button", { name: `预览图片 ${original.name}` })).toBeDisabled();
  expect(screen.queryByRole("img")).not.toBeInTheDocument();
  const nextBlob = new Blob(["next verified PNG bytes"], { type: "image/png" });
  await act(async () => finish(nextBlob));
  fireEvent.click(screen.getByRole("button", { name: `预览图片 ${original.name}` }));
  expect(within(screen.getByRole("dialog")).getByRole("img")).toHaveAttribute("src", "blob:verified-2");
  expect(client.downloadWorkspaceImage).toHaveBeenLastCalledWith(next, expect.any(AbortSignal));
  fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "复制图片" }));
  expect(await contents[0]["image/png"]).toBe(nextBlob);
  expect(await screen.findByText("已复制图片")).toHaveAttribute("role", "status");
  expect(fetch).not.toHaveBeenCalled();
});

it.each(["mime", "dimensions"])("rejects mismatched %s while retaining the original for download", async (mismatch) => {
  const jpeg = new Blob(["verified bytes"], { type: "image/jpeg" });
  const metadata: WorkspaceImageAttachment = { ...original,
    mime_type: mismatch === "mime" ? "image/png" : "image/jpeg" };
  const bitmap = { width: metadata.width + 1, height: metadata.height, close: vi.fn() };
  vi.stubGlobal("createImageBitmap", vi.fn(async () => bitmap));
  const contents = clipboard(async () => { await contents[0]["image/png"]; });
  const { dialog } = await open(fixture(jpeg), metadata);
  fireEvent.click(within(dialog).getByRole("button", { name: "复制图片" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(mismatch === "mime" ? "原图格式与保存记录不一致" : "原图尺寸与保存记录不一致");
  expect(screen.queryByText("已复制图片")).not.toBeInTheDocument();
  expect(within(dialog).getByRole("link", { name: `下载原图 ${original.name}` })).toHaveAttribute("href", "blob:verified-1");
  expect(fetch).not.toHaveBeenCalled();
  if (mismatch === "dimensions") expect(bitmap.close).toHaveBeenCalledTimes(1);
});

function render(ui: ReactElement, options?: RenderOptions) {
 localStorage.setItem("prayu.locale.v1", "zh-CN");
 const Wrapper=options?.wrapper;
 return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
