/// <reference types="node" />

import { readFileSync } from "node:fs";
import { defaultPrayuReviewColors } from "./lib/appearance";

const styles = readFileSync("src/styles.css", "utf8");
const uncommentedStyles = styles.replace(/\/\*[\s\S]*?\*\//gu, "");

function declarationsFor(selector: string, property: string): string[] {
  const values: string[] = [];
  for (const rule of uncommentedStyles.matchAll(/([^{}]+)\{([^{}]*)\}/gu)) {
    const selectors = rule[1]?.split(",").map((value) => value.trim()) ?? [];
    if (!selectors.includes(selector)) continue;
    const escapedProperty = property.replace(/[.*+?^${}()|[\]\\]/gu, "\\$&");
    const declarationPattern = new RegExp(`(?:^|;)\\s*${escapedProperty}\\s*:\\s*([^;]+)`, "gu");
    for (const declaration of (rule[2] ?? "").matchAll(declarationPattern)) {
      values.push(declaration[1]!.trim());
    }
  }
  return values;
}

function positionsFor(selector: string): string[] {
  return declarationsFor(selector, "position");
}

describe("workbench Composer layout", () => {
  it("preserves the sticky Session Composer and public model stream after the glass style layer", () => {
    expect(styles).toContain(".prayu-shell .session-composer");
    expect(positionsFor(".prayu-shell .session-composer")).toEqual(["sticky"]);
    expect(positionsFor(".prayu-shell .public-model-stream")).toEqual(["relative"]);
  });
});

describe("product typography roles", () => {
  it("uses proportional interface fonts and shares them with display text", () => {
    const uiStack = styles.match(/--prayu-font-ui:\s*([^;]+);/u)?.[1] ?? "";
    const displayStack = styles.match(/--prayu-font-display:\s*([^;]+);/u)?.[1] ?? "";

    expect(uiStack).toMatch(/^"Segoe UI",/u);
    expect(uiStack).toContain("HarmonyOS Sans SC");
    expect(uiStack).toContain("Microsoft YaHei UI");
    expect(uiStack).toContain("PingFang SC");
    expect(uiStack).toMatch(/system-ui,\s*sans-serif$/u);
    expect(uiStack).not.toMatch(/JetBrains Mono|Cascadia Code|monospace/u);
    expect(displayStack).toBe("var(--prayu-font-ui)");
  });

  it("loads the four byte-identical vendor HarmonyOS Sans TTF weights", () => {
    const harmonyFaces = [...styles.matchAll(/@font-face\s*\{([^}]+)\}/gu)]
      .map((match) => match[1] ?? "")
      .filter((block) => block.includes('font-family: "HarmonyOS Sans SC"'))
      .map((block) => ({
        source: block.match(/url\("\.\/assets\/fonts\/([^"]+)"\)/u)?.[1],
        format: block.match(/format\("([^"]+)"\)/u)?.[1],
        weight: Number(block.match(/font-weight:\s*(\d+)/u)?.[1]),
      }));

    expect(harmonyFaces).toEqual([
      { source: "HarmonyOSSansSC-Regular.ttf", format: "truetype", weight: 400 },
      { source: "HarmonyOSSansSC-Medium.ttf", format: "truetype", weight: 500 },
      { source: "HarmonyOSSansSC-Semibold.ttf", format: "truetype", weight: 600 },
      { source: "HarmonyOSSansSC-Bold.ttf", format: "truetype", weight: 700 },
    ]);
    expect(styles).not.toContain("HarmonyOSSansSC-Regular.woff2");
    expect(styles).not.toContain("HarmonyOSSansSC-Medium.woff2");
    expect(styles).not.toContain("HarmonyOSSansSC-Semibold.woff2");
    expect(styles).not.toContain("HarmonyOSSansSC-Bold.woff2");
  });

  it("keeps the bilingual brand in the display and interface roles", () => {
    expect(declarationsFor(".prayu-brand", "font-family")).toEqual(["var(--prayu-font-display)"]);
    expect(declarationsFor(".prayu-brand-copy small", "font-family")).toEqual(["var(--prayu-font-ui)"]);
    expect(styles).toMatch(/\.prayu-brand-name-latin\s*\{[^}]*font-weight:\s*650[^}]*letter-spacing:\s*-\.018em/isu);
    expect(styles).toMatch(/\.prayu-brand-name-cjk\s*\{[^}]*font-weight:\s*500[^}]*letter-spacing:\s*0/isu);
    expect(styles).toMatch(/\.prayu-brand-compact \.prayu-brand-name\s*\{[^}]*overflow:\s*hidden[^}]*text-overflow:\s*ellipsis[^}]*white-space:\s*nowrap/isu);
    expect(declarationsFor(".prayu-brand-copy small", "color")).toEqual([
      "var(--prayu-muted)",
      "color-mix(in srgb, var(--prayu-text) 64%, var(--prayu-muted))",
    ]);
  });

  it("reserves the mono role for technical content and keyboard shortcuts", () => {
    const monoStack = styles.match(/--prayu-font-mono:\s*([^;]+);/u)?.[1] ?? "";
    expect(monoStack).toContain("JetBrains Mono Variable");
    expect(declarationsFor("code", "font-family").at(-1)).toBe("var(--prayu-font-mono)");
    expect(declarationsFor("kbd", "font-family").at(-1)).toBe("var(--prayu-font-mono)");
    expect(declarationsFor(".unified-diff", "font-family").at(-1)).toBe("var(--prayu-font-mono)");
    expect(declarationsFor(".user-terminal-host", "font-family").at(-1)).toBe("var(--prayu-font-mono)");
    expect(styles.match(/"Cascadia Code"/gu)).toHaveLength(1);
  });

  it("prevents table and modal header horizontal overflow", () => {
    expect(declarationsFor(".safe-markdown table", "overflow-x")).toEqual(["auto"]);
    expect(declarationsFor(".safe-markdown table", "display")).toEqual(["block"]);
    expect(declarationsFor(".desktop-dialog h2", "text-overflow")).toEqual(["ellipsis"]);
  });
});

describe("review color tokens", () => {
  it.each(["light", "dark", "glass"] as const)("keeps the %s CSS tokens aligned with the appearance defaults", (theme) => {
    const selector = `[data-prayu-theme="${theme}"]`;
    expect(declarationsFor(selector, "--prayu-diff-add")).toEqual([defaultPrayuReviewColors[theme].add]);
    expect(declarationsFor(selector, "--prayu-diff-delete")).toEqual([defaultPrayuReviewColors[theme].remove]);
  });
});
