import type { CSSProperties } from "react";
import { useLocale, type LocaleText } from "../../lib/locale";
import type { ExecutionCompanion } from "./execution-companion-activity";
import "./execution-companions.css";

type State = ExecutionCompanion["state"];
type Seat = readonly [x: number, y: number];

const familyNames = { gpt: "GPT", claude: "Claude", deepseek: "DeepSeek", gemini: "Gemini" };
// Positions mark the feet, with the desks to the left and the sofa on the right.
const desks: Seat[][] = [[], [[24, 72]], [[24, 71], [52, 71]], [[12.5, 68], [61.5, 70], [37, 85]]];
const lounge: Seat[][] = [[], [[77, 72]], [[63, 68], [87.5, 84]], [[63, 66], [87.5, 84], [38.5, 84]]];
const fullRoom: Seat[] = [[12.5, 72], [37.5, 83], [62.5, 72], [87.5, 83]];

const stateNames: Record<State, [string, string]> = {
  working: ["工作中", "Working"], waiting: ["等待中", "Waiting"], stopping: ["正在停止", "Stopping"],
  idle: ["待命", "Idle"], paused: ["已暂停", "Paused"], completed: ["已完成", "Completed"],
  failed: ["失败", "Failed"], cancelled: ["已取消", "Cancelled"], unknown: ["状态未知", "Unknown"],
};

function occupiesDesk(state: State) {
  return state === "working" || state === "stopping";
}

function seatPriority({ state }: ExecutionCompanion) {
  if (occupiesDesk(state)) return 0;
  if (state === "waiting" || state === "paused" || state === "idle") return 1;
  return 2;
}

function sharedFamilyLabels(companions: readonly ExecutionCompanion[], t: LocaleText) {
  const groups = new Map<string, ExecutionCompanion[]>();
  for (const companion of companions) {
    const key = companion.family === "unknown" ? companion.model || companion.role : companion.family;
    const group = groups.get(key);
    if (group) group.push(companion);
    else groups.set(key, [companion]);
  }
  const labels = new Map<string, string>();
  for (const group of groups.values()) {
    if (group.length < 2) continue;
    const children = group.filter((actor) => actor.role === "specialist");
    let length = 4;
    const longest = Math.max(0, ...children.map((actor) => actor.id.length));
    while (length < longest && new Set(children.map((actor) => actor.id.slice(-length))).size < children.length) length++;
    for (const actor of group) {
      labels.set(actor.id, actor.role === "root" ? t("主", "Main") :
        children.length === 1 ? t("子", "Sub") : actor.id.slice(-length));
    }
  }
  return labels;
}

function OfficeAgent({ companion, seat, identity, t }: {
  companion: ExecutionCompanion; seat?: Seat; identity?: string; t: LocaleText;
}) {
  const { model, role, family, state } = companion;
  const roleName = role === "root" ? t("主代理", "Main agent") : t("子代理", "Specialist");
  const name = family === "unknown" ? model.split("/").filter(Boolean).at(-1) || roleName : familyNames[family];
  const status = t(...stateNames[state]);
  const details = [model || t("模型未知", "Unknown model"), roleName, identity && companion.id, status].filter(Boolean).join(" · ");
  const position = seat && { "--seat-x": `${seat[0]}%`, "--seat-y": `${seat[1]}%`, zIndex: seat[1] } as CSSProperties;

  return <figure className="v2-office-agent" data-family={family} data-state={state}
    style={position} aria-label={details} title={details}>
    <span className="v2-office-avatar" aria-hidden="true">
      <span className={family === "unknown" ? "v2-office-terminal" : "v2-office-sprite"} />
    </span>
    <figcaption className="v2-office-nameplate">
      <strong>{name}</strong><span>{identity && <small>{identity}</small>}{status}</span>
    </figcaption>
  </figure>;
}

export function ExecutionCompanions({ companions }: { companions: readonly ExecutionCompanion[] }) {
  const { t } = useLocale();
  const identities = sharedFamilyLabels(companions, t);
  // Past runs can retain many finished specialists. Keep the current workers in sight first.
  const ordered = companions.length > fullRoom.length
    ? [...companions].sort((a, b) => seatPriority(a) - seatPriority(b)) : companions;
  const inRoom = ordered.slice(0, fullRoom.length);
  const extraRows = [];
  for (let index = fullRoom.length; index < ordered.length; index += fullRoom.length) {
    extraRows.push(ordered.slice(index, index + fullRoom.length));
  }
  const deskCount = inRoom.filter((actor) => occupiesDesk(actor.state)).length;
  const deskSeats = inRoom.length === fullRoom.length ? fullRoom : desks[deskCount];
  const loungeSeats = inRoom.length === fullRoom.length ? fullRoom.slice(deskCount) : lounge[inRoom.length - deskCount];
  let atDesk = 0;
  let atLounge = 0;

  return <section className="v2-execution-office" aria-label={t("模型办公室", "Model office")}>
    <div className="v2-office-scroll" tabIndex={extraRows.length ? 0 : undefined}
      role={extraRows.length ? "region" : undefined}
      aria-label={extraRows.length ? t(`办公室内共 ${ordered.length} 位代理`, `${ordered.length} agents in the office`) : undefined}>
      <div className="v2-office-room" data-count={inRoom.length}>
        {inRoom.map((companion) => <OfficeAgent key={companion.id} companion={companion} t={t} identity={identities.get(companion.id)}
          seat={occupiesDesk(companion.state) ? deskSeats[atDesk++] : loungeSeats[atLounge++]} />)}
        {!inRoom.length && <p className="v2-office-empty">{t("暂无模型记录", "No models recorded")}</p>}
      </div>
      {extraRows.map((row) => <div key={row[0].id} className="v2-office-floor">
        {row.map((companion) => <OfficeAgent key={companion.id} companion={companion} t={t} identity={identities.get(companion.id)} />)}
      </div>)}
    </div>
  </section>;
}
