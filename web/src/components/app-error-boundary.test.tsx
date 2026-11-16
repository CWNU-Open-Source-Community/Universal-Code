import { cleanup, render, screen } from "@testing-library/react";
import { applyPrayuLocale, LocaleProvider } from "../lib/locale";
import { AppErrorBoundary } from "./app-error-boundary";

function Throws(): React.ReactElement {
  throw new Error("render crash fixture");
}

function ThrowsValue({ value }: { value: unknown }): React.ReactElement {
  throw value;
}

afterEach(() => {
  cleanup();
  localStorage.removeItem("prayu.locale.v1");
});

it.each([
  { locale: "zh-CN", summary: "页面出现异常。", guidance: "未保存的输入可能丢失", reload: "重新加载" },
  { locale: "en-US", summary: "Something went wrong.", guidance: "Unsaved input may be lost", reload: "Reload" },
] as const)("renders localized recovery controls and logs render crashes in $locale", ({ locale, summary, guidance, reload }) => {
  applyPrayuLocale(locale);
  const report = vi.spyOn(console, "error").mockImplementation(() => undefined);
  render(
    <LocaleProvider>
      <AppErrorBoundary>
        <Throws />
      </AppErrorBoundary>
    </LocaleProvider>,
  );
  const alert = screen.getByRole("alert");
  expect(alert).toHaveTextContent(summary);
  expect(alert).toHaveTextContent(guidance);
  expect(screen.getByRole("button", { name: reload })).toBeInTheDocument();
  expect(report).toHaveBeenCalledWith(expect.objectContaining({ message: "render crash fixture" }));
});

it.each([null, undefined, 0, "", false])("shows recovery UI when a child throws %s", (value) => {
  const report = vi.spyOn(console, "error").mockImplementation(() => undefined);
  render(
    <LocaleProvider>
      <AppErrorBoundary><ThrowsValue value={value} /></AppErrorBoundary>
    </LocaleProvider>,
  );
  expect(screen.getByRole("alert")).toHaveTextContent("页面出现异常。");
  expect(screen.getByRole("button", { name: "重新加载" })).toBeInTheDocument();
  expect(report).toHaveBeenCalledWith(value);
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
