import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../../lib/locale";
import { V2PromptCommandMenu } from "./prompt-command-menu";
import { createPromptCommand, promptCommandsStorageKey } from "../../lib/prompt-commands";

let backing: Record<string, string>;

function renderMenu(props: Parameters<typeof V2PromptCommandMenu>[0]) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  return render(<LocaleProvider><V2PromptCommandMenu {...props} /></LocaleProvider>);
}

const localStorageStub: Storage = {
  getItem: (key: string) => key in backing ? backing[key] : null,
  setItem: (key: string, value: string) => { backing[key] = value; },
  removeItem: (key: string) => { delete backing[key]; },
  clear: () => { backing = {}; },
  key: (index: number) => Object.keys(backing)[index] ?? null,
  get length() { return Object.keys(backing).length; },
};

beforeEach(() => { backing = {}; });
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe("V2PromptCommandMenu", () => {
  it("lists stored commands, filters by the typed query and inserts the expanded body", async () => {
    createPromptCommand(localStorageStub, { name: "release-check", title: "Release checklist",
      body: "Check $ARGUMENTS against the notes." });
    createPromptCommand(localStorageStub, { name: "audit", title: "Audit", body: "Audit the module." });
    const onSelect = vi.fn();
    renderMenu({ storage: localStorageStub, slash: { name: "rel", args: "" }, onSelect });

    const menu = screen.getByRole("menu", { name: "自定义指令" });
    expect(menu).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /release-check/ })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /audit/ })).not.toBeInTheDocument();

    await userEvent.setup().click(screen.getByRole("menuitem", { name: /release-check/ }));
    expect(onSelect).toHaveBeenCalledWith("Check  against the notes.");
  });

  it("reports an empty library and opens the manager from the menu", async () => {
    const onSelect = vi.fn();
    renderMenu({ storage: localStorageStub, slash: { name: "", args: "" }, onSelect: onSelect });
    expect(screen.getByText("指令库还是空的。")).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("menuitem", { name: "管理指令库…" }));
    expect(screen.getByRole("dialog", { name: "管理自定义指令" })).toBeInTheDocument();
  });

  it("creates, edits and deletes commands through the manager dialog", async () => {
    const user = userEvent.setup();
    const created = createPromptCommand(localStorageStub, { name: "audit", title: "Audit", body: "Audit the module." });
    renderMenu({ storage: localStorageStub, slash: { name: "", args: "" }, onSelect: vi.fn() });
    await user.click(screen.getByRole("menuitem", { name: "管理指令库…" }));

    await user.type(screen.getByRole("textbox", { name: "指令名" }), "release-check");
    await user.type(screen.getByRole("textbox", { name: "指令内容" }), "Check $ARGUMENTS against the notes.");
    await user.click(screen.getByRole("button", { name: "新建指令" }));
    await waitFor(() => expect(backing[promptCommandsStorageKey]).toContain("release-check"));

    // Duplicate names are rejected with the operator-facing reason.
    await user.click(screen.getAllByRole("button", { name: "编辑" })[1]);
    const nameInput = screen.getByRole("textbox", { name: "指令名" });
    await user.clear(nameInput);
    await user.type(nameInput, "audit");
    await user.click(screen.getByRole("button", { name: "保存修改" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("已存在同名指令");

    // Edit reuses the stored record and keeps one copy per name.
    await user.click(screen.getAllByRole("button", { name: "编辑" })[1]);
    const title = screen.getByRole("textbox", { name: "标题（可选）" });
    await user.clear(title);
    await user.type(title, "Release checklist v2");
    await user.click(screen.getByRole("button", { name: "保存修改" }));
    await waitFor(() => expect(backing[promptCommandsStorageKey]).toContain("Release checklist v2"));

    await user.click(screen.getByRole("button", { name: `删除 /audit` }));
    await waitFor(() => expect(backing[promptCommandsStorageKey]).not.toContain('"audit"'));
    expect(backing[promptCommandsStorageKey]).toContain("release-check");
    expect(created.id).toBeTypeOf("string");
  });

  it("passes typed arguments through to the expansion", async () => {
    const onSelect = vi.fn();
    createPromptCommand(localStorageStub, { name: "release-check", title: "",
      body: "Check $ARGUMENTS against the notes." });
    renderMenu({ storage: localStorageStub, slash: { name: "release-check", args: "src/main.ts" }, onSelect });
    await userEvent.setup().click(screen.getByRole("menuitem", { name: /release-check/ }));
    expect(onSelect).toHaveBeenCalledWith("Check src/main.ts against the notes.");
  });

  it("survives undefined storage by reporting storage unavailable on mutations", async () => {
    const user = userEvent.setup();
    renderMenu({ storage: undefined, slash: { name: "", args: "" }, onSelect: vi.fn() });
    await user.click(screen.getByRole("menuitem", { name: "管理指令库…" }));
    await user.type(screen.getByRole("textbox", { name: "指令名" }), "test-cmd");
    await user.type(screen.getByRole("textbox", { name: "指令内容" }), "Test body");
    await user.click(screen.getByRole("button", { name: "新建指令" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("本机存储不可用，指令库无法保存。");
  });
});
