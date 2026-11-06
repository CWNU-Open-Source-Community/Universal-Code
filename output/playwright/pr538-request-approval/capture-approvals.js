async page=>{
  const results=[];
  for(const [phase,size,viewport,locale] of [
    ["before","desktop",{width:1024,height:720},"zh-CN"],
    ["after","desktop",{width:1024,height:720},"zh-CN"],
    ["before","narrow",{width:390,height:800},"zh-CN"],
    ["after","narrow",{width:390,height:800},"zh-CN"],
    ["after","desktop-english",{width:1024,height:720},"en-US"],
  ]) {
    await page.setViewportSize(viewport);
    await page.goto(`http://127.0.0.1:5381/.tmp/pr538-approval-evidence.html?phase=${phase}&locale=${locale}`);
    await page.locator(".v2-composer").waitFor();
    await page.evaluate(()=>document.fonts.ready);
    if(phase==="after") {
      const approve=page.getByRole("button",{name:locale==="zh-CN"?"批准一次":"Approve once",exact:true});
      await approve.waitFor();
      if(!await approve.isEnabled()) throw new Error("Approval not enabled");
      const labels=await page.locator(".v2-approval-facts dt").allTextContents();
      for(const label of locale==="zh-CN"?["请求体大小（字节）","请求体 SHA-256","有效期至"]:["Request body size (bytes)","Request body SHA-256","Expires at"])
        if(!labels.includes(label))throw new Error("Missing translated field: "+label);
      const values=await page.locator(".v2-approval-facts pre").allTextContents();
      if(values.join("|")!=="POST|http://127.0.0.1:51234/write|19|91d28b1e660fca8e5a54e5f00e2756024be094bc718ce7fd660d1415f1a3922c8|2026-11-06T14:32:00Z")throw new Error("Preview fields differ");
    } else {
      await page.waitForFunction(()=>!document.querySelector('[role="status"]'));
      if(await page.locator(".v2-approval-card").count()) throw new Error("Before must have no page-request approval");
    }
    const metrics=await page.evaluate(()=>{
      const card=document.querySelector(".v2-approval-card");
      const composer=document.querySelector(".v2-composer").getBoundingClientRect();
      const fields=Array.from(document.querySelectorAll(".v2-approval-facts pre")).map(el=>({value:el.textContent,width:el.clientWidth,scrollWidth:el.scrollWidth,height:el.getBoundingClientRect().height,lineHeight:parseFloat(getComputedStyle(el).lineHeight)}));
      const controls=Array.from(document.querySelectorAll(".v2-approval-card footer button")).map(el=>{const rect=el.getBoundingClientRect();return {label:el.textContent,x:rect.x,right:rect.right,top:rect.top,bottom:rect.bottom};});
      return {theme:document.documentElement.dataset.prayuTheme,documentScrollWidth:document.documentElement.scrollWidth,
        fields,controls,cardBottom:card?.getBoundingClientRect().bottom,composerTop:composer.top,composerBottom:composer.bottom};
    });
    if(metrics.documentScrollWidth>viewport.width || metrics.fields.some(f=>f.scrollWidth>f.width))throw new Error("Horizontal overflow");
    if(metrics.controls.some(c=>c.x<0||c.right>viewport.width||c.bottom>viewport.height))throw new Error("Approval buttons clipped");
    if(metrics.cardBottom && metrics.cardBottom>=metrics.composerTop)throw new Error("Approval overlaps composer");
    const prefix=`output/playwright/pr538-request-approval/${phase}-${size}`;
    await page.screenshot({path:prefix+"-viewport.png"});
    if(phase==="after")await page.locator(".v2-approval-card").screenshot({path:prefix+"-card.png"});
    results.push({phase,size,viewport,locale,...metrics});
  }
  return results;
}
