import type { ReactElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { APIClient } from "../../api/client";
import type {
  PageResult,
  ThreadDetailView,
  ThreadExecutionView,
  ThreadTranscriptItemView,
  WorkspaceView,
} from "../../api/types";
import { LocaleProvider } from "../../lib/locale";
import type { NarrativeEntry } from "../projection/narrative";
import { V2Conversation } from "./conversation";
import { V2Settings } from "./settings";
import { V2SettingsSidebar } from "./sidebar";
import {
  readVoicePreferences,
  summarizeForVoice,
  V2VoiceCallStage,
  voicePreferencesStorageKey,
} from "./voice-call-mode";

vi.mock("../../hooks/use-run-event-stream", () => ({
  useRunEventStream: () => ({ error: null, frames: [], status: "live" }),
}));

vi.mock("../../hooks/use-public-model-stream", () => ({
  usePublicModelStream: () => ({ error: null, snapshot: null, status: "waiting" }),
}));

function render(ui: ReactElement) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  return renderComponent(<LocaleProvider>{ui}</LocaleProvider>);
}

interface MockUtterance {
  text: string;
  rate: number;
  lang: string;
  onstart: (() => void) | null;
  onend: (() => void) | null;
  onerror: (() => void) | null;
}

function installSpeechMocks(options: { withRecognition?: boolean } = { withRecognition: true }) {
  const spoken: MockUtterance[] = [];
  const synth = {
    speak: vi.fn((utterance: MockUtterance) => {
      spoken.push(utterance);
      utterance.onstart?.();
    }),
    cancel: vi.fn(),
    pause: vi.fn(),
    resume: vi.fn(),
    speaking: false,
    pending: false,
    paused: false,
  };

  class FakeUtterance implements MockUtterance {
    text: string;
    rate = 1;
    lang = "zh-CN";
    onstart: (() => void) | null = null;
    onend: (() => void) | null = null;
    onerror: (() => void) | null = null;
    constructor(text: string) {
      this.text = text;
    }
  }

  vi.stubGlobal("SpeechSynthesisUtterance", FakeUtterance);
  Object.defineProperty(window, "speechSynthesis", {
    configurable: true,
    writable: true,
    value: synth,
  });

  const recognitionInstances: Array<{
    lang: string;
    start: ReturnType<typeof vi.fn>;
    stop: ReturnType<typeof vi.fn>;
    abort: ReturnType<typeof vi.fn>;
    emitFinal: (transcript: string) => void;
  }> = [];

  if (options.withRecognition) {
    class FakeSpeechRecognition {
      lang = "zh-CN";
      continuous = false;
      interimResults = true;
      onstart: (() => void) | null = null;
      onend: (() => void) | null = null;
      onerror: (() => void) | null = null;
      onresult: ((event: unknown) => void) | null = null;
      start = vi.fn(() => {
        this.onstart?.();
      });
      stop = vi.fn(() => {
        this.onend?.();
      });
      abort = vi.fn(() => {
        this.onend?.();
      });
      constructor() {
        recognitionInstances.push({
          lang: this.lang,
          start: this.start,
          stop: this.stop,
          abort: this.abort,
          emitFinal: (transcript: string) => {
            this.onresult?.({
              resultIndex: 0,
              results: [{ isFinal: true, 0: { transcript }, length: 1 }],
            });
          },
        });
      }
    }
    Object.defineProperty(window, "SpeechRecognition", {
      configurable: true,
      writable: true,
      value: FakeSpeechRecognition,
    });
  } else {
    Reflect.deleteProperty(window as unknown as Record<string, unknown>, "SpeechRecognition");
    Reflect.deleteProperty(window as unknown as Record<string, unknown>, "webkitSpeechRecognition");
  }

  return { synth, spoken, recognitionInstances };
}

beforeEach(() => {
  window.localStorage.removeItem(voicePreferencesStorageKey);
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
});

afterEach(() => {
  cleanup();
  window.localStorage.removeItem(voicePreferencesStorageKey);
  Reflect.deleteProperty(window as unknown as Record<string, unknown>, "speechSynthesis");
  Reflect.deleteProperty(window as unknown as Record<string, unknown>, "SpeechRecognition");
  Reflect.deleteProperty(window as unknown as Record<string, unknown>, "webkitSpeechRecognition");
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("summarizeForVoice", () => {
  it("strips fenced code blocks, raw git diffs, file path noise, and markdown into 1–2 concise sentences", () => {
    const verboseReply = [
      "### 登录校验修复完成",
      "",
      "我已经更新了 `web/src/v2/components/login-form.tsx#L14-L52` 的邮箱必填校验逻辑，并同步补充了 [auth.test.ts](file:///C:/repo/web/src/v2/auth.test.ts#L8) 单元测试。具体改动如下：",
      "",
      "```tsx",
      "export function validateEmail(input: string) {",
      "  return /^[^@]+@[^@]+$/.test(input.trim());",
      "}",
      "```",
      "",
      "diff --git a/web/src/v2/components/login-form.tsx b/web/src/v2/components/login-form.tsx",
      "--- a/web/src/v2/components/login-form.tsx",
      "+++ b/web/src/v2/components/login-form.tsx",
      "@@ -14,3 +14,5 @@",
      "+if (!validateEmail(email)) return false;",
      "",
      "所有单元测试与类型检查均已顺利通过。接下来如果您需要，我还可以继续接入验证码限流策略和审计日志。",
    ].join("\n");

    const summary = summarizeForVoice(verboseReply, "completed", "zh-CN");

    expect(summary).toContain("login-form.tsx");
    expect(summary).toContain("auth.test.ts");
    expect(summary).not.toContain("```");
    expect(summary).not.toContain("validateEmail");
    expect(summary).not.toContain("diff --git");
    expect(summary).not.toContain("web/src/v2/components/");
    expect(summary).not.toContain("file:///");
    // Keeps at most 2 concise sentences (omits 3rd sentence)
    expect(summary).not.toContain("验证码限流策略");
  });

  it("provides clean spoken fallbacks for code-only replies and status-only progress", () => {
    const codeOnly = "```ts\nconst answer = 42;\n```";
    expect(summarizeForVoice(codeOnly, "completed", "zh-CN")).toBe("已完成代码变更整理，可在完整对话记录中查看细节。");
    expect(summarizeForVoice("", "running", "zh-CN")).toBe("正在执行当前任务，请稍候。");
    expect(summarizeForVoice("", "running", "en-US")).toBe("Working on your request now.");
  });
});

describe("V2VoiceCallStage", () => {
  it("displays ONLY the current message summary while hiding historical clutter, speaks automatically, and mutes immediately for text-only reading", async () => {
    const { synth, spoken } = installSpeechMocks({ withRecognition: false });
    const user = userEvent.setup();

    const initialEntries: NarrativeEntry[] = [
      {
        id: "msg-old-user",
        kind: "user",
        text: "早期历史提问：帮我排查旧接口超时原因",
        createdAt: "2026-11-18T00:00:01Z",
        provisional: false,
      },
      {
        id: "msg-old-assistant",
        kind: "assistant",
        text: "早期历史回复：已经定位到连接池耗尽问题并修复。",
        createdAt: "2026-11-18T00:00:05Z",
        provisional: false,
      },
      {
        id: "msg-current-assistant",
        kind: "assistant",
        text: "当前进展：已完成表单提交流程验证，所有断言均已通过。\n```ts\nexpect(ok).toBe(true);\n```",
        createdAt: "2026-11-18T00:01:00Z",
        provisional: false,
      },
    ];

    function StageHarness() {
      const [entries, setEntries] = useState<NarrativeEntry[]>(initialEntries);
      return (
        <div>
          <button
            onClick={() =>
              setEntries((prev) => [
                ...prev,
                {
                  id: "msg-next-assistant",
                  kind: "assistant",
                  text: "最新步骤：已生成合并请求并准备好供您审阅。",
                  createdAt: "2026-11-18T00:02:00Z",
                  provisional: false,
                },
              ])
            }
            type="button"
          >
            Push next assistant update
          </button>
          <V2VoiceCallStage
            entries={entries}
            onEndCall={vi.fn()}
            onSubmitPrompt={vi.fn()}
          />
        </div>
      );
    }

    render(<StageHarness />);

    // Historical messages and raw code blocks are NOT displayed on the minimal call stage
    expect(screen.queryByText(/早期历史提问/)).not.toBeInTheDocument();
    expect(screen.queryByText(/早期历史回复/)).not.toBeInTheDocument();
    expect(screen.queryByText(/expect\(ok\)/)).not.toBeInTheDocument();

    // Only the current active concise summary is displayed and spoken via speechSynthesis
    expect(screen.getByTestId("v2-voice-current-message")).toHaveTextContent(
      "当前进展：已完成表单提交流程验证，所有断言均已通过。",
    );
    expect(synth.speak).toHaveBeenCalledTimes(1);
    expect(spoken[0]?.text).toBe("当前进展：已完成表单提交流程验证，所有断言均已通过。");

    // Mute voice anytime to read text only
    synth.cancel.mockClear();
    const muteBtn = screen.getByRole("button", { name: "静音语音（仅看文字）" });
    expect(muteBtn).toHaveAttribute("aria-pressed", "false");
    await user.click(muteBtn);

    expect(muteBtn).toHaveAttribute("aria-pressed", "true");
    expect(synth.cancel).toHaveBeenCalledOnce();
    expect(screen.getByText("已静音语音 · 仅阅读实时文字")).toBeInTheDocument();

    // When a new assistant reply arrives while muted, TTS stays silent while text updates live!
    await user.click(screen.getByRole("button", { name: "Push next assistant update" }));
    expect(screen.getByTestId("v2-voice-current-message")).toHaveTextContent(
      "最新步骤：已生成合并请求并准备好供您审阅。",
    );
    expect(synth.speak).toHaveBeenCalledTimes(1);
  });

  it("captures speech recognition results and sends prompts, and supports text fallback input", async () => {
    const { recognitionInstances } = installSpeechMocks({ withRecognition: true });
    const onSubmitPrompt = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();

    render(
      <V2VoiceCallStage
        entries={[]}
        onEndCall={vi.fn()}
        onSubmitPrompt={onSubmitPrompt}
      />,
    );

    const micBtn = screen.getByRole("button", { name: "麦克风倾听" });
    await user.click(micBtn);
    expect(micBtn).toHaveAttribute("aria-pressed", "true");
    expect(recognitionInstances).toHaveLength(1);
    expect(recognitionInstances[0]!.start).toHaveBeenCalledOnce();

    act(() => {
      recognitionInstances[0]!.emitFinal("请帮我运行前端单元测试");
    });
    await waitFor(() => expect(onSubmitPrompt).toHaveBeenCalledWith("请帮我运行前端单元测试"));

    // Quick text fallback input on the call stage also submits cleanly
    const textInput = screen.getByLabelText("通话中补充文字指令");
    await user.type(textInput, "补充检查边界条件");
    await user.click(screen.getByRole("button", { name: "发送通话文字指令" }));
    await waitFor(() => expect(onSubmitPrompt).toHaveBeenCalledWith("补充检查边界条件"));
  });
});

describe("V2Conversation Voice Call lifecycle & Full History restoration", () => {
  it("transitions from Live Voice Call back to full conversation history with post-call banner on End Call", async () => {
    const { synth } = installSpeechMocks({ withRecognition: true });
    const user = userEvent.setup();

    const transcriptItems: ThreadTranscriptItemView[] = [
      {
        version: "thread_transcript.v1",
        id: "u-1",
        canonical_id: "u-1",
        run_id: "run-voice",
        run_ordinal: 1,
        sequence: 1,
        activity_type: "message",
        stage: "result",
        kind: "operator_input",
        source: "operator",
        title: "用户消息",
        detail: "第一轮提问：检查路由配置",
        durable: true,
        provisional: false,
        verifiable: true,
        instruction_authorized: true,
        created_at: "2026-11-18T01:00:01Z",
      },
      {
        version: "thread_transcript.v1",
        id: "a-1",
        canonical_id: "a-1",
        run_id: "run-voice",
        run_ordinal: 1,
        sequence: 2,
        activity_type: "message",
        stage: "result",
        kind: "model_update",
        source: "model",
        title: "Universal-Code",
        detail: "第一轮完整回复：路由配置检查完毕，未发现冲突。",
        durable: true,
        provisional: false,
        verifiable: true,
        instruction_authorized: false,
        created_at: "2026-11-18T01:00:05Z",
      },
      {
        version: "thread_transcript.v1",
        id: "a-2",
        canonical_id: "a-2",
        run_id: "run-voice",
        run_ordinal: 1,
        sequence: 3,
        activity_type: "message",
        stage: "result",
        kind: "model_update",
        source: "model",
        title: "Universal-Code",
        detail: "最新执行进展：所有集成测试已通过，随时可以提交。\n```bash\nnpm test\n```",
        durable: true,
        provisional: false,
        verifiable: true,
        instruction_authorized: false,
        created_at: "2026-11-18T01:01:00Z",
      },
    ];

    const detail: ThreadDetailView = {
      thread: {
        id: "thread-voice",
        title: "语音协同会话",
        status: "active",
        workspace_id: "workspace-1",
        composer_state: "ready",
      },
      last_run: { id: "run-voice", status: "completed" },
      runs: [],
      mission: {},
    } as unknown as ThreadDetailView;

    const client = {
      baseURL: "/api/v1",
      hasControl: true,
      hasThreadControl: true,
      hasRunExecution: true,
      hasThreadExecutionRead: true,
      get: vi.fn().mockResolvedValue(detail),
      getPage: vi.fn().mockResolvedValue({
        items: transcriptItems,
        page: { limit: 100 },
        requestID: "req-voice",
      } satisfies PageResult<ThreadTranscriptItemView>),
      threadExecution: vi.fn().mockResolvedValue({
        version: "thread_execution.v1",
        thread_id: "thread-voice",
        state: "idle",
        queued_messages: 0,
        capability_grant: false,
      } satisfies ThreadExecutionView),
      availableModelRoutes: vi.fn().mockResolvedValue({ items: [] }),
    } as unknown as APIClient;

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <V2Conversation
          client={client}
          threadID="thread-voice"
          workspaces={[{ id: "workspace-1", name: "Workspace 1" } as WorkspaceView]}
          onArchive={vi.fn()}
          onManageModels={vi.fn()}
        />
      </QueryClientProvider>,
    );

    // Initially shows full transcript
    expect(await screen.findByText("第一轮提问：检查路由配置")).toBeInTheDocument();
    expect(screen.getByText("第一轮完整回复：路由配置检查完毕，未发现冲突。")).toBeInTheDocument();

    // Enter Live Voice Mode
    const voiceBtn = screen.getByRole("button", { name: "语音通话" });
    await user.click(voiceBtn);
    expect(voiceBtn).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByTestId("v2-voice-call-stage")).toBeInTheDocument();

    // Historical clutter is hidden; only the current message summary is displayed
    expect(screen.queryByText("第一轮提问：检查路由配置")).not.toBeInTheDocument();
    expect(screen.queryByText("第一轮完整回复：路由配置检查完毕，未发现冲突。")).not.toBeInTheDocument();
    expect(screen.getByTestId("v2-voice-current-message")).toHaveTextContent(
      "最新执行进展：所有集成测试已通过，随时可以提交。",
    );

    // End Call & View Full History
    synth.cancel.mockClear();
    await user.click(screen.getByRole("button", { name: "结束通话（查看完整记录）" }));
    expect(synth.cancel).toHaveBeenCalled();

    // Full transcript and post-call banner are immediately visible
    expect(screen.queryByTestId("v2-voice-call-stage")).not.toBeInTheDocument();
    expect(screen.getByText("通话已结束 · 完整对话记录已展开")).toBeInTheDocument();
    expect(screen.getByText("第一轮提问：检查路由配置")).toBeInTheDocument();
    expect(screen.getByText("第一轮完整回复：路由配置检查完毕，未发现冲突。")).toBeInTheDocument();
    expect(screen.getByText(/npm test/)).toBeInTheDocument();

    // Dismiss post-call banner
    await user.click(screen.getByRole("button", { name: "关闭通话结束提示" }));
    expect(screen.queryByText("通话已结束 · 完整对话记录已展开")).not.toBeInTheDocument();
  });
});

describe("Voice Settings Panel (settings.tsx section='voice' & sidebar)", () => {
  it("exposes Voice Interaction in V2SettingsSidebar and persists voice settings in V2Settings", async () => {
    installSpeechMocks({ withRecognition: true });
    const user = userEvent.setup();
    const onSelect = vi.fn();

    const sidebarView = render(<V2SettingsSidebar section="voice" onBack={vi.fn()} onSelect={onSelect} />);
    const nav = screen.getByRole("navigation", { name: "设置分类" });
    const voiceNavBtn = within(nav).getByRole("button", { name: "语音交互" });
    expect(voiceNavBtn).toHaveAttribute("aria-current", "page");
    sidebarView.unmount();

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <V2Settings
          client={{} as APIClient}
          section="voice"
          threadID=""
          workspaces={[]}
          onSelectSection={vi.fn()}
          onOpenHistory={vi.fn()}
        />
      </QueryClientProvider>,
    );

    expect(screen.queryByText("设置项不可用。")).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: "语音交互" })).toBeInTheDocument();

    const autoSpeakSwitch = screen.getByRole("switch", { name: "自动播报进展摘要" });
    const defaultMuteSwitch = screen.getByRole("switch", { name: "默认静音语音（仅看文字）" });
    const autoListenSwitch = screen.getByRole("switch", { name: "回复播报后自动倾听" });
    const rateSlider = screen.getByRole("slider", { name: "语音播报语速" });

    expect(autoSpeakSwitch).toHaveAttribute("aria-checked", "true");
    expect(defaultMuteSwitch).toHaveAttribute("aria-checked", "false");
    expect(autoListenSwitch).toHaveAttribute("aria-checked", "true");
    expect(rateSlider).toHaveValue("1");

    await user.click(defaultMuteSwitch);
    await user.click(autoListenSwitch);
    fireEvent.change(rateSlider, { target: { value: "1.3" } });

    expect(defaultMuteSwitch).toHaveAttribute("aria-checked", "true");
    expect(autoListenSwitch).toHaveAttribute("aria-checked", "false");
    expect(screen.getByText("1.3x")).toBeInTheDocument();
    expect(readVoicePreferences()).toEqual({
      autoSpeakSummaries: true,
      speechRate: 1.3,
      defaultVoiceMuted: true,
      autoListenAfterReply: false,
    });

    await user.click(screen.getByRole("button", { name: "恢复默认" }));
    expect(readVoicePreferences()).toEqual({
      autoSpeakSummaries: true,
      speechRate: 1.0,
      defaultVoiceMuted: false,
      autoListenAfterReply: true,
    });
  });
});
