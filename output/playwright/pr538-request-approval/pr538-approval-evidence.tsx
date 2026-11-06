import {createRoot} from "react-dom/client";
import {QueryClient,QueryClientProvider} from "@tanstack/react-query";
import {V2ApprovalCards} from "../src/v2/components/approval-cards";
import {V2Composer} from "../src/v2/components/composer";
import {LocaleProvider} from "../src/lib/locale";
import {initializePrayuAppearance} from "../src/lib/appearance";
import type {APIClient} from "../src/api/client";
import "../src/styles.css";
import "../src/v2/styles.css";

const params=new URLSearchParams(location.search);
const phase=params.get("phase") || "after";
localStorage.clear();
localStorage.setItem("prayu.locale.v1",params.get("locale") || "zh-CN");
localStorage.setItem("prayu.theme","light");
initializePrayuAppearance();
const item={action_class:"browser_external_write",allowed_actions:["approve_once","deny"],capability_grant:false,
  created_at:"2026-11-06T12:30:00Z",id:"approval-browser-network-review",mode:"per_call",process_execution_enabled:false,
  proposal_id:"browser-network-review",run_id:"run-browser-review",session_id:"session-browser-review",status:"pending",
  tool_name:"agent_browser_sensitive",updated_at:"2026-11-06T12:30:00Z",version:1,workspace_id:""};
const preview={protocol_version:"approval_queue.v1",run_id:item.run_id,approval_id:item.id,proposal_id:item.proposal_id,
  tool_name:item.tool_name,workspace_id:item.workspace_id,effect:"browser_sensitive_action",working_directory:"",
  fields:[{name:"operation",value:"POST"},{name:"url",value:"http://127.0.0.1:51234/write"},
    {name:"body_bytes",value:"19"},{name:"body_sha256",value:"91d28b1e660fca8e5a54e5f00e2756024be094bc718ce7fd660d1415f1a3922c8"},
    {name:"expires_at",value:"2026-11-06T14:32:00Z"}],source_current:true,redacted:false,truncated:false};
const client={baseURL:"/local-approval-review",hasApprovalControl:true,
  approvalQueue:async()=>({protocol_version:"approval_queue.v1",run_id:item.run_id,items:phase==="before"?[]:[item],truncated:false,
    process_execution_enabled:false,session_grant_created:false,capability_grant:false}),
  approvalPreview:async()=>preview,
  decideApproval:async()=>{throw new Error("This review fixture does not execute approval decisions");},
} as unknown as APIClient;
const queryClient=new QueryClient({defaultOptions:{queries:{retry:false,refetchOnWindowFocus:false},mutations:{retry:false}}});
document.body.style.cssText="margin:0;min-height:100dvh;box-sizing:border-box;padding:24px 16px;background:var(--prayu-panel);";
createRoot(document.getElementById("root")!).render(<LocaleProvider><QueryClientProvider client={queryClient}>
  <main style={{maxWidth:"738px",minHeight:"calc(100dvh - 48px)",margin:"0 auto",display:"flex",flexDirection:"column",gap:"24px"}}>
    <div data-review-approval-region><V2ApprovalCards client={client} runID={item.run_id} threadID="thread-browser-review"/></div>
    <div style={{flex:1}}/>
    <div><V2Composer client={client} threadID="" workspaceID="" workspaces={[]} allowProjectless
      placeholder="继续对话" onWorkspaceChange={()=>{}} onSubmit={async()=>{}}/></div>
  </main>
</QueryClientProvider></LocaleProvider>);
