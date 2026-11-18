import { useRef, useState } from "react";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../../lib/locale";
import type { NarrativeEntry } from "../projection/narrative";
import { V2NavigatorRail, extractNavigatorStops } from "./navigator-rail";

const sampleEntries: NarrativeEntry[] = [
  {
    id: "u-1",
    kind: "user",
    text: "帮我重构认证中间件并梳理登录计划",
    createdAt: "2026-11-17T09:05:00Z",
    provisional: false,
  },
  {
    id: "promoted-cancelled",
    kind: "user",
    text: "Duplicate queued copy",
    status: "cancelled",
    promotedToMessageID: "u-2",
    createdAt: "2026-11-17T09:05:10Z",
    provisional: false,
  },
  {
    id: "plan-1",
    kind: "activity",
    activity: "plan",
    title: "生成交付计划",
    detail: "拆分为 Token 校验与 Session 续期两阶段",
    status: "completed",
    createdAt: "2026-11-17T09:06:00Z",
    runId: "run-1",
    count: 1,
    provisional: false,
    items: [{
      title: "生成交付计划",
      detail: "拆分为 Token 校验与 Session 续期两阶段",
      status: "completed",
      provisional: false,
      detailAvailable: false,
    }],
  },
  {
    id: "a-1",
    kind: "assistant",
    text: "计划已确认，接下来开始更新 `auth.ts` 的校验逻辑。",
    createdAt: "2026-11-17T09:07:00Z",
    provisional: false,
  },
  {
    id: "u-2",
    kind: "user",
    text: "运行集成测试并修复超时问题",
    createdAt: "2026-11-17T09:10:00Z",
    provisional: false,
  },
  {
    id: "err-tool-2",
    kind: "activity",
    activity: "execute",
    title: "运行命令",
    detail: "go test ./internal/auth — FAIL TestRefreshExpiredToken",
    status: "failed",
    createdAt: "2026-11-17T09:11:00Z",
    runId: "run-1",
    count: 1,
    provisional: false,
    items: [{
      title: "go test ./internal/auth",
      detail: "FAIL TestRefreshExpiredToken",
      status: "failed",
      provisional: false,
      detailAvailable: false,
    }],
  },
  {
    id: "chk-edit-2",
    kind: "activity",
    activity: "edit",
    title: "修改已应用",
    detail: "internal/auth/token.go",
    status: "applied",
    createdAt: "2026-11-17T09:12:00Z",
    runId: "run-1",
    count: 1,
    provisional: false,
    items: [{
      title: "修改已应用",
      detail: "internal/auth/token.go",
      status: "applied",
      fileChange: "applied",
      provisional: false,
      detailAvailable: false,
    }],
  },
  {
    id: "a-2",
    kind: "assistant",
    text: "已修复时钟偏移导致的刷新失败，所有集成测试均已通过。",
    createdAt: "2026-11-17T09:13:00Z",
    provisional: false,
  },
  {
    id: "u-3",
    kind: "user",
    text: "补充边界用例并生成最终变更摘要",
    createdAt: "2026-11-17T09:18:00Z",
    provisional: false,
  },
  {
    id: "a-3",
    kind: "assistant",
    text: "已新增 4 组边界单测，变更摘要已整理完毕。",
    createdAt: "2026-11-17T09:19:00Z",
    provisional: false,
  },
];

function NavigatorFixture({
  entries = sampleEntries,
  onJumpToEntry,
}: {
  entries?: NarrativeEntry[];
  onJumpToEntry?: (entryKey: string) => void;
}) {
  const scopeRef = useRef<HTMLDivElement>(null);
  const scrollRef = useRef<HTMLDivElement>(null);

  return (
    <div className="v2-conversation-main-column" ref={scopeRef} tabIndex={-1}>
      <div className="v2-conversation-viewport">
        <div
          className="v2-conversation-scroll"
          data-testid="conv-scroller"
          ref={(node) => {
            scrollRef.current = node;
            if (!node) return;
            let top = 1200;
            Object.defineProperties(node, {
              scrollHeight: { configurable: true, value: 1600 },
              clientHeight: { configurable: true, value: 400 },
              scrollTop: {
                configurable: true,
                get: () => top,
                set: (value: number) => { top = Math.max(0, Math.min(1200, value)); },
              },
            });
            node.getBoundingClientRect = () => ({
              top: 0,
              bottom: 400,
              left: 0,
              right: 720,
              width: 720,
              height: 400,
              x: 0,
              y: 0,
              toJSON: () => ({}),
            });
          }}
        >
          <ol className="v2-narrative v2-conv-entries">
            {entries.map((entry, idx) => (
              <li
                data-conv-entry-key={entry.id}
                key={entry.id}
                ref={(row) => {
                  if (!row) return;
                  row.getBoundingClientRect = () => {
                    const scrollerTop = scrollRef.current?.scrollTop ?? 0;
                    const rowTop = idx * 160 - scrollerTop;
                    return {
                      top: rowTop,
                      bottom: rowTop + 140,
                      left: 0,
                      right: 680,
                      width: 680,
                      height: 140,
                      x: 0,
                      y: rowTop,
                      toJSON: () => ({}),
                    };
                  };
                }}
              >
                {entry.id}
              </li>
            ))}
          </ol>
        </div>
        <V2NavigatorRail
          entries={entries}
          onJumpToEntry={(key) => onJumpToEntry?.(key)}
          scopeRef={scopeRef}
          scrollRef={scrollRef}
        />
      </div>
    </div>
  );
}

describe("V2NavigatorRail", () => {
  beforeEach(() => {
    window.localStorage.setItem("universal-code.locale", "zh-CN");
  });

  afterEach(() => {
    cleanup();
    window.localStorage.clear();
  });

  it("extracts numbered user prompt turns, checkpoints/phases, and tool/runtime errors cleanly", () => {
    const stops = extractNavigatorStops(sampleEntries);
    expect(stops.map((stop) => ({
      entryKey: stop.entryKey,
      kind: stop.kind,
      turnNumber: stop.turnNumber,
      tagLabel: stop.tagLabel,
    }))).toEqual([
      { entryKey: "u-1", kind: "turn", turnNumber: 1, tagLabel: "对话回合" },
      { entryKey: "plan-1", kind: "checkpoint", turnNumber: 1, tagLabel: "阶段 · 计划" },
      { entryKey: "u-2", kind: "turn", turnNumber: 2, tagLabel: "对话回合" },
      { entryKey: "err-tool-2", kind: "error", turnNumber: 2, tagLabel: "工具失败" },
      { entryKey: "chk-edit-2", kind: "checkpoint", turnNumber: 2, tagLabel: "检查点 · 变更" },
      { entryKey: "u-3", kind: "turn", turnNumber: 3, tagLabel: "对话回合" },
    ]);

    expect(stops[0]?.promptPreview).toContain("帮我重构认证中间件");
    expect(stops[0]?.outcomePreview).toContain("计划已确认，接下来开始更新 auth.ts 的校验逻辑。");
  });

  it("renders a floating preview card on hover/focus with #N, tag, timestamp, prompt+outcome, and shortcut hint", () => {
    render(
      <LocaleProvider>
        <NavigatorFixture />
      </LocaleProvider>,
    );

    const nav = screen.getByRole("navigation", { name: "对话快速导航" });
    const firstTurnStop = within(nav).getByRole("button", { name: /#1 对话回合/u });

    fireEvent.mouseEnter(firstTurnStop);

    const preview = screen.getByTestId("navigator-preview-card");
    expect(preview).toBeVisible();
    expect(within(preview).getByText("#1")).toBeInTheDocument();
    expect(within(preview).getByText("对话回合")).toBeInTheDocument();
    expect(preview.querySelector("time")).toHaveAttribute("datetime", "2026-11-17T09:05:00.000Z");
    expect(within(preview).getByText(/帮我重构认证中间件/u)).toBeInTheDocument();
    expect(within(preview).getByText(/计划已确认，接下来开始更新 auth\.ts/u)).toBeInTheDocument();
    expect(within(preview).getByText("Alt+↑ / Alt+↓")).toBeInTheDocument();

    // Pressing Escape dismisses the preview card cleanly.
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByTestId("navigator-preview-card")).not.toBeInTheDocument();
  });

  it("jumps directly to the clicked stop entry, updates active state, and triggers a pulse highlight", async () => {
    const onJump = vi.fn();
    const user = userEvent.setup();
    const { container } = render(
      <LocaleProvider>
        <NavigatorFixture onJumpToEntry={onJump} />
      </LocaleProvider>,
    );

    const nav = screen.getByRole("navigation", { name: "对话快速导航" });
    const errorStop = within(nav).getByRole("button", { name: /#2 工具失败/u });

    await user.click(errorStop);

    expect(onJump).toHaveBeenCalledWith("err-tool-2");
    expect(errorStop).toHaveAttribute("aria-current", "true");

    const targetRow = container.querySelector('[data-conv-entry-key="err-tool-2"]')!;
    expect(targetRow).toHaveClass("is-nav-pulse");
    expect(targetRow).toHaveAttribute("data-nav-highlighted", "true");
  });

  it("filters stops by All Turns, Checkpoints & Phases, Errors & Tool Failures, and search query", async () => {
    const onJump = vi.fn();
    const user = userEvent.setup();
    render(
      <LocaleProvider>
        <NavigatorFixture onJumpToEntry={onJump} />
      </LocaleProvider>,
    );

    await user.click(screen.getByRole("button", { name: "快速导航列表" }));
    const dialog = screen.getByRole("dialog", { name: "对话快速跳转" });
    expect(dialog).toBeVisible();

    expect(within(dialog).getAllByRole("listitem")).toHaveLength(6);

    // Filter to Checkpoints & Phases (plan-1 + chk-edit-2).
    await user.click(within(dialog).getByRole("button", { name: /检查点与阶段/u }));
    expect(within(dialog).getAllByRole("listitem")).toHaveLength(2);
    expect(within(dialog).getByText("阶段 · 计划")).toBeInTheDocument();
    expect(within(dialog).getByText("检查点 · 变更")).toBeInTheDocument();

    // Filter to Errors & Tool Failures (err-tool-2).
    await user.click(within(dialog).getByRole("button", { name: /错误与工具失败/u }));
    expect(within(dialog).getAllByRole("listitem")).toHaveLength(1);
    expect(within(dialog).getByText(/FAIL TestRefreshExpiredToken/u)).toBeInTheDocument();

    // Switch back to All Turns and filter via search input.
    await user.click(within(dialog).getByRole("button", { name: /全部回合/u }));
    const searchInput = within(dialog).getByRole("searchbox", { name: "搜索对话节点" });
    await user.type(searchInput, "边界用例");
    expect(within(dialog).getAllByRole("listitem")).toHaveLength(1);

    await user.click(within(dialog).getByRole("button", { name: /#3/u }));
    expect(onJump).toHaveBeenCalledWith("u-3");
    expect(screen.queryByRole("dialog", { name: "对话快速跳转" })).not.toBeInTheDocument();
  });

  it("supports Alt+ArrowUp and Alt+ArrowDown turn navigation while ignoring conflicting modifiers", () => {
    const onJump = vi.fn();
    render(
      <LocaleProvider>
        <NavigatorFixture onJumpToEntry={onJump} />
      </LocaleProvider>,
    );

    const nav = screen.getByRole("navigation", { name: "对话快速导航" });
    const turn1 = within(nav).getByRole("button", { name: /#1 对话回合/u });
    const turn2 = within(nav).getByRole("button", { name: /#2 对话回合/u });
    const turn3 = within(nav).getByRole("button", { name: /#3 对话回合/u });

    // Initial scroll is at bottom -> turn #3 is active.
    expect(turn3).toHaveAttribute("aria-current", "true");

    // Conflicting modifiers (Ctrl+Alt+ArrowUp or Shift+Alt+ArrowUp) must be ignored.
    fireEvent.keyDown(window, { key: "ArrowUp", altKey: true, ctrlKey: true });
    fireEvent.keyDown(window, { key: "ArrowUp", altKey: true, shiftKey: true });
    expect(onJump).not.toHaveBeenCalled();
    expect(turn3).toHaveAttribute("aria-current", "true");

    // Alt+ArrowUp jumps from #3 -> #2, then #2 -> #1.
    fireEvent.keyDown(window, { key: "ArrowUp", altKey: true });
    expect(onJump).toHaveBeenLastCalledWith("u-2");
    expect(turn2).toHaveAttribute("aria-current", "true");

    fireEvent.keyDown(window, { key: "ArrowUp", altKey: true });
    expect(onJump).toHaveBeenLastCalledWith("u-1");
    expect(turn1).toHaveAttribute("aria-current", "true");

    // Alt+ArrowDown jumps from #1 -> #2.
    fireEvent.keyDown(window, { key: "ArrowDown", altKey: true });
    expect(onJump).toHaveBeenLastCalledWith("u-2");
    expect(turn2).toHaveAttribute("aria-current", "true");
  });

  it("supports keyboard focus preview, roving arrow keys on rail ticks, English localization, and modal-dialog shortcut guard", async () => {
    window.localStorage.setItem("prayu.locale.v1", "en-US");
    const onJump = vi.fn();
    const user = userEvent.setup();

    render(
      <LocaleProvider>
        <div>
          <NavigatorFixture onJumpToEntry={onJump} />
          <div aria-label="Blocking modal" aria-modal="true" role="dialog" />
        </div>
      </LocaleProvider>,
    );

    const nav = screen.getByRole("navigation", { name: "Conversation quick navigation" });
    const activeTurn = within(nav).getByRole("button", { name: /#3 Prompt turn/u });

    // Focusing a rail tick opens the preview card for keyboard-only users.
    fireEvent.focus(activeTurn);
    expect(screen.getByTestId("navigator-preview-card")).toHaveTextContent("#3");

    // ArrowUp inside the rail moves focus to the preceding stop (#2 Checkpoint · Changes).
    fireEvent.keyDown(activeTurn, { key: "ArrowUp" });
    const prevStop = within(nav).getByRole("button", { name: /#2 Checkpoint · Changes/u });
    expect(prevStop).toHaveFocus();

    // While an aria-modal="true" dialog is mounted, Alt+ArrowUp does not hijack shortcuts.
    fireEvent.keyDown(window, { key: "ArrowUp", altKey: true });
    expect(onJump).not.toHaveBeenCalled();

    // Quick navigation list renders English labels and empty-search message.
    await user.click(screen.getByRole("button", { name: "Quick navigation list" }));
    const dialog = screen.getByRole("dialog", { name: "Conversation quick jump" });
    expect(within(dialog).getByRole("button", { name: /All Turns/u })).toHaveAttribute("aria-pressed", "true");
    expect(within(dialog).getByRole("button", { name: /Checkpoints & Phases/u })).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: /Errors & Tool Failures/u })).toBeInTheDocument();

    await user.type(within(dialog).getByRole("searchbox", { name: "Search conversation stops" }), "nonexistent-xyz-query");
    expect(within(dialog).getByText("No matching navigation stops")).toBeVisible();
  });

  it("does not hijack Alt+ArrowUp / Alt+ArrowDown inside textarea, input, select, or ProseMirror contenteditable", () => {
    const onJump = vi.fn();
    render(
      <LocaleProvider>
        <div>
          <NavigatorFixture onJumpToEntry={onJump} />
        </div>
      </LocaleProvider>,
    );

    const scope = document.querySelector(".v2-conversation-main-column")!;
    const textarea = document.createElement("textarea");
    const input = document.createElement("input");
    const proseMirror = document.createElement("div");
    proseMirror.className = "ProseMirror";
    proseMirror.setAttribute("contenteditable", "true");
    proseMirror.tabIndex = 0;
    scope.append(textarea, input, proseMirror);

    for (const control of [textarea, input, proseMirror]) {
      control.focus();
      const upEvent = new KeyboardEvent("keydown", { key: "ArrowUp", altKey: true, bubbles: true, cancelable: true });
      control.dispatchEvent(upEvent);
      expect(upEvent.defaultPrevented).toBe(false);

      const downEvent = new KeyboardEvent("keydown", { key: "ArrowDown", altKey: true, bubbles: true, cancelable: true });
      control.dispatchEvent(downEvent);
      expect(downEvent.defaultPrevented).toBe(false);
    }
    expect(onJump).not.toHaveBeenCalled();
  });

  it("skips hidden collapsed process stops in passive scroll-spy and auto-expands collapsed NarrativeTaskRow on jump", async () => {
    const onJump = vi.fn();
    const user = userEvent.setup();

    function CollapsedTaskFixture() {
      const scopeRef = useRef<HTMLDivElement>(null);
      const scrollRef = useRef<HTMLDivElement>(null);
      const [expanded, setExpanded] = useState(false);

      const taskEntries: NarrativeEntry[] = [
        {
          id: "u-1",
          kind: "user",
          text: "执行部署前安全检查",
          createdAt: "2026-11-17T10:00:00Z",
          provisional: false,
        },
        {
          id: "err-collapsed-1",
          kind: "activity",
          activity: "execute",
          title: "运行安全检查",
          detail: "FAIL secret check",
          status: "failed",
          createdAt: "2026-11-17T10:01:00Z",
          runId: "run-1",
          count: 1,
          provisional: false,
          items: [{ title: "运行安全检查", detail: "FAIL secret check", status: "failed", provisional: false, detailAvailable: false }],
        },
        {
          id: "a-final-1",
          kind: "assistant",
          text: "已完成修复并复测通过。",
          createdAt: "2026-11-17T10:02:00Z",
          provisional: false,
        },
      ];

      return (
        <div className="v2-conversation-main-column" ref={scopeRef} tabIndex={-1}>
          <div className="v2-conversation-viewport">
            <div
              className="v2-conversation-scroll"
              ref={(node) => {
                scrollRef.current = node;
                if (!node) return;
                let top = 0;
                Object.defineProperties(node, {
                  scrollHeight: { configurable: true, value: 800 },
                  clientHeight: { configurable: true, value: 400 },
                  scrollTop: {
                    configurable: true,
                    get: () => top,
                    set: (v: number) => { top = Math.max(0, Math.min(400, v)); },
                  },
                });
                node.getBoundingClientRect = () => ({
                  top: 0, bottom: 400, left: 0, right: 700, width: 700, height: 400, x: 0, y: 0, toJSON: () => ({}),
                });
              }}
            >
              <ol className="v2-narrative v2-conv-entries">
                <li className="v2-task-turn" data-conv-entry-key="task:t1" data-task-id="t1" data-task-state="completed">
                  <ol className="v2-task-inputs">
                    <li
                      className="v2-user-turn"
                      data-conv-entry-key="u-1"
                      ref={(row) => {
                        if (!row) return;
                        row.getBoundingClientRect = () => ({
                          top: 10 - (scrollRef.current?.scrollTop ?? 0),
                          bottom: 90 - (scrollRef.current?.scrollTop ?? 0),
                          left: 0, right: 660, width: 660, height: 80, x: 0, y: 10, toJSON: () => ({}),
                        });
                      }}
                    >
                      u-1
                    </li>
                  </ol>
                  <div className="v2-task-process-heading">
                    <button
                      aria-expanded={expanded}
                      onClick={() => setExpanded((prev) => !prev)}
                      type="button"
                    >
                      查看过程
                    </button>
                  </div>
                  <ol className="v2-task-entries">
                    <li
                      className="v2-activity-turn"
                      data-conv-entry-key="err-collapsed-1"
                      hidden={!expanded}
                      ref={(row) => {
                        if (!row) return;
                        row.getBoundingClientRect = () => ({
                          top: 220 - (scrollRef.current?.scrollTop ?? 0),
                          bottom: 320 - (scrollRef.current?.scrollTop ?? 0),
                          left: 0, right: 660, width: 660, height: 100, x: 0, y: 220, toJSON: () => ({}),
                        });
                      }}
                    >
                      err-collapsed-1
                    </li>
                  </ol>
                </li>
              </ol>
            </div>
            <V2NavigatorRail
              entries={taskEntries}
              onJumpToEntry={(key) => onJump(key)}
              scopeRef={scopeRef}
              scrollRef={scrollRef}
            />
          </div>
        </div>
      );
    }

    const { container } = render(
      <LocaleProvider>
        <CollapsedTaskFixture />
      </LocaleProvider>,
    );

    const nav = screen.getByRole("navigation", { name: "对话快速导航" });
    const turnStop = within(nav).getByRole("button", { name: /#1 对话回合/u });
    const errorStop = within(nav).getByRole("button", { name: /#1 工具失败/u });

    // While process entries are collapsed (hidden), passive scroll-spy highlights the visible turn (#1 对话回合), NOT the hidden error stop.
    expect(turnStop).toHaveAttribute("aria-current", "true");
    expect(errorStop).not.toHaveAttribute("aria-current");

    const hiddenRow = container.querySelector('[data-conv-entry-key="err-collapsed-1"]')!;
    expect(hiddenRow).toHaveAttribute("hidden");

    // Clicking the error stop on the rail auto-expands the collapsed task process section, scrolls to the revealed row, and pulses it!
    await user.click(errorStop);
    expect(onJump).toHaveBeenCalledWith("err-collapsed-1");
    expect(hiddenRow).not.toHaveAttribute("hidden");
    expect(hiddenRow).toHaveClass("is-nav-pulse");
    expect(errorStop).toHaveAttribute("aria-current", "true");
  });
});
