import { useLocale, type LocaleText } from "../../lib/locale";
import { Component, Suspense, type ReactNode } from "react";

type LazySurfaceProps = {
  children: ReactNode;
  loadingText: string;
  errorLabel: string;
  resetKey?: string;
  onDismiss?: () => void;
};

class LazySurfaceBoundary extends Component<LazySurfaceProps & { t: LocaleText }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  render() {
    if (this.state.failed) return <div className="v2-module-loading" role="alert">
      <p>{this.props.t(`${this.props.errorLabel}加载失败。`, `Could not load ${this.props.errorLabel}.`)}</p>
      <p>{this.props.t("请先保存输入，再重新加载页面重试。", "Save your input, then reload the page to retry.")}</p>
      {this.props.onDismiss && <button type="button" onClick={this.props.onDismiss}>{this.props.t("关闭", "Close")}</button>}
    </div>;
    return <Suspense fallback={<div className="v2-module-loading" role="status">{this.props.loadingText}</div>}>
      {this.props.children}
    </Suspense>;
  }
}

export function V2LazySurface(props: LazySurfaceProps) {
  const { t } = useLocale();
  return <LazySurfaceBoundary key={props.resetKey} {...props} t={t} />;
}
