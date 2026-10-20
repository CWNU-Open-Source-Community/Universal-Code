import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { act, cleanup, render as renderComponent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useRef, useState, type RefObject } from "react";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { V2IdentityDisclosure } from "./identity-disclosure";

afterEach(cleanup);

const props = (identity = "run-origin-01a07bd8-5240-7950-9def-9c3039ae57dc") => ({
  identity, identityLabel: "执行 ID", summary: "执行信息",
});

function LoadingFileModal() {
  const close = useRef<HTMLButtonElement>(null);
  const dialog = useModalFocusTrap(true, () => undefined, false, close);
  return <section ref={dialog} role="dialog" aria-label="加载中的文件面板">
    <button ref={close} type="button">关闭</button>
    <V2IdentityDisclosure {...props()} />
    <p>正在确认文件目录…</p>
  </section>;
}

function ClosableFileModal({ onClose, returnFocusRef }: {
  onClose: () => void;
  returnFocusRef: RefObject<HTMLButtonElement | null>;
}) {
  const close = useRef<HTMLButtonElement>(null);
  const dialog = useModalFocusTrap(true, onClose, false, close, { returnFocusRef });
  return <section ref={dialog} role="dialog" aria-label="文件面板">
    <button onClick={onClose} ref={close} type="button">关闭</button>
    <V2IdentityDisclosure {...props()} />
  </section>;
}

function CopyModalHarness() {
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  return <>
    <button onClick={() => setOpen(true)} ref={trigger} type="button">打开文件面板</button>
    {open && <ClosableFileModal onClose={() => setOpen(false)} returnFocusRef={trigger} />}
  </>;
}

it("keeps the native disclosure in the modal's keyboard cycle while files are still loading", async () => {
  const user = userEvent.setup();
  render(<LoadingFileModal />);
  const close = screen.getByRole("button", { name: "关闭" });
  const summary = screen.getByText("执行信息");
  expect(screen.getByRole("button", { name: "复制完整 ID", hidden: true })).toBeDisabled();
  expect(close).toHaveFocus();
  await user.tab();
  expect(summary).toHaveFocus();
  await user.tab();
  expect(close).toHaveFocus();
  await user.tab({ shift: true });
  expect(summary).toHaveFocus();
  await user.click(summary);
  await user.tab();
  expect(screen.getByRole("button", { name: "复制完整 ID" })).toHaveFocus();
  await user.tab();
  expect(close).toHaveFocus();
});

it.each(["success", "failure"] as const)("preserves keyboard focus while copying and lets Escape close the modal after %s", async (outcome) => {
  const user = userEvent.setup();
  let resolve!: () => void;
  let reject!: (reason: Error) => void;
  const pending = new Promise<void>((finish, fail) => { resolve = finish; reject = fail; });
  const writeText = vi.spyOn(navigator.clipboard, "writeText").mockReturnValue(pending);
  render(<CopyModalHarness />);
  const trigger = screen.getByRole("button", { name: "打开文件面板" });
  await user.click(trigger);
  await user.tab();
  await user.click(screen.getByText("执行信息"));
  await user.tab();
  const copy = screen.getByRole("button", { name: "复制完整 ID" });
  expect(copy).toHaveFocus();
  await user.keyboard("{Enter}");

  // JSDOM does not model Chromium's blur on native disabled. Explicitly forbid
  // native disabling during pending, in addition to asserting retained focus.
  expect(copy).toBeEnabled();
  expect(copy).toHaveAttribute("aria-disabled", "true");
  expect(copy).toHaveFocus();
  await user.keyboard("{Enter} ");
  await user.click(copy);
  expect(writeText).toHaveBeenCalledTimes(1);
  expect(writeText).toHaveBeenCalledWith(props().identity);

  await act(async () => {
    if (outcome === "success") resolve();
    else reject(new Error("Clipboard denied"));
    await pending.catch(() => undefined);
  });
  expect(screen.getByRole("status")).toHaveTextContent(outcome === "success"
    ? "已复制此执行 ID。" : "复制失败，请选中上方完整 ID 手动复制。");
  expect(copy).toHaveAttribute("aria-disabled", "false");
  expect(copy).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog", { name: "文件面板" })).not.toBeInTheDocument();
  expect(trigger).toHaveFocus();
});

it("discloses the complete identity and copies it using the keyboard with confirmed feedback", async () => {
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
  const current = props();
  render(<V2IdentityDisclosure {...current} />);

  const summary = screen.getByText("执行信息");
  const value = screen.getByText(current.identity);
  expect(summary.closest("details")).not.toHaveAttribute("open");
  expect(value).not.toBeVisible();
  await user.tab();
  expect(summary).toHaveFocus();
  await user.click(summary);
  expect(value).toBeVisible();
  await user.tab();
  expect(screen.getByRole("button", { name: "复制完整 ID" })).toHaveFocus();
  await user.keyboard("{Enter}");

  await waitFor(() => expect(writeText).toHaveBeenCalledWith(current.identity));
  expect(screen.getByRole("status")).toHaveTextContent("已复制此执行 ID。");
});

it("keeps the complete identity available for manual copying after clipboard rejection", async () => {
  const user = userEvent.setup();
  vi.spyOn(navigator.clipboard, "writeText").mockRejectedValue(new Error("Permission denied"));
  render(<V2IdentityDisclosure {...props("run-retained")} />);
  await user.click(screen.getByText("执行信息"));
  await user.click(screen.getByRole("button", { name: "复制完整 ID" }));

  expect(await screen.findByText("复制失败，请选中上方完整 ID 手动复制。")).toBeVisible();
  expect(screen.getByText("run-retained")).toBeVisible();
  expect(screen.getByRole("button", { name: "复制完整 ID" })).toBeEnabled();
});

it("does not carry completed copy feedback across identity changes or back to an earlier identity", async () => {
  const user = userEvent.setup();
  vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
  const { rerender } = render(<V2IdentityDisclosure {...props("run-a")} />);
  await user.click(screen.getByText("执行信息"));
  await user.click(screen.getByRole("button", { name: "复制完整 ID" }));
  expect(screen.getByRole("status")).toHaveTextContent("已复制此执行 ID。");

  rerender(<V2IdentityDisclosure {...props("run-b")} />);
  expect(screen.getByText("执行信息").closest("details")).not.toHaveAttribute("open");
  await user.click(screen.getByText("执行信息"));
  expect(screen.getByRole("status")).toBeEmptyDOMElement();
  expect(screen.getByText("run-b")).toBeVisible();
  rerender(<V2IdentityDisclosure {...props("run-a")} />);
  await user.click(screen.getByText("执行信息"));
  expect(screen.getByRole("status")).toBeEmptyDOMElement();
});

it("keeps a pending clipboard response tied to the original identity after navigation", async () => {
  const user = userEvent.setup();
  let resolve!: () => void;
  const pending = new Promise<void>((finish) => { resolve = finish; });
  const writeText = vi.spyOn(navigator.clipboard, "writeText").mockReturnValueOnce(pending)
    .mockResolvedValue(undefined);
  const { rerender } = render(<V2IdentityDisclosure {...props("run-a")} />);
  await user.click(screen.getByText("执行信息"));
  await user.click(screen.getByRole("button", { name: "复制完整 ID" }));
  expect(screen.getByRole("button", { name: "正在复制…" })).toBeEnabled();
  expect(screen.getByRole("button", { name: "正在复制…" })).toHaveAttribute("aria-disabled", "true");

  rerender(<V2IdentityDisclosure {...props("run-b")} />);
  await user.click(screen.getByText("执行信息"));
  await act(async () => { resolve(); await pending; });
  expect(screen.getByRole("status")).toBeEmptyDOMElement();
  expect(screen.getByText("run-b")).toBeVisible();
  await user.click(screen.getByRole("button", { name: "复制完整 ID" }));
  expect(writeText.mock.calls.map(([identity]) => identity)).toEqual(["run-a", "run-b"]);
  expect(screen.getByRole("status")).toHaveTextContent("已复制此执行 ID。");
});

function render(ui: ReactElement, options?: RenderOptions) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
