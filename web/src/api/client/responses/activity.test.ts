import { APIRequestError } from "../protocol";
import { parseThreadActivityDetail } from "./activity";

function imageActivity() {
  return {
    version: "thread_activity_detail.v2", activity_ref: "call-image", run_id: "run-image",
    tools: [{
      agent_id: "agent-image", agent_label: "Agent", agent_role: "root", label: "查看图片",
      name: "workspace_view_image", status: "completed", duration_milliseconds: 10,
      started_at: "2026-11-12T00:00:00Z", completed_at: "2026-11-12T00:00:00.010Z",
      detail: { kind: "file_read", file_read: {
        operation: "查看图片", path: "output/preview.png", query: "", pattern: "",
        start_line: 0, end_line: 0, limit: 0, result_count: 1, truncated: false,
        summary: "图片查看已完成", boundary: {
          authorization: "policy_checked", error_code: "", failure_reason: "",
          truncated: false, untrusted: true,
        },
      } },
    }],
  };
}

it("accepts workspace image observations through the existing read-only activity contract", () => {
  const value = imageActivity();
  expect(parseThreadActivityDetail(value, "thread-image", "call-image")).toEqual(value);
});

it("keeps image activity responses strict about tool names, branches and unexpected image payloads", () => {
  const unknown = imageActivity();
  unknown.tools[0].name = "workspace_view_image_unregistered";
  expect(() => parseThreadActivityDetail(unknown, "thread-image", "call-image")).toThrow(APIRequestError);

  const mismatch = imageActivity();
  mismatch.tools[0].name = "code_diagnostics";
  expect(() => parseThreadActivityDetail(mismatch, "thread-image", "call-image")).toThrow(APIRequestError);

  const rawImage = imageActivity();
  Object.assign(rawImage.tools[0].detail.file_read, { image: { data: "private-pixels" } });
  expect(() => parseThreadActivityDetail(rawImage, "thread-image", "call-image")).toThrow(APIRequestError);
});
