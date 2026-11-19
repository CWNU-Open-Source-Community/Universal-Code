import { useEffect, useRef, useState } from "react";
import { FileArchive, FolderOpen, LoaderCircle, PackageCheck, ShieldCheck, X } from "lucide-react";
import {
  desktopErrorMessage,
  installDesktopSkillPackage,
  selectDesktopSkillPreview,
  type DesktopSkillInstallResult,
  type DesktopSkillPreview,
} from "../lib/desktop-bridge";
import { formatBytes, formatNumber } from "../lib/format";
import { useLocale } from "../lib/locale";
import { OperationReceipt } from "./operation-receipt";
import { useModalFocusTrap } from "../hooks/use-modal-focus-trap";

export function DesktopSkillPreviewDialog({ open, onClose, installationEnabled = false }: {
  open: boolean;
  onClose: () => void;
  installationEnabled?: boolean;
}) {
  const { t } = useLocale();
  const [preview, setPreview] = useState<DesktopSkillPreview | null>(null);
  const [installed, setInstalled] = useState<DesktopSkillInstallResult | null>(null);
  const [surface, setSurface] = useState<"code" | "cyber">("code");
  const [confirmed, setConfirmed] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const operationKey = useRef("");
  const dialogRef = useModalFocusTrap<HTMLElement>(open, onClose, loading);

  useEffect(() => {
    if (!open) {
      setPreview(null);
      setInstalled(null);
      setConfirmed(false);
      setError("");
      setLoading(false);
    }
  }, [open]);

  if (!open) {
    return null;
  }

  const select = async () => {
    if (loading) {
      return;
    }
    setLoading(true);
    setError("");
    try {
      const selected = await selectDesktopSkillPreview();
      if (selected) {
        setPreview(selected);
        setInstalled(null);
        setConfirmed(false);
        operationKey.current = `desktop-skill-install-${globalThis.crypto.randomUUID()}`;
      }
    } catch (caught) {
      setError(desktopErrorMessage(caught));
    } finally {
      setLoading(false);
    }
  };

  const install = async () => {
    if (!preview || !installationEnabled || !confirmed || loading || installed) return;
    setLoading(true);
    setError("");
    try {
      setInstalled(await installDesktopSkillPackage(preview, surface, operationKey.current));
    } catch (caught) {
      setError(desktopErrorMessage(caught));
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="desktop-dialog-backdrop" role="presentation">
      <section aria-labelledby="desktop-skill-title" aria-modal="true" className="desktop-dialog"
        ref={dialogRef} role="dialog" tabIndex={-1}>
        <header>
          <div>
            <span className="dialog-icon"><FileArchive aria-hidden="true" size={18} /></span>
            <h2 id="desktop-skill-title">{t("Skill 包预览", "Skill package preview")}</h2>
          </div>
          <button aria-label={t("关闭", "Close")} autoFocus className="icon-button" disabled={loading} onClick={onClose} title={t("关闭", "Close")} type="button">
            <X aria-hidden="true" size={17} />
          </button>
        </header>

        <div className="desktop-dialog-body">
          {!preview && (
            <div className="desktop-package-empty">
              <FileArchive aria-hidden="true" size={28} />
              <button className="desktop-select-command" disabled={loading} onClick={() => void select()} type="button">
                {loading ? <LoaderCircle aria-hidden="true" className="spin" size={17} /> : <FolderOpen aria-hidden="true" size={17} />}
                {t("选择 SKILL.md、plugin.json 或 .zip", "Select SKILL.md, plugin.json or .zip")}
              </button>
            </div>
          )}
          {preview && <SkillPreviewDetails preview={preview} />}
          {preview && installationEnabled && !installed && <div className="desktop-install-controls">
            <div aria-label={t("Skill 工作面", "Skill surface")} className="segmented-control" role="group">
              <button aria-pressed={surface === "code"} onClick={() => setSurface("code")}
                type="button">Code</button>
              <button aria-pressed={surface === "cyber"} onClick={() => setSurface("cyber")}
                type="button">Cyber</button>
            </div>
            <label className="desktop-install-confirmation">
              <input checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)}
                type="checkbox" />
              <span>{t("确认以不受信任状态登记，执行时遵循任务权限与审批", "Register as untrusted; execution follows task permissions and approvals")}</span>
            </label>
          </div>}
          {installed && <div className="desktop-install-success" role="status">
            <PackageCheck aria-hidden="true" size={17} />
            <span>{"installation" in installed
              ? t(`${installed.installation.manifest.name} 已登记（${installed.installation.state}）。请到扩展设置审阅并启用。`,
                `${installed.installation.manifest.name} registered (${installed.installation.state}). Review and enable it in Extensions.`)
              : t(`${installed.name} ${installed.version} 已登记到 ${installed.surface}`, `${installed.name} ${installed.version} registered for ${installed.surface}`)}</span>
          </div>}
          {installed && "receipt" in installed && <OperationReceipt receipt={installed.receipt} />}
          {error && <div className="connection-error" role="alert">{error}</div>}
        </div>

        {preview && (
          <footer>
            <span><ShieldCheck aria-hidden="true" size={15} />{installed ? t("已登记到技能库", "Registered in the skill library") : t("结构已验证，待确认安装", "Structure validated; ready for install confirmation")}</span>
            <div className="desktop-dialog-actions">
              {installationEnabled && !installed &&
                <button className="desktop-select-command" disabled={loading || !confirmed}
                  onClick={() => void install()} type="button">
                  {loading ? <LoaderCircle aria-hidden="true" className="spin" size={16} />
                    : <PackageCheck aria-hidden="true" size={16} />}{t("安装", "Install")}
                </button>}
              <button className="desktop-select-command" disabled={loading} onClick={() => void select()} type="button">
                <FolderOpen aria-hidden="true" size={16} />{t("重新选择", "Choose again")}
              </button>
            </div>
          </footer>
        )}
      </section>
    </div>
  );
}

function SkillPreviewDetails({ preview }: { preview: DesktopSkillPreview }) {
  const { t } = useLocale();
  if ("skill_count" in preview) {
    return <div className="desktop-package-preview">
      <div className="desktop-package-heading"><strong>{preview.name}</strong><span>{preview.version}</span></div>
      <dl className="desktop-package-metrics">
        <div><dt>{t("格式", "Format")}</dt><dd>{preview.format}</dd></div>
        <div><dt>Skills</dt><dd>{formatNumber(preview.skill_count)}</dd></div>
        <div><dt>{t("文件条目", "Entries")}</dt><dd>{formatNumber(preview.entry_count)}</dd></div>
        <div><dt>{t("归档大小", "Archive")}</dt><dd>{formatBytes(preview.archive_bytes)}</dd></div>
      </dl>
      <small>{t("原文件已保留。安装后需审阅并启用。", "Original files retained. Review and enable after installation.")}</small>
      <code>{preview.archive_sha256.slice(0, 12)}</code>
    </div>;
  }
  return (
    <div className="desktop-package-preview">
      <div className="desktop-package-heading">
        <div><strong>{preview.name}</strong><span>{preview.version}</span></div>
        <code>{preview.trust_class}</code>
      </div>
      <dl className="desktop-package-metrics">
        <div><dt>{t("配置档", "Profiles")}</dt><dd>{preview.profiles.join(", ")}</dd></div>
        <div><dt>{t("工具", "Tools")}</dt><dd>{formatNumber(preview.declared_tool_count)}</dd></div>
        <div><dt>{t("内容", "Content")}</dt><dd>{formatBytes(preview.content_bytes)}</dd></div>
        <div><dt>{t("令牌", "Tokens")}</dt><dd>{formatNumber(preview.content_token_upper_bound)}</dd></div>
        <div><dt>{t("压缩包", "Archive")}</dt><dd>{formatBytes(preview.archive_bytes)}</dd></div>
        <div><dt>{t("条目", "Entries")}</dt><dd>{formatNumber(preview.entry_count)}</dd></div>
      </dl>
      <div className="desktop-package-list">
        <span>{t("声明的工具", "Declared tools")}</span>
        <div>{preview.declared_tools.length > 0
          ? preview.declared_tools.map((tool) => <code key={tool}>{tool}</code>)
          : <small>{t("无", "none")}</small>}</div>
      </div>
      <div className="desktop-package-list">
        <span>{t("风险代码", "Risk codes")}</span>
        <div>{preview.risk_codes.map((risk) => <code key={risk}>{risk}</code>)}</div>
      </div>
      <div className="desktop-package-digests">
        <span>{t("压缩包", "Archive")} <code>{preview.archive_sha256.slice(0, 12)}</code></span>
        <span>{t("包", "Package")} <code>{preview.package_fingerprint.slice(0, 12)}</code></span>
      </div>
    </div>
  );
}
