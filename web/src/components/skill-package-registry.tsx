import { useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { LoaderCircle, RefreshCw, Trash2 } from "lucide-react";
import type { SkillPackageInventoryView } from "../api/types";
import { useLocale } from "../lib/locale";

export type SkillPackageRegistrySource = {
  load: (includeRemoved: boolean, signal?: AbortSignal) => Promise<SkillPackageInventoryView>;
  remove: (skill: string, operationKey: string) => Promise<unknown>;
};

export function SkillPackageRegistry({ enabled, source }: {
  enabled: boolean; source: SkillPackageRegistrySource;
}) {
  const { t } = useLocale();
  const [includeRemoved, setIncludeRemoved] = useState(false);
  const [confirmedSkill, setConfirmedSkill] = useState("");
  const [notice, setNotice] = useState("");
  const operationKeys = useRef(new Map<string, string>());
  const list = useQuery({
    queryKey: ["skill-package-registry", includeRemoved],
    queryFn: ({ signal }) => source.load(includeRemoved, signal),
    enabled, refetchOnWindowFocus: false,
  });
  const removal = useMutation({
    mutationFn: (skill: string) => {
      const existing = operationKeys.current.get(skill);
      const operationKey = existing ?? `skill-package-remove-${globalThis.crypto.randomUUID()}`;
      operationKeys.current.set(skill, operationKey);
      return source.remove(skill, operationKey);
    },
    onSuccess: (_value, skill) => {
      operationKeys.current.delete(skill);
      setNotice(t("已移除登记，历史任务记录保持不变",
        "Registry entry removed; historical Run records stay unchanged"));
      setConfirmedSkill("");
      void list.refetch();
    },
  });

  if (!enabled) {
    return <p role="status">{t("连接具备 Skill 管理权限的服务后，可在这里查看和移除已登记的技能包。",
      "Select a connection with Skill management to review and remove registered packages.")}</p>;
  }
  const items = list.data?.items ?? [];
  return <div className="skill-package-registry">
    <p className="v2-settings-help">{t("这里列出本机已登记的历史技能包。移除只停止后续可选用，不删除已完成的改动记录。",
      "This lists historical packages registered on this machine. Removal only stops future selection; completed work stays recorded.")}</p>
    <div className="skill-package-registry-controls">
      <button className="settings-action" disabled={list.isFetching || removal.isPending}
        onClick={() => void list.refetch()} type="button">
        {list.isFetching ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
          : <RefreshCw aria-hidden="true" size={15} />}
        {t("刷新列表", "Refresh list")}
      </button>
      <label><input checked={includeRemoved} disabled={list.isFetching || removal.isPending}
        onChange={(event) => {
          setIncludeRemoved(event.target.checked);
          setConfirmedSkill("");
          setNotice("");
        }} type="checkbox" />
        {t("显示已移除记录", "Show removed entries")}</label>
    </div>
    {list.error && <p role="alert" className="inline-warning">
      {list.error instanceof Error ? list.error.message : t("列表读取失败", "The list could not be read")}
    </p>}
    {list.isPending && <p role="status">{t("正在读取本机技能库", "Reading the local Skill registry")}</p>}
    {list.data && items.length === 0 && <p role="status">{t(includeRemoved
      ? "本机没有已登记的历史技能包。" : "本机没有可移除的历史技能包。已安装的新包在扩展页管理。",
      includeRemoved ? "No historical packages are registered here."
        : "No removable historical packages here. New installs are managed under Extensions.")}</p>}
    {list.data && items.length > 0 && <ul className="skill-package-registry-list">
      {items.map((item) => <li key={item.skill}>
        <div className="skill-package-registry-row">
          <code>{item.skill}</code>
          <span>{item.status === "removed" ? t("已移除", "Removed") : t("已登记", "Registered")}</span>
          <span>{item.surface}</span>
          {!item.object_verified && item.status === "installed" && <span className="inline-warning">
            {t("包内容暂不可读，安装记录仍在", "Package content is unreadable; the record remains")}
          </span>}
        </div>
        {item.removal && <p className="skill-package-registry-facts">
          {t("包对象已保留；历史恢复记录保留；后续任务不可再选用。",
            "Package object retained; recovery history retained; no longer selectable in new Runs.")}
        </p>}
        {item.status === "installed" && <div className="skill-package-registry-action">
          <label><input checked={confirmedSkill === item.skill} disabled={removal.isPending}
            onChange={(event) => {
              setConfirmedSkill(event.target.checked ? item.skill : "");
              setNotice("");
              removal.reset();
            }} type="checkbox" />
            {t("确认移除此技能包的登记，保留历史与包对象",
              "Remove this registration and keep its history and package object")}</label>
          <button className="settings-action" disabled={confirmedSkill !== item.skill ||
            removal.isPending} onClick={() => removal.mutate(item.skill)} type="button">
            {removal.isPending && removal.variables === item.skill
              ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
              : <Trash2 aria-hidden="true" size={15} />}
            {t("移除登记", "Remove registration")}
          </button>
        </div>}
      </li>)}
    </ul>}
    {removal.error && <p role="alert" className="inline-warning">
      {removal.error instanceof Error ? removal.error.message : t("移除未完成", "Removal did not complete")}
    </p>}
    {notice && <p role="status" className="projection-placeholder">{notice}</p>}
  </div>;
}
