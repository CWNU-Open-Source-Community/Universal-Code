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
      if(!await page.getByText(locale==="zh-CN"?"网页请求":"Web request",{exact:true}).isVisible())throw new Error("Request title missing");
      if(await page.getByText("agent_browser_sensitive",{exact:true}).count())throw new Error("Raw tool name shown");
      if(await page.locator(".v2-approval-card details").evaluate(el=>el.open))throw new Error("Request details must start collapsed");
      if(await page.locator(".v2-approval-card details pre").first().isVisible())throw new Error("Digest should start hidden");
    } else {
      await page.waitForFunction(()=>!document.querySelector('[role="status"]'));
      if(await page.locator(".v2-approval-card").count()) throw new Error("Before must have no page-request approval");
    }
    const metrics=await page.evaluate(()=>{
      const card=document.querySelector(".v2-approval-card");
      const composer=document.querySelector(".v2-composer").getBoundingClientRect();
      const fields=Array.from(document.querySelectorAll(".v2-approval-facts pre")).map(el=>({value:el.textContent,visible:!el.closest("details")||el.closest("details").open,width:el.clientWidth,scrollWidth:el.scrollWidth,height:el.getBoundingClientRect().height,lineHeight:parseFloat(getComputedStyle(el).lineHeight)}));
      const controls=Array.from(document.querySelectorAll(".v2-approval-card footer button")).map(el=>{const rect=el.getBoundingClientRect();return {label:el.textContent,x:rect.x,right:rect.right,top:rect.top,bottom:rect.bottom};});
      return {theme:document.documentElement.dataset.prayuTheme,documentScrollWidth:document.documentElement.scrollWidth,
        fields,controls,cardBottom:card?.getBoundingClientRect().bottom,composerTop:composer.top,composerBottom:composer.bottom};
    });
    if(metrics.documentScrollWidth>viewport.width || metrics.fields.some(f=>f.visible&&f.scrollWidth>f.width))throw new Error("Horizontal overflow");
    if(metrics.controls.some(c=>c.x<0||c.right>viewport.width||c.bottom>viewport.height))throw new Error("Approval buttons clipped");
    if(metrics.cardBottom && metrics.cardBottom>=metrics.composerTop)throw new Error("Approval overlaps composer");
    const prefix=`output/playwright/pr538-request-approval/${phase}-${size}`;
    await page.screenshot({path:prefix+"-viewport.png"});
    if(phase==="after")await page.locator(".v2-approval-card").screenshot({path:prefix+"-card.png"});
    let expanded;
    if(phase==="after") {
      await page.getByText(locale==="zh-CN"?"请求详情":"Request details",{exact:true}).click();
      expanded=await page.evaluate(()=>{
        const fields=Array.from(document.querySelectorAll(".v2-approval-card details pre")).map(el=>({value:el.textContent,width:el.clientWidth,scrollWidth:el.scrollWidth,height:el.getBoundingClientRect().height}));
        const buttons=Array.from(document.querySelectorAll(".v2-approval-card footer button")).map(el=>{const r=el.getBoundingClientRect();return {label:el.textContent,x:r.x,right:r.right,bottom:r.bottom};});
        return{fields,buttons};
      });
      if(expanded.fields.some(f=>!f.width||f.scrollWidth>f.width)||expanded.buttons.some(b=>b.x<0||b.right>viewport.width||b.bottom>viewport.height))throw new Error("Expanded details overflow or clip controls");
      await page.screenshot({path:prefix+"-expanded-viewport.png"});
      await page.locator(".v2-approval-card").screenshot({path:prefix+"-expanded-card.png"});
    }
    let decisions;
    if(phase==="after"&&size==="desktop") {
      decisions=[];
      for(const [action,label,message] of [["approve_once","批准一次","已批准这次网页请求。"],["deny","拒绝","已拒绝这次网页请求。"]]) {
        await page.goto("http://127.0.0.1:5381/.tmp/pr538-approval-evidence.html?phase=after");
        const button=page.getByRole("button",{name:label,exact:true});
        await button.waitFor();
        await button.click();
        await page.getByRole("status").filter({hasText:message}).waitFor();
        if(await page.getByRole("status").textContent()!==message)throw new Error("Incorrect decision feedback");
        const recorded=await page.evaluate(()=>window.reviewFixture.decisions);
        if(recorded.length!==1||recorded[0]!==action)throw new Error("Wrong fixture decision");
        await page.screenshot({path:prefix+`-${action}-viewport.png`});
        decisions.push({action,message,response_continuation:"absent"});
      }
    }
    results.push({phase,size,viewport,locale,...metrics,expanded,decisions});
  }
  return results;
}
