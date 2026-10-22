import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import { afterEach, vi } from "vitest";
import { openUserTerminal } from "./open-user-terminal";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("nonces real xterm styles before mounting and recovers its DOM text measurement", () => {
  vi.stubGlobal("OffscreenCanvas", undefined);
  vi.stubGlobal("matchMedia", () => ({ addListener() {}, removeListener() {} }));
  // JSDOM has no layout: expose character metrics only after the real measuring spans mount.
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.isConnected && this.classList.contains("xterm-char-measure-element") ? 256 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    return this.isConnected && this.classList.contains("xterm-char-measure-element") ? 16 : 0;
  });
  const meta = document.createElement("meta");
  meta.name = "uc-style-nonce";
  meta.nonce = "response-specific-nonce";
  document.head.appendChild(meta);
  const host = document.createElement("div");
  document.body.appendChild(host);
  const terminal = new Terminal({ cols: 120, rows: 32 });
  const fit = new FitAddon();
  terminal.loadAddon(fit);
  const appendChild = host.appendChild.bind(host);
  const mount = vi.spyOn(host, "appendChild").mockImplementation(<T extends Node>(node: T): T => {
    expect(node.isConnected).toBe(false);
    const styles = (node as unknown as HTMLElement).querySelectorAll("style");
    expect(styles).toHaveLength(3);
    expect([...styles].map((style) => style.nonce)).toEqual(Array(3).fill(meta.nonce));
    expect((node as unknown as HTMLElement).querySelector<HTMLElement>(".xterm-screen")?.style.width).toBe("0px");
    return appendChild(node);
  });
  try {
    openUserTerminal(terminal, fit, host);
    expect(mount).toHaveBeenCalledOnce();
    expect(terminal.element?.parentElement).toBe(host);
    expect(host.querySelector<HTMLElement>(".xterm-screen")?.style.width).toBe("960px");
    const styles = [...host.querySelectorAll("style")];
    terminal.options.theme = { foreground: "#ededed" };
    terminal.resize(130, 35);
    fit.fit();
    expect([...host.querySelectorAll("style")]).toEqual(styles);
    expect(styles.every((style) => style.nonce === meta.nonce)).toBe(true);
  } finally {
    terminal.dispose();
    host.remove();
    meta.remove();
  }
});
