import { describe, expect, it } from "vitest";
import { narrativeTaskIndex, taskDurationLabel, type NarrativeTask } from "./narrative-task";
const t = (chinese: string) => chinese;
const task: NarrativeTask = { id:"input",inputMessageID:"input",runId:"run",state:"completed",
  startedAt:"2026-11-12T00:00:00Z",finishedAt:"2026-11-12T00:30:29Z" };
describe("task elapsed time",()=>{
  it("uses the full task lifecycle rather than the last model step",()=>{
    expect(taskDurationLabel(task,t)).toBe("用时30分钟29秒");
    expect(taskDurationLabel({...task,finishedAt:"2026-11-12T01:02:03Z"},t)).toBe("用时1小时2分钟3秒");
  });
  it("does not fabricate missing, invalid or reversed times",()=>{
    for(const value of [{...task,startedAt:undefined},{...task,finishedAt:undefined},
      {...task,startedAt:"invalid"},{...task,finishedAt:"2026-11-11T00:00:00Z"}]) {
      expect(taskDurationLabel(value,t)).toBeUndefined();
    }
  });
});

it("uses the latest lifecycle row when a resumed input withdraws a cached terminal footer",()=>{
  const completed={id:"input",input_message_id:"input",state:"completed" as const,final_message_id:"old-final",finished_at:"2026-11-12T00:30:29Z"};
  const running={id:"input",input_message_id:"input",state:"running" as const,started_at:"2026-11-12T00:00:00Z"};
  const tasks=narrativeTaskIndex([{run_id:"run",sequence:20,task:running},{run_id:"run",sequence:10,task:completed}]);
  expect(tasks.get("input")).toMatchObject({state:"running",finalMessageID:undefined,changeSourceEventID:undefined,finishedAt:undefined});
});
