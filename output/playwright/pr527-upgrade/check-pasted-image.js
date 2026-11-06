async page => {
  const checks=[];
  await page.context().grantPermissions(["clipboard-read","clipboard-write"]);
  for(const [phase,port] of [["before",5270],["after",5271]]) {
    for(const standalone of [true,false]) {
      await page.goto(`http://127.0.0.1:${port}/.tmp/pr527-editor-evidence.html?phase=${phase}`);
      const input=page.getByRole("textbox",{name:"开始新对话"});
      await input.waitFor();
      await input.click(); await page.keyboard.press("Control+a");
      const text="## 粘贴草稿\n\n**保留格式**与 `inline`\n\n| 项 | 值 |\n| --- | --- |\n| 中文 | 1 |\n\n"+(standalone?"":"预览：")+"![编辑器图像](/.tmp/editor-image.svg)";
      await page.evaluate(text=>navigator.clipboard.writeText(text),text);
      await page.keyboard.press("Control+v");
      await input.getByRole("heading",{name:"粘贴草稿",exact:true}).waitFor();
      checks.push({phase,standalone,imageCount:await input.getByRole("img").count(),draft:await page.evaluate(()=>window.reviewFixture.current)});
    }
  }
  return checks;
}
