async (page) => {
  const phase = new URL(page.url()).searchParams.get("phase") || "after";
  const port = phase === "before" ? 5270 : 5271;
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  const checks = [];
  for (const [size, viewport, theme] of [
    ["desktop", {width:1024,height:600}, "light"],
    ["narrow", {width:390,height:800}, "light"],
    ["desktop-dark", {width:1024,height:600}, "dark"],
  ]) {
    await page.setViewportSize(viewport);
    await page.goto(`http://127.0.0.1:${port}/.tmp/pr527-editor-evidence.html?phase=${phase}&theme=${theme}`);
    const input = page.getByRole("textbox", {name:"开始新对话"});
    await page.getByRole("heading", {name:"发布检查",exact:true}).waitFor();
    await page.evaluate(() => document.fonts.ready);
    const image = input.getByRole("img", {name:"编辑器图像"});
    await image.evaluate(async img => {await img.decode(); if (!img.naturalWidth) throw new Error("Image failed to load");});
    if (await input.getByRole("row").count() !== 3) throw new Error("Initial table missing");
    const prefix = `output/playwright/pr527-upgrade/${phase}-${size}`;
    await page.screenshot({path:prefix+"-rich-viewport.png"});
    await page.locator(".v2-composer").screenshot({path:prefix+"-rich-composer.png"});
    const metrics = await page.evaluate(() => {
      const input=document.querySelector(".v2-markdown-input");
      const form=document.querySelector(".v2-composer");
      const icons=Array.from(form.querySelectorAll("svg")).map(svg => ({name:svg.getAttribute("class"),width:svg.getBoundingClientRect().width,height:svg.getBoundingClientRect().height}));
      return {viewport:{width:innerWidth,height:innerHeight}, theme:document.documentElement.dataset.prayuTheme,
        documentScrollWidth:document.documentElement.scrollWidth,inputWidth:input.clientWidth,inputScrollWidth:input.scrollWidth,
        image:{width:input.querySelector("img").naturalWidth,height:input.querySelector("img").naturalHeight},icons};
    });
    if (metrics.documentScrollWidth > viewport.width || metrics.inputScrollWidth > metrics.inputWidth) throw new Error("Horizontal overflow");
    const requiredIcons=await page.locator(".v2-send-button svg,.v2-composer-add-trigger svg").evaluateAll(icons=>icons.map(svg=>({width:svg.getBoundingClientRect().width,height:svg.getBoundingClientRect().height})));
    if(requiredIcons.length!==2 || requiredIcons.some(icon=>icon.width<=0 || icon.height<=0)) throw new Error("Primary control icon invisible");
    const toggleMode = async label => {
      const direct=page.getByRole("button",{name:label,exact:true});
      if(await direct.count()) {await direct.click();return;}
      await page.getByRole("button",{name:"添加附件",exact:true}).click();
      await page.getByRole("menuitem",{name:label,exact:true}).click();
    };
    await toggleMode("切换为纯文本编辑");
    const source=await input.inputValue();
    const initial=await page.evaluate(()=>window.reviewFixture.initial);
    if (source!==initial) throw new Error("Mode switch rewrote source");
    if (size==="desktop") await page.screenshot({path:prefix+"-source-viewport.png"});
    await toggleMode("切换为 Markdown 编辑");
    await input.click();
    await page.keyboard.press("Control+a");
    const pasted="## 粘贴草稿\n\n**保留格式**与 `inline`\n\n| 项 | 值 |\n| --- | --- |\n| 中文 | 1 |\n\n预览：![编辑器图像](/.tmp/editor-image.svg)";
    await page.evaluate(text=>navigator.clipboard.writeText(text),pasted);
    await page.keyboard.press("Control+v");
    await input.getByRole("heading",{name:"粘贴草稿",exact:true}).waitFor();
    if(await input.locator("strong").textContent()!=="保留格式") throw new Error("Markdown paste lost bold");
    if(await input.locator("code").textContent()!=="inline") throw new Error("Markdown paste lost code");
    await input.getByRole("img",{name:"编辑器图像"}).evaluate(img=>img.decode());
    await input.getByRole("cell",{name:"中文",exact:true}).click();
    await page.keyboard.press("End");
    await page.keyboard.type("输入");
    if(await input.getByRole("cell",{name:"中文输入",exact:true}).count()!==1) throw new Error("Table edit failed");
    await input.evaluate(el=>{el.scrollTop=0;});
    await page.screenshot({path:prefix+"-pasted-viewport.png"});
    if(size==="desktop") await page.locator(".v2-composer").screenshot({path:prefix+"-pasted-composer.png"});
    await page.keyboard.press("Control+Enter");
    await page.waitForFunction(()=>window.reviewFixture.submitted.length===1);
    const submitted=await page.evaluate(()=>window.reviewFixture.submitted[0]);
    if(!submitted.includes("**保留格式**") || !submitted.includes("中文输入") || !submitted.includes("![编辑器图像]")) throw new Error("Submitted Markdown lost content");
    await input.click();
    await page.evaluate(()=>navigator.clipboard.writeText("**literal**"));
    await page.keyboard.press("Control+Shift+v");
    if(await input.textContent()!=="**literal**" || await input.locator("strong").count()) throw new Error("Plain paste interpreted Markdown");
    checks.push({phase,size,...metrics,modeRoundTrip:"source preserved",paste:"system clipboard Ctrl+V",tableEdit:"中文输入",submitted,plainPaste:"literal punctuation preserved"});
  }
  return checks;
}

