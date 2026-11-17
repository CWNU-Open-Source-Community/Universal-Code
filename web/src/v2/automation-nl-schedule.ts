import type { ScheduledJobCreateRequestView } from "../api/types";

export type NaturalScheduleKind = "once" | "periodic";
export type NaturalScheduleCadence =
  | "once"
  | "minutes"
  | "hours"
  | "daily"
  | "weekdays"
  | "weekly";

export interface NaturalSchedulePreset {
  id: string;
  label: { zh: string; en: string };
  expression: { zh: string; en: string };
  description: { zh: string; en: string };
}

export interface AutomationPromptTemplate {
  id: string;
  title: { zh: string; en: string };
  schedule: { zh: string; en: string };
  prompt: { zh: string; en: string };
}

export interface ParsedNaturalSchedule {
  ok: true;
  raw: string;
  kind: NaturalScheduleKind;
  cadence: NaturalScheduleCadence;
  /** Interval in minutes for periodic schedules. Undefined for one-shot schedules. */
  intervalMinutes?: number;
  /** Interval in seconds for backend ScheduledJobScheduleRequestView. */
  intervalSeconds?: number;
  /** Next computed trigger timestamp (ISO 8601 UTC). */
  anchorAt: string;
  /** Computed deadline timestamp (ISO 8601 UTC), bounded within 90 days. */
  deadlineAt: string;
  /** Suggested max_rounds matching the cadence. */
  suggestedMaxRounds: number;
  /** IANA timezone string. */
  timezone: string;
  /** Next scheduled trigger timestamps (1 for once, 3 for periodic). */
  nextTriggers: string[];
  /** Human-readable bilingual summary. */
  summary: { zh: string; en: string };
  /** Ready-to-send schedule payload for ScheduledJobCreateRequestView. */
  scheduleRequest: ScheduledJobCreateRequestView["schedule"];
}

export interface NaturalScheduleParseError {
  ok: false;
  raw: string;
  error: { zh: string; en: string };
  suggestions: ReadonlyArray<NaturalSchedulePreset>;
}

export type NaturalScheduleResult = ParsedNaturalSchedule | NaturalScheduleParseError;

export const NATURAL_SCHEDULE_PRESETS: ReadonlyArray<NaturalSchedulePreset> = [
  {
    id: "hourly-patrol",
    label: { zh: "每小时巡检", en: "Hourly check" },
    expression: { zh: "每 1 小时", en: "every 1 hour" },
    description: { zh: "每隔 60 分钟自动执行一次巡检", en: "Run automatically every 60 minutes" },
  },
  {
    id: "morning-brief-0900",
    label: { zh: "每天早报 09:00", en: "Daily brief 09:00" },
    expression: { zh: "每天早上 9 点", en: "every day at 9am" },
    description: { zh: "每天上午 09:00 生成项目进展早报", en: "Generate a daily summary at 09:00" },
  },
  {
    id: "daily-build-1800",
    label: { zh: "每日构建检查 18:00", en: "Daily build check 18:00" },
    expression: { zh: "每天 18:00", en: "every day at 18:00" },
    description: { zh: "每天傍晚 18:00 检查构建与测试健康度", en: "Verify build and test health at 18:00 daily" },
  },
  {
    id: "monday-digest-1000",
    label: { zh: "每周一汇总 10:00", en: "Monday digest 10:00" },
    expression: { zh: "每周一 10:00", en: "every monday at 10:00" },
    description: { zh: "每周一上午 10:00 汇总待办与技术债", en: "Weekly summary every Monday at 10:00" },
  },
];

export const AUTOMATION_PROMPT_TEMPLATES: ReadonlyArray<AutomationPromptTemplate> = [
  {
    id: "build-and-tests",
    title: { zh: "构建与测试巡检", en: "Build & test patrol" },
    schedule: { zh: "每天 18:00", en: "every day at 18:00" },
    prompt: {
      zh: "## 每日构建与回归检查\n\n1. 运行工作区核心类型检查与单元测试套件。\n2. 若发现失败用例或回归问题，定位根因并给出最小修复建议。\n3. 输出简明检查摘要（通过率、耗时与风险项）。",
      en: "## Daily Build & Regression Check\n\n1. Run the workspace typecheck and unit test suite.\n2. Diagnose any failing tests or regressions and propose minimal fixes.\n3. Summarize pass rates, slow suites, and action items.",
    },
  },
  {
    id: "morning-repo-brief",
    title: { zh: "每日工程早报", en: "Morning engineering brief" },
    schedule: { zh: "每天早上 9 点", en: "every day at 9am" },
    prompt: {
      zh: "## 每日工程进展早报\n\n- 汇总最近 24 小时内的提交、未完成任务与阻塞项。\n- 检查是否有待处理的审阅意见或高优缺陷。\n- 按优先级列出今日建议推进的 3 件事。",
      en: "## Morning Engineering Brief\n\n- Summarize commits, open tasks, and blockers from the last 24 hours.\n- Check for pending review comments or high-priority bugs.\n- List the top 3 recommended priorities for today.",
    },
  },
  {
    id: "weekly-quality-audit",
    title: { zh: "每周代码质量与安全汇总", en: "Weekly quality & security digest" },
    schedule: { zh: "每周一 10:00", en: "every monday at 10:00" },
    prompt: {
      zh: "## 每周代码质量与安全巡检\n\n1. 扫描最近新增代码中的潜在安全隐患、未处理异常与配置风险。\n2. 识别可清理的重复逻辑或过时依赖。\n3. 生成结构化周报清单。",
      en: "## Weekly Code Quality & Security Audit\n\n1. Scan recent changes for security risks, unhandled errors, or config drift.\n2. Identify duplicate logic or outdated dependencies.\n3. Produce a structured weekly report.",
    },
  },
];

export interface AutomationDraftPrefill {
  prompt?: string;
  scheduleExpression?: string;
  title?: string;
}

let pendingAutomationPrefill: AutomationDraftPrefill | null = null;

export function requestAutomationDraftPrefill(prefill: AutomationDraftPrefill): void {
  pendingAutomationPrefill = { ...prefill };
}

export function consumeAutomationDraftPrefill(): AutomationDraftPrefill | null {
  const value = pendingAutomationPrefill;
  pendingAutomationPrefill = null;
  return value;
}

const MAX_DEADLINE_MS = 88 * 24 * 60 * 60 * 1_000;
const MAX_INTERVAL_MINUTES = 30 * 24 * 60;

const CHINESE_DIGITS: Record<string, number> = {
  零: 0,
  〇: 0,
  一: 1,
  二: 2,
  两: 2,
  俩: 2,
  三: 3,
  四: 4,
  五: 5,
  六: 6,
  七: 7,
  八: 8,
  九: 9,
};

const WEEKDAY_NAMES_ZH = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"] as const;
const WEEKDAY_NAMES_EN = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"] as const;

const ZH_WEEKDAY_MAP: Record<string, number> = {
  日: 0,
  天: 0,
  一: 1,
  二: 2,
  三: 3,
  四: 4,
  五: 5,
  六: 6,
};

const EN_WEEKDAY_MAP: Record<string, number> = {
  sun: 0,
  sunday: 0,
  sundays: 0,
  mon: 1,
  monday: 1,
  mondays: 1,
  tue: 2,
  tues: 2,
  tuesday: 2,
  tuesdays: 2,
  wed: 3,
  wednesday: 3,
  wednesdays: 3,
  thu: 4,
  thur: 4,
  thurs: 4,
  thursday: 4,
  thursdays: 4,
  fri: 5,
  friday: 5,
  fridays: 5,
  sat: 6,
  saturday: 6,
  saturdays: 6,
};

function parseChineseOrArabicInt(raw: string): number | null {
  const trimmed = raw.trim();
  if (!trimmed) return null;
  if (/^\d+$/u.test(trimmed)) {
    const parsed = Number.parseInt(trimmed, 10);
    return Number.isSafeInteger(parsed) ? parsed : null;
  }
  if (trimmed === "半") return 30;
  if (trimmed === "十") return 10;
  if (trimmed.startsWith("十") && trimmed.length === 2) {
    const ones = CHINESE_DIGITS[trimmed[1]!];
    return ones !== undefined ? 10 + ones : null;
  }
  if (trimmed.endsWith("十") && trimmed.length === 2) {
    const tens = CHINESE_DIGITS[trimmed[0]!];
    return tens !== undefined ? tens * 10 : null;
  }
  if (trimmed.length === 3 && trimmed[1] === "十") {
    const tens = CHINESE_DIGITS[trimmed[0]!];
    const ones = CHINESE_DIGITS[trimmed[2]!];
    return tens !== undefined && ones !== undefined ? tens * 10 + ones : null;
  }
  if (trimmed.length === 1 && CHINESE_DIGITS[trimmed] !== undefined) {
    return CHINESE_DIGITS[trimmed]!;
  }
  return null;
}

function pad2(n: number): string {
  return String(n).padStart(2, "0");
}

type DayPeriod = "dawn" | "morning" | "noon" | "afternoon" | "evening" | "night" | "am" | "pm" | null;

function resolve24Hour(hour: number, minute: number, period: DayPeriod): { hour: number; minute: number } | null {
  if (!Number.isInteger(hour) || !Number.isInteger(minute) || minute < 0 || minute > 59) return null;
  if (hour < 0 || hour > 23) return null;
  let adjusted = hour;
  if (period === "am" || period === "dawn" || period === "morning") {
    if (hour > 12) return null;
    if (period === "am" && hour === 12) adjusted = 0;
  } else if (period === "pm" || period === "afternoon" || period === "evening") {
    if (hour > 12 && period === "pm") return null;
    if (hour >= 1 && hour < 12) adjusted = hour + 12;
  } else if (period === "noon") {
    if (hour >= 1 && hour <= 3) adjusted = hour + 12;
  } else if (period === "night") {
    if (hour >= 6 && hour <= 11) adjusted = hour + 12;
    else if (hour === 12) adjusted = 0;
  }
  if (adjusted < 0 || adjusted > 23) return null;
  return { hour: adjusted, minute };
}

function parseChineseTimePart(raw: string): { hour: number; minute: number } | null {
  const cleaned = raw.trim().replace(/：/gu, ":");
  if (!cleaned) {
    return { hour: 9, minute: 0 };
  }
  const periodMatch = /^(凌晨|早上|早晨|上午|中午|下午|傍晚|晚上|夜间|夜里)?\s*(.+)$/u.exec(cleaned);
  if (!periodMatch) return null;
  const periodToken = periodMatch[1] ?? "";
  const rest = periodMatch[2]!.trim();
  const period: DayPeriod =
    periodToken === "凌晨" ? "dawn"
      : periodToken === "早上" || periodToken === "早晨" || periodToken === "上午" ? "morning"
        : periodToken === "中午" ? "noon"
          : periodToken === "下午" || periodToken === "傍晚" ? "afternoon"
            : periodToken === "晚上" || periodToken === "夜间" || periodToken === "夜里" ? "night"
              : null;

  const colonMatch = /^(\d{1,2}):(\d{1,2})(?:\s*(am|pm))?$/iu.exec(rest);
  if (colonMatch) {
    const rawHour = Number.parseInt(colonMatch[1]!, 10);
    const rawMinute = Number.parseInt(colonMatch[2]!, 10);
    const ampm = colonMatch[3]?.toLowerCase() as "am" | "pm" | undefined;
    return resolve24Hour(rawHour, rawMinute, ampm ?? period);
  }

  const zhClockMatch = /^([零〇一二两三四五六七八九十\d]+)\s*(?:点|时)(?:\s*(半|整|[零〇一二两三四五六七八九十\d]{1,3}\s*分?))?$/u.exec(rest);
  if (zhClockMatch) {
    const rawHour = parseChineseOrArabicInt(zhClockMatch[1]!);
    if (rawHour === null) return null;
    const minToken = (zhClockMatch[2] ?? "").replace(/分$/u, "").trim();
    let rawMinute = 0;
    if (minToken === "半") rawMinute = 30;
    else if (minToken === "" || minToken === "整") rawMinute = 0;
    else {
      const parsedMin = parseChineseOrArabicInt(minToken.replace(/^零/u, ""));
      if (parsedMin === null) return null;
      rawMinute = parsedMin;
    }
    return resolve24Hour(rawHour, rawMinute, period);
  }
  return null;
}

function parseEnglishTimePart(raw: string): { hour: number; minute: number } | null {
  const cleaned = raw.trim().replace(/^at\s+/iu, "").trim();
  if (!cleaned) return { hour: 9, minute: 0 };
  if (/^noon$/iu.test(cleaned)) return { hour: 12, minute: 0 };
  if (/^midnight$/iu.test(cleaned)) return { hour: 0, minute: 0 };
  const match = /^(\d{1,2})(?::(\d{2}))?\s*(am|pm|a\.m\.|p\.m\.)?$/iu.exec(cleaned);
  if (!match) return null;
  const rawHour = Number.parseInt(match[1]!, 10);
  const rawMinute = match[2] ? Number.parseInt(match[2], 10) : 0;
  const meridiemRaw = match[3]?.toLowerCase().replace(/\./gu, "");
  const period: DayPeriod = meridiemRaw === "am" ? "am" : meridiemRaw === "pm" ? "pm" : null;
  if (period && (rawHour < 1 || rawHour > 12)) return null;
  return resolve24Hour(rawHour, rawMinute, period);
}

function buildDateAtLocalTime(base: Date, daysOffset: number, hour: number, minute: number): Date {
  const next = new Date(base.getTime());
  next.setDate(next.getDate() + daysOffset);
  next.setHours(hour, minute, 0, 0);
  return next;
}

function nextDailyTriggers(now: Date, hour: number, minute: number, count = 3): Date[] {
  const results: Date[] = [];
  let offset = 0;
  const firstCandidate = buildDateAtLocalTime(now, 0, hour, minute);
  if (firstCandidate.getTime() <= now.getTime()) {
    offset = 1;
  }
  for (let i = 0; i < count; i += 1) {
    results.push(buildDateAtLocalTime(now, offset + i, hour, minute));
  }
  return results;
}

function nextWeekdayTriggers(now: Date, hour: number, minute: number, count = 3): Date[] {
  const results: Date[] = [];
  let offset = 0;
  while (results.length < count && offset < 30) {
    const candidate = buildDateAtLocalTime(now, offset, hour, minute);
    const day = candidate.getDay();
    if (day >= 1 && day <= 5 && candidate.getTime() > now.getTime()) {
      results.push(candidate);
    }
    offset += 1;
  }
  return results;
}

function nextWeeklyTriggers(now: Date, targetWeekday: number, hour: number, minute: number, count = 3): Date[] {
  const currentDay = now.getDay();
  let daysUntil = (targetWeekday - currentDay + 7) % 7;
  let first = buildDateAtLocalTime(now, daysUntil, hour, minute);
  if (first.getTime() <= now.getTime()) {
    daysUntil += 7;
    first = buildDateAtLocalTime(now, daysUntil, hour, minute);
  }
  const results: Date[] = [first];
  for (let i = 1; i < count; i += 1) {
    results.push(buildDateAtLocalTime(now, daysUntil + i * 7, hour, minute));
  }
  return results;
}

function clampDeadline(now: Date, anchor: Date, desiredMs: number): Date {
  const maxAllowed = now.getTime() + MAX_DEADLINE_MS;
  const minAllowed = anchor.getTime() + 60 * 60 * 1_000;
  const target = Math.min(Math.max(anchor.getTime() + desiredMs, minAllowed), maxAllowed);
  return new Date(target);
}

function makePeriodicResult(params: {
  raw: string;
  cadence: Exclude<NaturalScheduleCadence, "once">;
  intervalMinutes: number;
  triggers: Date[];
  now: Date;
  timezone: string;
  windowDays: number;
  suggestedMaxRounds: number;
  summary: { zh: string; en: string };
}): NaturalScheduleResult {
  const { raw, cadence, intervalMinutes, triggers, now, timezone, windowDays, suggestedMaxRounds, summary } = params;
  if (!Number.isSafeInteger(intervalMinutes) || intervalMinutes < 1 || intervalMinutes > MAX_INTERVAL_MINUTES) {
    return {
      ok: false,
      raw,
      error: {
        zh: "定时间隔需在 1 分钟到 30 天之间。",
        en: "Schedule interval must be between 1 minute and 30 days.",
      },
      suggestions: NATURAL_SCHEDULE_PRESETS,
    };
  }
  const anchor = triggers[0]!;
  const deadline = clampDeadline(now, anchor, windowDays * 24 * 60 * 60 * 1_000);
  const anchorAt = anchor.toISOString();
  const intervalSeconds = intervalMinutes * 60;
  return {
    ok: true,
    raw,
    kind: "periodic",
    cadence,
    intervalMinutes,
    intervalSeconds,
    anchorAt,
    deadlineAt: deadline.toISOString(),
    suggestedMaxRounds: Math.min(Math.max(1, suggestedMaxRounds), 1_000),
    timezone,
    nextTriggers: triggers.map((item) => item.toISOString()),
    summary,
    scheduleRequest: {
      kind: "periodic",
      timezone,
      anchor_at: anchorAt,
      misfire_policy: "run_once",
      interval_seconds: intervalSeconds,
    },
  };
}

function makeOnceResult(params: {
  raw: string;
  trigger: Date;
  now: Date;
  timezone: string;
  summary: { zh: string; en: string };
}): NaturalScheduleResult {
  const { raw, trigger, now, timezone, summary } = params;
  if (trigger.getTime() <= now.getTime()) {
    return {
      ok: false,
      raw,
      error: {
        zh: "单次触发时间需要晚于当前时间，例如“明天下午 3 点”或“30 分钟后”。",
        en: "One-time schedule must be in the future, e.g. \"tomorrow at 3pm\" or \"in 30 minutes\".",
      },
      suggestions: NATURAL_SCHEDULE_PRESETS,
    };
  }
  const deadline = clampDeadline(now, trigger, 24 * 60 * 60 * 1_000);
  const anchorAt = trigger.toISOString();
  return {
    ok: true,
    raw,
    kind: "once",
    cadence: "once",
    anchorAt,
    deadlineAt: deadline.toISOString(),
    suggestedMaxRounds: 1,
    timezone,
    nextTriggers: [anchorAt],
    summary,
    scheduleRequest: {
      kind: "once",
      timezone,
      anchor_at: anchorAt,
      misfire_policy: "run_once",
    },
  };
}

export function parseNaturalSchedule(
  input: string,
  options?: { now?: Date; timezone?: string },
): NaturalScheduleResult {
  const raw = input.trim();
  const now = options?.now ? new Date(options.now.getTime()) : new Date();
  const timezone = options?.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

  if (!raw) {
    return {
      ok: false,
      raw,
      error: {
        zh: "请输入自然语言时间描述，例如“每天早上 9 点”、“每 30 分钟”或“每周一 10:00”。",
        en: "Enter a natural-language schedule such as \"every day at 9am\", \"every 30 minutes\", or \"every monday at 10:00\".",
      },
      suggestions: NATURAL_SCHEDULE_PRESETS,
    };
  }

  // Detect raw 5-field / 6-field Cron expressions and guide the user to natural language
  if (/^(?:[\d*/,-]+\s+){4,5}[\d*/,-]+$/u.test(raw)) {
    return {
      ok: false,
      raw,
      error: {
        zh: "无需编写 Cron 表达式，请直接输入自然语言（如“每天早上 9 点”或“每 30 分钟”）。",
        en: "No Cron syntax needed — describe the schedule in plain words (e.g. \"every day at 9am\" or \"every 30 minutes\").",
      },
      suggestions: NATURAL_SCHEDULE_PRESETS,
    };
  }

  const normalized = raw.replace(/\s+/gu, " ").replace(/：/gu, ":");

  // 1. Chinese minute/hour intervals: "每 30 分钟", "每隔15分钟", "每半小时", "每 2 小时", "每小时"
  if (/^每(?:隔)?\s*半\s*个?\s*小时$/u.test(normalized)) {
    const triggers = [1, 2, 3].map((step) => new Date(now.getTime() + step * 30 * 60_000));
    return makePeriodicResult({
      raw,
      cadence: "minutes",
      intervalMinutes: 30,
      triggers,
      now,
      timezone,
      windowDays: 14,
      suggestedMaxRounds: 48,
      summary: { zh: "每 30 分钟执行一次", en: "Every 30 minutes" },
    });
  }

  const zhMinuteInterval = /^每(?:隔)?\s*([零〇一二两三四五六七八九十\d]+)\s*分钟$/u.exec(normalized);
  if (zhMinuteInterval) {
    const minutes = parseChineseOrArabicInt(zhMinuteInterval[1]!);
    if (minutes !== null && minutes > 0) {
      const triggers = [1, 2, 3].map((step) => new Date(now.getTime() + step * minutes * 60_000));
      return makePeriodicResult({
        raw,
        cadence: "minutes",
        intervalMinutes: minutes,
        triggers,
        now,
        timezone,
        windowDays: 14,
        suggestedMaxRounds: Math.min(200, Math.max(12, Math.floor((7 * 24 * 60) / minutes))),
        summary: { zh: `每 ${minutes} 分钟执行一次`, en: `Every ${minutes} minute${minutes === 1 ? "" : "s"}` },
      });
    }
  }

  if (/^每(?:隔)?\s*(?:一|1)?\s*个?\s*小时$/u.test(normalized)) {
    const triggers = [1, 2, 3].map((step) => new Date(now.getTime() + step * 60 * 60_000));
    return makePeriodicResult({
      raw,
      cadence: "hours",
      intervalMinutes: 60,
      triggers,
      now,
      timezone,
      windowDays: 30,
      suggestedMaxRounds: 72,
      summary: { zh: "每 1 小时执行一次", en: "Every 1 hour" },
    });
  }

  const zhHourInterval = /^每(?:隔)?\s*([零〇一二两三四五六七八九十\d]+)\s*个?\s*小时$/u.exec(normalized);
  if (zhHourInterval) {
    const hours = parseChineseOrArabicInt(zhHourInterval[1]!);
    if (hours !== null && hours > 0) {
      const intervalMinutes = hours * 60;
      const triggers = [1, 2, 3].map((step) => new Date(now.getTime() + step * intervalMinutes * 60_000));
      return makePeriodicResult({
        raw,
        cadence: "hours",
        intervalMinutes,
        triggers,
        now,
        timezone,
        windowDays: 30,
        suggestedMaxRounds: Math.min(180, Math.max(14, Math.floor((30 * 24) / hours))),
        summary: { zh: `每 ${hours} 小时执行一次`, en: `Every ${hours} hour${hours === 1 ? "" : "s"}` },
      });
    }
  }

  // 2. English minute/hour intervals: "every 45 minutes", "every hour", "every 2 hours", "hourly"
  if (/^(?:hourly|every\s+hour)$/iu.test(normalized)) {
    const triggers = [1, 2, 3].map((step) => new Date(now.getTime() + step * 60 * 60_000));
    return makePeriodicResult({
      raw,
      cadence: "hours",
      intervalMinutes: 60,
      triggers,
      now,
      timezone,
      windowDays: 30,
      suggestedMaxRounds: 72,
      summary: { zh: "每 1 小时执行一次", en: "Every 1 hour" },
    });
  }

  const enInterval = /^every\s+(\d+)\s*(m|min|mins|minute|minutes|h|hr|hrs|hour|hours)$/iu.exec(normalized);
  if (enInterval) {
    const amount = Number.parseInt(enInterval[1]!, 10);
    const unit = enInterval[2]!.toLowerCase();
    const isHour = unit.startsWith("h");
    if (amount > 0) {
      const intervalMinutes = isHour ? amount * 60 : amount;
      const triggers = [1, 2, 3].map((step) => new Date(now.getTime() + step * intervalMinutes * 60_000));
      return makePeriodicResult({
        raw,
        cadence: isHour ? "hours" : "minutes",
        intervalMinutes,
        triggers,
        now,
        timezone,
        windowDays: isHour ? 30 : 14,
        suggestedMaxRounds: isHour ? 60 : 96,
        summary: isHour
          ? { zh: `每 ${amount} 小时执行一次`, en: `Every ${amount} hour${amount === 1 ? "" : "s"}` }
          : { zh: `每 ${amount} 分钟执行一次`, en: `Every ${amount} minute${amount === 1 ? "" : "s"}` },
      });
    }
  }

  // 3. Chinese weekdays: "工作日早上 9:30", "每个工作日下午 6 点", "周一到周五 09:00"
  const zhWeekdays = /^(?:每\s*个?\s*工作日|工作日|每周工作日|周一(?:至|到)周五)\s*(.*)$/u.exec(normalized);
  if (zhWeekdays) {
    const parsedTime = parseChineseTimePart(zhWeekdays[1]!);
    if (parsedTime) {
      const timeText = `${pad2(parsedTime.hour)}:${pad2(parsedTime.minute)}`;
      const triggers = nextWeekdayTriggers(now, parsedTime.hour, parsedTime.minute, 3);
      return makePeriodicResult({
        raw,
        cadence: "weekdays",
        intervalMinutes: 24 * 60,
        triggers,
        now,
        timezone,
        windowDays: 45,
        suggestedMaxRounds: 30,
        summary: { zh: `工作日 ${timeText} 执行`, en: `Weekdays at ${timeText}` },
      });
    }
  }

  // 4. English weekdays: "weekdays at 6pm", "every weekday at 9:30am", "workdays 09:30"
  const enWeekdays = /^(?:every\s+)?(?:weekday|weekdays|workday|workdays|mon\s*-\s*fri)(?:\s+(?:at\s+)?(.+))?$/iu.exec(normalized);
  if (enWeekdays) {
    const parsedTime = parseEnglishTimePart(enWeekdays[1] ?? "");
    if (parsedTime) {
      const timeText = `${pad2(parsedTime.hour)}:${pad2(parsedTime.minute)}`;
      const triggers = nextWeekdayTriggers(now, parsedTime.hour, parsedTime.minute, 3);
      return makePeriodicResult({
        raw,
        cadence: "weekdays",
        intervalMinutes: 24 * 60,
        triggers,
        now,
        timezone,
        windowDays: 45,
        suggestedMaxRounds: 30,
        summary: { zh: `工作日 ${timeText} 执行`, en: `Weekdays at ${timeText}` },
      });
    }
  }

  // 5. Chinese weekly: "每周一 10:00", "每周五下午 5 点", "礼拜三 14:30", "周二早上 9 点"
  const zhWeekly = /^(?:每\s*(?:周|星期|礼拜)|(?:周|星期|礼拜))([一二三四五六日天])\s*(.*)$/u.exec(normalized);
  if (zhWeekly) {
    const weekday = ZH_WEEKDAY_MAP[zhWeekly[1]!];
    const parsedTime = parseChineseTimePart(zhWeekly[2]!);
    if (weekday !== undefined && parsedTime) {
      const timeText = `${pad2(parsedTime.hour)}:${pad2(parsedTime.minute)}`;
      const triggers = nextWeeklyTriggers(now, weekday, parsedTime.hour, parsedTime.minute, 3);
      return makePeriodicResult({
        raw,
        cadence: "weekly",
        intervalMinutes: 7 * 24 * 60,
        triggers,
        now,
        timezone,
        windowDays: 60,
        suggestedMaxRounds: 12,
        summary: {
          zh: `每${WEEKDAY_NAMES_ZH[weekday]} ${timeText} 执行`,
          en: `Every ${WEEKDAY_NAMES_EN[weekday]} at ${timeText}`,
        },
      });
    }
  }

  // 6. English weekly: "every monday at 10:00", "mondays at 10am", "weekly on fri at 5pm"
  const enWeekly = /^(?:every\s+|weekly\s+(?:on\s+)?)?(sun(?:day)?s?|mon(?:day)?s?|tue(?:s|sday)?s?|wed(?:nesday)?s?|thu(?:r|rs|rsday)?s?|fri(?:day)?s?|sat(?:urday)?s?)(?:\s+(?:at\s+)?(.+))?$/iu.exec(normalized);
  if (enWeekly) {
    const key = enWeekly[1]!.toLowerCase();
    const weekday = EN_WEEKDAY_MAP[key];
    const parsedTime = parseEnglishTimePart(enWeekly[2] ?? "");
    if (weekday !== undefined && parsedTime) {
      const timeText = `${pad2(parsedTime.hour)}:${pad2(parsedTime.minute)}`;
      const triggers = nextWeeklyTriggers(now, weekday, parsedTime.hour, parsedTime.minute, 3);
      return makePeriodicResult({
        raw,
        cadence: "weekly",
        intervalMinutes: 7 * 24 * 60,
        triggers,
        now,
        timezone,
        windowDays: 60,
        suggestedMaxRounds: 12,
        summary: {
          zh: `每${WEEKDAY_NAMES_ZH[weekday]} ${timeText} 执行`,
          en: `Every ${WEEKDAY_NAMES_EN[weekday]} at ${timeText}`,
        },
      });
    }
  }

  // 7. Chinese daily: "每天早上 9 点", "每天 18:30", "每日下午 3 点"
  const zhDaily = /^(?:每天|每日|天天)\s*(.*)$/u.exec(normalized);
  if (zhDaily) {
    const parsedTime = parseChineseTimePart(zhDaily[1]!);
    if (parsedTime) {
      const timeText = `${pad2(parsedTime.hour)}:${pad2(parsedTime.minute)}`;
      const triggers = nextDailyTriggers(now, parsedTime.hour, parsedTime.minute, 3);
      return makePeriodicResult({
        raw,
        cadence: "daily",
        intervalMinutes: 24 * 60,
        triggers,
        now,
        timezone,
        windowDays: 30,
        suggestedMaxRounds: 30,
        summary: { zh: `每天 ${timeText} 执行`, en: `Every day at ${timeText}` },
      });
    }
  }

  // 8. English daily: "every day at 9am", "daily at 18:30", "everyday 9:00"
  const enDaily = /^(?:every\s*day|daily)(?:\s+(?:at\s+)?(.+))?$/iu.exec(normalized);
  if (enDaily) {
    const parsedTime = parseEnglishTimePart(enDaily[1] ?? "");
    if (parsedTime) {
      const timeText = `${pad2(parsedTime.hour)}:${pad2(parsedTime.minute)}`;
      const triggers = nextDailyTriggers(now, parsedTime.hour, parsedTime.minute, 3);
      return makePeriodicResult({
        raw,
        cadence: "daily",
        intervalMinutes: 24 * 60,
        triggers,
        now,
        timezone,
        windowDays: 30,
        suggestedMaxRounds: 30,
        summary: { zh: `每天 ${timeText} 执行`, en: `Every day at ${timeText}` },
      });
    }
  }

  // 9. One-shot relative Chinese: "30 分钟后", "2 小时后", "半小时后"
  if (/^半\s*个?\s*小时(?:后|之后|以后)$/u.test(normalized)) {
    const trigger = new Date(now.getTime() + 30 * 60_000);
    return makeOnceResult({
      raw,
      trigger,
      now,
      timezone,
      summary: { zh: "单次执行 · 30 分钟后", en: "One-time · in 30 minutes" },
    });
  }
  const zhRelative = /^([零〇一二两三四五六七八九十\d]+)\s*(分钟|个?小时|天)(?:后|之后|以后)$/u.exec(normalized);
  if (zhRelative) {
    const amount = parseChineseOrArabicInt(zhRelative[1]!);
    const unit = zhRelative[2]!;
    if (amount !== null && amount > 0) {
      const ms = unit === "分钟" ? amount * 60_000 : unit.endsWith("小时") ? amount * 3_600_000 : amount * 86_400_000;
      const trigger = new Date(now.getTime() + ms);
      const unitZh = unit === "分钟" ? "分钟" : unit.endsWith("小时") ? "小时" : "天";
      const unitEn = unit === "分钟" ? "minute" : unit.endsWith("小时") ? "hour" : "day";
      return makeOnceResult({
        raw,
        trigger,
        now,
        timezone,
        summary: {
          zh: `单次执行 · ${amount} ${unitZh}后`,
          en: `One-time · in ${amount} ${unitEn}${amount === 1 ? "" : "s"}`,
        },
      });
    }
  }

  // 10. One-shot calendar Chinese: "今天晚上 8 点", "明天下午 3 点", "后天早上 9:30"
  const zhOnceDay = /^(今天|今日|明天|明日|后天)\s*(.*)$/u.exec(normalized);
  if (zhOnceDay) {
    const dayWord = zhOnceDay[1]!;
    const dayOffset = dayWord === "今天" || dayWord === "今日" ? 0 : dayWord === "明天" || dayWord === "明日" ? 1 : 2;
    const parsedTime = parseChineseTimePart(zhOnceDay[2]!);
    if (parsedTime) {
      const trigger = buildDateAtLocalTime(now, dayOffset, parsedTime.hour, parsedTime.minute);
      const timeText = `${pad2(parsedTime.hour)}:${pad2(parsedTime.minute)}`;
      const dayLabelZh = dayOffset === 0 ? "今天" : dayOffset === 1 ? "明天" : "后天";
      const dayLabelEn = dayOffset === 0 ? "today" : dayOffset === 1 ? "tomorrow" : "in 2 days";
      return makeOnceResult({
        raw,
        trigger,
        now,
        timezone,
        summary: {
          zh: `单次执行 · ${dayLabelZh} ${timeText}`,
          en: `One-time · ${dayLabelEn} at ${timeText}`,
        },
      });
    }
  }

  // 11. One-shot English: "tomorrow at 3pm", "today at 8pm", "in 30 minutes", "in 2 hours"
  const enRelative = /^in\s+(\d+)\s*(m|min|mins|minute|minutes|h|hr|hrs|hour|hours|d|day|days)$/iu.exec(normalized);
  if (enRelative) {
    const amount = Number.parseInt(enRelative[1]!, 10);
    const unit = enRelative[2]!.toLowerCase();
    if (amount > 0) {
      const isMin = unit.startsWith("m");
      const isHour = unit.startsWith("h");
      const ms = isMin ? amount * 60_000 : isHour ? amount * 3_600_000 : amount * 86_400_000;
      const trigger = new Date(now.getTime() + ms);
      const unitZh = isMin ? "分钟" : isHour ? "小时" : "天";
      const unitEn = isMin ? "minute" : isHour ? "hour" : "day";
      return makeOnceResult({
        raw,
        trigger,
        now,
        timezone,
        summary: {
          zh: `单次执行 · ${amount} ${unitZh}后`,
          en: `One-time · in ${amount} ${unitEn}${amount === 1 ? "" : "s"}`,
        },
      });
    }
  }

  const enOnceDay = /^(today|tomorrow)(?:\s+(?:at\s+)?(.+))?$/iu.exec(normalized);
  if (enOnceDay) {
    const dayOffset = enOnceDay[1]!.toLowerCase() === "today" ? 0 : 1;
    const parsedTime = parseEnglishTimePart(enOnceDay[2] ?? "");
    if (parsedTime) {
      const trigger = buildDateAtLocalTime(now, dayOffset, parsedTime.hour, parsedTime.minute);
      const timeText = `${pad2(parsedTime.hour)}:${pad2(parsedTime.minute)}`;
      return makeOnceResult({
        raw,
        trigger,
        now,
        timezone,
        summary: {
          zh: `单次执行 · ${dayOffset === 0 ? "今天" : "明天"} ${timeText}`,
          en: `One-time · ${dayOffset === 0 ? "today" : "tomorrow"} at ${timeText}`,
        },
      });
    }
  }

  return {
    ok: false,
    raw,
    error: {
      zh: "暂未识别该时间表达式。可尝试“每天早上 9 点”、“工作日早上 9:30”、“每周一 10:00”、“每 30 分钟”或“明天下午 3 点”。",
      en: "Could not parse schedule. Try \"every day at 9am\", \"weekdays at 6pm\", \"every monday at 10:00\", \"every 45 minutes\", or \"tomorrow at 3pm\".",
    },
    suggestions: NATURAL_SCHEDULE_PRESETS,
  };
}

export function describeExistingSchedule(
  schedule: { kind: string; anchor_at: string; interval_seconds?: number },
  locale: string,
  t: (zh: string, en: string) => string,
): string {
  const formattedAnchor = new Intl.DateTimeFormat(locale, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(schedule.anchor_at));

  if (schedule.kind === "once" || !schedule.interval_seconds) {
    return `${t("单次计划", "One-time")} · ${formattedAnchor}`;
  }
  const minutes = Math.round(schedule.interval_seconds / 60);
  if (minutes % (7 * 24 * 60) === 0) {
    const weeks = minutes / (7 * 24 * 60);
    return weeks === 1
      ? `${t("每周定期", "Weekly")} · ${formattedAnchor}`
      : `${t(`每 ${weeks} 周`, `Every ${weeks} weeks`)} · ${formattedAnchor}`;
  }
  if (minutes % (24 * 60) === 0) {
    const days = minutes / (24 * 60);
    return days === 1
      ? `${t("每天定期", "Daily")} · ${formattedAnchor}`
      : `${t(`每 ${days} 天`, `Every ${days} days`)} · ${formattedAnchor}`;
  }
  if (minutes % 60 === 0) {
    const hours = minutes / 60;
    return `${t(`每 ${hours} 小时`, `Every ${hours} hr${hours === 1 ? "" : "s"}`)} · ${formattedAnchor}`;
  }
  return `${t(`每 ${minutes} 分钟`, `Every ${minutes} min`)} · ${formattedAnchor}`;
}

export function compileScheduledJobRequest(params: {
  parsed: ParsedNaturalSchedule;
  mode: "thread" | "workflow" | "observation";
  executionTarget?: ScheduledJobCreateRequestView["execution_target"];
  maxRounds?: number;
  notification?: ScheduledJobCreateRequestView["notification"];
  now?: Date;
}): ScheduledJobCreateRequestView {
  const nowMs = (params.now ?? new Date()).getTime();
  const deadlineMs = new Date(params.parsed.deadlineAt).getTime();
  const maxElapsedSeconds = Math.min(
    90 * 24 * 60 * 60,
    Math.max(60, Math.floor((deadlineMs - nowMs) / 1_000)),
  );
  const rounds = Math.min(1_000, Math.max(1, Math.trunc(params.maxRounds ?? params.parsed.suggestedMaxRounds)));
  const isObservation = params.mode === "observation";

  return {
    version: "scheduled-job.v1",
    schedule: params.parsed.scheduleRequest,
    deadline_at: params.parsed.deadlineAt,
    stop_on_target_terminal: isObservation,
    max_rounds: rounds,
    max_model_calls: 0,
    max_elapsed_seconds: maxElapsedSeconds,
    retry: {
      max_attempts: 3,
      initial_backoff_seconds: 5,
      max_backoff_seconds: 60,
    },
    notification: params.notification ?? "on_change",
    execution_mode: isObservation ? "read_only" : "automatic",
    confirm_repair: false,
    observation_consent_version: isObservation ? 1 : 0,
    ...(isObservation || !params.executionTarget
      ? {}
      : {
          execution_target: params.executionTarget,
          execution_consent_version: 1,
        }),
  };
}
