import { useLocale, type LocaleText } from "../lib/locale";
import { Component, type ReactNode } from "react";

type AppShellErrorBoundaryProps = { children: ReactNode };

class AppShellErrorBoundary extends Component<
  AppShellErrorBoundaryProps & { t: LocaleText },
  { hasError: boolean }
> {
  state = { hasError: false };

  static getDerivedStateFromError() {
    return { hasError: true };
  }

  componentDidCatch(error: unknown) {
    // UI-evidence diagnostics count console_errors, so logging here keeps a
    // render crash observable without an external reporter.
    console.error(error);
  }

  render() {
    if (this.state.hasError) {
      return <div className="v2-module-loading" role="alert">
        <p>{this.props.t("页面出现异常。", "Something went wrong.")}</p>
        <p>{this.props.t("重新加载页面重试。未保存的输入可能丢失，浏览器中需要重新输入访问令牌。",
          "Reload the page to retry. Unsaved input may be lost; browser connections require the access token again.")}</p>
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
