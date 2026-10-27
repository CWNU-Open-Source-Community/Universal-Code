import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CommandPalette } from "./command-palette";

describe("CommandPalette", () => {
  it("opens from the keyboard, filters, and invokes only supplied commands", async () => {
    const openFiles = vi.fn();
    const refresh = vi.fn();
    const user = userEvent.setup();
    render(<CommandPalette commands={[
      { id: "files", label: "Open Files", group: "Navigate", run: openFiles },
      { id: "refresh", label: "Refresh Run data", group: "Data", run: refresh },
    ]} />);

    const opener = screen.getByRole("button", { name: "Open command palette" });
    opener.focus();
    fireEvent.keyDown(window, { ctrlKey: true, key: "k" });
    expect(await screen.findByRole("dialog", { name: "Command palette" })).toBeInTheDocument();
    const input = screen.getByRole("searchbox", { name: "Find a command" });
    await user.type(input, "files");
    expect(screen.getByRole("option", { name: /Open Files/ })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /Refresh Run data/ })).not.toBeInTheDocument();
    await user.keyboard("{Enter}");
    expect(openFiles).toHaveBeenCalledTimes(1);
    expect(refresh).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog", { name: "Command palette" })).not.toBeInTheDocument();
    expect(opener).toHaveFocus();
  });

  it("does not run a command when Enter only commits an IME candidate", async () => {
    const openFiles = vi.fn();
    const user = userEvent.setup();
    render(<CommandPalette commands={[
      { id: "files", label: "Open Files", group: "Navigate", run: openFiles },
    ]} />);
    fireEvent.keyDown(window, { ctrlKey: true, key: "k" });
    const input = screen.getByRole("searchbox", { name: "Find a command" });
    await user.type(input, "files");
    fireEvent.keyDown(input, { key: "Enter", isComposing: true });
    fireEvent.keyDown(input, { key: "Enter", keyCode: 229 });
    expect(openFiles).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog", { name: "Command palette" })).toBeInTheDocument();
    fireEvent.keyDown(input, { key: "Enter" });
    expect(openFiles).toHaveBeenCalledTimes(1);
  });
});
