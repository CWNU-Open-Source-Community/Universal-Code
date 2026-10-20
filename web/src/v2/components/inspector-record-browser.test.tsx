import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render as renderComponent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import { InspectorRecordBrowser } from "./inspector-record-browser";

function mount(getPage: ReturnType<typeof vi.fn>) {
  const onOpen = vi.fn();
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <InspectorRecordBrowser client={{ getPage } as unknown as APIClient} onOpen={onOpen} />
  </QueryClientProvider>);
  return onOpen;
}
const record = (id: string) => ({ id, status: "completed", updated_at: "2026-09-10T01:00:00Z" });
const page = (items: unknown[], next_cursor = "") => ({ items, page: { limit: 50, next_cursor } });

describe("Inspector saved record browser", () => {
  it("presents a running Run as an unfinished execution record without guessing live activity or fetching each Run", async () => {
    const getPage = vi.fn().mockResolvedValue(page([{ ...record("run-open"), status: "running" }]));
    mount(getPage);
    await userEvent.setup().click(screen.getByRole("button", { name: "运行记录" }));
    const saved = await screen.findByRole("button", { name: "打开运行记录 run-open" });
    expect(saved).toHaveTextContent("未结束");
    expect(saved).not.toHaveTextContent("running");
    expect(screen.getByText(/选择历史执行或会话/)).toHaveTextContent("需要继续任务时，回到对应对话发送消息");
    expect(getPage).toHaveBeenCalledExactlyOnceWith("/runs", { limit: 50 }, "", expect.any(AbortSignal));
  });

  it("reads only the selected collection and preserves search while paging to an exact Run identity", async () => {
    const user = userEvent.setup();
    const getPage = vi.fn().mockResolvedValueOnce(page([record("run-new")], "older"))
      .mockResolvedValueOnce(page([record("run-new"), record("run-old")]));
    const onOpen = mount(getPage);
    expect(getPage).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "运行记录" }));
    await screen.findByRole("button", { name: "打开运行记录 run-new" });
    expect(getPage).toHaveBeenCalledWith("/runs", { limit: 50 }, "", expect.any(AbortSignal));
    await user.type(screen.getByRole("searchbox", { name: "搜索运行记录" }), "run-old");
    expect(screen.getByText("已加载记录中没有匹配项")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "加载更早运行记录" }));
    await user.click(await screen.findByRole("button", { name: "打开运行记录 run-old" }));
    expect(onOpen).toHaveBeenCalledExactlyOnceWith("run", "run-old");
    expect(getPage).toHaveBeenLastCalledWith("/runs", { limit: 50 }, "older", expect.any(AbortSignal));
    expect(getPage).toHaveBeenCalledTimes(2);
    expect(screen.getByText(/搜索 2 条已加载运行 ID/)).toHaveTextContent("不含消息正文");
    expect(screen.getByRole("searchbox")).toHaveValue("run-old");
  });

  it("opens the selected Session ID even when two saved sessions share a title", async () => {
    const user = userEvent.setup();
    const getPage = vi.fn().mockResolvedValue(page([
      { ...record("session-a"), title: "Same title", status: "active" },
      { ...record("session-b"), title: "Same title", status: "archived" },
    ]));
    const onOpen = mount(getPage);
    await user.click(screen.getByRole("button", { name: "会话记录" }));
    await user.click(await screen.findByRole("button", { name: "打开会话记录 session-b" }));
    expect(screen.getByRole("button", { name: "打开会话记录 session-a" })).toHaveTextContent("未关闭");
    expect(onOpen).toHaveBeenCalledExactlyOnceWith("session", "session-b");
    expect(getPage).toHaveBeenCalledExactlyOnceWith("/sessions", { limit: 50 }, "", expect.any(AbortSignal));
    expect(screen.getByText(/会话标题和 ID/)).toHaveTextContent("不含消息正文");
  });

  it("retains loaded records on older-page failure and retries the same cursor", async () => {
    const user = userEvent.setup();
    const getPage = vi.fn().mockResolvedValueOnce(page([record("run-kept")], "next-page"))
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce(page([record("run-next")]));
    const onOpen = mount(getPage);
    await user.click(screen.getByRole("button", { name: "运行记录" }));
    await screen.findByRole("button", { name: "打开运行记录 run-kept" });
    await user.click(screen.getByRole("button", { name: "加载更早运行记录" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("已加载记录仍可打开");
    expect(screen.getByRole("button", { name: "打开运行记录 run-kept" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "加载更早运行记录" })).not.toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "重试运行记录" })).toHaveLength(1);
    expect(onOpen).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "重试运行记录" }));
    await screen.findByRole("button", { name: "打开运行记录 run-next" });
    expect(getPage.mock.calls[1].slice(0, 3)).toEqual(getPage.mock.calls[2].slice(0, 3));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps failed and pending-approval records directly visible and opens their exact IDs", async () => {
    const user = userEvent.setup();
    const getPage = vi.fn().mockResolvedValue(page([
      { ...record("run-failed"), status: "failed" }, { ...record("run-approval"), status: "waiting_approval" },
    ]));
    const onOpen = mount(getPage);
    await user.click(screen.getByRole("button", { name: "运行记录" }));
    const failed = await screen.findByRole("button", { name: "打开运行记录 run-failed" });
    const pending = screen.getByRole("button", { name: "打开运行记录 run-approval" });
    expect(failed).toHaveTextContent("failed");
    expect(pending).toHaveTextContent("等待审批");
    expect(failed.querySelector(".v2-record-status")).toHaveClass("is-failed");
    expect(pending.querySelector(".v2-record-status")).toHaveClass("is-pending");
    await user.click(pending);
    expect(onOpen).toHaveBeenCalledExactlyOnceWith("run", "run-approval");
    expect(getPage).toHaveBeenCalledOnce();
  });

  it("distinguishes initial loading failure from a successful empty collection", async () => {
    const user = userEvent.setup();
    const getPage = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce(page([]));
    mount(getPage);
    await user.click(screen.getByRole("button", { name: "会话记录" }));
    await screen.findByRole("alert");
    expect(screen.queryByText("暂无会话记录")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "重试会话记录" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("暂无会话记录"));
  });
});

function render(ui: ReactElement, options?: RenderOptions) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
