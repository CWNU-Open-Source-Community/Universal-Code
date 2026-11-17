import { describe, expect, it } from "vitest";
import {
  compileScheduledJobRequest,
  describeExistingSchedule,
  NATURAL_SCHEDULE_PRESETS,
  parseNaturalSchedule,
} from "./automation-nl-schedule";

// Fixed reference point: Friday, 2026-11-20 10:15:00 local time
function localRefDate(
  year = 2026,
  month = 11,
  day = 20,
  hour = 10,
  minute = 15,
): Date {
  return new Date(year, month - 1, day, hour, minute, 0, 0);
}

describe("automation-nl-schedule", () => {
  it("parses Chinese daily, minute, hour, and weekly expressions with next-3 trigger previews", () => {
    const now = localRefDate(2026, 11, 20, 10, 15); // Friday 10:15

    // "每天早上 9 点" -> 09:00 already passed today (10:15), so starts tomorrow 09:00
    const morning = parseNaturalSchedule("每天早上 9 点", { now, timezone: "Asia/Shanghai" });
    expect(morning.ok).toBe(true);
    if (morning.ok) {
      expect(morning.kind).toBe("periodic");
      expect(morning.cadence).toBe("daily");
      expect(morning.intervalMinutes).toBe(1440);
      expect(morning.intervalSeconds).toBe(86_400);
      expect(morning.nextTriggers).toHaveLength(3);
      const first = new Date(morning.anchorAt);
      expect(first.getDate()).toBe(21);
      expect(first.getHours()).toBe(9);
      expect(first.getMinutes()).toBe(0);
      expect(morning.summary.zh).toContain("每天 09:00");
    }

    // "每天 18:30" -> still in future today, so starts today 18:30
    const evening = parseNaturalSchedule("每天 18:30", { now });
    expect(evening.ok).toBe(true);
    if (evening.ok) {
      expect(evening.intervalMinutes).toBe(1440);
      const first = new Date(evening.anchorAt);
      expect(first.getDate()).toBe(20);
      expect(first.getHours()).toBe(18);
      expect(first.getMinutes()).toBe(30);
      expect(evening.nextTriggers).toHaveLength(3);
    }

    // "每 30 分钟"
    const every30m = parseNaturalSchedule("每 30 分钟", { now });
    expect(every30m.ok).toBe(true);
    if (every30m.ok) {
      expect(every30m.intervalMinutes).toBe(30);
      expect(new Date(every30m.nextTriggers[0]!).getTime() - now.getTime()).toBe(30 * 60_000);
      expect(new Date(every30m.nextTriggers[1]!).getTime() - now.getTime()).toBe(60 * 60_000);
      expect(new Date(every30m.nextTriggers[2]!).getTime() - now.getTime()).toBe(90 * 60_000);
    }

    // "每 2 小时" & Chinese numerals "每两小时"
    for (const expr of ["每 2 小时", "每两小时"]) {
      const every2h = parseNaturalSchedule(expr, { now });
      expect(every2h.ok).toBe(true);
      if (every2h.ok) {
        expect(every2h.intervalMinutes).toBe(120);
        expect(new Date(every2h.anchorAt).getTime() - now.getTime()).toBe(120 * 60_000);
      }
    }

    // "每周一 10:00" from Friday Nov 20 -> Monday Nov 23 10:00
    const monday = parseNaturalSchedule("每周一 10:00", { now });
    expect(monday.ok).toBe(true);
    if (monday.ok) {
      expect(monday.cadence).toBe("weekly");
      expect(monday.intervalMinutes).toBe(10_080);
      const first = new Date(monday.anchorAt);
      expect(first.getDay()).toBe(1);
      expect(first.getDate()).toBe(23);
      expect(first.getHours()).toBe(10);
      expect(monday.nextTriggers).toHaveLength(3);
    }
  });

  it("skips weekend days when calculating weekday trigger previews in both Chinese and English", () => {
    // Friday Nov 20 at 10:15 -> "工作日早上 9:30" has passed Friday 09:30, so next 3 must be Mon Nov 23, Tue Nov 24, Wed Nov 25!
    const now = localRefDate(2026, 11, 20, 10, 15);
    const zhWeekday = parseNaturalSchedule("工作日早上 9:30", { now });
    expect(zhWeekday.ok).toBe(true);
    if (zhWeekday.ok) {
      expect(zhWeekday.cadence).toBe("weekdays");
      const days = zhWeekday.nextTriggers.map((iso) => new Date(iso).getDay());
      expect(days).toEqual([1, 2, 3]);
    }

    const enWeekday = parseNaturalSchedule("weekdays at 6pm", { now });
    expect(enWeekday.ok).toBe(true);
    if (enWeekday.ok) {
      // Friday 18:00 is still ahead, then Monday 18:00, Tuesday 18:00
      const days = enWeekday.nextTriggers.map((iso) => new Date(iso).getDay());
      expect(days).toEqual([5, 1, 2]);
      expect(new Date(enWeekday.anchorAt).getHours()).toBe(18);
    }
  });

  it("parses English daily, interval, weekly, and one-shot expressions", () => {
    const now = localRefDate(2026, 11, 20, 8, 0);

    const daily9 = parseNaturalSchedule("every day at 9am", { now });
    expect(daily9.ok).toBe(true);
    if (daily9.ok) {
      expect(daily9.intervalMinutes).toBe(1440);
      expect(new Date(daily9.anchorAt).getDate()).toBe(20);
      expect(new Date(daily9.anchorAt).getHours()).toBe(9);
    }

    const every45 = parseNaturalSchedule("every 45 minutes", { now });
    expect(every45.ok).toBe(true);
    if (every45.ok) {
      expect(every45.intervalMinutes).toBe(45);
      expect(every45.scheduleRequest.interval_seconds).toBe(2700);
    }

    const weeklyMon = parseNaturalSchedule("every monday at 10:00", { now });
    expect(weeklyMon.ok).toBe(true);
    if (weeklyMon.ok) {
      expect(weeklyMon.intervalMinutes).toBe(10_080);
      expect(new Date(weeklyMon.anchorAt).getDay()).toBe(1);
    }

    const onceZh = parseNaturalSchedule("明天下午 3 点", { now });
    expect(onceZh.ok).toBe(true);
    if (onceZh.ok) {
      expect(onceZh.kind).toBe("once");
      expect(onceZh.intervalMinutes).toBeUndefined();
      expect(onceZh.nextTriggers).toHaveLength(1);
      const dt = new Date(onceZh.anchorAt);
      expect(dt.getDate()).toBe(21);
      expect(dt.getHours()).toBe(15);
    }

    const onceEn = parseNaturalSchedule("tomorrow at 3pm", { now });
    expect(onceEn.ok).toBe(true);
    if (onceEn.ok) {
      expect(onceEn.kind).toBe("once");
      expect(new Date(onceEn.anchorAt).getHours()).toBe(15);
    }
  });

  it("compiles valid ScheduledJobCreateRequestView payloads and preset chips", () => {
    const now = localRefDate(2026, 11, 20, 10, 0);
    for (const preset of NATURAL_SCHEDULE_PRESETS) {
      const zhParsed = parseNaturalSchedule(preset.expression.zh, { now });
      const enParsed = parseNaturalSchedule(preset.expression.en, { now });
      expect(zhParsed.ok).toBe(true);
      expect(enParsed.ok).toBe(true);
      if (zhParsed.ok) {
        const compiled = compileScheduledJobRequest({
          parsed: zhParsed,
          mode: "thread",
          executionTarget: { kind: "thread", thread_id: "thread-1", prompt: "Run check" },
          now,
        });
        expect(compiled.version).toBe("scheduled-job.v1");
        expect(compiled.execution_mode).toBe("automatic");
        expect(compiled.execution_consent_version).toBe(1);
        expect(compiled.max_elapsed_seconds).toBeGreaterThan(0);
        expect(compiled.max_elapsed_seconds).toBeLessThanOrEqual(90 * 24 * 60 * 60);
        expect(new Date(compiled.deadline_at).getTime()).toBeGreaterThan(
          new Date(compiled.schedule.anchor_at).getTime(),
        );
      }
    }

    const label = describeExistingSchedule(
      { kind: "periodic", anchor_at: "2026-11-21T09:00:00Z", interval_seconds: 86_400 },
      "zh-CN",
      (zh) => zh,
    );
    expect(label).toContain("每天定期");
  });

  it("gracefully handles empty input, raw Cron syntax, out-of-range times, and invalid strings", () => {
    const now = localRefDate(2026, 11, 20, 14, 0);

    const empty = parseNaturalSchedule("   ", { now });
    expect(empty.ok).toBe(false);
    if (!empty.ok) {
      expect(empty.suggestions.length).toBeGreaterThan(0);
    }

    const cron = parseNaturalSchedule("0 9 * * 1-5", { now });
    expect(cron.ok).toBe(false);
    if (!cron.ok) {
      expect(cron.error.zh).toContain("无需编写 Cron");
      expect(cron.error.en).toContain("No Cron syntax needed");
    }

    const invalidHour = parseNaturalSchedule("每天 27:99", { now });
    expect(invalidHour.ok).toBe(false);

    const pastToday = parseNaturalSchedule("今天早上 8 点", { now });
    expect(pastToday.ok).toBe(false);
    if (!pastToday.ok) {
      expect(pastToday.error.zh).toContain("晚于当前时间");
    }

    const unknown = parseNaturalSchedule("whenever the moon is blue", { now });
    expect(unknown.ok).toBe(false);
  });
});
