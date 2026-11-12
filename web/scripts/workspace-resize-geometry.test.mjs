import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { after, before, test } from "node:test";
import { chromium } from "@playwright/test";

const css = (await Promise.all([
  "../src/v2/styles.css", "../src/v2/components/workspace-dock.css", "../src/v2/components/shared-browser.css",
].map((path) => readFile(new URL(path, import.meta.url), "utf8")))).join("\n");
let browser;
before(async () => { browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE }); });
after(async () => { await browser?.close(); });

// Use the two components' actual wrapper/child ordering and production CSS.
// The native WebView covers exactly the reported surface rect; no CSS z-index
// can rescue a separator whose rect intersects that native child window.
async function geometry(kind, width, scale, fullView = false) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, deviceScaleFactor: scale });
  try {
    const page = await context.newPage();
    const separator = `<div class="v2-workspace-resize-handle" role="separator"${fullView ? " hidden" : ""}></div>`;
    await page.setContent(`<style>${css}
      :root{--v2-border:#ddd;--prayu-surface-solid:#fff;--v2-pane-ease:linear}
      html,body{margin:0;width:100%;height:100%} *{transition:none!important;animation:none!important}
      .v2-conversation-stage{height:100vh} .v2-workspace-tabbar{box-sizing:border-box}
    </style><div class="v2-conversation-stage${kind === "draft" ? " v2-draft-browser-stage" : ""}${fullView ? " is-full-view" : ""}">
      <div class="v2-conversation-main-column"></div>
      <div class="v2-workbench-slot is-open" style="--v2-workbench-width:${width}px">
        <section class="v2-workspace-dock is-docked${fullView ? " is-full-view" : ""}">
          ${kind === "draft" ? separator : ""}
          <header class="v2-workspace-tabbar">Browser tab</header>
          <div class="v2-workspace-dock-body">${kind === "conversation" ? '<div class="v2-workspace-tabpanel">' : ""}
            <div class="v2-shared-browser"><div class="v2-browser-toolbar">Address</div><div class="v2-browser-native-surface"></div></div>
          ${kind === "conversation" ? "</div>" : ""}</div>
          ${kind === "conversation" ? separator : ""}
        </section>
      </div>
    </div>`);
    return await page.evaluate(() => {
      const rect = (selector) => {
        const box = document.querySelector(selector).getBoundingClientRect();
        return { left: box.left, right: box.right, top: box.top, bottom: box.bottom, width: box.width };
      };
      const handle = document.querySelector('[role="separator"]');
      const grip = rect('[role="separator"]'), surface = rect(".v2-browser-native-surface"), dock = rect(".v2-workspace-dock"), slot = rect(".v2-workbench-slot");
      const y = (surface.top + surface.bottom) / 2;
      const hits = [0.5, grip.width / 2, grip.width - 0.5].map((offset) => document.elementFromPoint(grip.left + offset, y) === handle);
      return { grip, surface, dock, slot, hits, cursor: getComputedStyle(handle).cursor, display: getComputedStyle(handle).display, scale: devicePixelRatio };
    });
  } finally { await context.close(); }
}

for (const kind of ["draft", "conversation"]) {
  test(`${kind}: the complete separator strip is outside native browser bounds`, async () => {
    for (const width of [340, 440, 900]) for (const scale of [1, 1.5, 2]) {
      const result = await geometry(kind, width, scale);
      assert.ok(Math.round(result.grip.right * scale) <= Math.round(result.surface.left * scale), `native browser overlaps the separator: ${JSON.stringify(result)}`);
      assert.ok(result.grip.width >= 8, JSON.stringify(result));
      assert.ok(result.grip.left >= result.slot.left && result.grip.right <= result.slot.right, "separator was clipped by the slot");
      assert.deepEqual(result.hits, [true, true, true]);
      assert.equal(result.cursor, "col-resize");
    }
  });
  test(`${kind}: full view hides the separator and removes its reserved strip`, async () => {
    const result = await geometry(kind, 440, 1, true);
    assert.equal(result.display, "none");
    assert.equal(result.surface.left, result.dock.left);
  });
}
