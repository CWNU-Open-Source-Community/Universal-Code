import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { APIClient } from "../api/client";
import type { SkillCatalogPublisherView } from "../api/types";
import { useLocale } from "../lib/locale";

const queryKey = ["skill-catalog-publishers"];

export function SkillCatalogTrust({ client }: { client: APIClient }) {
  const { t } = useLocale();
  const [open, setOpen] = useState(false);
  return <details className="extension-onboarding" onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary>{t("旧 Skill Catalog 发布者信任", "Legacy Skill Catalog publisher trust")}</summary>
    {open && <CatalogControls client={client} />}
  </details>;
}

function CatalogControls({ client }: { client: APIClient }) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const list = useQuery({ queryKey, queryFn: ({ signal }) => client.listSkillCatalogPublishers(signal), retry: false });
  const [name, setName] = useState("");
  const [team, setTeam] = useState("");
  const [publicKey, setPublicKey] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const trust = useMutation({ mutationFn: async () => {
    const encoded = publicKey.trim();
    const decoded = Uint8Array.from(atob(encoded), (char) => char.charCodeAt(0));
    if (decoded.length !== 32 || btoa(String.fromCharCode(...decoded)) !== encoded) throw new Error(t("请输入规范 Base64 格式的 32 字节 Ed25519 公钥。", "Enter a canonical base64 Ed25519 public key of 32 bytes."));
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", decoded));
    const fingerprint = Array.from(digest, (byte) => byte.toString(16).padStart(2, "0")).join("");
    return client.controlSkillCatalogPublisher({ version: "skill-catalog-control.v1", action: "trust", expected_updated_at: "",
      fingerprint, public_key: encoded, name: name.trim(), team: team.trim(), confirm: true });
  }, onSuccess: async () => {
    setName(""); setTeam(""); setPublicKey(""); setConfirmed(false);
    await queryClient.invalidateQueries({ queryKey });
  }, onError: () => { setConfirmed(false); void queryClient.invalidateQueries({ queryKey }); } });
  const enabled = client.hasSkillInstallation && list.data?.control_enabled === true && !list.isError;
  const change = (update: () => void) => { update(); setConfirmed(false); trust.reset(); };
  return <>
    <p>{t("管理旧 Skill Catalog 的发布者公钥，影响其版本固定与后续启用。Plugin 发布者信任在扩展界面管理。",
      "Manage publisher keys used by the legacy Skill Catalog for version pinning and later enablement. Manage Plugin publishers in Extensions.")}</p>
    <button className="settings-action" type="button" disabled={list.isFetching || trust.isPending}
      onClick={() => void list.refetch()}>{t("刷新发布者", "Refresh publishers")}</button>
    {list.isPending && <p role="status">{t("正在读取发布者…", "Loading publishers…")}</p>}
    {list.error && <p role="alert">{list.error instanceof Error ? list.error.message : t("发布者读取失败", "Could not read publishers")}</p>}
    {list.data && !enabled && <p role="status">{t("当前连接只能查看 Catalog 信任。", "This connection can only inspect Catalog trust.")}</p>}
    {list.data?.publishers.map((publisher) => <PublisherControl client={client} enabled={enabled}
      key={`${publisher.fingerprint}/${publisher.updated_at}`} publisher={publisher} />)}
    {list.data?.publishers.length === 0 && <p>{t("尚未登记 Catalog 发布者。", "No Catalog publishers are registered.")}</p>}
    <form className="extension-form" onSubmit={(event) => { event.preventDefault(); trust.mutate(); }}>
      <fieldset disabled={!enabled || trust.isPending}>
        <legend>{t("信任新发布者", "Trust a new publisher")}</legend>
        <label>{t("发布者名称", "Publisher name")}<input required maxLength={96} value={name} onChange={(event) => change(() => setName(event.target.value))} /></label>
        <label>{t("团队（可选）", "Team (optional)")}<input maxLength={128} value={team} onChange={(event) => change(() => setTeam(event.target.value))} /></label>
        <label>{t("Ed25519 公钥（Base64）", "Ed25519 public key (base64)")}<input required maxLength={44} value={publicKey} onChange={(event) => change(() => setPublicKey(event.target.value))} /></label>
        <div className="extension-checks"><label><input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} />
          {t("我已核对公钥来源，信任此 Catalog 发布者", "I verified the key source and trust this Catalog publisher")}</label></div>
        <button className="settings-action" type="submit" disabled={!confirmed || !name.trim() || !publicKey.trim()}>
          {trust.isPending ? t("正在保存…", "Saving…") : t("保存发布者信任", "Save publisher trust")}</button>
      </fieldset>
    </form>
    {trust.error && <p role="alert">{trust.error instanceof Error ? trust.error.message : t("信任变更失败", "Trust change failed")}
      {t(" 刷新后核对当前状态；已有发布者请使用对应记录操作。", " Refresh and review the current state; use the existing row for a registered publisher.")}</p>}
  </>;
}

function PublisherControl({ client, enabled, publisher }: { client: APIClient; enabled: boolean; publisher: SkillCatalogPublisherView }) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [confirmed, setConfirmed] = useState(false);
  const action = publisher.trust_class === "trusted" ? "revoke" : "trust";
  const change = useMutation({ mutationFn: () => client.controlSkillCatalogPublisher({
    version: "skill-catalog-control.v1", action, fingerprint: publisher.fingerprint,
    expected_updated_at: publisher.updated_at, confirm: true,
    ...(action === "trust" ? { public_key: publisher.public_key, name: publisher.name, team: publisher.team } : {}),
  }), onSettled: () => { setConfirmed(false); return queryClient.invalidateQueries({ queryKey }); } });
  return <article className="extension-review">
    <strong>{publisher.name}{publisher.team ? ` · ${publisher.team}` : ""}</strong>
    <p>{t("信任状态", "Trust state")}: {publisher.trust_class} · {publisher.updated_at}</p>
    <p>{t("公钥指纹", "Key fingerprint")}: <code>{publisher.fingerprint}</code></p>
    <details><summary>{t("查看公钥", "Inspect public key")}</summary><code>{publisher.public_key}</code></details>
    <label><input type="checkbox" checked={confirmed} disabled={!enabled || change.isPending} onChange={(event) => setConfirmed(event.target.checked)} />
      {action === "revoke" ? t("确认撤销此 Catalog 发布者信任", "Revoke trust in this Catalog publisher") : t("已重新核对来源，确认恢复此 Catalog 发布者信任", "I rechecked the source and confirm restoring Catalog publisher trust")}</label>
    <button className="settings-action" type="button" disabled={!enabled || !confirmed || change.isPending} onClick={() => change.mutate()}>
      {action === "revoke" ? t("撤销 Catalog 信任", "Revoke Catalog trust") : t("重新信任 Catalog 发布者", "Trust Catalog publisher again")}</button>
    {change.error && <p role="alert">{change.error instanceof Error ? change.error.message : t("操作失败，请刷新当前状态", "Action failed; refresh the current state")}</p>}
  </article>;
}
