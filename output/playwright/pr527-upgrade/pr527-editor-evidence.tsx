import {useState} from "react";
import {createRoot} from "react-dom/client";
import {QueryClient, QueryClientProvider} from "@tanstack/react-query";
import {V2Composer} from "../src/v2/components/composer";
import {LocaleProvider} from "../src/lib/locale";
import {initializePrayuAppearance} from "../src/lib/appearance";
import type {APIClient} from "../src/api/client";
import "../src/styles.css";
import "../src/v2/styles.css";

localStorage.clear();
localStorage.setItem("prayu.locale.v1", "zh-CN");
localStorage.setItem("prayu.theme", new URLSearchParams(location.search).get("theme") || "light");
initializePrayuAppearance();
const initial = "## 发布检查\n\n| 组件 | 内容 |\n| --- | --- |\n| 图片 | 本地预览 |\n| 表格 | 两列 |\n\n![编辑器图像](/.tmp/editor-image.svg)";
const client = {baseURL:"/local-composer-review",hasThreadControl:true} as APIClient;
const queries = new QueryClient({defaultOptions:{queries:{retry:false,refetchOnWindowFocus:false}}});
Object.assign(window, {reviewFixture:{initial,submitted:[],current:initial}});
document.body.style.cssText = "margin:0; min-height:100dvh; box-sizing:border-box; padding:32px 16px; background:var(--prayu-panel);";
function Fixture() {
  const [draft,setDraft] = useState(initial);
  return <div style={{maxWidth:"738px",margin:"0 auto"}}><V2Composer client={client}
    threadID="" workspaceID="editor-review" workspaces={[{id:"editor-review",name:"发布检查",created_at:""}]}
    onWorkspaceChange={() => {}} draft={draft} onDraftChange={value => {
      setDraft(value); (window as any).reviewFixture.current = value;
    }} onSubmit={async value => {(window as any).reviewFixture.submitted.push(value);}}
  /></div>;
}
createRoot(document.getElementById("root")!).render(<LocaleProvider><QueryClientProvider client={queries}><Fixture/></QueryClientProvider></LocaleProvider>);
