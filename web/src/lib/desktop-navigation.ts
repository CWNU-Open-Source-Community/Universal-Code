import { desktopBridgeAvailable } from "./desktop-bridge";
import { openWebURL, webURL } from "./chat-resources";

function sameRendererOrigin(raw: string): boolean {
  try {
    const target = new URL(raw, window.location.href);
    return target.origin === window.location.origin &&
      (target.protocol === "http:" || target.protocol === "https:") &&
      target.username === "" && target.password === "";
  } catch {
    return false;
  }
}

function sameRendererBlobDownload(anchor: Element | null, raw: string, event: Event): boolean {
  if (!anchor?.hasAttribute("download") || event.type !== "click" ||
    !(event instanceof MouseEvent) || event.button !== 0 ||
    event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) {
    return false;
  }
  try {
    const target = new URL(raw, window.location.href);
    return target.protocol === "blob:" && target.origin !== "null" &&
      target.origin === window.location.origin;
  } catch {
    return false;
  }
}

function closestElement(event: Event, selector: string): Element | null {
  const target = event.target;
  if (target instanceof Element) {
    return target.closest(selector);
  }
  return target instanceof Node ? target.parentElement?.closest(selector) ?? null : null;
}

function block(event: Event): void {
  event.preventDefault();
  event.stopImmediatePropagation();
}

// installDesktopNavigationGuard is renderer defense in depth. Wails' empty
// BindingsAllowedOrigins remains the native authority boundary; this guard
// prevents ordinary links, forms, and popup calls from leaving that origin.
export function installDesktopNavigationGuard(): () => void {
  if (!desktopBridgeAvailable()) {
    return () => undefined;
  }
  const onLink = (event: Event) => {
    const anchor = closestElement(event, "a[href]");
    const href = anchor?.getAttribute("href");
    // File links use a same-origin fragment and a React handler. External web
    // addresses leave through the native browser, never inside this renderer.
    if (href !== null && href !== undefined && !sameRendererOrigin(href) &&
      !sameRendererBlobDownload(anchor, href, event)) {
      block(event);
      if (webURL(href) && event instanceof MouseEvent && (event.button === 0 || event.button === 1)) {
        void openWebURL(href).catch((error) => {
          document.dispatchEvent(new CustomEvent("uc-link-error", { detail: error instanceof Error ? error.message : "无法打开链接" }));
        });
      }
    }
  };
  const onSubmit = (event: Event) => {
    const form = closestElement(event, "form[action]");
    const action = form?.getAttribute("action");
    if (action !== null && action !== undefined && !sameRendererOrigin(action)) {
      block(event);
    }
  };
  document.addEventListener("click", onLink, true);
  document.addEventListener("auxclick", onLink, true);
  document.addEventListener("submit", onSubmit, true);

  const originalOpen = window.open;
  const blockedOpen: typeof window.open = () => null;
  window.open = blockedOpen;
  return () => {
    document.removeEventListener("click", onLink, true);
    document.removeEventListener("auxclick", onLink, true);
    document.removeEventListener("submit", onSubmit, true);
    if (window.open === blockedOpen) {
      window.open = originalOpen;
    }
  };
}
