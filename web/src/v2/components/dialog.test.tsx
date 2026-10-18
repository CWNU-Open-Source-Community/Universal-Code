import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { LocaleProvider } from "../../lib/locale";
import { V2ConfirmDialog } from "./dialog";

it.each([
  { locale: "zh-CN", cancel: "取消", close: "关闭", processing: "正在处理…" },
  { locale: "en-US", cancel: "Cancel", close: "Close", processing: "Processing…" },
])("localizes confirmation defaults in $locale while preserving supplied content and busy behavior", async ({ locale, cancel, close, processing }) => {
  localStorage.setItem("prayu.locale.v1", locale);
  const onCancel = vi.fn();
  const onConfirm = vi.fn();
  const props = { open: true, title: "项目：我的新对话", description: "保留这段用户提供的说明", confirmLabel: "用户操作", onCancel, onConfirm };
  const view = render(<LocaleProvider><V2ConfirmDialog {...props} busy /></LocaleProvider>);
  const dialog = screen.getByRole("dialog", { name: props.title });
  expect(dialog).toHaveTextContent(props.description);
  expect(within(dialog).getByRole("button", { name: cancel })).toBeDisabled();
  expect(within(dialog).getByRole("button", { name: close })).toBeDisabled();
  expect(within(dialog).getByRole("button", { name: processing })).toBeDisabled();
  await userEvent.click(within(dialog).getByRole("button", { name: processing }));
  expect(onConfirm).not.toHaveBeenCalled();
  view.rerender(<LocaleProvider><V2ConfirmDialog {...props} /></LocaleProvider>);
  await userEvent.click(within(dialog).getByRole("button", { name: props.confirmLabel }));
  expect(onConfirm).toHaveBeenCalledOnce();
  await userEvent.click(within(dialog).getByRole("button", { name: cancel }));
  expect(onCancel).toHaveBeenCalledOnce();
});
