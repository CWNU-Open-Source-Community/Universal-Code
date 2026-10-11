import { useRef } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { V2ComposerAddMenu, type ComposerAddAction } from "./composer-add-menu";

function Fixture({ disabled = false, referenceDisabled = false, onSelect = vi.fn(), onSubmit = vi.fn(), onNext = vi.fn() }) {
  const ref = useRef<HTMLButtonElement>(null);
  const actions: ComposerAddAction[] = [
    { id: "upload", label: "添加图片或文件", detail: "选择本机附件", icon: null, onSelect },
    { id: "reference", label: "引用项目文件", detail: referenceDisabled ? "当前执行中，稍后可引用" : "添加项目参考", icon: null, disabled: referenceDisabled, onSelect },
    { id: "paste", label: "粘贴文件", detail: "从资源管理器复制", icon: null, onSelect },
  ];
  return <form onSubmit={(event) => { event.preventDefault(); onSubmit(); }}>
    <textarea aria-label="草稿" defaultValue="保留要求" />
    <V2ComposerAddMenu actions={actions} disabled={disabled} triggerRef={ref} />
    <button type="button" onClick={onNext}>下一个操作</button>
  </form>;
}

it("keeps one collapsed entry and restores focus and draft on Escape without any action", async () => {
  const user = userEvent.setup(); const onSelect = vi.fn(); const onSubmit = vi.fn();
  render(<Fixture onSelect={onSelect} onSubmit={onSubmit} />);
  expect(screen.queryByRole("menuitem")).not.toBeInTheDocument();
  const trigger = screen.getByRole("button", { name: "添加附件" });
  await user.click(trigger);
  expect(screen.getByRole("menu", { name: "添加内容" })).toBeInTheDocument();
  expect(screen.getByRole("menuitem", { name: "添加图片或文件" })).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(trigger).toHaveFocus(); expect(trigger).toHaveAttribute("aria-expanded", "false");
  expect(screen.getByRole("textbox", { name: "草稿" })).toHaveValue("保留要求");
  expect(onSelect).not.toHaveBeenCalled(); expect(onSubmit).not.toHaveBeenCalled();
});

it("navigates with the keyboard, skips unavailable references, and invokes paste only on explicit selection", async () => {
  const user = userEvent.setup(); const onSelect = vi.fn(); const onSubmit = vi.fn();
  render(<Fixture referenceDisabled onSelect={onSelect} onSubmit={onSubmit} />);
  const trigger = screen.getByRole("button", { name: "添加附件" });
  trigger.focus(); await user.keyboard("{ArrowDown}");
  expect(screen.getByRole("menuitem", { name: "引用项目文件" })).toBeDisabled();
  await user.keyboard("{ArrowDown}");
  expect(screen.getByRole("menuitem", { name: "粘贴文件" })).toHaveFocus();
  expect(onSelect).not.toHaveBeenCalled();
  await user.keyboard("{Enter}");
  expect(onSelect).toHaveBeenCalledTimes(1); expect(onSubmit).not.toHaveBeenCalled();
  expect(screen.queryByRole("menu")).not.toBeInTheDocument(); expect(trigger).toHaveFocus();
});

it("allows tabbing out and closes when editing elsewhere or when controls become disabled", async () => {
  const user = userEvent.setup(); const onSelect = vi.fn();
  const view = render(<Fixture onSelect={onSelect} />);
  const trigger = screen.getByRole("button", { name: "添加附件" });
  await user.click(trigger); await user.tab();
  expect(screen.getByRole("button", { name: "下一个操作" })).toHaveFocus();
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  await user.click(trigger); await user.click(screen.getByRole("textbox", { name: "草稿" }));
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: "草稿" })).toHaveFocus();
  await user.click(trigger); view.rerender(<Fixture disabled onSelect={onSelect} />);
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  expect(onSelect).not.toHaveBeenCalled();
});

it("uses non-submit buttons even when the menu is inside the composer form", () => {
  const onSelect = vi.fn(); const onSubmit = vi.fn();
  render(<Fixture onSelect={onSelect} onSubmit={onSubmit} />);
  fireEvent.click(screen.getByRole("button", { name: "添加附件" }));
  fireEvent.click(screen.getByRole("menuitem", { name: "引用项目文件" }));
  expect(onSelect).toHaveBeenCalledTimes(1); expect(onSubmit).not.toHaveBeenCalled();
});

it("activates the next control on the first outside click without losing the draft", async () => {
  const user = userEvent.setup(); const onNext = vi.fn(); const onSelect = vi.fn();
  render(<Fixture onNext={onNext} onSelect={onSelect} />);
  await user.click(screen.getByRole("button", { name: "添加附件" }));
  const next = screen.getByRole("button", { name: "下一个操作" });
  await user.pointer({ keys: "[MouseLeft>]", target: next });
  expect(onNext).not.toHaveBeenCalled();
  await user.pointer({ keys: "[/MouseLeft]", target: next });
  expect(onNext).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: "草稿" })).toHaveValue("保留要求");
  expect(onSelect).not.toHaveBeenCalled();
});

it("closes with a second trigger click and with Shift+Tab without reopening on blur", async () => {
  const user = userEvent.setup(); render(<Fixture />);
  const trigger = screen.getByRole("button", { name: "添加附件" });
  await user.click(trigger);
  expect(screen.getByRole("menuitem", { name: "添加图片或文件" })).toHaveFocus();
  await user.click(trigger);
  expect(trigger).toHaveAttribute("aria-expanded", "false");
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  expect(trigger).toHaveFocus();
  await user.click(trigger); await user.tab({ shift: true });
  expect(screen.queryByRole("menu")).not.toBeInTheDocument(); expect(trigger).toHaveFocus();
});
