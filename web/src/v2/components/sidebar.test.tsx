import type { ReactElement } from "react";
import { fireEvent, render as renderComponent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ThreadView, WorkspaceView } from "../../api/types";
import { LocaleProvider, useLocale } from "../../lib/locale";
import { V2SettingsSidebar, V2Sidebar } from "./sidebar";

function render(ui: ReactElement) {
  return renderComponent(<LocaleProvider>{ui}</LocaleProvider>);
}

beforeEach(() => localStorage.setItem("prayu.locale.v1", "zh-CN"));

function LocaleSwitch() {
  const { locale, setLocale } = useLocale();
  return <button onClick={() => setLocale(locale === "zh-CN" ? "en-US" : "zh-CN")} type="button">Switch test language</button>;
}

const workspace: WorkspaceView = {
  id: "workspace-1", name: "Traverse Board", created_at: "2026-08-29T00:00:00Z",
};
const thread: ThreadView = {
  id: "thread-1", protocol_version: "thread.v1", workspace_id: workspace.id,
  mission_id: "mission-1", title: "Permission audit", status: "active",
  active_run_id: "run-1", last_run_id: "run-1", version: 3,
  composer_state: "ready", created_at: "2026-08-29T00:00:00Z",
  updated_at: "2026-08-29T00:00:00Z",
};

describe("sidebar project moves", () => {
  const destination = { ...workspace, id: "workspace-2", name: "Empty project" };
  const movable: ThreadView = { ...thread, project_workspace_id: workspace.id,
    project_assignment_version: 1, execution_state: "idle" };
  const props = { onArchive: vi.fn(), onNewConversation: vi.fn(), onOpenModels: vi.fn(),
    onOpenSettings: vi.fn(), onSearchOpen: vi.fn(), onSelectThread: vi.fn(),
    searchOpen: false, selectedThreadID: thread.id, workspaces: [workspace, destination],
    onNewInProject: vi.fn() };

  it("drops a known chat into an empty project without changing selection optimistically", () => {
    const onMoveThread = vi.fn();
    render(<V2Sidebar {...props} threads={[movable]} onMoveThread={onMoveThread} />);
    const row = screen.getByRole("button", { name: thread.title });
    const target = screen.getByRole("region", { name: "项目 Empty project" });
    const data = new Map<string, string>();
    const dataTransfer = { setData: (key: string, value: string) => data.set(key, value), getData: (key: string) => data.get(key) ?? "" };
    fireEvent.dragStart(row, { dataTransfer });
    fireEvent.dragOver(target, { dataTransfer });
    expect(target).toHaveClass("is-drop-target");
    fireEvent.drop(target, { dataTransfer });
    expect(onMoveThread).toHaveBeenCalledWith(movable, destination.id);
    expect(within(screen.getByRole("region", { name: "项目 Traverse Board" })).getByRole("button", { name: thread.title })).toHaveAttribute("aria-current", "page");
    fireEvent.drop(target, { dataTransfer });
    expect(onMoveThread).toHaveBeenCalledOnce();
  });

  it("supports the keyboard project picker and groups local chats separately from their execution folder", async () => {
    const user = userEvent.setup();
    const onMoveThread = vi.fn();
    const local = { ...movable, project_workspace_id: "" };
    render(<V2Sidebar {...props} threads={[local]} onMoveThread={onMoveThread} />);
    expect(within(screen.getByRole("region", { name: "项目 本地聊天" })).getByRole("button", { name: thread.title })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "移动 Permission audit 到项目" }));
    await user.selectOptions(screen.getByRole("combobox", { name: "目标项目" }), destination.id);
    await user.click(screen.getByRole("button", { name: "移动" }));
    expect(onMoveThread).toHaveBeenCalledWith(local, destination.id);
  });

  it("does not let running chats or foreign drag data move projects", () => {
    const onMoveThread = vi.fn();
    render(<V2Sidebar {...props} threads={[{ ...movable, execution_state: "running" }]} onMoveThread={onMoveThread} />);
    expect(screen.getByRole("button", { name: thread.title })).toHaveAttribute("draggable", "false");
    expect(screen.getByRole("button", { name: "移动 Permission audit 到项目" })).toBeDisabled();
    fireEvent.drop(screen.getByRole("region", { name: "项目 Empty project" }), { dataTransfer: { getData: () => thread.id } });
    expect(onMoveThread).not.toHaveBeenCalled();
  });
});

describe("V2Sidebar archive menu", () => {
  it("keeps identically named projects separate by identity and states search coverage", async () => {
    const second = { ...workspace, id: "workspace-2" };
    const user = userEvent.setup();
    const onSelectThread = vi.fn();
    const onLoadMore = vi.fn();
    const onSearchChange = vi.fn();
    render(<V2Sidebar onArchive={vi.fn()} onNewConversation={vi.fn()} onOpenModels={vi.fn()}
      onOpenSettings={vi.fn()} onSearchOpen={vi.fn()} onSelectThread={onSelectThread}
      onLoadMore={onLoadMore} onSearchChange={onSearchChange} hasMore searchOpen selectedThreadID="" workspaces={[workspace, second]}
      threads={[thread, { ...thread, id: "thread-2", workspace_id: second.id }]} />);
    const first = screen.getByRole("region", { name: `项目 ${workspace.name} · ${workspace.id}` });
    const other = screen.getByRole("region", { name: `项目 ${workspace.name} · ${second.id}` });
    await user.click(within(other).getByRole("button", { name: thread.title }));
    expect(onSelectThread).toHaveBeenCalledWith("thread-2");
    expect(within(first).getByRole("button", { name: thread.title })).toBeInTheDocument();
    expect(screen.getByText(/搜索全部已保存/)).toHaveTextContent("用户、助手正文");
    expect(screen.getByRole("status")).toHaveTextContent("已加载 2 条对话；还有更早记录");
    fireEvent.change(screen.getByRole("searchbox", { name: "搜索对话" }), { target: { value: "missing" } });
    expect(onSearchChange).toHaveBeenCalledWith("missing");
    expect(within(first).getByRole("button", { name: thread.title })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "加载更早对话" }));
    expect(onLoadMore).toHaveBeenCalledOnce();
  });
  it("passes the exact selected Thread to the archive confirmation owner", async () => {
    const user = userEvent.setup();
    const onArchive = vi.fn();
    render(<V2Sidebar onArchive={onArchive} onNewConversation={vi.fn()}
      onOpenModels={vi.fn()} onOpenSettings={vi.fn()} onSearchOpen={vi.fn()} onSelectThread={vi.fn()}
      searchOpen={false} selectedThreadID={thread.id} threads={[thread]}
      workspaces={[workspace]} />);

    await user.click(screen.getByRole("button", { name: "归档 Permission audit" }));

    expect(onArchive).toHaveBeenCalledTimes(1);
    expect(onArchive).toHaveBeenCalledWith(thread);
  });
});

describe("V2Sidebar execution status", () => {
  const props = { onArchive: vi.fn(), onNewConversation: vi.fn(), onOpenModels: vi.fn(),
    onOpenSettings: vi.fn(), onSearchOpen: vi.fn(), onSelectThread: vi.fn(),
    searchOpen: false, selectedThreadID: thread.id, workspaces: [workspace] };

  it.each([
    ["idle", "空闲"], ["running", "执行中"], ["stopping", "正在停止"], ["stop_failed", "停止失败"],
    ["waiting_approval", "等待审批"], ["paused", "已暂停"], ["completed", "已完成"],
    ["failed", "执行失败"], ["cancelled", "已取消"], ["unknown", "状态未知"],
  ])("describes Go execution state %s independently of composer readiness", (state, label) => {
    render(<V2Sidebar {...props} threads={[{ ...thread, execution_state: state } as ThreadView]} />);
    const row = screen.getByRole("button", { name: thread.title });
    expect(row).toHaveTextContent(label);
    expect(row).toHaveAccessibleDescription(state === "unknown" ? "执行状态未知，请刷新列表重试" : label);
    expect(row).toHaveAttribute("aria-current", "page");
  });

  it.each([[undefined], ["unsupported"], [["idle"]], [{ toString: "idle" }]])("treats missing or invalid state %j as unknown", (state) => {
    render(<V2Sidebar {...props} threads={[{ ...thread, execution_state: state } as ThreadView]} />);
    const row = screen.getByRole("button", { name: thread.title });
    expect(row).toHaveTextContent("状态未知");
    expect(row).not.toHaveTextContent("空闲");
  });

  it("does not display stale idle status after a list read failure and retries the failed operation", async () => {
    const onRetry = vi.fn();
    const onLoadMore = vi.fn();
    render(<V2Sidebar {...props} threads={[{ ...thread, execution_state: "idle" } as ThreadView]}
      hasMore loadFailed onRetry={onRetry} onLoadMore={onLoadMore} />);
    const row = screen.getByRole("button", { name: thread.title });
    expect(row).toHaveTextContent("状态未知");
    expect(row).toHaveAccessibleDescription("本次列表读取失败，执行状态未知");
    expect(row).not.toHaveTextContent("空闲");
    await userEvent.setup().click(screen.getByRole("button", { name: "重试加载对话" }));
    expect(onRetry).toHaveBeenCalledOnce();
    expect(onLoadMore).not.toHaveBeenCalled();
  });

  it.each([false, true])("reports capped result sets without claiming pagination is complete (search=%s)", (searching) => {
    render(<V2Sidebar {...props} threads={[thread]} truncated appliedSearch={searching ? "audit" : ""} />);
    const status = screen.getByRole("status", { name: "对话列表状态" });
    expect(status).toHaveTextContent("结果达到读取上限");
    expect(status).not.toHaveTextContent("全部匹配结果已加载");
    expect(status).not.toHaveTextContent("当前列表已加载完毕");
  });
});

describe("V2 model navigation", () => {
  it("places a dedicated model entry immediately after new conversation", async () => {
    const user = userEvent.setup();
    const onOpenModels = vi.fn();
    render(<V2Sidebar onArchive={vi.fn()} onNewConversation={vi.fn()}
      onOpenModels={onOpenModels} onOpenSettings={vi.fn()} onSearchOpen={vi.fn()}
      onSelectThread={vi.fn()} searchOpen={false} selectedThreadID="" threads={[]}
      workspaces={[]} />);

    const navigation = screen.getByRole("navigation", { name: "对话导航" });
    const actions = within(navigation).getAllByRole("button");
    expect(actions.map((button) => button.textContent)).toEqual([
      expect.stringContaining("新对话"),
      "接入模型",
    ]);
    await user.click(within(navigation).getByRole("button", { name: "接入模型" }));
    expect(onOpenModels).toHaveBeenCalledOnce();
  });

  it("places Models between General and Permissions and exposes page state", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(<V2SettingsSidebar onBack={vi.fn()} onSelect={onSelect} section="models" />);

    const navigation = screen.getByRole("navigation", { name: "设置分类" });
    const labels = within(navigation).getAllByRole("button").map((button) => button.textContent);
    expect(labels).toEqual(["常规", "配置", "外观", "用量统计", "快捷键", "关于", "连接与环境", "模型",
      "扩展与代码智能", "Skill 包", "执行环境", "任务预算与项目配置", "当前任务权限", "观察视图偏好"]);
    expect(labels).not.toContain("智能伙伴");
    const models = within(navigation).getByRole("button", { name: "模型" });
    expect(models).toHaveAttribute("aria-current", "page");
    await user.click(models);
    expect(onSelect).toHaveBeenCalledWith("models");
  });
});

it("opens the dedicated configuration section from Settings", async () => {
  const onSelect = vi.fn();
  render(<V2SettingsSidebar section="configuration" onSelect={onSelect} onBack={vi.fn()} />);
  const entry = screen.getByRole("button", { name: "配置" });
  expect(entry).toHaveAttribute("aria-current", "page");
  await userEvent.click(entry);
  expect(onSelect).toHaveBeenCalledExactlyOnceWith("configuration");
});

it("updates both navigation sidebars at runtime and searches the translated settings labels", async () => {
  const user = userEvent.setup();
  const onSelect = vi.fn();
  render(<><LocaleSwitch /><V2Sidebar onArchive={vi.fn()} onNewConversation={vi.fn()}
    onOpenModels={vi.fn()} onOpenSettings={vi.fn()} onSearchOpen={vi.fn()} onSelectThread={vi.fn()}
    searchOpen selectedThreadID={thread.id} threads={[{ ...thread, title: "新对话检查", execution_state: "running" }]}
    workspaces={[{ ...workspace, name: "我的新对话项目" }]} />
    <V2SettingsSidebar section="models" onBack={vi.fn()} onSelect={onSelect} /></>);

  expect(screen.getByRole("navigation", { name: "对话导航" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "新对话检查" })).toHaveAccessibleDescription("执行中");
  await user.click(screen.getByRole("button", { name: "Switch test language" }));

  const navigation = screen.getByRole("navigation", { name: "Conversation navigation" });
  expect(within(navigation).getByRole("button", { name: "New conversation" })).toBeInTheDocument();
  expect(screen.queryByRole("navigation", { name: "对话导航" })).not.toBeInTheDocument();
  const project = screen.getByRole("region", { name: "Project 我的新对话项目" });
  expect(within(project).getByText("我的新对话项目")).toBeInTheDocument();
  expect(within(project).getByRole("button", { name: "新对话检查" })).toHaveAccessibleDescription("Running");
  expect(screen.getByRole("status", { name: "Conversation list status" })).toHaveTextContent("Loaded 1 conversation; The current list is fully loaded.");
  expect(screen.getByRole("searchbox", { name: "Search conversations" })).toHaveAttribute("placeholder", "Search titles and message text…");

  const settings = screen.getByRole("navigation", { name: "Settings categories" });
  expect(within(settings).getByRole("button", { name: "Models" })).toHaveAttribute("aria-current", "page");
  await user.type(screen.getByRole("searchbox", { name: "Search settings" }), "MODELS");
  expect(within(settings).getAllByRole("button")).toHaveLength(1);
  await user.click(within(settings).getByRole("button", { name: "Models" }));
  expect(onSelect).toHaveBeenCalledExactlyOnceWith("models");

  await user.clear(screen.getByRole("searchbox", { name: "Search settings" }));
  await user.click(screen.getByRole("button", { name: "Switch test language" }));
  expect(screen.getByRole("navigation", { name: "设置分类" })).toBeInTheDocument();
  expect(screen.getByRole("region", { name: "项目 我的新对话项目" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "新对话检查" })).toHaveAccessibleDescription("执行中");
});
