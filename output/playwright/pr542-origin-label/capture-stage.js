async (page) => {
  const phase = new URL(page.url()).searchParams.get("phase") || "current";
  const checks = [];
  for (const [size, viewport, query] of [
    ["desktop", {width: 1024, height: 600}, ""],
    ["narrow-long-origins", {width: 390, height: 800}, "&long=1"],
    ...(phase === "after" ? [["desktop-dark", {width: 1024, height: 600}, "&theme=dark"],
      ["desktop-english", {width: 1024, height: 600}, "&locale=en-US"]] : []),
  ]) {
    await page.setViewportSize(viewport);
    await page.goto(`http://127.0.0.1:5420/.tmp/pr542-browser-evidence.html?phase=${phase}${query}`);
    await page.getByRole("region", {name: /Agent 浏览器|Agent browser/, exact: true}).waitFor();
    await page.evaluate(() => document.fonts.ready);
    const expected = phase === "base" ? null : phase === "after"
      ? size === "desktop-english" ? "Declared navigation origins: " : "已声明的导航来源："
      : "本会话的浏览器仅在操作者声明的来源内工作：";
    const note = page.locator(".v2-agent-browser-note").first();
    if (expected && !(await note.textContent()).startsWith(expected)) throw new Error("Unexpected label at " + phase);
    if (phase === "base" && (await page.locator(".v2-agent-browser").textContent()).includes("example.org:443")) throw new Error("Base unexpectedly includes origins");
    const prefix = `output/playwright/pr542-origin-label/${phase}-${size}`;
    await page.screenshot({path: prefix + "-viewport.png"});
    await page.locator(".v2-agent-browser").screenshot({path: prefix + "-card.png"});
    const metrics = await page.evaluate(() => {
      const card = document.querySelector(".v2-agent-browser");
      const body = document.querySelector(".v2-agent-browser-body");
      const note = document.querySelector(".v2-agent-browser-note");
      const dismiss = document.querySelector(".v2-agent-browser-dismiss");
      const stop = Array.from(document.querySelectorAll("button")).find(el => /停止浏览器|Stop browser/.test(el.textContent));
      const dimensions = el => ({width: el.clientWidth, height: el.clientHeight, scrollWidth: el.scrollWidth, scrollHeight: el.scrollHeight});
      const controls = [dismiss, stop].map(el => {const rect = el.getBoundingClientRect(); return {label: el.getAttribute("aria-label") || el.textContent, x: rect.x, right: rect.right, top: rect.top, bottom: rect.bottom};});
      return {theme: document.documentElement.dataset.prayuTheme, locale: document.documentElement.lang,
        viewport: {width: innerWidth, height: innerHeight}, document: dimensions(document.documentElement), card: dimensions(card), body: dimensions(body),
        note: note.textContent, noteDimensions: dimensions(note), controls};
    });
    if (metrics.document.scrollWidth > viewport.width || metrics.body.scrollWidth > metrics.body.width) throw new Error("Horizontal overflow at " + size);
    if (metrics.controls.some(c => c.right > viewport.width || c.x < 0)) throw new Error("Control outside viewport at " + size);
    if (size === "narrow-long-origins") {
      await page.locator(".v2-agent-browser-body").evaluate(el => {el.scrollTop = el.scrollHeight;});
      if (!(await page.getByText("最近状态", {exact:true}).isVisible())) throw new Error("Status footer unreachable");
      await page.screenshot({path: prefix + "-scrolled-viewport.png"});
    }
    checks.push({phase, size, ...metrics});
  }
  return checks;
}

