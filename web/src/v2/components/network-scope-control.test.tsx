import { render as renderComponent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState, type ReactElement } from "react";
import { LocaleProvider } from "../../lib/locale";
import { V2NetworkScopeControl } from "./network-scope-control";
import type { WebAccessMode } from "../../api/types";

function render(ui: ReactElement) {
  return renderComponent(<LocaleProvider>{ui}</LocaleProvider>);
}

beforeEach(() => localStorage.setItem("prayu.locale.v1", "zh-CN"));

describe("V2NetworkScopeControl", () => {
  it("keeps web access approval explicit in English and preserves exact domains", async () => {
    localStorage.setItem("prayu.locale.v1", "en-US");
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<V2NetworkScopeControl mode="rules" onChange={onChange} targets={[]} />);
    await user.click(screen.getByRole("button", { name: "Rule-based search" }));
    const panel = screen.getByRole("dialog", { name: "New conversation web access" });
    expect(within(panel).getByRole("combobox", { name: "Web search mode" })).toHaveValue("rules");
    expect(within(panel).getByRole("option", { name: "Cached search (coming soon)" })).toBeDisabled();
    await user.type(within(panel).getByRole("textbox", { name: "Allowed HTTPS targets" }), "https://Docs.Example.COM:443/");
    await user.click(within(panel).getByRole("button", { name: "Save domains" }));
    expect(onChange).not.toHaveBeenCalled();
    const dialog = screen.getByRole("dialog", { name: "Save web rules for new conversations?" });
    expect(dialog).toHaveTextContent("Other web requests require approval.");
    await user.click(within(dialog).getByRole("button", { name: "Save domains" }));
    expect(onChange).toHaveBeenCalledExactlyOnceWith("rules", ["docs.example.com"]);
  });
  it("requires an explicit confirmation before granting a bounded HTTPS allowlist", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<V2NetworkScopeControl mode="rules" onChange={onChange} targets={[]} />);

    await user.click(screen.getByRole("button", { name: /规则搜索/u }));
    await user.type(screen.getByRole("textbox", { name: "允许访问的 HTTPS 目标" }),
      "docs.example.com\nhttps://search.example.org");
    await user.click(screen.getByRole("button", { name: /保存域名/u }));

    expect(onChange).not.toHaveBeenCalled();
    const dialog = screen.getByRole("dialog", { name: "保存新对话网页规则？" });
    await user.click(within(dialog).getByRole("button", { name: "保存域名" }));
    expect(onChange).toHaveBeenCalledWith("rules", [
      "docs.example.com", "search.example.org",
    ]);
  });

  it("canonicalizes equivalent HTTPS origins before granting them", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<V2NetworkScopeControl mode="rules" onChange={onChange} targets={[]} />);

    await user.click(screen.getByRole("button", { name: /规则搜索/u }));
    await user.type(screen.getByRole("textbox", { name: "允许访问的 HTTPS 目标" }),
      "HTTPS://Docs.Example.COM:443/\ndocs.example.com");
    await user.click(screen.getByRole("button", { name: /保存域名/u }));
    await user.click(within(screen.getByRole("dialog", { name: "保存新对话网页规则？" }))
      .getByRole("button", { name: "保存域名" }));

    expect(onChange).toHaveBeenCalledWith("rules", ["docs.example.com"]);
  });

  it("rejects wildcard, path-bearing, and non-HTTPS targets before submission", async () => {
    const user = userEvent.setup();
    render(<V2NetworkScopeControl mode="rules" onChange={vi.fn()} targets={[]} />);

    await user.click(screen.getByRole("button", { name: /规则搜索/u }));
    await user.type(screen.getByRole("textbox", { name: "允许访问的 HTTPS 目标" }),
      "*.example.com\nhttp://example.org\nhttps://example.net/path\nlocalhost\n192.168.1.2");

    expect(screen.getByRole("alert")).toHaveTextContent("只接受无路径、查询或通配符");
    expect(screen.getByRole("button", { name: /保存域名/u })).toBeDisabled();
  });

  it.each(["disabled", "full"] as const)("shows the %s default and changes the mode without per-domain input", async (mode) => {
    const user = userEvent.setup(); const onChange = vi.fn();
    render(<V2NetworkScopeControl mode={mode} onChange={onChange} targets={[]} />);
    const trigger = screen.getByRole("button", { name: mode === "full" ? "完全访问" : "禁止搜索" });
    trigger.focus(); await user.keyboard("{Enter}");
    const dialog = screen.getByRole("dialog", { name: "新对话网页访问" });
    expect(dialog).toHaveFocus();
    expect(within(dialog).queryByRole("textbox")).not.toBeInTheDocument();
    await user.tab(); expect(within(dialog).getByRole("combobox")).toHaveFocus();
    await user.selectOptions(within(dialog).getByRole("combobox"), mode === "full" ? "disabled" : "full");
    expect(onChange).toHaveBeenCalledExactlyOnceWith(mode === "full" ? "disabled" : "full", []);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument(); expect(trigger).toHaveFocus();
  });

  it("restores the controlled domain list when returning to rules instead of showing an abandoned draft", async () => {
    function ControlledDefault() {
      const [value, setValue] = useState<{ mode: WebAccessMode; targets: string[] }>({ mode: "rules", targets: ["saved.example.org"] });
      return <V2NetworkScopeControl {...value} onChange={(mode, targets) => setValue({ mode, targets })} />;
    }
    render(<ControlledDefault />); const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "规则搜索" }));
    await user.clear(screen.getByRole("textbox", { name: "允许访问的 HTTPS 目标" }));
    await user.type(screen.getByRole("textbox", { name: "允许访问的 HTTPS 目标" }), "unsaved.example.org");
    const select = screen.getByRole("combobox", { name: "网页搜索模式" });
    await user.selectOptions(select, "full"); await user.selectOptions(select, "rules");
    expect(screen.getByRole("textbox", { name: "允许访问的 HTTPS 目标" })).toHaveValue("");
  });
});
