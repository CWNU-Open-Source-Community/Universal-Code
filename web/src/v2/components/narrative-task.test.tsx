import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { APIClient } from "../../api/client";
import type { ThreadChangeSummary } from "../../api/thread-changes";
import type { NarrativeEntry } from "../projection/narrative";
import type { NarrativeTask } from "../projection/narrative-task";
import { V2Narrative } from "./narrative";

afterEach(cleanup);
const task: NarrativeTask = { id:"input-1",inputMessageID:"input-1",runId:"run-1",state:"completed",
  startedAt:"2026-11-12T00:00:00Z",finishedAt:"2026-11-12T00:30:29Z",finalMessageID:"final",changeSourceEventID:"final" };
function entries(value: NarrativeTask = task): NarrativeEntry[] {
  const rows: NarrativeEntry[] = [
    {id:"user",kind:"user",text:"Improve this page",createdAt:"",provisional:false},
    {id:"progress",kind:"assistant",text:"Checking the implementation.",createdAt:"",provisional:false,model:"internal-model",runId:value.runId},
    {id:"tools",kind:"activity",activity:"read",title:"Read",detail:"src/app.tsx",status:"completed",createdAt:"",runId:value.runId,count:1,provisional:false,
      items:[{title:"Read source",detail:"src/app.tsx",status:"completed",provisional:false,detailAvailable:false}]},
    {id:"steer",kind:"user",text:"Keep my existing changes",createdAt:"",provisional:false},
  ];
  if(value.finalMessageID) rows.push({id:value.finalMessageID,kind:"assistant",text:"The page is updated and verified.",createdAt:"",provisional:false,model:"final-model",branchSourceID:value.finalMessageID,runId:value.runId});
  return rows.map((entry)=>({...entry,task:value}));
}
function setup(rows = entries(), locatedMessageID?: string) {
  const summary: ThreadChangeSummary = {version:"thread_change_summary.v1",thread_id:"thread-1",run_id:"run-1",source_event_id:rows[0]?.task?.changeSourceEventID ?? "final",
    change_scope:"recorded_file_edits",file_count:4,additions:4,deletions:4,counts_available:true,partial:false,reasons:[],
    files:["src/app.tsx","src/theme.css","README.md","tests/app.test.ts"].map((path,index)=>({workspace_id:"workspace-1",path,operation:"replace" as const,additions:1,deletions:1,
      counts_available:true,diff:`--- a/${path}\n+++ b/${path}\n@@ -1 +1 @@\n-old\n+new\n`,diff_truncated:false,edit_ids:[`edit-${index}`]}))};
  const threadChanges=vi.fn(async()=>summary);
  const client={threadChanges,hasWorkspaceCheckpointControl:true,undoThreadChanges:vi.fn()} as unknown as APIClient;
  const queries=new QueryClient({defaultOptions:{queries:{retry:false}}});
  const tree=(next: NarrativeEntry[], target=locatedMessageID)=><QueryClientProvider client={queries}>
    <V2Narrative client={client} entries={next} threadID="thread-1" locatedMessageID={target}/></QueryClientProvider>;
  const view=render(tree(rows));
  return {...view,threadChanges,rerenderRows:(next: NarrativeEntry[], target?: string)=>view.rerender(tree(next,target))};
}
describe("user task narrative",()=>{
  it("shows collapsed work, final answer, file changes and one footer in that order",async()=>{
    const view=setup();
    const toggle=screen.getByRole("button",{name:"Worked for 30m 29s"});
    expect(toggle).toHaveAttribute("aria-expanded","false");
    expect(screen.getByText("Checking the implementation.")).not.toBeVisible();
    expect(screen.getByText("Keep my existing changes")).toBeVisible();
    const final=screen.getByText("The page is updated and verified.");
    expect(final).toBeVisible();
    const card=await screen.findByRole("region",{name:"Changes in this turn"});
    expect(card.closest(".v2-task-entries")).toBeNull();
    expect(within(card).getAllByRole("listitem")).toHaveLength(3);
    expect(within(card).getByRole("button",{name:"Show 1 more files"})).toBeVisible();
    expect(within(card).getByRole("button",{name:"Undo this turn’s changes"})).toBeVisible();
    expect(within(card).getByRole("button",{name:"View changes"})).toBeVisible();
    const footer=view.container.querySelector(".v2-task-turn > .v2-assistant-actions")!;
    expect(final.compareDocumentPosition(card)&Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(card.compareDocumentPosition(footer)&Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.getAllByRole("button",{name:"Copy response"})).toHaveLength(1);
    expect(view.container.querySelector(".v2-assistant-model")).toBeNull();
    expect(footer.querySelector("time")).toHaveAttribute("datetime","2026-11-12T00:30:29.000Z");
    await userEvent.setup().click(toggle);
    expect(screen.getByText("Checking the implementation.")).toBeVisible();
    expect(screen.getAllByRole("button",{name:"Copy response"})).toHaveLength(1);
    expect(view.threadChanges).toHaveBeenCalledOnce();
  });
  it("keeps durable intermediate model steps compact and without footer until the whole task ends",()=>{
    const value={...task,state:"running" as const,finishedAt:undefined,finalMessageID:undefined,changeSourceEventID:undefined};
    const view=setup(entries(value));
    expect(screen.getByText("Checking the implementation.")).toBeVisible();
    expect(screen.queryByRole("button",{name:"Copy response"})).not.toBeInTheDocument();
    expect(view.container.querySelector(".v2-task-turn > .v2-assistant-actions")).toBeNull();
    expect(view.container.querySelector(".is-task-progress")).not.toBeNull();
    expect(view.threadChanges).not.toHaveBeenCalled();
  });
  it("keeps interrupted work expanded and its changes available without inventing a final response",async()=>{
    const interrupted={...task,state:"interrupted" as const,finalMessageID:undefined,changeSourceEventID:"ending-event"};
    const view=setup(entries(interrupted));
    const toggle=screen.getByRole("button",{name:"Worked for 30m 29s"});
    expect(toggle).toHaveAttribute("aria-expanded","true");
    expect(screen.getByText("Checking the implementation.")).toBeVisible();
    expect(screen.queryByText("The page is updated and verified.")).not.toBeInTheDocument();
    const card=await screen.findByRole("region",{name:"Changes in this turn"});
    expect(card).toBeVisible();
    expect(view.threadChanges).toHaveBeenCalledWith("thread-1","run-1","ending-event",expect.any(AbortSignal));
    expect(screen.getAllByRole("button",{name:"Copy response"})).toHaveLength(1);
    await userEvent.setup().click(toggle);
    expect(card).toBeVisible();
    expect(screen.getByText("Keep my existing changes")).toBeVisible();
  });
  it("keeps failure and recovery notices visible when completed work is collapsed",()=>{
    const rows=entries();
    rows.splice(2,0,{id:"failure",kind:"notice",tone:"warning",text:"A saved operation needs recovery",createdAt:"",task});
    setup(rows);
    expect(screen.getByText("Checking the implementation.")).not.toBeVisible();
    expect(screen.getByText("A saved operation needs recovery")).toBeVisible();
  });
  it("collapses an earlier failed tool call once the same task has a reliable successful final answer",async()=>{
    const rows=entries();
    const tool=rows.find((entry)=>entry.kind==="activity")!;
    if(tool.kind!=="activity") throw new Error("missing activity fixture");
    tool.status="failed"; tool.items[0].status="failed";
    setup(rows);
    expect(screen.getByText("Read source")).not.toBeVisible();
    expect(screen.getByText("The page is updated and verified.")).toBeVisible();
    await userEvent.setup().click(screen.getByRole("button",{name:"Worked for 30m 29s"}));
    expect(screen.getByText("Read source")).toBeVisible();
  });
  it("reveals a located saved message once and then respects manual collapse",async()=>{
    const view=setup();
    view.rerenderRows(entries(),"progress");
    expect(screen.getByText("Checking the implementation.")).toBeVisible();
    expect(screen.getByRole("button",{name:"Worked for 30m 29s"})).toHaveAttribute("aria-expanded","true");
    await userEvent.setup().click(screen.getByRole("button",{name:"Worked for 30m 29s"}));
    expect(screen.getByText("Checking the implementation.")).not.toBeVisible();
    view.rerenderRows(entries(),"progress");
    expect(screen.getByRole("button",{name:"Worked for 30m 29s"})).toHaveAttribute("aria-expanded","false");
    const running={...task,state:"running" as const,finishedAt:undefined,finalMessageID:undefined,changeSourceEventID:undefined};
    view.rerenderRows(entries(running),"progress");
    const nextEnding={...task,finishedAt:"2026-11-12T00:40:29Z",finalMessageID:"new-final"};
    view.rerenderRows(entries(nextEnding),"progress");
    expect(screen.getByRole("button",{name:"Worked for 40m 29s"})).toHaveAttribute("aria-expanded","false");
  });
  it("keeps different user tasks in the same Run separate",()=>{
    const next={...task,id:"input-2",inputMessageID:"input-2",state:"running" as const,finishedAt:undefined,finalMessageID:undefined,changeSourceEventID:undefined};
    const first=entries();
    const second=entries(next).map((entry)=>({...entry,id:`next:${entry.id}`}));
    const view=setup([...first,...second]);
    expect(view.container.querySelectorAll(".v2-task-turn")).toHaveLength(2);
    expect(screen.getAllByRole("button",{name:"Copy response"})).toHaveLength(1);
    expect(screen.getAllByText("Checking the implementation.")[1]).toBeVisible();
  });
  it("withdraws the footer on resume and defaults a later interruption back to expanded",async()=>{
    const view=setup();
    const user=userEvent.setup();
    await user.click(screen.getByRole("button",{name:"Worked for 30m 29s"}));
    await user.click(screen.getByRole("button",{name:"Worked for 30m 29s"}));
    const running={...task,state:"running" as const,finishedAt:undefined,finalMessageID:undefined,changeSourceEventID:undefined};
    view.rerenderRows(entries(running));
    expect(screen.getByText("Checking the implementation.")).toBeVisible();
    expect(screen.queryByRole("button",{name:"Copy response"})).not.toBeInTheDocument();
    expect(screen.queryByRole("region",{name:"Changes in this turn"})).not.toBeInTheDocument();
    const interrupted={...task,state:"interrupted" as const,finalMessageID:undefined,finishedAt:"2026-11-12T00:40:29Z",changeSourceEventID:"new-ending"};
    view.rerenderRows(entries(interrupted));
    expect(screen.getByText("Checking the implementation.")).toBeVisible();
    expect(screen.getByRole("button",{name:"Worked for 40m 29s"})).toHaveAttribute("aria-expanded","true");
  });
  it("leaves unknown legacy content expanded without declaring a finished task",async()=>{
    const view=setup([{id:"legacy",kind:"assistant",text:"Legacy text remains selectable.",runId:"run-1",changeSourceID:"legacy",createdAt:"2026-11-12T00:00:00Z",provisional:false}]);
    expect(screen.getByText("Legacy text remains selectable.")).toBeVisible();
    expect(screen.queryByRole("button",{name:"Copy response"})).not.toBeInTheDocument();
    expect(view.container.querySelector(".v2-task-process-heading")).toBeNull();
    await waitFor(()=>expect(view.threadChanges).not.toHaveBeenCalled());
  });
});
