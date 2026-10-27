import type { PublicModelStreamSnapshot, ThreadTranscriptItemView } from "../../api/types";
import { prepareThreadNarrative, projectLiveThreadNarrative, projectThreadNarrative, type ThreadTranscriptActivityItem } from "./narrative";
import { peerMessageFixture } from "../../test/thread-peer-message";

function item(overrides: Partial<ThreadTranscriptActivityItem>): ThreadTranscriptActivityItem {
  return {
    version: "thread_transcript.v1",
    id: "item-1",
    canonical_id: "canonical-1",
    run_id: "run-1",
    run_ordinal: 1,
    sequence: 1,
    activity_type: "message",
    stage: "result",
    kind: "model_update",
    source: "model",
    title: "更新",
    detail: "我会先检查相关实现。",
    verifiable: true,
    instruction_authorized: false,
    provisional: false,
    durable: true,
    created_at: "2026-08-29T00:00:00Z",
    ...overrides,
  };
}

function snapshot(overrides: Partial<PublicModelStreamSnapshot> = {}): PublicModelStreamSnapshot {
  return {
    version: "public_model_stream.v1",
    call: {
      attempt_id: "attempt-1", cancel_requested: false, max_attempts: 3,
      model: "model-1", model_attempt: 1, protocol_repair: 0, provider: "provider-1",
      run_id: "run-1", session_id: "session-1", started_at: "2026-08-29T00:00:01Z",
      stream_bytes: 12, stream_chunks: 2, tool_round: 0, transport_attempt: 1,
    },
    content_kind: "tool_commentary", event_sequence: 2, items: [],
    message_complete: false, provisional: true, revision: 2,
    text: "我先检查相关实现。", updated_at: "2026-08-29T00:00:02Z",
    ...overrides,
  };
}

it("branches only from saved, delivered user inputs", () => {
  for (const status of ["committed", "pending", "cancelled"]) {
    const entries = projectThreadNarrative([item({ id: "user-event", source: "operator", kind: "operator_input", status,
      detail: "User request", instruction_authorized: true })]);
    expect(entries[0]).toMatchObject({ kind: "user", branchSourceID: status === "committed" ? "user-event" : undefined });
  }
});

it("keeps imported replies branchable without requesting edits from their new run", () => {
  const entries = projectThreadNarrative([item({ id: "copied-reply", source_ref: "branch-assistant:original-reply" })]);
  expect(entries[0]).toMatchObject({ kind: "assistant", branchSourceID: "copied-reply", changeSourceID: undefined });
});

describe("projectThreadNarrative", () => {
  it("preserves distinct tool names when no specialized title or command summary exists", () => {
    const projected = projectThreadNarrative(["thread_list", "thread_send_message"].map((name, index) => item({
      id: name, canonical_id: name, sequence: index + 1, tool_name: name,
      source: "harness", kind: "tool_call", activity_type: "execute",
      title: "工具执行已完成", detail: name, status: "completed", detail_available: false,
    })));
    expect(projected).toEqual([expect.objectContaining({ kind: "activity", count: 2, items: [
      expect.objectContaining({ title: "thread_list", detailAvailable: false }),
      expect.objectContaining({ title: "thread_send_message", detailAvailable: false }),
    ] })]);
  });

  it("keeps cross-chat messages separate from user input and assistant replies", () => {
    const message = peerMessageFixture();
    const entries = projectThreadNarrative([item({ source: "peer", kind: "peer_message", peer_message: message })]);
    expect(entries).toEqual([{ id: "item-1", kind: "peer", message, text: message.content,
      createdAt: "2026-08-29T00:00:00Z", provisional: false }]);
  });
  it("does not treat delivery receipts as branchable model replies", () => {
    const projected = projectThreadNarrative([item({ id: "delivery-receipt", source: "harness", kind: "plan",
      activity_type: "delivery", title: "切片交付检查点已记录", detail: "", status: "completed" })]);
    expect(projected).toEqual([expect.objectContaining({ kind: "assistant", branchSourceID: undefined })]);
  });

  it("projects live output without rereading or rebuilding a thousand durable rows", () => {
    let historicalReads = 0;
    const transcript = Array.from({ length: 1_000 }, (_, index) => item({
      id: `history-${index}`, canonical_id: `history-${index}`, sequence: index + 1,
      source: index % 2 ? "model" : "operator", kind: index % 2 ? "model_update" : "operator_input",
      get detail() { historicalReads++; return `Historical message ${index}`; },
    }));
    // Instrument the actual input, not just the fixture's object spread.
    transcript.forEach((entry, index) => Object.defineProperty(entry, "detail", {
      get() { historicalReads++; return `Historical message ${index}`; },
    }));
    const prepared = prepareThreadNarrative(transcript);
    historicalReads = 0;
    for (let revision = 1; revision <= 20; revision++) {
      const output = projectLiveThreadNarrative(prepared, { runId: "run-1", status: "live",
        snapshot: snapshot({ text: `Streaming ${revision}`, revision }) });
      expect(output).toHaveLength(1_001);
      expect(output.slice(0, -1).every((entry, index) => entry === prepared.entries[index])).toBe(true);
      expect(output.at(-1)).toMatchObject({ text: `Streaming ${revision}`, provisional: true });
    }
    expect(historicalReads).toBe(0);
  });

  it("extends only the current activity group and retains the durable source references", () => {
    const transcript = [item({ id: "user", source: "operator", kind: "operator_input" }),
      item({ id: "read", canonical_id: "read", activity_type: "read", kind: "tool_call",
        source: "harness", tool_name: "workspace_read", activity_detail_ref: "exact-source",
        detail_available: true, sequence: 2 })];
    const prepared = prepareThreadNarrative(transcript);
    const before = JSON.stringify(prepared.entries);
    const live = { runId: "run-1", status: "live" as const, snapshot: snapshot({ text: "", items: [{
      id: "next-read", response_id: "response", type: "tool_call", status: "receiving_arguments",
      tool_name: "workspace_read", durable: false, provisional: true,
    }] }) };
    const output = projectLiveThreadNarrative(prepared, live);
    expect(output).toEqual(projectThreadNarrative(transcript, live));
    expect(output[0]).toBe(prepared.entries[0]);
    expect(output[1]).toMatchObject({ count: 2, items: [
      expect.objectContaining({ detailRef: "exact-source", detailAvailable: true }),
      expect.objectContaining({ provisional: true }),
    ] });
    expect(JSON.stringify(prepared.entries)).toBe(before);
    if (output[1]?.kind === "activity" && prepared.entries[1]?.kind === "activity") {
      expect(output[1].items[0]).toBe(prepared.entries[1].items[0]);
    }
  });

  it("keeps replay ordering and removes live duplicates after durable persistence", () => {
    const transcript = [item({ id: "user", source: "operator", kind: "operator_input" }),
      item({ id: "failed", source: "harness", kind: "harness_status", activity_type: "checkpoint",
        stage: "blocked", status: "failed", source_ref: "failure-origin", title: "执行失败",
        created_at: "2026-08-29T00:00:03Z", sequence: 3 })];
    const live = { runId: "run-1", status: "finalizing" as const, snapshot: snapshot() };
    const replay = projectLiveThreadNarrative(prepareThreadNarrative(transcript), live);
    expect(replay).toEqual(projectThreadNarrative(transcript, live));
    expect(replay.at(-1)).toMatchObject({ failureOrigin: { runId: "run-1", sourceRef: "failure-origin", eventSequence: 3 } });
    const durable = [...transcript, item({ id: "committed", sequence: 2, attempt_id: "attempt-1",
      model_attempt: 1, tool_round: 1, detail: live.snapshot.text, created_at: live.snapshot.updated_at })];
    const prepared = prepareThreadNarrative(durable);
    expect(projectLiveThreadNarrative(prepared, live)).toBe(prepared.entries);
    expect(projectLiveThreadNarrative(prepared, { ...live, runId: "different-run" })).toBe(prepared.entries);
  });

  it("labels historical recall as search and read without implying a tool rerun", () => {
    const projected = projectThreadNarrative([
      item({ id: "search-history", canonical_id: "search-history", kind: "tool_call", source: "harness",
        activity_type: "search", tool_name: "history_search", status: "completed", title: "工具执行已完成" }),
      item({ id: "read-history", canonical_id: "read-history", kind: "tool_call", source: "harness", sequence: 2,
        activity_type: "read", tool_name: "history_read", status: "completed", title: "工具执行已完成" }),
    ]);
    const encoded = JSON.stringify(projected);
    expect(encoded).toContain("搜索对话历史");
    expect(encoded).toContain("读取历史原文");
    expect(encoded).not.toContain('"activity":"execute"');
  });

  it("groups the observed Plan tool lifecycle without turning audit text into an assistant answer", () => {
    // Exact identity pattern from the isolated first-Plan transcript: the
    // business result has its own event ID, unlike the four stream-bound rows.
    const plan = { kind: "tool_call" as const, source: "harness" as const,
      activity_type: "delivery" as const, tool_name: "plan_delivery_propose",
      canonical_id: "item_3b4dcc0d070a952d5350d607",
      durable_call_id: "toolu_82e499fbfbb029655496ec36" };
    const projected = projectThreadNarrative([
      item({ ...plan, id: "arguments", sequence: 27, stage: "arguments_ready", status: "pending", title: "工具参数已就绪", detail: undefined }),
      item({ ...plan, id: "started", sequence: 28, stage: "running", status: "running", title: "工具执行已开始", detail: "生成交付计划" }),
      item({ ...plan, id: "business-result", canonical_id: "business-result", durable_call_id: undefined,
        sequence: 32, stage: "result", status: "completed", title: "工具操作完成", detail: "生成交付计划" }),
      item({ ...plan, id: "completed", sequence: 33, stage: "result", status: "completed", title: "工具执行已完成", detail: "生成交付计划" }),
      item({ ...plan, id: "recorded", sequence: 34, stage: "result", status: "completed", title: "工具结果已记录", detail: "生成交付计划" }),
      item({ id: "answer", canonical_id: "answer", sequence: 40, detail: "已规划，等待你确认后执行。" }),
    ]);
    expect(projected).toEqual([
      expect.objectContaining({ kind: "activity", activity: "plan", count: 2, status: "completed",
        items: [expect.objectContaining({ title: "生成交付计划", status: "completed" }),
          expect.objectContaining({ title: "规划结果记录", status: "completed" })] }),
      expect.objectContaining({ kind: "assistant", text: "已规划，等待你确认后执行。" }),
    ]);
  });

  it("keeps Plan failures, real model text and separate Run identities", () => {
    const plan = { kind: "tool_call" as const, source: "harness" as const,
      activity_type: "delivery" as const, tool_name: "plan_delivery_propose", canonical_id: "same-call",
      durable_call_id: "durable-plan-call" };
    const projected = projectThreadNarrative([
      item({ ...plan, id: "started", sequence: 1, stage: "running", status: "running" }),
      item({ ...plan, id: "failed", sequence: 2, stage: "blocked", status: "failed", detail: "计划参数不符合当前接口" }),
      item({ id: "answer", canonical_id: "answer", sequence: 3, detail: "工具参数已就绪；生成交付计划是下一步。" }),
      item({ ...plan, id: "next-run", sequence: 4, run_id: "run-2", run_ordinal: 2, stage: "result", status: "completed" }),
    ]);
    expect(projected).toEqual([
      expect.objectContaining({ kind: "activity", activity: "plan", runId: "run-1", count: 1, status: "failed",
        items: [expect.objectContaining({ detail: "计划参数不符合当前接口", status: "failed" })] }),
      expect.objectContaining({ kind: "assistant", text: "工具参数已就绪；生成交付计划是下一步。" }),
      expect.objectContaining({ kind: "activity", activity: "plan", runId: "run-2", count: 1 }),
    ]);
    const live = projectThreadNarrative([], { runId: "run-1", status: "live", snapshot: snapshot({ text: "",
      items: [{ id: "live-plan", response_id: "response-1", type: "tool_call", status: "ready_for_validation",
        tool_name: "plan_delivery_propose", durable: false, provisional: true }] }) });
    expect(live).toEqual([expect.objectContaining({ kind: "activity", activity: "plan", provisional: true, title: "生成交付计划" })]);
  });

  it("projects a pure image without inventing user text from the queue status title", () => {
    const images = [{ id: "image-pure", workspace_id: "workspace-pure", sha256: "a".repeat(64),
      mime_type: "image/png" as const, byte_size: 120, width: 80, height: 80, name: "截图.png" }];
    const projected = projectThreadNarrative([item({ source: "operator", kind: "operator_input",
      title: "用户消息", detail: undefined, images })]);
    expect(projected).toEqual([expect.objectContaining({ kind: "user", text: "", images })]);
  });

  it("retains uploaded file metadata for pure and mixed messages without inventing queue text", () => {
    const attachment = { id: "file-original", workspace_id: "workspace-files", sha256: "b".repeat(64),
      mime_type: "application/pdf", byte_size: 300, name: "说明.pdf", readability: "stored_only" as const,
      text_bytes: 0, redacted: false, reason: "未解析此格式" };
    const image = { id: "image-original", workspace_id: "workspace-files", sha256: "c".repeat(64),
      mime_type: "image/png" as const, byte_size: 120, width: 80, height: 80, name: "截图.png" };
    const projected = projectThreadNarrative([
      item({ id: "pure-file", source: "operator", kind: "operator_input", title: "用户消息已排队",
        detail: "", attachments: [attachment] }),
      item({ id: "mixed", sequence: 2, source: "operator", kind: "operator_input", title: "用户消息",
        detail: "按附件要求检查", attachments: [attachment], images: [image] }),
    ]);
    expect(projected).toEqual([
      expect.objectContaining({ id: "pure-file", kind: "user", text: "", attachments: [attachment] }),
      expect.objectContaining({ id: "mixed", kind: "user", text: "按附件要求检查", attachments: [attachment], images: [image] }),
    ]);
    expect(projected[0]).not.toHaveProperty("instruction_authorized", true);
  });
  it("hides empty artifact-storage delivery placeholders while preserving tool facts and actual messages", () => {
    // Exact public shape captured from workspace_change's tool_output artifact.
    // The artifact.created audit record is not a project delivery assertion.
    const placeholder = item({ id: "evt-20260910072057-8803f1406cc2",
      canonical_id: "evt-20260910072057-8803f1406cc2", run_id: "run-20260910064350-a4f973a7ede3",
      run_ordinal: 2, sequence: 357, activity_type: "delivery", stage: "result", kind: "plan",
      source: "harness", title: "交付物已记录", status: "completed", detail: undefined,
      verifiable: true, instruction_authorized: false, provisional: false, durable: true,
      created_at: "2026-09-10T07:20:57.8759082Z" });
    const transcript = [
      item({ id: "list", canonical_id: "list", sequence: 1, source: "harness", kind: "tool_call", activity_type: "search",
        tool_name: "workspace_list", detail: "当前项目为空", status: "completed" }),
      { ...placeholder, id: "stored-list-output", sequence: 2 },
      item({ id: "failed", canonical_id: "failed", sequence: 3, source: "harness", kind: "tool_call", activity_type: "read",
        tool_name: "workspace_read", detail: "读取失败", status: "failed" }),
      { ...placeholder, id: "stored-failed-output", sequence: 4 },
      item({ id: "proposal", canonical_id: "proposal", sequence: 5, source: "harness", kind: "file_change", activity_type: "edit",
        status: "proposed", detail: "package.json，尚未应用" }),
      placeholder,
    ];
    const before = JSON.stringify(transcript);
    const projected = projectThreadNarrative(transcript);
    expect(projected).toHaveLength(3);
    expect(projected.every((entry) => entry.kind === "activity")).toBe(true);
    expect(projected).toEqual(expect.arrayContaining([
      expect.objectContaining({ detail: "读取失败", status: "failed" }),
      expect.objectContaining({ title: "已提出修改，等待审阅", status: "proposed" }),
    ]));
    expect(JSON.stringify(projected)).not.toContain("交付物已记录");
    expect(JSON.stringify(transcript)).toBe(before);

    const substantive = projectThreadNarrative([
      { ...placeholder, id: "model-answer", source: "model", kind: "model_update", sequence: 1 } as ThreadTranscriptActivityItem,
      { ...placeholder, id: "actual-detail", sequence: 2, detail: "具体交付记录可供审阅" },
      { ...placeholder, id: "checkpoint", sequence: 3, title: "切片交付检查点已记录" },
    ]);
    expect(JSON.stringify(substantive)).toContain("交付物已记录");
    expect(JSON.stringify(substantive)).toContain("具体交付记录可供审阅");
    expect(JSON.stringify(substantive)).toContain("切片交付检查点已记录");
  });

  it("preserves cancellation on a durable user message instead of displaying it as accepted", () => {
    const projected = projectThreadNarrative([item({ source: "operator", kind: "operator_input",
      status: "cancelled", detail: "Cancelled follow-up", instruction_authorized: false })]);
    expect(projected).toEqual([expect.objectContaining({ kind: "user", text: "Cancelled follow-up", status: "cancelled" })]);
  });

  it("hides Run boundaries and groups tool activity", () => {
    const projected = projectThreadNarrative([
      item({ id: "boundary", sequence: 0, source: "harness", kind: "harness_status",
        title: "Run boundary", detail: "successor" }),
      item({ id: "search-1", canonical_id: "search-call-1", sequence: 2,
        source: "harness", kind: "tool_call",
        activity_type: "search", title: "搜索 RunSupervisor", detail: "3 个结果",
        activity_detail_ref: "detail-search-1", detail_available: true }),
      item({ id: "search-2", canonical_id: "search-call-2", sequence: 3,
        source: "harness", kind: "tool_call",
        activity_type: "search", title: "搜索 execute", detail: "2 个结果" }),
    ]);
    expect(projected).toHaveLength(1);
    expect(projected[0]).toMatchObject({ kind: "activity", activity: "search", count: 2,
      items: [expect.objectContaining({ detailRef: "detail-search-1", detailAvailable: true }),
        expect.objectContaining({ detailAvailable: false })] });
  });

  it("keeps the user story while omitting benign Harness chatter", () => {
    const projected = projectThreadNarrative([
      item({ id: "user", source: "operator", kind: "operator_input",
        detail: "为什么阻塞了？" }),
      item({ id: "model", sequence: 2, detail: "问题出在执行循环。" }),
      item({ id: "checkpoint", sequence: 3, source: "harness", kind: "harness_status",
        activity_type: "checkpoint", status: "checkpointed", title: "检查点", detail: "已记录" }),
    ]);
    expect(projected.map((entry) => entry.kind)).toEqual(["user", "assistant"]);
  });

  it("collapses identical durable answers across a synthetic continuation", () => {
    const projected = projectThreadNarrative([
      item({ id: "user", source: "operator", kind: "operator_input",
        detail: "只回复：流式链路已连通" }),
      item({ id: "answer-1", sequence: 2, detail: "流式链路已连通" }),
      item({ id: "synthetic", sequence: 3, source: "operator", kind: "operator_input",
        detail: "Continue mission at turn 2 using only the structured tools offered by Go when needed: 只回复：流式链路已连通" }),
      item({ id: "answer-2", sequence: 4, detail: "流式链路已连通" }),
    ]);

    expect(projected).toEqual([
      expect.objectContaining({ kind: "user", text: "只回复：流式链路已连通" }),
      expect.objectContaining({ kind: "assistant", text: "流式链路已连通", branchSourceID: "answer-2" }),
    ]);
  });

  it("omits synthetic continuation and all internal turn state while keeping real input and blockers", () => {
    const projected = projectThreadNarrative([
      item({ id: "initial-user", sequence: 1, source: "operator", kind: "operator_input",
        title: "用户消息", detail: "请检查归档权限为什么被阻塞。",
        instruction_authorized: true }),
      item({ id: "model-call-complete", sequence: 2, source: "harness", kind: "model_call",
        activity_type: "checkpoint", status: "completed", title: "模型响应完成",
        detail: "mock / mock-code" }),
      item({ id: "model-call-failed", sequence: 3, source: "harness", kind: "model_call",
        activity_type: "checkpoint", stage: "blocked", status: "failed",
        title: "模型调用失败", detail: "provider internal failure" }),
      item({ id: "agent-turn-failed", sequence: 4, source: "harness", kind: "harness_status",
        activity_type: "checkpoint", stage: "blocked", status: "failed",
        title: "Agent 回合失败", detail: "internal protocol failure" }),
      item({ id: "mock-plan", sequence: 5, source: "model", kind: "model_update",
        title: "针路簿更新",
        detail: "mock plan [mock-code]: inspect workspace context, keep actions scoped" }),
      item({ id: "synthetic-continuation", sequence: 6, source: "operator",
        kind: "operator_input", title: "用户消息",
        detail: "Continue mission at turn 2 using only the structured tools offered by Go when needed: audit archive flow" }),
      item({ id: "harness-checkpoint", sequence: 7, source: "harness", kind: "plan",
        activity_type: "checkpoint", status: "completed", title: "Supervisor 检查点已记录",
        detail: "checkpoint 2" }),
      item({ id: "assistant", sequence: 8, source: "model", kind: "model_update",
        title: "针路簿更新", detail: "已确认阻塞来自权限门。" }),
      item({ id: "blocked", sequence: 9, source: "harness", kind: "dependency",
        activity_type: "checkpoint", stage: "blocked", status: "blocked",
        title: "检测到无进展循环", detail: "同一权限依赖未能取得进展" }),
    ]);

    expect(projected).toEqual([
      expect.objectContaining({ kind: "user", text: "请检查归档权限为什么被阻塞。" }),
      expect.objectContaining({ kind: "assistant", text: "已确认阻塞来自权限门。" }),
      expect.objectContaining({ kind: "notice", tone: "warning",
        text: "同一权限依赖未能取得进展" }),
    ]);
    expect(projected.map((entry) => "text" in entry ? entry.text : "").join("\n"))
      .not.toMatch(/mock plan|Continue mission|Agent 回合|模型响应|检查点/u);
  });

  it("projects safe live commentary and replaces it by stable durable turn identity", () => {
    const live = snapshot();
    const provisional = projectThreadNarrative([], {
      runId: "run-1", snapshot: live, status: "live",
    });
    expect(provisional).toEqual([
      expect.objectContaining({
        id: "live-message:attempt-1:1:1", kind: "assistant",
        text: "我先检查相关实现。", provisional: true,
      }),
    ]);

    const durable = item({
      id: "durable-commentary", attempt_id: "attempt-1", model_attempt: 1,
      tool_round: 1, detail: "我先检查相关实现。", created_at: "2026-08-29T00:00:03Z",
    });
    const replaced = projectThreadNarrative([durable], {
      runId: "run-1", snapshot: live, status: "finalizing",
    });
    expect(replaced).toHaveLength(1);
    expect(replaced[0]).toMatchObject({ id: "durable-commentary", provisional: false });
  });

  it("uses safe tool labels and deduplicates a live tool by stream identity", () => {
    const live = snapshot({
      text: "",
      items: [{
        id: "stream-tool-1", response_id: "response-1", type: "tool_call",
        status: "ready_for_validation", tool_name: "workspace_read", argument_bytes: 128,
        durable: false, provisional: true,
      }],
    });
    const provisional = projectThreadNarrative([], {
      runId: "run-1", snapshot: live, status: "live",
    });
    expect(provisional).toEqual([
      expect.objectContaining({ kind: "activity", activity: "read", title: "读取文件",
        provisional: true }),
    ]);
    expect(JSON.stringify(provisional)).not.toContain("workspace_read");

    const durable = item({
      id: "durable-tool", canonical_id: "durable-tool", source: "harness", kind: "tool_call",
      activity_type: "read", title: "读取完成", detail: "已读取", stream_item_id: "stream-tool-1",
      tool_name: "workspace_read", created_at: "2026-08-29T00:00:03Z",
    });
    const replaced = projectThreadNarrative([durable], {
      runId: "run-1", snapshot: live, status: "finalizing",
    });
    expect(replaced).toHaveLength(1);
    expect(replaced[0]).toMatchObject({ kind: "activity", title: "读取文件",
      provisional: false, count: 1 });
  });

  it("presents source discovery as a safe search activity", () => {
    const live = snapshot({
      text: "",
      items: [{
        id: "stream-source-search", response_id: "response-1", type: "tool_call",
        status: "ready_for_validation", tool_name: "source_search", argument_bytes: 96,
        durable: false, provisional: true,
      }],
    });
    expect(projectThreadNarrative([], { runId: "run-1", snapshot: live, status: "live" }))
      .toEqual([expect.objectContaining({ kind: "activity", activity: "search",
        title: "搜索专业来源", provisional: true })]);
  });

  it("merges durable lifecycle stages by canonical tool-call identity", () => {
    const projected = projectThreadNarrative([
      item({ id: "tool-arguments", canonical_id: "call-workspace-list", sequence: 1,
        source: "harness", kind: "tool_call", activity_type: "search",
        stage: "arguments_ready", status: "arguments_ready", tool_name: "workspace_list",
        detail: "参数已就绪" }),
      item({ id: "tool-running", canonical_id: "call-workspace-list", sequence: 2,
        source: "harness", kind: "tool_call", activity_type: "search",
        stage: "running", status: "running", tool_name: "workspace_list",
        detail: "正在列出工作区" }),
      item({ id: "progress", canonical_id: "progress", sequence: 3,
        source: "model", kind: "model_update", activity_type: "message",
        detail: "我正在读取工作区。" }),
      item({ id: "tool-result", canonical_id: "call-workspace-list", sequence: 4,
        source: "harness", kind: "tool_call", activity_type: "search",
        stage: "result", status: "completed", tool_name: "workspace_list",
        detail: "列出了 4 个条目" }),
      item({ id: "second-result", canonical_id: "call-workspace-search", sequence: 5,
        source: "harness", kind: "tool_call", activity_type: "search",
        stage: "result", status: "completed", tool_name: "workspace_search",
        detail: "找到 2 个结果" }),
    ]);

    const activities = projected.filter((entry) => entry.kind === "activity");
    expect(activities).toHaveLength(2);
    expect(activities[0]).toMatchObject({ count: 1, detail: "列出了 4 个条目",
      status: "completed", items: [{ detail: "列出了 4 个条目", status: "completed" }] });
    expect(activities[1]).toMatchObject({ count: 1, detail: "找到 2 个结果" });
  });

  it("shows Web fetch security outcomes and hides generic batch completion", () => {
    const evidence = {
      version: "web_evidence_presentation.v1", source_id: "source-web",
      snapshot_id: "snapshot-web", url: "https://docs.example.com/report",
      title: "Report", fetched_at: "2026-08-29T00:00:00Z",
      stale_at: "2026-08-30T00:00:00Z", digest: "a".repeat(64),
      partial: false, stale: false, citeable: false, untrusted: true,
      instruction_authorized: false,
    } as const;
    const projected = projectThreadNarrative([
      item({ id: "blocked-fetch", canonical_id: "call-web", source: "harness",
        kind: "tool_call", activity_type: "read", tool_name: "web_fetch",
        title: "Robots 规则阻止抓取", detail: "已记录", status: "blocked",
        web_evidence: { ...evidence, state: "blocked" } }),
      item({ id: "batch-complete", canonical_id: "batch-complete", sequence: 2,
        source: "harness", kind: "tool_call", activity_type: "execute",
        title: "工具批次完成", detail: "", status: "completed" }),
      item({ id: "failed-fetch", canonical_id: "call-web-failed", sequence: 3,
        source: "harness", kind: "tool_call", activity_type: "read", tool_name: "web_fetch",
        title: "工具结果已记录", detail: "Web Fetch", status: "completed",
        web_evidence: { ...evidence, snapshot_id: "snapshot-failed", state: "failed" } }),
      item({ id: "unchecked-fetch", canonical_id: "call-web-unchecked", sequence: 4,
        source: "harness", kind: "tool_call", activity_type: "read", tool_name: "web_fetch",
        title: "网页已抓取（未检查 Robots）", detail: "已记录", status: "robots_ignored",
        web_evidence: { ...evidence, snapshot_id: "snapshot-unchecked", state: "fetched",
          citeable: true } }),
      item({ id: "robots-disallow", canonical_id: "call-web-robots-disallow", sequence: 5,
        source: "harness", kind: "tool_call", activity_type: "read", tool_name: "web_fetch",
        title: "Full Access 已忽略站点 Robots 限制", detail: "已记录", status: "robots_ignored",
        web_evidence: { ...evidence, snapshot_id: "snapshot-disallow", state: "fetched",
          citeable: true } }),
      item({ id: "robots-unknown", canonical_id: "call-web-robots-unknown", sequence: 6,
        source: "harness", kind: "tool_call", activity_type: "read", tool_name: "web_fetch",
        title: "Robots 无法验证，已按 Full Access 继续", detail: "已记录", status: "robots_ignored",
        web_evidence: { ...evidence, snapshot_id: "snapshot-unknown", state: "fetched",
          citeable: true } }),
    ]);

    expect(projected).toHaveLength(1);
	expect(projected[0]).toMatchObject({ kind: "activity", count: 5,
	  title: "Robots 无法验证，已按 Full Access 继续",
	  detail: "未能验证站点 Robots 规则；Full Access 仍继续创建了快照",
      status: "robots_ignored",
      items: [
        expect.objectContaining({ title: "Robots 规则阻止抓取", status: "blocked" }),
        expect.objectContaining({ title: "网页验证不可用",
          status: "verification_unavailable" }),
        expect.objectContaining({ title: "网页已抓取（未检查 Robots）",
          status: "robots_ignored" }),
		expect.objectContaining({ title: "Full Access 已忽略站点 Robots 限制",
		  status: "robots_ignored" }),
		expect.objectContaining({ title: "Robots 无法验证，已按 Full Access 继续",
		  status: "robots_ignored" }),
      ] });
    expect(JSON.stringify(projected)).not.toContain("工具批次完成");
    expect(JSON.stringify(projected)).not.toContain("工具结果已记录");
  });

  it("never carries a previous Run or Attempt projection into the current binding", () => {
    const staleRun = projectThreadNarrative([], {
      runId: "run-2", snapshot: snapshot(), status: "live",
    });
    expect(staleRun).toEqual([]);

    const nextAttempt = snapshot({
      call: { ...snapshot().call, attempt_id: "attempt-2", model_attempt: 2 },
      text: "我正在重试。", revision: 3,
    });
    const projected = projectThreadNarrative([], {
      runId: "run-1", snapshot: nextAttempt, status: "live",
    });
    expect(projected).toEqual([
      expect.objectContaining({ id: "live-message:attempt-2:2:1", text: "我正在重试。" }),
    ]);
    expect(JSON.stringify(projected)).not.toContain("attempt-1");
  });

  it("preserves assistant source runId and does not merge assistant answers across runs", () => {
    const projected = projectThreadNarrative([
      item({ id: "answer-run1", canonical_id: "canonical-a1", run_id: "run-1", detail: "这是第一次运行的回答。" }),
      item({ id: "answer-run2", canonical_id: "canonical-a2", run_id: "run-2", detail: "这是第二次运行的回答。" }),
    ]);
    expect(projected).toHaveLength(2);
    expect(projected[0]).toEqual(expect.objectContaining({
      kind: "assistant",
      runId: "run-1",
      text: "这是第一次运行的回答。",
    }));
    expect(projected[1]).toEqual(expect.objectContaining({
      kind: "assistant",
      runId: "run-2",
      text: "这是第二次运行的回答。",
    }));
  });

  it("merges adjacent durable assistant answers within the same run", () => {
    const projected = projectThreadNarrative([
      item({ id: "part-1", canonical_id: "canonical-p1", run_id: "run-1", detail: "第一部分内容。" }),
      item({ id: "part-2", canonical_id: "canonical-p2", run_id: "run-1", detail: "第二部分内容。" }),
    ]);
    expect(projected).toHaveLength(1);
    expect(projected[0]).toEqual(expect.objectContaining({
      kind: "assistant",
      runId: "run-1",
      text: "第一部分内容。\n\n第二部分内容。",
      messageIDs: ["part-1", "part-2"],
      branchSourceID: "part-2",
      changeSourceID: "part-1",
    }));
  });

  it("keeps normalized display text and replay identities while identifying the contributing bodies", () => {
    const projected = projectThreadNarrative([
      item({ id: "part-1", sequence: 1, detail: "    first();\n" }),
      item({ id: "part-2", sequence: 2, detail: "\n\tsecond();  \n" }),
      item({ id: "ending", sequence: 3, detail: "\nfirst();\n\nsecond();\n" }),
    ]);
    expect(projected).toHaveLength(1);
    expect(projected[0]).toMatchObject({
      kind: "assistant", text: "first();\n\nsecond();",
      messageIDs: ["part-1", "part-2", "ending"],
      textMessageIDs: ["part-1", "part-2"],
      branchSourceID: "ending", changeSourceID: "part-1",
    });
  });
});

it("localizes generated activity labels while preserving original messages, commands, and evidence", () => {
  const english = (_chinese: string, translated: string) => translated;
  const transcript = [item({ id: "read", canonical_id: "read", activity_type: "read", kind: "tool_call", tool_name: "workspace_read", detail: "原始工具输出" }),
    item({ id: "reply", canonical_id: "reply", sequence: 2, detail: "原始中文答复" })];
  const prepared = prepareThreadNarrative(transcript, english);
  expect(prepared.entries[0]).toMatchObject({ kind: "activity", title: "Read file", detail: "原始工具输出" });
  expect(prepared.entries[1]).toMatchObject({ kind: "assistant", text: "原始中文答复" });
  const live = projectLiveThreadNarrative(prepareThreadNarrative([], english), { runId: "run-1", status: "live",
    snapshot: snapshot({ text: "", items: [{ id: "tool-1", type: "tool_call", tool_name: "command_runtime", status: "running", argument_bytes: 12, durable: false, provisional: true, response_id: "response-1" }] }) });
  expect(live[0]).toMatchObject({ kind: "activity", title: "Run command", detail: "Preparing tool request · 12 bytes" });
});
