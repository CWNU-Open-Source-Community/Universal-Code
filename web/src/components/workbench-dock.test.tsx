import { fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { APIClient } from "../api/client";
import { LocaleProvider } from "../lib/locale";
import { WorkbenchDock } from "./workbench-dock";

describe("WorkbenchDock", () => {
  it("keeps summary, bottom panel, and sidecar independently controllable", () => {
    renderDock();

    expect(screen.getByText("conversation body")).toBeInTheDocument();
    const summary = screen.getByRole("button", { name: "切换摘要" });
    const bottom = screen.getByRole("button", { name: "切换底部面板显示" });
    const sidecar = screen.getByRole("button", { name: "显示或隐藏右侧栏" });
    expect(summary).toHaveAttribute("aria-pressed", "false");
    expect(bottom).toHaveAttribute("aria-pressed", "false");
    expect(sidecar).toHaveAttribute("aria-pressed", "false");

    fireEvent.click(summary);
    expect(screen.getByRole("complementary", { name: "摘要" })).toBeInTheDocument();
    expect(summary).toHaveAttribute("aria-pressed", "true");

    fireEvent.click(bottom);
    expect(screen.getByRole("region", { name: "底部面板" })).toBeInTheDocument();
    expect(screen.getByText("在桌面应用中使用终端")).toBeInTheDocument();

    fireEvent.click(sidecar);
    expect(screen.getByRole("complementary", { name: "右侧工具栏" })).toBeInTheDocument();
    expect(within(screen.getByRole("complementary", { name: "摘要" })).getByText("未绑定项目。")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "关闭右侧栏" }));
    expect(screen.queryByRole("complementary", { name: "右侧工具栏" })).not.toBeInTheDocument();
    expect(screen.getByRole("complementary", { name: "摘要" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "底部面板" })).toBeInTheDocument();
  });

  it("groups ready tools and keeps the reserved browser unavailable", () => {
    renderDock();

    fireEvent.keyDown(window, { key: "g", ctrlKey: true, shiftKey: true });
    expect(screen.getByText("审阅")).toBeInTheDocument();
    expect(screen.getByText("未绑定项目。")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "添加右侧工具" }));
    expect(screen.getByRole("group", { name: "工作区" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "运行" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "即将推出" })).toBeInTheDocument();
    expect(screen.getByRole("menuitemradio", { name: "浏览器" })).toBeDisabled();

    fireEvent.keyDown(window, { key: "p", ctrlKey: true });
    expect(screen.getByRole("button", { name: "文件" })).toBeInTheDocument();
    expect(screen.getByText("未绑定项目。")).toBeInTheDocument();

    fireEvent.keyDown(window, { key: "j", ctrlKey: true });
    expect(screen.getByRole("region", { name: "底部面板" })).toBeInTheDocument();
    fireEvent.keyDown(window, { key: "j", ctrlKey: true });
    expect(screen.queryByRole("region", { name: "底部面板" })).not.toBeInTheDocument();
  });

  it("does not offer native Workspace opening in an ordinary browser", () => {
    renderDock();
    expect(screen.getByRole("button", { name: "打开工作区" })).toBeDisabled();
  });

  it("localizes the native Workspace action in English", () => {
    renderDock("en-US");
    expect(screen.getByRole("button", { name: "Open workspace" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "打开工作区" })).not.toBeInTheDocument();
  });
});

function renderDock(locale: "zh-CN" | "en-US" = "zh-CN") {
  window.localStorage.setItem("prayu.locale.v1", locale);
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const client = {} as APIClient;
  return render(
    <LocaleProvider><QueryClientProvider client={queryClient}>
        <WorkbenchDock client={client} desktop={false} resourceKind="run"
          runID="" sessionID="" title="针路簿工作台">
          <div>conversation body</div>
        </WorkbenchDock>
      </QueryClientProvider></LocaleProvider>,
  );
}
