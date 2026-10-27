import DOMPurify from "dompurify";
// @ts-expect-error Monaco ships this runtime sanitizer without declarations.
import { safeSetInnerHtml, sanitizeHtml } from "monaco-editor/base/browser/domSanitize.js";

describe("Monaco sanitizer", () => {
  it.each(["trusted HTML", "DOM fragment"])("uses the installed DOMPurify for %s", (output) => {
    const sanitize = vi.spyOn(DOMPurify, "sanitize");
    const dirty = '<p>Kept <strong>formatting</strong></p><script>alert(1)</script>' +
      '<img src="data:," onerror="alert(2)"><a href="javascript:alert(3)">unsafe</a>' +
      '<a href="https://example.com">safe</a>';
    const target = document.createElement("div");

    if (output === "trusted HTML") target.innerHTML = String(sanitizeHtml(dirty));
    else safeSetInnerHtml(target, dirty);

    expect(sanitize).toHaveBeenCalled();
    expect(target.querySelector("strong")).toHaveTextContent("formatting");
    expect(target.querySelector('script, [onerror], a[href^="javascript:"]')).toBeNull();
    expect(target.querySelector('a[href="https://example.com"]')).toHaveTextContent("safe");
  });
});
