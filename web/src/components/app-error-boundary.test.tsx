import { cleanup, render, screen } from "@testing-library/react";
import { LocaleProvider } from "../lib/locale";
import { AppErrorBoundary } from "./app-error-boundary";

function Throws(): React.ReactElement {
  throw new Error("render crash fixture");
}

afterEach(cleanup);

it("renders an actionable fallback and logs when a child render crashes", () => {
  const report = vi.spyOn(console, "error").mockImplementation(() => undefined);
  render(
    <LocaleProvider>
      <AppErrorBoundary>
        <Throws />
      </AppErrorBoundary>
    </LocaleProvider>,
  );
  const alert = screen.getByRole("alert");
  expect(alert).toHaveTextContent("页面出现异常。");
  expect(alert).toHaveTextContent("重新加载后会回到连接页");
  expect(screen.getByRole("button", { name: "重新加载" })).toBeTruthy();
  expect(report).toHaveBeenCalled();
});

it("renders children unchanged when nothing throws", () => {
  render(
    <LocaleProvider>
      <AppErrorBoundary>
        <main>正常内容</main>
      </AppErrorBoundary>
    </LocaleProvider>,
  );
  expect(screen.getByRole("main")).toHaveTextContent("正常内容");
  expect(screen.queryByRole("alert")).toBeNull();
});
