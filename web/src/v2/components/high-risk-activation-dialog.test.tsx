import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { cleanup, fireEvent, render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useRef, useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { V2HighRiskActivationDialog } from "./high-risk-activation-dialog";

afterEach(cleanup);

describe("V2HighRiskActivationDialog", () => {
  it.each(["full_access", "debug"] as const)("keeps %s keyboard confirmation inside its portal form", async (profile) => {
    const user = userEvent.setup(); const onConfirm = vi.fn(); const parentSubmit = vi.fn();
    render(<form onSubmit={(event) => { event.preventDefault(); parentSubmit(); }}>
      <textarea defaultValue="独立的未发送草稿" aria-label="父表单草稿" />
      <V2HighRiskActivationDialog open profile={profile} onCancel={vi.fn()} onConfirm={onConfirm} />
    </form>);
    const dialog = screen.getByRole("dialog");
    const confirm = within(dialog).getByRole("button", { name: profile === "full_access" ? "启用完全访问" : "启用并重启" });
    confirm.focus(); await user.keyboard("{Enter}");
    expect(onConfirm).toHaveBeenCalledOnce();
    expect(parentSubmit).not.toHaveBeenCalled();
    expect(screen.getByLabelText("父表单草稿")).toHaveValue("独立的未发送草稿");
  });

  it("describes full access, focuses the safe action, and confirms only on demand", async () => {
    const user = userEvent.setup();
    const onCancel = vi.fn();
    const onConfirm = vi.fn();
    render(<V2HighRiskActivationDialog onCancel={onCancel} onConfirm={onConfirm}
      open phase="idle" profile="full_access" />);

    const dialog = screen.getByRole("dialog", { name: "要开启完全访问权限吗？" });
    expect(within(dialog).getByText("文件和文件夹")).toBeInTheDocument();
    expect(within(dialog).getByText("终端命令")).toBeInTheDocument();
    expect(within(dialog).getByText("互联网、浏览器和已连接的应用")).toBeInTheDocument();
    expect(within(dialog).getByText(/当前系统用户权限范围/u)).toBeInTheDocument();
    const scope = within(dialog).getByRole("note");
    expect(within(scope).getByText("影响范围")).toBeInTheDocument();
    expect(within(scope).getByText(/请先暂停执行并等待资源释放完成/u)).toBeInTheDocument();
    expect(within(scope).getByText(/启用范围为当前任务，应用保持运行/u)).toBeInTheDocument();
    expect(within(dialog).getByText(/完整 CDP 随完全访问默认开启/u)).toBeInTheDocument();

    const cancel = within(dialog).getByRole("button", { name: "取消" });
    await waitFor(() => expect(cancel).toHaveFocus());
    expect(onConfirm).not.toHaveBeenCalled();
    await user.click(within(dialog).getByRole("button", { name: "启用完全访问" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("cancels with Escape or the backdrop without confirming", async () => {
    const user = userEvent.setup();
    const onCancel = vi.fn();
    const onConfirm = vi.fn();
    const { container } = render(<V2HighRiskActivationDialog onCancel={onCancel}
      onConfirm={onConfirm} open profile="debug" />);

    expect(screen.getByText("独立的任务权限")).toBeInTheDocument();
    expect(screen.getByText("持久终端和后台进程")).toBeInTheDocument();
    expect(screen.getByText("终端输入与调试控制")).toBeInTheDocument();
    expect(screen.getByText(/完全访问仍需逐个任务明确确认/u)).toBeInTheDocument();
    expect(screen.getByText(/准备持久终端、后台进程和限时终端输入/u))
      .toBeInTheDocument();
    expect(screen.getByText(/任务已保存的权限会保留/u)).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(onCancel).toHaveBeenCalledTimes(1);

    const overlay = document.body.querySelector<HTMLElement>(".v2-high-risk-overlay");
    expect(overlay).not.toBeNull();
    fireEvent.mouseDown(overlay!);
    expect(onCancel).toHaveBeenCalledTimes(2);
    expect(onConfirm).not.toHaveBeenCalled();
    expect(container).toBeEmptyDOMElement();
  });

  it("locks dismissal while the native confirmation or restart is in progress", async () => {
    const user = userEvent.setup();
    const onCancel = vi.fn();
    render(<V2HighRiskActivationDialog onCancel={onCancel} onConfirm={vi.fn()}
      open phase="restarting" profile="debug" />);

    const dialog = screen.getByRole("dialog", { name: "要开启调试模式吗？" });
    expect(dialog).toHaveAttribute("aria-busy", "true");
    expect(within(dialog).getByRole("status")).toHaveTextContent("正在重启…");
    expect(within(dialog).getByRole("button", { name: "取消" })).toBeDisabled();
    await user.keyboard("{Escape}");
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("isolates the background tree and returns focus to an explicit activation control", async () => {
    function Harness() {
      const [open, setOpen] = useState(false);
      const triggerRef = useRef<HTMLButtonElement>(null);
      return <><button onClick={() => setOpen(true)} ref={triggerRef} type="button">
        Activate full access
      </button><V2HighRiskActivationDialog onCancel={() => setOpen(false)}
        onConfirm={vi.fn()} open={open} profile="full_access" returnFocusRef={triggerRef} /></>;
    }
    const user = userEvent.setup();
    const { container } = render(<Harness />);
    const trigger = screen.getByRole("button", { name: "Activate full access" });

    await user.click(trigger);

    expect(container).toHaveAttribute("inert");
    expect(container).toHaveAttribute("aria-hidden", "true");
    const dialog = screen.getByRole("dialog", { name: "要开启完全访问权限吗？" });
    await user.click(within(dialog).getByRole("button", { name: "取消" }));

    expect(container).not.toHaveAttribute("inert");
    expect(container).not.toHaveAttribute("aria-hidden");
    expect(trigger).toHaveFocus();
  });
});

function render(ui: ReactElement, options?: RenderOptions) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
