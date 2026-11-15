import { useLocale, type LocaleText } from "../lib/locale";
import { Component, type ReactNode } from "react";

type AppShellErrorBoundaryProps = { children: ReactNode };

class AppShellErrorBoundary extends Component<
  AppShellErrorBoundaryProps & { t: LocaleText },
  { error: Error | null }
> {
  state = { error: null as Error | null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  componentDidCatch(error: Error) {
    // UI-evidence diagnostics count console_errors, so logging here keeps a
    // render crash observable without an external reporter.
    console.error(error);
  }

  render() {
    if (this.state.error) {
      return <div className="v2-module-loading" role="alert">
        <p>{this.props.t("页面出现异常。", "Something went wrong.")}</p>
        <p>{this.props.t("请先保存输入，再重新加载页面重试；重新加载后会回到连接页。",
          "Save your input, then reload the page to retry; reloading returns to the connection page.")}</p>
        <button type="button" onClick={() => window.location.reload()}>
          {this.props.t("重新加载", "Reload")}
        </button>
      </div>;
    }
    return this.props.children;
  }
}

// The exported shell wrapper supplies the bilingual text selector to the
// class boundary, mirroring the V2LazySurface convention.
export function AppErrorBoundary(props: AppShellErrorBoundaryProps) {
  const { t } = useLocale();
  return <AppShellErrorBoundary {...props} t={t} />;
}
