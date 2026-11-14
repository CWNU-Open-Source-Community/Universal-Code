import { StrictMode, useState, type ReactNode } from "react";
import { cleanup, fireEvent, render as renderComponent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ApprovalModeControlProps } from "./approval-mode-contract";
import { V2ApprovalModeControl } from "./approval-mode-control";
import { LocaleProvider, useLocale, type PrayuLocale } from "../../lib/locale";

afterEach(() => { cleanup(); window.localStorage.removeItem("prayu.locale.v1"); });

function render(ui: ReactNode, locale: PrayuLocale = "zh-CN") {
  window.localStorage.setItem("prayu.locale.v1", locale);
  return renderComponent(ui, { wrapper: LocaleProvider });
}

function props(overrides: Partial<ApprovalModeControlProps> = {}): ApprovalModeControlProps {
  return { mode: "ask", fullActivation: "inactive", pending: false, onRequestChange: vi.fn(), ...overrides };
}

const labels = ["请求批准", "帮我批准", "完全访问权限"];

describe.each(["menu", "settings"] as const)("V2ApprovalModeControl English %s", (variant) => {
  const role = variant === "menu" ? "menuitemradio" : "button";
  async function open(user: ReturnType<typeof userEvent.setup>) {
    if (variant === "menu") await user.click(screen.getByRole("button", { expanded: false }));
  }

  it("localizes choices, descriptions and accessible names while preserving the Auto request", async () => {
    const user = userEvent.setup(); const initial = props({ variant });
    const view = render(<V2ApprovalModeControl {...initial} />, "en-US");
    await open(user);
    expect(within(screen.getByRole("group", { name: "Execution approval modes" })).getAllByRole(role)
      .map((item) => item.getAttribute("aria-label"))).toEqual(["Request approval", "Approve for me", "Full access"]);
    expect(screen.getByText("Execution permissions")).toBeVisible();
    expect(screen.getByText("Full access inactive")).toBeVisible();
    expect(screen.getByRole(role, { name: "Request approval" })).toHaveAccessibleDescription(/approval for file, command, and web operations/u);
    expect(screen.queryByText(/Read-only claims are checked against actual effects/u)).not.toBeInTheDocument();
    expect(view.container.textContent).not.toMatch(/[\u3400-\u9fff]/u);
    await user.click(screen.getByRole(role, { name: "Approve for me" }));
    expect(initial.onRequestChange).toHaveBeenCalledExactlyOnceWith({ mode: "auto", confirmFull: false });
  });

  it("cancels in English without a write, then confirms Full exactly once", async () => {
    const user = userEvent.setup(); const initial = props({ variant });
    render(<V2ApprovalModeControl {...initial} />, "en-US");
    await open(user); await user.click(screen.getByRole(role, { name: "Full access" }));
    const dialog = screen.getByRole("dialog", { name: "Enable Full access?" });
    expect(within(dialog).getByRole("button", { name: "Close" })).toHaveFocus();
    expect(within(dialog).getByText(/project file access, local commands, and public network access/u)).toBeVisible();
    expect(within(dialog).getByText(/Configured environment and network restrictions still apply/u)).toBeVisible();
    expect(dialog.textContent).not.toMatch(/[\u3400-\u9fff]/u);
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(initial.onRequestChange).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    const returnTarget = variant === "menu" ? screen.getByRole("button", { expanded: false })
      : screen.getByRole(role, { name: "Full access" });
    expect(returnTarget).toHaveFocus();
    await open(user); await user.click(screen.getByRole(role, { name: "Full access" }));
    await user.dblClick(within(screen.getByRole("dialog")).getByRole("button", { name: "Confirm activation" }));
    expect(initial.onRequestChange).toHaveBeenCalledExactlyOnceWith({ mode: "full", confirmFull: true });
  });

  it("keeps cold Full inactive until English reactivation is confirmed", async () => {
    const user = userEvent.setup(); const initial = props({ variant, mode: "full" });
    render(<V2ApprovalModeControl {...initial} />, "en-US");
    await open(user);
    expect(screen.getByText("Full access inactive")).toBeVisible();
    expect(screen.getByRole(role, { name: "Full access" })).toHaveAccessibleDescription(/Selected · inactive/u);
    expect(initial.onRequestChange).not.toHaveBeenCalled();
    await user.click(screen.getByRole(variant === "menu" ? "menuitem" : "button", { name: /Reactivate Full access/u }));
    const dialog = screen.getByRole("dialog", { name: "Reactivate Full access?" });
    expect(initial.onRequestChange).not.toHaveBeenCalled();
    await user.click(within(dialog).getByRole("button", { name: "Confirm reactivation" }));
    expect(initial.onRequestChange).toHaveBeenCalledExactlyOnceWith({ mode: "full", confirmFull: true });
  });

  it.each(["inactive", "active", "unavailable"] as const)("localizes Full %s without issuing a request", async (fullActivation) => {
    const user = userEvent.setup(); const initial = props({ variant, mode: "full", fullActivation });
    const view = render(<V2ApprovalModeControl {...initial} />, "en-US");
    await open(user);
    expect(screen.getByText(`Full access ${fullActivation}`)).toBeVisible();
    if (fullActivation === "unavailable") {
      expect(screen.getByRole(role, { name: "Full access" })).toHaveAccessibleDescription(
        "Currently unavailable Full access requires runtime support. Check the service's permission configuration.");
    }
    expect(view.container.textContent).not.toMatch(/[\u3400-\u9fff]/u);
    expect(initial.onRequestChange).not.toHaveBeenCalled();
  });

  it("localizes pending and blank-error fallbacks while preserving a supplied reason", async () => {
    const user = userEvent.setup(); const initial = props({ variant, pending: true,
      fullActivation: "unavailable", fullUnavailableReason: "Provider has disabled this capability", error: " " });
    const view = render(<V2ApprovalModeControl {...initial} />, "en-US");
    await open(user);
    expect(screen.getByRole("status")).toHaveTextContent("Updating permissions…");
    expect(screen.getByRole("alert")).toHaveTextContent("Failed to update permissions");
    await user.click(screen.getByText("View Full access requirements"));
    expect(screen.getByText("Provider has disabled this capability")).toBeVisible();
    expect(view.container.textContent).not.toMatch(/[\u3400-\u9fff]/u);
    expect(initial.onRequestChange).not.toHaveBeenCalled();
  });

  it("keeps a pending English confirmation locked with a localized busy label", async () => {
    const user = userEvent.setup(); const initial = props({ variant });
    const view = render(<V2ApprovalModeControl {...initial} />, "en-US");
    await open(user); await user.click(screen.getByRole(role, { name: "Full access" }));
    view.rerender(<V2ApprovalModeControl {...initial} pending />);
    const dialog = screen.getByRole("dialog", { name: "Enable Full access?" });
    expect(within(dialog).getByRole("button", { name: "Processing…" })).toBeDisabled();
    expect(within(dialog).getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(within(dialog).getByRole("button", { name: "Close" })).toBeDisabled();
    await user.keyboard("{Escape}{Enter}");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(initial.onRequestChange).not.toHaveBeenCalled();
  });
});

it("updates an open confirmation when locale changes without changing consent or preference", async () => {
  const user = userEvent.setup(); const initial = props({ variant: "settings" });
  function Host() {
    const { setLocale } = useLocale();
    return <><button onClick={() => setLocale("en-US")}>Use English</button><V2ApprovalModeControl {...initial} /></>;
  }
  render(<Host />);
  await user.click(screen.getByRole("button", { name: "完全访问权限" }));
  expect(screen.getByRole("dialog", { name: "启用完全访问权限？" })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Use English" }));
  const dialog = screen.getByRole("dialog", { name: "Enable Full access?" });
  expect(dialog.textContent).not.toMatch(/[\u3400-\u9fff]/u);
  expect(initial.onRequestChange).not.toHaveBeenCalled();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Full access" })).toHaveFocus();
});

describe.each(["menu", "settings"] as const)("V2ApprovalModeControl %s", (variant) => {
  const role = variant === "menu" ? "menuitemradio" : "button";
  const selectedAttribute = variant === "menu" ? "aria-checked" : "aria-pressed";
  async function open(user: ReturnType<typeof userEvent.setup>) {
    if (variant === "menu") await user.click(screen.getByRole("button", { expanded: false }));
  }

  it("renders exactly the three choices and keeps refreshed preference separate from activation", async () => {
    const user = userEvent.setup();
    const initial = props({ variant });
    const view = render(<StrictMode><V2ApprovalModeControl {...initial} /></StrictMode>);
    await open(user);
    expect(within(screen.getByRole("group", { name: "执行权限档位" })).getAllByRole(role)
      .map((item) => item.getAttribute("aria-label"))).toEqual(labels);
    expect(screen.getByRole(role, { name: "请求批准" })).toHaveAttribute(selectedAttribute, "true");
    for (const next of [
      { mode: "auto", fullActivation: "inactive" },
      { mode: "full", fullActivation: "inactive" },
      { mode: "full", fullActivation: "active" },
      { mode: "ask", fullActivation: "inactive" },
    ] as const) {
      view.rerender(<StrictMode><V2ApprovalModeControl {...initial} {...next} /></StrictMode>);
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    }
    expect(initial.onRequestChange).not.toHaveBeenCalled();
  });

  it.each([
    ["auto", "ask", "请求批准"], ["ask", "auto", "帮我批准"], ["full", "auto", "帮我批准"],
  ] as const)("sends only the exact %s to %s selection", async (from, to, label) => {
    const user = userEvent.setup();
    const initial = props({ variant, mode: from, fullActivation: from === "full" ? "active" : "inactive" });
    render(<V2ApprovalModeControl {...initial} />);
    await open(user);
    await user.click(screen.getByRole(role, { name: label }));
    expect(initial.onRequestChange).toHaveBeenCalledExactlyOnceWith({ mode: to, confirmFull: false });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    if (variant === "menu") expect(screen.getByRole("button", { expanded: false })).toHaveFocus();
    else expect(screen.getByRole(role, { name: from === "full" ? "完全访问权限" : labels[from === "ask" ? 0 : 1] }))
      .toHaveAttribute(selectedAttribute, "true");
  });

  it("confirms Full by keyboard once, traps focus, and never submits an enclosing draft form", async () => {
    const user = userEvent.setup();
    const initial = props({ variant });
    const submit = vi.fn((event) => event.preventDefault());
    render(<form onSubmit={submit}><input aria-label="未发送草稿" defaultValue="保留草稿" />
      <V2ApprovalModeControl {...initial} /><button type="submit">发送</button></form>);
    await open(user);
    await user.click(screen.getByRole(role, { name: "完全访问权限" }));
    const dialog = screen.getByRole("dialog", { name: "启用完全访问权限？" });
    expect(initial.onRequestChange).not.toHaveBeenCalled();
    expect(within(dialog).getByText(/访问项目文件、执行本机命令和访问公共网络/u)).toBeInTheDocument();
    expect(within(dialog).getByText(/已设置的环境与网络限制仍然有效/u)).toBeInTheDocument();
    const close = within(dialog).getByRole("button", { name: "关闭" });
    const confirm = within(dialog).getByRole("button", { name: "确认启用" });
    expect(close).toHaveFocus();
    await user.tab({ shift: true }); expect(confirm).toHaveFocus();
    await user.tab(); expect(close).toHaveFocus();
    await user.tab(); expect(within(dialog).getByRole("button", { name: "取消" })).toHaveFocus();
    await user.tab(); await user.keyboard("{Enter}");
    fireEvent.click(confirm);
    expect(initial.onRequestChange).toHaveBeenCalledExactlyOnceWith({ mode: "full", confirmFull: true });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(submit).not.toHaveBeenCalled();
    expect(screen.getByRole("textbox", { name: "未发送草稿" })).toHaveValue("保留草稿");
    if (variant === "menu") expect(screen.getByRole("button", { expanded: false })).toHaveFocus();
    else expect(screen.getByRole("region", { name: "执行权限" })).toHaveFocus();
  });

  it.each(["Escape", "cancel", "close", "backdrop"] as const)("cancels through %s without sending and restores focus", async (action) => {
    const user = userEvent.setup();
    const initial = props({ variant });
    render(<V2ApprovalModeControl {...initial} />);
    await open(user);
    const full = screen.getByRole(role, { name: "完全访问权限" });
    const trigger = variant === "menu" ? screen.getByRole("button", { expanded: true }) : full;
    await user.click(full);
    const dialog = screen.getByRole("dialog");
    if (action === "Escape") await user.keyboard("{Escape}");
    else if (action === "backdrop") fireEvent.mouseDown(dialog.parentElement!);
    else await user.click(within(dialog).getByRole("button", { name: action === "cancel" ? "取消" : "关闭" }));
    expect(initial.onRequestChange).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("keeps cold Full selected and requires fresh confirmation to reactivate", async () => {
    const user = userEvent.setup();
    const initial = props({ variant, mode: "full" });
    render(<V2ApprovalModeControl {...initial} />);
    await open(user);
    expect(screen.getByRole(role, { name: "完全访问权限" })).toHaveAttribute(selectedAttribute, "true");
    expect(screen.getByText("完全访问未激活")).toBeInTheDocument();
    const reactivate = screen.getByRole(variant === "menu" ? "menuitem" : "button", { name: /重新激活完全访问权限/u });
    await user.click(reactivate);
    expect(initial.onRequestChange).not.toHaveBeenCalled();
    await user.keyboard("{Escape}");
    if (variant === "menu") {
      expect(screen.getByRole("button", { expanded: false })).toHaveFocus();
      await open(user);
    } else expect(reactivate).toHaveFocus();
    await user.click(screen.getByRole(variant === "menu" ? "menuitem" : "button", { name: /重新激活完全访问权限/u }));
    const confirm = within(screen.getByRole("dialog", { name: "重新激活完全访问权限？" }))
      .getByRole("button", { name: "确认重新激活" });
    await user.dblClick(confirm);
    expect(initial.onRequestChange).toHaveBeenCalledExactlyOnceWith({ mode: "full", confirmFull: true });
    if (variant === "menu") expect(screen.getByRole("button", { expanded: false })).toHaveTextContent("未激活");
    else expect(screen.getByText("完全访问未激活")).toBeInTheDocument();
  });

  it("shows active Full from host props without a reactivation request", async () => {
    const user = userEvent.setup();
    const initial = props({ variant, mode: "full", fullActivation: "active" });
    render(<V2ApprovalModeControl {...initial} />);
    await open(user);
    expect(screen.getByText("完全访问已激活")).toBeInTheDocument();
    expect(screen.getByRole(role, { name: "完全访问权限" })).toBeDisabled();
    expect(screen.queryByText("重新激活完全访问权限")).not.toBeInTheDocument();
    expect(initial.onRequestChange).not.toHaveBeenCalled();
  });

  it.each(["pending", "disabled"] as const)("prevents all selections and reactivation while %s", async (state) => {
    const user = userEvent.setup();
    const initial = props({ variant, mode: "full", [state]: true });
    render(<V2ApprovalModeControl {...initial} />);
    await open(user);
    for (const option of within(screen.getByRole("group", { name: "执行权限档位" })).getAllByRole(role)) {
      expect(option).toBeDisabled(); await user.click(option);
    }
    const reactivate = screen.getByRole(variant === "menu" ? "menuitem" : "button", { name: /重新激活完全访问权限/u });
    expect(reactivate).toBeDisabled(); await user.click(reactivate);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    if (state === "pending") expect(screen.getByRole("status")).toHaveTextContent("正在更新权限");
    expect(initial.onRequestChange).not.toHaveBeenCalled();
  });

  it("keeps unavailable Full disabled and exposes the actual reason and request error", async () => {
    const user = userEvent.setup();
    const initial = props({ variant, fullActivation: "unavailable", fullUnavailableReason: "供应商未开放此能力", error: "权限更改未完成" });
    render(<V2ApprovalModeControl {...initial} />);
    expect(screen.getByRole("alert")).toHaveTextContent("权限更改未完成");
    await open(user);
    const full = screen.getByRole(role, { name: "完全访问权限" });
    expect(full).toBeDisabled();
    expect(full).toHaveAccessibleDescription("当前不可用 供应商未开放此能力");
    expect(screen.getByText("供应商未开放此能力")).not.toBeVisible();
    await user.click(screen.getByText("查看完全访问的启用条件"));
    expect(screen.getByText("供应商未开放此能力")).toBeVisible();
    await user.click(full);
    expect(initial.onRequestChange).not.toHaveBeenCalled();
    await user.click(screen.getByRole(role, { name: "帮我批准" }));
    expect(initial.onRequestChange).toHaveBeenCalledExactlyOnceWith({ mode: "auto", confirmFull: false });
  });
});

describe.each(["menu", "settings"] as const)("V2ApprovalModeControl pre-creation %s", (variant) => {
  const role = variant === "menu" ? "menuitemradio" : "button";
  async function open(user: ReturnType<typeof userEvent.setup>) {
    if (variant === "menu") await user.click(screen.getByRole("button", { expanded: false }));
  }

  it("confirms the choice once without presenting a running activation", async () => {
    const user = userEvent.setup();
    const initial = props({ variant, selectionOnly: true });
    const view = render(<V2ApprovalModeControl {...initial} />);
    await open(user);
    expect(view.container.textContent).not.toMatch(/已激活|未激活|重新激活/u);
    await user.click(screen.getByRole(role, { name: "完全访问权限" }));
    const dialog = screen.getByRole("dialog", { name: "确认完全访问权限？" });
    expect(within(dialog).getByText(/项目文件、执行本机命令和访问公共网络/u)).toBeVisible();
    expect(initial.onRequestChange).not.toHaveBeenCalled();
    await user.dblClick(within(dialog).getByRole("button", { name: "确认" }));
    expect(initial.onRequestChange).toHaveBeenCalledExactlyOnceWith({ mode: "full", confirmFull: true });
    view.rerender(<V2ApprovalModeControl {...initial} mode="full" fullActivation="active" />);
    if (variant === "menu") {
      const trigger = screen.getByRole("button", { expanded: false });
      expect(trigger).toHaveTextContent(/^完全访问权限$/u);
      expect(trigger).toHaveAttribute("title", "完全访问权限");
    }
    await open(user);
    expect(screen.getByRole(role, { name: "完全访问权限" })).toHaveAccessibleDescription("允许文件、本机命令与公共网络访问，无需逐次批准。");
    expect(view.container.textContent).not.toMatch(/已激活|未激活|重新激活|已选择/u);
  });

  it("allows a fresh confirmation after the pre-creation Full choice expires", async () => {
    const user = userEvent.setup();
    const initial = props({ variant, selectionOnly: true, mode: "full", fullActivation: "inactive" });
    const view = render(<V2ApprovalModeControl {...initial} />);
    await open(user);
    const confirmationRole = variant === "menu" ? "menuitem" : "button";
    await user.click(screen.getByRole(confirmationRole, { name: "确认完全访问权限" }));
    expect(initial.onRequestChange).not.toHaveBeenCalled();
    await user.click(within(screen.getByRole("dialog", { name: "确认完全访问权限？" })).getByRole("button", { name: "取消" }));
    expect(initial.onRequestChange).not.toHaveBeenCalled();
    await open(user);
    await user.click(screen.getByRole(confirmationRole, { name: "确认完全访问权限" }));
    await user.click(within(screen.getByRole("dialog", { name: "确认完全访问权限？" })).getByRole("button", { name: "确认" }));
    expect(initial.onRequestChange).toHaveBeenCalledExactlyOnceWith({ mode: "full", confirmFull: true });
    expect(view.container.textContent).not.toMatch(/已激活|未激活|重新激活/u);
  });
});

describe("V2ApprovalModeControl changing host state", () => {
  it("blocks repeated Ask/Auto submissions as the host marks a request pending", async () => {
    const user = userEvent.setup();
    const request = vi.fn();
    function Host() {
      const [pending, setPending] = useState(false);
      return <V2ApprovalModeControl {...props({ variant: "settings", pending,
        onRequestChange: (value) => { setPending(true); request(value); } })} />;
    }
    render(<Host />);
    await user.dblClick(screen.getByRole("button", { name: "帮我批准" }));
    expect(request).toHaveBeenCalledExactlyOnceWith({ mode: "auto", confirmFull: false });
    expect(screen.getByRole("button", { name: "帮我批准" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "请求批准" })).toHaveAttribute("aria-pressed", "true");
  });

  it("locks an open confirmation while pending and permits keyboard cancellation after settlement", async () => {
    const user = userEvent.setup();
    const initial = props({ variant: "settings" });
    const view = render(<V2ApprovalModeControl {...initial} />);
    const trigger = screen.getByRole("button", { name: "完全访问权限" });
    await user.click(trigger);
    view.rerender(<V2ApprovalModeControl {...initial} pending />);
    const dialog = screen.getByRole("dialog");
    for (const button of within(dialog).getAllByRole("button")) expect(button).toBeDisabled();
    await user.keyboard("{Escape}{Enter}");
    fireEvent.mouseDown(dialog.parentElement!);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(initial.onRequestChange).not.toHaveBeenCalled();
    view.rerender(<V2ApprovalModeControl {...initial} />);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it.each([
    { disabled: true }, { fullActivation: "unavailable" }, { fullActivation: "active" }, { mode: "auto" },
  ] satisfies Partial<ApprovalModeControlProps>[])("invalidates an outstanding confirmation after props change: %j", async (changed) => {
    const user = userEvent.setup();
    const initial = props({ variant: "settings" });
    const view = render(<V2ApprovalModeControl {...initial} />);
    await user.click(screen.getByRole("button", { name: "完全访问权限" }));
    const confirm = within(screen.getByRole("dialog")).getByRole("button", { name: "确认启用" });
    view.rerender(<V2ApprovalModeControl {...initial} {...changed} />);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    if (changed.disabled || changed.fullActivation === "unavailable") {
      expect(screen.getByRole("region", { name: "执行权限" })).toHaveFocus();
    } else expect(screen.getByRole("button", { name: "完全访问权限" })).toHaveFocus();
    fireEvent.click(confirm);
    view.rerender(<V2ApprovalModeControl {...initial} />);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(initial.onRequestChange).not.toHaveBeenCalled();
  });

  it("provides a readable fallback in the permission menu when the unavailable reason is blank", async () => {
    const user = userEvent.setup();
    render(<V2ApprovalModeControl {...props({ mode: "full", fullActivation: "unavailable", fullUnavailableReason: " " })} />);
    expect(screen.getByRole("button", { expanded: false })).toHaveTextContent("完全访问权限 · 不可用");
    expect(screen.queryByText("查看完全访问的启用条件")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { expanded: false }));
    await user.click(screen.getByText("查看完全访问的启用条件"));
    expect(screen.getByText("完全访问需要当前运行环境提供支持，请查看服务的权限配置。")).toBeVisible();
  });

  it("supports keyboard menu navigation, Escape and Tab without choosing a mode", async () => {
    const user = userEvent.setup();
    const initial = props();
    render(<><V2ApprovalModeControl {...initial} /><button>后续操作</button></>);
    const trigger = screen.getByRole("button", { expanded: false });
    trigger.focus(); await user.keyboard("{ArrowUp}");
    expect(screen.getByRole("menuitemradio", { name: "完全访问权限" })).toHaveFocus();
    await user.keyboard("{Home}"); expect(screen.getByRole("menuitemradio", { name: "帮我批准" })).toHaveFocus();
    await user.keyboard("{End}{ArrowDown}"); expect(screen.getByRole("menuitemradio", { name: "帮我批准" })).toHaveFocus();
    await user.keyboard("{Escape}"); expect(trigger).toHaveFocus();
    await user.keyboard("{ArrowDown}"); await user.tab();
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "后续操作" })).toHaveFocus();
    trigger.focus(); await user.keyboard("{ArrowDown}"); await user.tab({ shift: true });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
    expect(initial.onRequestChange).not.toHaveBeenCalled();
  });
});
