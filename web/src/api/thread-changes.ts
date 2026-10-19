import type { components } from "./schema";

export type ThreadChangeSummary = components["schemas"]["ThreadChangeSummary"] & { undo?: ThreadChangeUndo };
export type ThreadChangeFile = components["schemas"]["ThreadChangeFile"];

export interface ThreadChangeUndo {
  version: "thread_change_undo.v1";
  thread_id: string;
  source_event_id: string;
  run_id: string;
  state: "available" | "unavailable" | "conflict" | "applying" | "completed" | "partial";
  conflicts: { path?: string; reason: string }[];
  restored_paths: string[];
  remaining_paths: string[];
  replayed: boolean;
}
