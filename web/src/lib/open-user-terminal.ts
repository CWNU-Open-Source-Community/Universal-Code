import type { FitAddon } from "@xterm/addon-fit";
import type { Terminal } from "@xterm/xterm";

export function openUserTerminal(terminal: Terminal, fit: FitAddon, host: HTMLElement) {
  const staging = host.ownerDocument.createElement("div");
  terminal.open(staging);
  const nonce = host.ownerDocument.querySelector<HTMLMetaElement>('meta[name="uc-style-nonce"]')?.nonce;
  // xterm 6 creates its styles synchronously; authorize them before their first DOM connection.
  if (nonce) {
    for (const style of staging.querySelectorAll("style")) style.nonce = nonce;
  }
  host.appendChild(terminal.element!);
  // Older WebViews measure text using DOM layout, which was unavailable in staging.
  terminal.resize(terminal.cols, terminal.rows);
  fit.fit();
}
