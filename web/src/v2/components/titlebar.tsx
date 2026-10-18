import { ArrowLeft, Minus, PanelLeft, Square, X } from "lucide-react";
import { desktopBridgeAvailable, desktopIsMacPlatform } from "../../lib/desktop-bridge";
import { useLocale } from "../../lib/locale";
import {
  closeDesktopWindow,
  minimiseDesktopWindow,
  toggleDesktopWindowMaximised,
} from "../../lib/desktop-window";

export function V2Titlebar({ sidebarVisible, onToggleSidebar, onBack, canGoBack = false }: {
  sidebarVisible: boolean;
  onToggleSidebar: () => void;
  onBack: () => void;
  canGoBack?: boolean;
}) {
  const { t } = useLocale();
  const desktop = desktopBridgeAvailable();
  const mac = desktopIsMacPlatform();
  return <header className={`v2-titlebar${mac ? " is-mac" : ""}`}>
    <div className="v2-titlebar-navigation" data-v2-no-drag="true">
      <button aria-label={sidebarVisible ? t("隐藏侧栏", "Hide sidebar") : t("显示侧栏", "Show sidebar")}
        aria-pressed={sidebarVisible} onClick={onToggleSidebar} type="button">
        <PanelLeft aria-hidden="true" size={16} />
      </button>
      <button aria-label={t("返回", "Back")} disabled={!canGoBack} onClick={onBack} type="button">
        <ArrowLeft aria-hidden="true" size={17} />
      </button>
    </div>
    <div className="v2-titlebar-drag-region" data-v2-drag="true" />
    {desktop && !mac && <div aria-label={t("窗口控制", "Window controls")} className="v2-window-controls"
      data-v2-no-drag="true">
      <button aria-label={t("最小化", "Minimize")} onClick={() => void minimiseDesktopWindow()} type="button">
        <Minus aria-hidden="true" size={16} />
      </button>
      <button aria-label={t("最大化", "Maximize")} onClick={() => void toggleDesktopWindowMaximised()} type="button">
        <Square aria-hidden="true" size={13} />
      </button>
      <button aria-label={t("关闭", "Close")} className="close" onClick={() => void closeDesktopWindow()} type="button">
        <X aria-hidden="true" size={17} />
      </button>
    </div>}
  </header>;
}
