import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { APIClient } from "../../api/client";
import type { PublicModelStreamSnapshot, ThreadDetailView, ThreadTranscriptItemView } from "../../api/types";
import { V2Inspector, type V2InspectorProps } from "./inspector";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
const createdAt = "2026-09-10T15:00:00Z";
function item(id: string, overrides: Partial<ThreadTranscriptItemView> = {}): ThreadTranscriptItemView {
  return { version: "thread_transcript.v1", id, canonical_id: id, run_id: "run-1", run_ordinal: 1,
    sequence: 1, created_at: createdAt, activity_type: "checkpoint", stage: "result", kind: "harness_status",
    source: "harness", title: id, status: "completed", verifiable: true, instruction_authorized: false,
    durable: true, provisional: false, ...overrides };
}
function setup(items: ThreadTranscriptItemView[], client = {} as APIClient) {
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const props: V2InspectorProps = { client, threadID: "thread-1", detail: { thread: { id: "thread-1", last_run_id: "run-2" } } as ThreadDetailView,
    durableItems: items, liveSnapshot: null, liveStatus: "stopped", hasOlder: true, isFetchingOlder: false, onLoadOlder: vi.fn() };
  const draw = (current = props) => render(<QueryClientProvider client={cache}><V2Inspector {...current} /></QueryClientProvider>);
  return { cache, props, draw };
}
function result(runID = "run-1") {
  return { version: "thread_activity_detail.v2", activity_ref: "detail-tool", run_id: runID, tools: [{
    name: "command_runtime", label: "执行检查", agent_id: "agent-1", agent_label: "Agent", agent_role: "root",
    status: "failed", duration_milliseconds: 50, detail: { kind: "command", command: { commands: [{
      command: "actual check", status: "failed", exit_code: 7, working_directory: "/workspace",
      duration_milliseconds: 50, stdout_preview: "preserved output", stderr_preview: "actual error", truncated: false,
      artifacts: [], execution_environment: "Host", network: "disabled",
    }] } },
  }] };
}

describe("V2Inspector", () => {
  it("keeps an old model start as a time-bound fact without presenting it as current Agent activity", async () => {
    const { draw } = setup([
      item("open-boundary", { sequence: 0, status: "running" }),
      item("old-model-start", { sequence: 2, title: "模型调用开始", stage: "started", status: "running", source_ref: "model.started:old-attempt" }),
      item("later-model-result", { sequence: 3, title: "模型调用完成", status: "completed" }),
    ]);
    const view = draw();
    expect(view.container.querySelector('[data-transcript-id="open-boundary"]')).toHaveTextContent("未结束");
    expect(view.container.querySelector('[data-transcript-id="old-model-start"]')).toHaveTextContent("记录时执行中");
    expect(view.container.querySelector('[data-transcript-id="later-model-result"]')).toHaveTextContent("已完成");
    await userEvent.setup().click(screen.getByRole("button", { name: /模型调用开始/ }));
    expect(screen.getByText(/这是记录保存时的执行状态/)).toHaveTextContent("Agent 的当前活动请查看对话上方的工作状态");
    expect(screen.getByText("model.started:old-attempt")).toBeInTheDocument();
    expect(screen.queryByText("运行中")).not.toBeInTheDocument();
  });

  it("filters loaded records using structured type/status and keeps exact Run boundaries", async () => {
    const user = userEvent.setup();
    const { draw, props } = setup([
      item("boundary-1", { sequence: 0 }),
      item("tool-failed", { sequence: 2, kind: "tool_call", activity_type: "execute", tool_name: "command_runtime", status: "failed" }),
      item("model-says-failed", { sequence: 3, source: "model", kind: "model_update", activity_type: "message", detail: "failed is only quoted text" }),
      item("approval-record", { sequence: 4, kind: "approval", activity_type: "approval", status: "denied" }),
      item("boundary-2", { sequence: 0, run_id: "run-2", run_ordinal: 2 }),
      item("current-read", { sequence: 2, run_id: "run-2", run_ordinal: 2, kind: "tool_call", activity_type: "read", tool_name: "workspace_read" }),
    ]);
    const view = draw();
    expect(screen.getByText(/已加载 6 条/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "异常" }));
    expect(view.container.querySelector('[data-transcript-id="tool-failed"]')).toBeInTheDocument();
    expect(view.container.querySelector('[data-transcript-id="approval-record"]')).toBeInTheDocument();
    expect(view.container.querySelector('[data-transcript-id="model-says-failed"]')).not.toBeInTheDocument();
    expect(view.container.querySelector('[data-transcript-id="boundary-1"]')).toBeInTheDocument();
    expect(view.container.querySelector('[data-transcript-id="boundary-2"]')).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "全部" }));
    await user.selectOptions(screen.getByLabelText("执行记录范围"), "run-1");
    expect(view.container.querySelector('[data-transcript-id="current-read"]')).not.toBeInTheDocument();
    await user.type(screen.getByLabelText("在已加载记录中查找"), "not-loaded-yet");
    expect(screen.getByText("已加载范围内没有匹配记录。")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "清除筛选" })).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "加载较早记录" }));
    expect(props.onLoadOlder).toHaveBeenCalledTimes(1);
  });

  it("loads exact selected tool details on demand, preserves failures, and returns keyboard focus", async () => {
    const user = userEvent.setup();
    const read = vi.fn().mockResolvedValue(result());
    const client = { threadActivityDetail: read, resumeHostCommandProposal: vi.fn(), submitThreadTurn: vi.fn() } as unknown as APIClient;
    const { draw } = setup([item("actual-tool", { kind: "tool_call", activity_type: "execute", tool_name: "command_runtime",
      detail_available: true, activity_detail_ref: "detail-tool", status: "failed" })], client);
    draw();
    expect(read).not.toHaveBeenCalled();
    const row = screen.getByRole("button", { name: /command_runtime/ });
    await user.click(row);
    expect(await screen.findByText("preserved output")).toBeInTheDocument();
    expect(screen.getByText("Exit 7 · 50ms")).toBeInTheDocument();
    expect(read).toHaveBeenCalledExactlyOnceWith("thread-1", "detail-tool", expect.any(AbortSignal));
    expect(screen.getByRole("button", { name: "关闭记录详情" })).toHaveFocus();
    const hiddenAtFocus: boolean[] = [];
    const originalFocus = row.focus.bind(row);
    vi.spyOn(row, "focus").mockImplementation(() => {
      // On narrow layouts the records cannot receive focus until selection is cleared.
      hiddenAtFocus.push(Boolean(row.closest(".has-selection")));
      originalFocus();
    });
    await user.keyboard("{Escape}");
    expect(screen.queryByLabelText("选中记录详情")).not.toBeInTheDocument();
    expect(row).toHaveFocus();
    expect(hiddenAtFocus).toEqual([false]);
    expect(client.resumeHostCommandProposal).not.toHaveBeenCalled();
    expect(client.submitThreadTurn).not.toHaveBeenCalled();
  });

  it("keeps persisted failed web evidence in exceptions without treating quoted failure text as execution failure", async () => {
    const evidence = { version: "web_evidence_presentation.v1", source_id: "source-failed", snapshot_id: "snapshot-failed",
      url: "https://docs.example.com/page", state: "failed", fetched_at: createdAt, stale_at: createdAt,
      digest: "a".repeat(64), partial: false, stale: false, citeable: false, untrusted: true, instruction_authorized: false };
    const { draw } = setup([
      item("failed-web", { kind: "tool_call", activity_type: "read", tool_name: "web_fetch",
        stage: "result", status: "verification_unavailable", web_evidence: evidence }),
      item("persisted-failure-state", { kind: "tool_call", activity_type: "read", tool_name: "web_fetch",
        stage: "result", status: "completed", web_evidence: evidence }),
      item("quoted-failure", { source: "model", kind: "model_update", activity_type: "message", detail: "verification_unavailable is quoted text" }),
      item("successful-web", { kind: "tool_call", activity_type: "read", tool_name: "web_fetch",
        web_evidence: { ...evidence, state: "fetched", citeable: true } }),
    ]);
    const view = draw();
    await userEvent.setup().click(screen.getByRole("button", { name: "异常" }));
    expect(view.container.querySelector('[data-transcript-id="failed-web"]')).toBeInTheDocument();
    expect(view.container.querySelector('[data-transcript-id="persisted-failure-state"]')).toBeInTheDocument();
    expect(view.container.querySelector('[data-transcript-id="quoted-failure"]')).not.toBeInTheDocument();
    expect(view.container.querySelector('[data-transcript-id="successful-web"]')).not.toBeInTheDocument();
  });

  it("omits a redundant single-Run selector but preserves and clears an unloaded historical selection", async () => {
    const { draw, cache } = setup([item("only-current", { run_id: "run-2", run_ordinal: 2 })]);
    const view = draw();
    expect(screen.queryByLabelText("执行记录范围")).not.toBeInTheDocument();
    act(() => cache.setQueryData(["v2", "thread", "thread-1", "inspector-view"], {
      filter: "all", search: "", runID: "run-old-not-loaded", selected: null,
    }));
    await waitFor(() => expect(screen.getByLabelText("执行记录范围")).toHaveValue("run-old-not-loaded"));
    expect(screen.getByRole("option", { name: "已选执行（暂未加载）" })).toHaveValue("run-old-not-loaded");
    expect(view.container.querySelector('[data-transcript-id="only-current"]')).not.toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "清除筛选" })).toHaveLength(1);
    await userEvent.setup().click(screen.getByRole("button", { name: "清除筛选" }));
    expect(screen.queryByLabelText("执行记录范围")).not.toBeInTheDocument();
    expect(view.container.querySelector('[data-transcript-id="only-current"]')).toBeInTheDocument();
  });

  it("keeps failure and provenance visible while exact sequence and stage stay in selected details", async () => {
    const { draw } = setup([item("blocked-original", { sequence: 87, status: "failed", stage: "blocked", detail: "Actual failure explanation" })]);
    const view = draw();
    const row = view.container.querySelector('[data-transcript-id="blocked-original"] button')!;
    expect(row).toHaveTextContent("失败");
    expect(row).toHaveTextContent("执行记录");
    expect(row).toHaveTextContent("Actual failure explanation");
    expect(row).not.toHaveTextContent("#87");
    expect(row).not.toHaveTextContent("已阻塞");
    await userEvent.setup().click(row);
    const detail = within(screen.getByLabelText("选中记录详情"));
    expect(detail.getByText(/执行记录 · 已阻塞/)).toBeInTheDocument();
    const identities = detail.getByText("来源与精确标识").closest("details")!;
    expect(identities).not.toHaveAttribute("open");
    await userEvent.setup().click(detail.getByText("来源与精确标识"));
    expect(identities).toHaveAttribute("open");
    expect(within(identities).getByText("87")).toBeVisible();
    expect(within(identities).getByText("run-1")).toBeVisible();
  });

  it("rejects a detail returned for another Run and keeps the original event available", async () => {
    const user = userEvent.setup();
    const read = vi.fn().mockResolvedValue(result("wrong-run"));
    const { draw } = setup([item("original-event", { detail: "original failure remains", kind: "tool_call", activity_type: "execute",
      detail_available: true, activity_detail_ref: "detail-tool" })], { threadActivityDetail: read } as unknown as APIClient);
    draw();
    await user.click(screen.getByRole("button", { name: /original-event/ }));
    expect(await screen.findByText("执行详情加载失败。")).toBeInTheDocument();
    expect(screen.queryByText("preserved output")).not.toBeInTheDocument();
    expect(within(screen.getByLabelText("选中记录详情")).getByText("original failure remains")).toBeInTheDocument();
  });

  it("restores selected event and filters through the query cache without leaking to another Thread", async () => {
    const user = userEvent.setup();
    const { draw, props } = setup([item("approval-to-inspect", { activity_type: "approval", kind: "approval", detail: "specific saved decision" })]);
    let view = draw();
    await user.click(screen.getByRole("button", { name: "审批" }));
    await user.type(screen.getByLabelText("在已加载记录中查找"), "specific");
    await user.click(screen.getByRole("button", { name: /approval-to-inspect/ }));
    view.unmount();
    view = draw();
    expect(screen.getByLabelText("在已加载记录中查找")).toHaveValue("specific");
    expect(screen.getByRole("button", { name: "审批" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByLabelText("选中记录详情")).toBeInTheDocument();
    view.unmount();
    draw({ ...props, threadID: "thread-other" });
    expect(screen.getByLabelText("在已加载记录中查找")).toHaveValue("");
    expect(screen.queryByLabelText("选中记录详情")).not.toBeInTheDocument();
  });

  it("restores a reading anchor, preserves it while prepending, and explicitly returns to latest", async () => {
    vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(240);
    vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(8000);
    const items = Array.from({ length: 50 }, (_, index) => item(`record-${index}`, { sequence: index + 10 }));
    const { draw, props, cache } = setup(items);
    let view = draw();
    let viewport = screen.getByTestId("thread-transcript-viewport");
    fireEvent.scroll(viewport, { target: { scrollTop: 400 } });
    await waitFor(() => expect(cache.getQueryData<{ position: { nearBottom: boolean } }>(["v2", "thread", "thread-1", "inspector-view"])?.position.nearBottom).toBe(false));
    view.unmount();
    view = draw();
    viewport = screen.getByTestId("thread-transcript-viewport");
    expect(viewport.scrollTop).toBe(400);
    const older = Array.from({ length: 3 }, (_, index) => item(`older-${index}`, { sequence: index + 1 }));
    act(() => view.rerender(<QueryClientProvider client={cache}><V2Inspector {...props} durableItems={[...older, ...items]} /></QueryClientProvider>));
    expect(viewport.scrollTop).toBe(400 + 3 * 132);
    await userEvent.setup().click(screen.getByRole("button", { name: "回到最新" }));
    expect(viewport.scrollTop).toBe(8000);
  });

  it("bounds the DOM and does not merge similar live text without an exact identity", () => {
    const { draw, props } = setup(Array.from({ length: 10000 }, (_, index) => item(`record-${index}`, { sequence: index + 1 })));
    let view = draw();
    expect(view.container.querySelectorAll("[data-transcript-id]").length).toBeLessThanOrEqual(80);
    view.unmount();
    const live = { version: "model_public_stream.v3", call: { run_id: "run-1", session_id: "session-1", attempt_id: "attempt-1",
      model_attempt: 1, transport_attempt: 1, max_attempts: 1, protocol_repair: 0, tool_round: 0, provider: "test", model: "test",
      started_at: createdAt, stream_chunks: 1, stream_bytes: 8, cancel_requested: false }, revision: 1, response_id: "response-1", event_sequence: 1,
      items: [{ type: "message", id: "stream-message-new", response_id: "response-1", status: "completed", provisional: true, durable: false }],
      content_kind: "tool_commentary", text: "same public text", message_complete: true, provisional: true, updated_at: createdAt } as PublicModelStreamSnapshot;
    view = draw({ ...props, durableItems: [item("original-message", { source: "model", kind: "model_update", activity_type: "message", detail: "same public text" })],
      liveSnapshot: live, liveStatus: "live" });
    expect(view.container.querySelectorAll("[data-transcript-id]")).toHaveLength(2);
  });
});

function render(ui: ReactElement, options?: RenderOptions) {
 localStorage.setItem("prayu.locale.v1", "zh-CN");
 const Wrapper=options?.wrapper;
 return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
