import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import type { PublicModelStreamSnapshot, RunActivityView } from "../api/types";
import { LocaleProvider, type PrayuLocale } from "../lib/locale";
import { RunActivityTimeline } from "./run-activity-timeline";

describe("RunActivityTimeline", () => {
  it("marks a saved running tool fact as historical while live tool preparation remains distinct", () => {
    const value = activity();
    value.items = [toolItem(13, "工具操作开始", "running")];
    const live = publicSnapshot();
    live.text = "";
    live.items = [{ response_id: "response-new", id: "new-tool", type: "tool_call",
      status: "in_progress", call_id: "new-call", tool_name: "new_live_tool",
      argument_bytes: 24, provisional: true, durable: false }];
    renderTimeline(<RunActivityTimeline activity={value} liveCommentary={live} liveStatus="live" />);
    expect(screen.getByText("记录时执行中")).toHaveAttribute("title", "事件保存时的执行状态。查看当前进度请核对最新活动。");
    expect(screen.getByText("正在准备调用 · 24 字节")).toBeInTheDocument();
    expect(screen.getByText("临时")).toBeInTheDocument();
    expect(screen.getByText("git diff --check")).toBeInTheDocument();
    expect(screen.queryByText("进行中")).not.toBeInTheDocument();
  });

  it("separates public model updates from verifiable Harness facts", () => {
    renderTimeline(<RunActivityTimeline activity={activity()} />);

    expect(screen.getByText("模型公开更新")).toBeInTheDocument();
    expect(screen.getByText("模型调用")).toBeInTheDocument();
    expect(screen.getByText("我会先核对工作区，再运行测试。")).toBeInTheDocument();
    expect(screen.getByText("模型响应完成")).toBeInTheDocument();
    expect(screen.getByText("公开活动")).toBeInTheDocument();
    expect(screen.getByText(/实际操作结果请核对执行记录/u)).toBeInTheDocument();
    expect(screen.getByText("模型调用").closest("details")).not.toHaveAttribute("open");
  });

  it("groups consecutive Harness tool facts into one disclosure", () => {
    const value = activity();
    value.items.push(toolItem(13, "工具操作开始", "running"),
      toolItem(14, "工具操作完成", "completed"));
    renderTimeline(<RunActivityTimeline activity={value} />);

    expect(screen.getByText("运行了 2 个操作")).toBeInTheDocument();
    expect(screen.getAllByText("git diff --check")).toHaveLength(2);
    expect(screen.queryByText("工具操作开始")).not.toBeInTheDocument();
    expect(screen.queryByText("工具操作完成")).not.toBeInTheDocument();
  });

  it("keeps truncation and stream failure visible without inventing activity", () => {
    const value = activity();
    value.items = [];
    value.truncated = true;
    renderTimeline(<RunActivityTimeline activity={value} streamError="connection lost" />);

    expect(screen.getByText(/最近一段活动/u)).toBeInTheDocument();
    expect(screen.getByText(/connection lost/u)).toBeInTheDocument();
    expect(screen.getByText("还没有公开活动")).toBeInTheDocument();
  });

  it("fails closed if a server ever marks private reasoning as included", () => {
    const value = activity();
    value.private_reasoning_included = true;
    renderTimeline(<RunActivityTimeline activity={value} />);

    expect(screen.getByText(/活动投影已拒绝/u)).toBeInTheDocument();
    expect(screen.queryByText("针路簿更新")).not.toBeInTheDocument();
  });

  it("shows provisional public commentary as live Activity", () => {
    const value = activity();
    renderTimeline(<RunActivityTimeline activity={value}
      liveCommentary={publicSnapshot()} liveStatus="live" />);

    expect(screen.getByText("Universal-Code")).toBeInTheDocument();
    expect(screen.getByText("正在检查差异，下一步运行测试。")).toBeInTheDocument();
    expect(screen.getByText("临时")).toBeInTheDocument();
    expect(screen.getByText(/对话历史保存最终确认的内容/u)).toBeInTheDocument();
  });

  it("replaces provisional commentary when its durable identity arrives", () => {
    const value = activity();
    value.items.push({
      id: "event-13",
      sequence: 13,
      kind: "model_update",
      source: "model",
      title: "针路簿进度",
      detail: "持久化后的公开进度。",
      verifiable: false,
      instruction_authorized: false,
      created_at: "2026-07-30T01:00:03Z",
      attempt_id: "attempt-live",
      model_attempt: 2,
      tool_round: 4,
    });
    renderTimeline(<RunActivityTimeline activity={value}
      liveCommentary={publicSnapshot()} liveStatus="live" />);

    expect(screen.getByText("持久化后的公开进度。")).toBeInTheDocument();
    expect(screen.queryByText("正在检查差异，下一步运行测试。")).not.toBeInTheDocument();
    expect(screen.queryByText("临时")).not.toBeInTheDocument();
  });

  it("does not project a provisional root reply as Live Activity", () => {
    const snapshot = publicSnapshot();
    snapshot.content_kind = "root_message";
    renderTimeline(<RunActivityTimeline activity={activity()}
      liveCommentary={snapshot} liveStatus="live" />);

    expect(screen.queryByText("正在检查差异，下一步运行测试。")).not.toBeInTheDocument();
    expect(screen.queryByText("临时")).not.toBeInTheDocument();
  });

  it("shows tool preparation without exposing streamed arguments", () => {
    const snapshot = publicSnapshot();
    snapshot.text = "";
    snapshot.items = [{
      response_id: "response-live", id: "item-tool-1", type: "tool_call",
      status: "in_progress", call_id: "call-tool-1", tool_name: "read_file",
      argument_bytes: 32, provisional: true, durable: false,
    }];
    renderTimeline(<RunActivityTimeline activity={activity()}
      liveCommentary={snapshot} liveStatus="live" />);

    expect(screen.getByText("read_file")).toBeInTheDocument();
    expect(screen.getByText("正在准备调用 · 32 字节")).toBeInTheDocument();
    expect(screen.getByText(/服务端验证通过后执行/u)).toBeInTheDocument();
    expect(screen.queryByText(/README\.md/u)).not.toBeInTheDocument();
  });

  it("condenses a completed tool lifecycle into actual operation rows", () => {
    const value = activity();
    value.items.push(
      toolLifecycleItem(13, "工具调用已请求", "浏览工作区、读取文件", "running"),
      toolLifecycleItem(14, "工具结果已记录", "浏览工作区", "completed"),
      toolLifecycleItem(15, "工具结果已记录", "读取文件", "completed"),
      toolLifecycleItem(16, "工具批次完成", "", "completed"),
    );
    renderTimeline(<RunActivityTimeline activity={value} />);

    expect(screen.getByText("运行了 2 个操作")).toBeInTheDocument();
    expect(screen.getByText("浏览工作区")).toBeInTheDocument();
    expect(screen.getByText("读取文件")).toBeInTheDocument();
    expect(screen.queryByText("工具批次完成")).not.toBeInTheDocument();
  });

  it("localizes Activity chrome in English", () => {
    const value = activity();
    value.items = [];
    renderTimeline(<RunActivityTimeline activity={value} />, "en-US");

    expect(screen.getByRole("region", { name: "Run activity" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Activity" })).toBeInTheDocument();
    expect(screen.getByText("Public activity")).toBeInTheDocument();
    expect(screen.getByText("No public activity yet")).toBeInTheDocument();
    expect(screen.queryByText("还没有公开活动")).not.toBeInTheDocument();
  });
});

function renderTimeline(node: ReactNode, locale: PrayuLocale = "zh-CN") {
  window.localStorage.setItem("prayu.locale.v1", locale);
  return render(<LocaleProvider>{node}</LocaleProvider>);
}

function activity(): RunActivityView {
  return {
    version: "run_activity.v1",
    run_id: "run-1",
    through_sequence: 12,
    truncated: false,
    private_reasoning_included: false,
    items: [
      {
        id: "event-11",
        sequence: 11,
        kind: "model_update",
        source: "model",
        title: "针路簿更新",
        detail: "我会先核对工作区，再运行测试。",
        verifiable: false,
        instruction_authorized: false,
        created_at: "2026-07-30T01:00:00Z",
      },
      {
        id: "event-12",
        sequence: 12,
        kind: "model_call",
        source: "harness",
        title: "模型响应完成",
        detail: "mock / deterministic · 120 tokens",
        status: "completed",
        verifiable: true,
        instruction_authorized: false,
        created_at: "2026-07-30T01:00:01Z",
      },
    ],
  };
}

function publicSnapshot(): PublicModelStreamSnapshot {
  return {
    version: "model_public_stream.v3",
    revision: 3,
    response_id: "response-live",
    event_sequence: 5,
    items: [],
    content_kind: "tool_commentary",
    provisional: true,
    text: "正在检查差异，下一步运行测试。",
    message_complete: false,
    updated_at: "2026-07-30T01:00:02Z",
    call: {
      run_id: "run-1",
      session_id: "session-1",
      attempt_id: "attempt-live",
      model_attempt: 2,
      max_attempts: 3,
      protocol_repair: 0,
      tool_round: 3,
      provider: "deepseek",
      model: "deepseek-v4-flash",
      transport_attempt: 1,
      started_at: "2026-07-30T01:00:02Z",
      stream_chunks: 2,
      stream_bytes: 48,
      cancel_requested: false,
    },
  };
}

function toolItem(sequence: number, title: string, status: "running" | "completed") {
  return {
    id: `event-${sequence}`,
    sequence,
    kind: "tool_call" as const,
    source: "harness" as const,
    title,
    detail: "git diff --check",
    status,
    verifiable: true,
    instruction_authorized: false,
    created_at: `2026-07-30T01:00:${String(sequence).padStart(2, "0")}Z`,
  };
}

function toolLifecycleItem(sequence: number, title: string, detail: string,
  status: "running" | "completed") {
  return {
    ...toolItem(sequence, title, status),
    detail,
  };
}
