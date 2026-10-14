import { installDesktopNavigationGuard } from "./desktop-navigation";

describe("Desktop renderer navigation guard", () => {
  beforeEach(() => {
    window.go = {
      desktop: {
        DesktopBridge: {
          Bootstrap: async () => ({}),
          InstallSkillPackage: async () => ({}),
          SelectSkillPackage: async () => ({}),
          PreviewSkillPackage: async () => ({}),
        },
      },
    };
  });

  afterEach(() => {
    document.body.replaceChildren();
    delete window.go;
  });

  it("blocks external links, form actions, and popup calls while allowing same-origin routes", () => {
    const originalOpen = window.open;
    const remove = installDesktopNavigationGuard();

    const external = document.createElement("a");
    external.href = "https://example.com/automated-notes";
    document.body.append(external);
    expect(external.dispatchEvent(new Event("click", { bubbles: true, cancelable: true }))).toBe(false);

    const internal = document.createElement("a");
    internal.href = "#run-local";
    document.body.append(internal);
    expect(internal.dispatchEvent(new Event("click", { bubbles: true, cancelable: true }))).toBe(true);

    const form = document.createElement("form");
    form.action = "https://example.com/upload";
    document.body.append(form);
    expect(form.dispatchEvent(new SubmitEvent("submit", { bubbles: true, cancelable: true }))).toBe(false);
    expect(window.open("https://example.com")).toBeNull();

    remove();
    expect(window.open).toBe(originalOpen);
  });

  it("does not change ordinary browser navigation without the native bridge", () => {
    delete window.go;
    const remove = installDesktopNavigationGuard();
    const external = document.createElement("a");
    external.href = "https://example.com/ordinary-browser";
    document.body.append(external);
    expect(external.dispatchEvent(new Event("click", { bubbles: true, cancelable: true }))).toBe(true);
    remove();
  });

  it("allows a primary download click for a same-origin blob", () => {
    const remove = installDesktopNavigationGuard();
    try {
      const anchor = document.createElement("a");
      anchor.href = `blob:${window.location.origin}/verified-image`;
      anchor.download = "source.png";
      document.body.append(anchor);
      const event = new MouseEvent("click", { bubbles: true, cancelable: true });
      expect(anchor.dispatchEvent(event)).toBe(true);
      expect(event.defaultPrevented).toBe(false);
    } finally { remove(); }
  });

  it("continues to block blob navigation and untrusted downloads", () => {
    const remove = installDesktopNavigationGuard();
    try {
      for (const [href, download, type, options] of [
        [`blob:${window.location.origin}/image`, false, "click", {}],
        ["blob:https://example.com/image", true, "click", {}],
        ["blob:null/image", true, "click", {}],
        ["data:image/png;base64,aGVsbG8=", true, "click", {}],
        ["javascript:alert(1)", true, "click", {}],
        [`blob:${window.location.origin}/image`, true, "auxclick", { button: 1 }],
        [`blob:${window.location.origin}/image`, true, "click", { ctrlKey: true }],
      ] as const) {
        const anchor = document.createElement("a");
        anchor.href = href;
        if (download) anchor.download = "source.png";
        document.body.append(anchor);
        expect(anchor.dispatchEvent(new MouseEvent(type, {
          bubbles: true, cancelable: true, ...options,
        }))).toBe(false);
        anchor.remove();
      }
    } finally { remove(); }
  });
});
