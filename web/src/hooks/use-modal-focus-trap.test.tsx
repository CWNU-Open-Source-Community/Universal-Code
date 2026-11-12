import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useModalFocusTrap } from "./use-modal-focus-trap";

function Harness({ escapeDisabled = false }: { escapeDisabled?: boolean }) {
  const [open, setOpen] = useState(false);
  const dialogRef = useModalFocusTrap<HTMLElement>(open, () => setOpen(false), escapeDisabled);
  return <>
    <button onClick={() => setOpen(true)} type="button">Open dialog</button>
    <a href="#outside">Outside link</a>
    {open && <section aria-label="Example dialog" aria-modal="true" ref={dialogRef}
      role="dialog" tabIndex={-1}>
      <button type="button">First action</button>
      <button disabled type="button">Disabled action</button>
      <button type="button">Last action</button>
    </section>}
  </>;
}

describe("useModalFocusTrap", () => {
  it("keeps tab focus inside the dialog and returns it to the opener", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const opener = screen.getByRole("button", { name: "Open dialog" });
    await user.click(opener);

    const first = screen.getByRole("button", { name: "First action" });
    const last = screen.getByRole("button", { name: "Last action" });
    expect(first).toHaveFocus();
    await user.tab({ shift: true });
    expect(last).toHaveFocus();
    await user.tab();
    expect(first).toHaveFocus();
    expect(screen.getByRole("link", { name: "Outside link" })).not.toHaveFocus();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(opener).toHaveFocus();
  });

  it("keeps a busy dialog open when Escape is disabled", async () => {
    const user = userEvent.setup();
    render(<Harness escapeDisabled />);
    await user.click(screen.getByRole("button", { name: "Open dialog" }));
    expect(screen.getByRole("button", { name: "First action" })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: "Last action" })).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog", { name: "Example dialog" })).toBeInTheDocument();
  });
});
