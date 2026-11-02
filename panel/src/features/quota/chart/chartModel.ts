/**
 * Pure logic for the pool chart: scales, ticks, paths, formatting and derived series.
 * No React and no DOM, so all of it is unit tested directly.
 */

import type { PoolAccount, QuotaPoint, QuotaPool } from '@/services/api/tpr';

export const HOUR_MS = 3_600_000;
export const DAY_MS = 24 * HOUR_MS;

export interface ChartBox {
  width: number;
  height: number;
  left: number;
  right: number;
  top: number;
  bottom: number;
}

export interface ChartScales {
  x: (ms: number) => number;
  y: (value: number) => number;
  invertX: (px: number) => number;
  plotLeft: number;
  plotRight: number;
  plotTop: number;
  plotBottom: number;
  from: number;
  to: number;
  max: number;
}

export interface TimedValue {
  at: number;
  value: number | null;
}

export const toTimed = (points: QuotaPoint[]): TimedValue[] =>
  points
    .map((point) => ({ at: Date.parse(point.at), value: point.percent }))
    .filter((point) => Number.isFinite(point.at))
    .sort((a, b) => a.at - b.at);

const clamp = (value: number, min: number, max: number) => Math.min(max, Math.max(min, value));

export const createScales = (
  box: ChartBox,
  from: number,
  to: number,
  domainMax: number
): ChartScales => {
  const plotLeft = box.left;
  const plotRight = Math.max(box.left + 1, box.width - box.right);
  const plotTop = box.top;
  const plotBottom = Math.max(box.top + 1, box.height - box.bottom);
  const span = to > from ? to - from : 1;
  const max = domainMax > 0 ? domainMax : 100;
  return {
    x: (ms) => plotLeft + ((ms - from) / span) * (plotRight - plotLeft),
    // Values are clamped so a stray reading cannot draw outside the plot.
    y: (value) => plotBottom - (clamp(value, 0, max) / max) * (plotBottom - plotTop),
    invertX: (px) => from + ((clamp(px, plotLeft, plotRight) - plotLeft) / (plotRight - plotLeft)) * span,
    plotLeft,
    plotRight,
    plotTop,
    plotBottom,
    from,
    to,
    max,
  };
};

/** Y labels: zero, half, capacity. */
export const yTicks = (max: number): number[] => {
  const top = max > 0 ? max : 100;
  return [0, top / 2, top];
};

export const startOfLocalDay = (ms: number): number => {
  const date = new Date(ms);
  date.setHours(0, 0, 0, 0);
  return date.getTime();
};

export interface DayTick {
  /** Local midnight, where the grid line sits. */
  at: number;
  /** Middle of the day, where the label sits. */
  labelAt: number;
  showLabel: boolean;
}

/** One grid line per day. Labels thin out so they never collide on narrow charts. */
export const dayTicks = (from: number, to: number, plotWidth: number, minLabelPx = 58): DayTick[] => {
  const ticks: DayTick[] = [];
  const cursor = new Date(startOfLocalDay(from));
  while (cursor.getTime() <= to) {
    const at = cursor.getTime();
    const next = new Date(at);
    next.setDate(next.getDate() + 1);
    ticks.push({ at, labelAt: at + (next.getTime() - at) / 2, showLabel: false });
    cursor.setDate(cursor.getDate() + 1);
    if (ticks.length > 400) break;
  }
  const dayPx = plotWidth / Math.max(1, (to - from) / DAY_MS);
  const stride = Math.max(1, Math.ceil(minLabelPx / Math.max(1, dayPx)));
  let shown = 0;
  ticks.forEach((tick) => {
    if (tick.labelAt < from || tick.labelAt > to) return;
    tick.showLabel = shown % stride === 0;
    shown += 1;
  });
  return ticks;
};

/* --------------------------------- formatting ------------------------------- */

const weekdayShort = (ms: number, locale?: string, timeZone?: string) =>
  new Intl.DateTimeFormat(locale, { weekday: 'short', timeZone }).format(ms);

/** "Fri 23" */
export const formatDayTick = (ms: number, locale?: string, timeZone?: string): string => {
  const day = new Intl.DateTimeFormat(locale, { day: 'numeric', timeZone }).format(ms);
  return `${weekdayShort(ms, locale, timeZone)} ${day}`;
};

/** "Sun 12:00 PM" */
export const formatWhen = (ms: number, locale?: string, timeZone?: string): string => {
  const time = new Intl.DateTimeFormat(locale, { hour: 'numeric', minute: '2-digit', timeZone }).format(ms);
  return `${weekdayShort(ms, locale, timeZone)} ${time}`;
};

/** "Oct 23" */
export const formatShortDate = (ms: number, locale?: string, timeZone?: string): string =>
  new Intl.DateTimeFormat(locale, { month: 'short', day: 'numeric', timeZone }).format(ms);

/** "15h 49m", "2d 18h", "5m". Negative or zero durations read as "now". */
export const formatDuration = (ms: number): string => {
  if (!Number.isFinite(ms) || ms <= 0) return 'now';
  const minutes = Math.round(ms / 60_000);
  if (minutes < 1) return 'under 1m';
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) {
    const rest = minutes - hours * 60;
    return rest ? `${hours}h ${rest}m` : `${hours}h`;
  }
  const days = Math.floor(hours / 24);
  const restHours = hours - days * 24;
  return restHours ? `${days}d ${restHours}h` : `${days}d`;
};

/** Whole percents, because the pool numbers read as "555%". */
export const formatPercentValue = (value: number): string => `${Math.round(value)}%`;

export const formatSignedPercent = (value: number): string =>
  `${value >= 0 ? '+' : '-'}${Math.abs(Math.round(value))}%`;

export const formatBurn = (value: number): string => {
  const rounded = Math.round(value * 10) / 10;
  return `${rounded}%/h`;
};

/** Axis labels for the drain chart: tidy tenths, no float noise. */
export const formatAxisBurn = (value: number): string =>
  `${Number((Math.round(value * 10) / 10).toFixed(1))}%/h`;

/* ----------------------------------- paths ---------------------------------- */

const num = (value: number) => Number(value.toFixed(2));

export interface LineSegments {
  /** One subpath per contiguous run; holes in the data break the line. */
  runs: TimedValue[][];
  /** Runs of one reading, drawn as a dot so they are not invisible. */
  isolated: TimedValue[];
}

export const splitRuns = (points: TimedValue[]): LineSegments => {
  const runs: TimedValue[][] = [];
  const isolated: TimedValue[] = [];
  let current: TimedValue[] = [];
  const flush = () => {
    if (current.length === 1) isolated.push(current[0]);
    else if (current.length > 1) runs.push(current);
    current = [];
  };
  points.forEach((point) => {
    if (point.value === null) flush();
    else current.push(point);
  });
  flush();
  return { runs, isolated };
};

export const runPath = (run: TimedValue[], scales: ChartScales): string =>
  run
    .map((point, index) => {
      const command = index === 0 ? 'M' : 'L';
      return `${command}${num(scales.x(point.at))} ${num(scales.y(point.value ?? 0))}`;
    })
    .join(' ');

/** Closes a line down to the baseline for the soft area under it. */
export const areaPath = (run: TimedValue[], scales: ChartScales): string => {
  if (run.length < 2) return '';
  const first = run[0];
  const last = run[run.length - 1];
  return `${runPath(run, scales)} L${num(scales.x(last.at))} ${num(scales.plotBottom)} L${num(scales.x(first.at))} ${num(scales.plotBottom)} Z`;
};

/**
 * Forecast line with resets drawn as true vertical jumps. A rising segment is a reset
 * (usage only ever moves the line down), so any rise becomes a step at its own time.
 */
export const steppedRun = (run: TimedValue[]): TimedValue[] => {
  const stepped: TimedValue[] = [];
  run.forEach((point, index) => {
    const previous = run[index - 1];
    if (
      previous &&
      previous.value !== null &&
      point.value !== null &&
      point.value > previous.value &&
      point.at !== previous.at
    ) {
      stepped.push({ at: point.at, value: previous.value });
    }
    stepped.push(point);
  });
  return stepped;
};

/* ---------------------------------- lookup ---------------------------------- */

export type ValueKind = 'recorded' | 'projected';

export interface ValueAt {
  value: number;
  kind: ValueKind;
}

const interpolate = (points: TimedValue[], at: number): number | null => {
  if (points.length === 0) return null;
  if (at < points[0].at || at > points[points.length - 1].at) return null;
  for (let index = 1; index < points.length; index += 1) {
    const before = points[index - 1];
    const after = points[index];
    if (at >= before.at && at <= after.at) {
      if (before.value === null || after.value === null) return null;
      if (after.at === before.at) return after.value;
      // Rising forecast segments are steps, so hold the lower value until the jump.
      const ratio = (at - before.at) / (after.at - before.at);
      return before.value + (after.value - before.value) * ratio;
    }
  }
  return points[points.length - 1].value;
};

export const valueAtTime = (
  recorded: TimedValue[],
  projected: TimedValue[],
  at: number,
  now: number
): ValueAt | null => {
  if (at <= now) {
    const value = interpolate(recorded, at);
    if (value !== null) return { value, kind: 'recorded' };
    return null;
  }
  const value = interpolate(steppedRun(projected), at);
  return value === null ? null : { value, kind: 'projected' };
};

/* ------------------------------- reset labels ------------------------------- */

export interface ResetLabel {
  key: string;
  x: number;
  y: number;
  text: string;
  projected: boolean;
}

const LABEL_WIDTH = 38;
const LABEL_HEIGHT = 13;

/** Places "+100%" above each jump. A label is lifted, or dropped, rather than overlapped. */
export const layoutResetLabels = (
  resets: Array<{ at: number; deltaPercent: number; projected: boolean; top: number | null }>,
  scales: ChartScales
): ResetLabel[] => {
  const placed: ResetLabel[] = [];
  const collides = (x: number, y: number) =>
    placed.some(
      (label) => Math.abs(label.x - x) < LABEL_WIDTH && Math.abs(label.y - y) < LABEL_HEIGHT
    );
  resets
    .filter((reset) => reset.at >= scales.from && reset.at <= scales.to && reset.deltaPercent !== 0)
    .sort((a, b) => a.at - b.at)
    .forEach((reset, index) => {
      const x = clamp(scales.x(reset.at), scales.plotLeft + LABEL_WIDTH / 2, scales.plotRight - LABEL_WIDTH / 2);
      const base = (reset.top === null ? scales.plotTop + 14 : scales.y(reset.top)) - 7;
      const candidates = [base, base - LABEL_HEIGHT, base + 2 * LABEL_HEIGHT];
      const y = candidates.find(
        (candidate) => candidate >= scales.plotTop - 8 && candidate <= scales.plotBottom - 2 && !collides(x, candidate)
      );
      if (y === undefined) return;
      placed.push({
        key: `${reset.at}-${index}`,
        x,
        y,
        text: formatSignedPercent(reset.deltaPercent),
        projected: reset.projected,
      });
    });
  return placed;
};

/* ------------------------------- derived series ----------------------------- */

/**
 * Burn over time: quota used per hour over a trailing window. Upward moves are resets,
 * not negative usage, so they are excluded; holes contribute nothing.
 */
export const drainSeries = (points: TimedValue[], windowHours: number, sampleCount = 96): TimedValue[] => {
  const known = points.filter((point) => point.value !== null);
  if (known.length < 2) return [];
  const windowMs = windowHours * HOUR_MS;
  const start = known[0].at + windowMs;
  const end = known[known.length - 1].at;
  if (end <= known[0].at) return [];
  const usedBetween = (from: number, to: number) => {
    let used = 0;
    for (let index = 1; index < known.length; index += 1) {
      const before = known[index - 1];
      const after = known[index];
      if (after.at <= from || before.at >= to) continue;
      const drop = (before.value ?? 0) - (after.value ?? 0);
      if (drop <= 0 || after.at === before.at) continue;
      const overlap = Math.min(after.at, to) - Math.max(before.at, from);
      used += drop * (overlap / (after.at - before.at));
    }
    return used;
  };
  const series: TimedValue[] = [];
  const first = Math.min(start, end);
  const steps = Math.max(1, sampleCount - 1);
  for (let index = 0; index <= steps; index += 1) {
    const at = first + ((end - first) * index) / steps;
    const from = Math.max(known[0].at, at - windowMs);
    const hours = (at - from) / HOUR_MS;
    series.push({ at, value: hours > 0 ? usedBetween(from, at) / hours : 0 });
  }
  return series;
};

/** Top of the drain chart's axis: rounded up to a tidy value, never below 1%/h. */
export const niceDrainMax = (values: Array<number | null>): number => {
  const max = values.reduce<number>((top, value) => (value !== null && value > top ? value : top), 0);
  if (max <= 1) return 1;
  const magnitude = 10 ** Math.floor(Math.log10(max));
  const step = magnitude / 2;
  return Math.ceil(max / step) * step;
};

/* --------------------------------- filtering -------------------------------- */

/**
 * Pool view for a subset of accounts. The API aggregates server-side, so a subset is rebuilt
 * from each account's own history: weighted sums for the past, then a simple linear forecast.
 */
export const aggregateAccounts = (input: {
  pool: QuotaPool;
  selected: ReadonlySet<string>;
  histories: Readonly<Record<string, QuotaPoint[]>>;
  now: number;
  to: number;
  burnHours: number;
  futureMultiplier: number;
}): QuotaPool => {
  const { pool, selected, histories, now, to, burnHours, futureMultiplier } = input;
  const accounts: PoolAccount[] = pool.accounts.filter((account) => selected.has(account.id));
  const capacity = accounts.reduce((sum, account) => sum + 100 * account.weight, 0);
  const remaining = accounts.reduce(
    (sum, account) => sum + (account.remainingPercent ?? 0) * account.weight,
    0
  );

  const perAccount = accounts.map((account) => ({
    account,
    points: toTimed(histories[account.id] ?? []).filter((point) => point.value !== null),
  }));
  const times = Array.from(new Set(perAccount.flatMap(({ points }) => points.map((point) => point.at)))).sort(
    (a, b) => a - b
  );
  const cursors = perAccount.map(() => 0);
  const recordedTimed: TimedValue[] = times.map((at) => {
    let total = 0;
    let known = 0;
    perAccount.forEach(({ account, points }, index) => {
      while (cursors[index] + 1 < points.length && points[cursors[index] + 1].at <= at) {
        cursors[index] += 1;
      }
      const point = points[cursors[index]];
      if (point && point.at <= at && point.value !== null) {
        total += point.value * account.weight;
        known += 1;
      }
    });
    return { at, value: known ? total : null };
  });
  if (recordedTimed.length && accounts.length) recordedTimed.push({ at: now, value: remaining });

  const windowStart = now - burnHours * HOUR_MS;
  const inWindow = recordedTimed.filter((point) => point.at >= windowStart);
  let used = 0;
  for (let index = 1; index < inWindow.length; index += 1) {
    const drop = (inWindow[index - 1].value ?? 0) - (inWindow[index].value ?? 0);
    if (drop > 0) used += drop;
  }
  const burn = used / burnHours;
  const futureBurn = burn * futureMultiplier;

  const resets = accounts
    .flatMap((account) => {
      const first = account.resetAt ? Date.parse(account.resetAt) : NaN;
      if (!Number.isFinite(first)) return [];
      const times: Array<{ at: number; delta: number }> = [];
      for (let at = first; at <= to; at += 7 * DAY_MS) {
        if (at > now) times.push({ at, delta: 100 * account.weight });
      }
      return times;
    })
    .sort((a, b) => a.at - b.at);

  const projected: QuotaPoint[] = [{ at: new Date(now).toISOString(), percent: remaining }];
  const resetMarks: QuotaPool['resets'] = [];
  const emptyMarks: QuotaPool['emptyMarks'] = [];
  let value = remaining;
  let cursor = now;
  let poolEmptyAt: string | null = null;
  const burnTo = (target: number) => {
    const needed = ((target - cursor) / HOUR_MS) * futureBurn;
    if (futureBurn > 0 && value - needed <= 0) {
      const at = cursor + (value / futureBurn) * HOUR_MS;
      const iso = new Date(at).toISOString();
      projected.push({ at: iso, percent: 0 });
      poolEmptyAt = poolEmptyAt ?? iso;
      emptyMarks.push({ at: iso, label: 'Empty' });
      value = 0;
    } else {
      value -= needed;
    }
    projected.push({ at: new Date(target).toISOString(), percent: value });
    cursor = target;
  };
  resets.forEach((reset) => {
    burnTo(reset.at);
    const next = Math.min(capacity, value + reset.delta);
    resetMarks.push({ at: new Date(reset.at).toISOString(), deltaPercent: Math.round(next - value), projected: true });
    value = next;
    projected.push({ at: new Date(reset.at).toISOString(), percent: value });
  });
  if (cursor < to) burnTo(to);

  const nextReset = resetMarks[0]
    ? { at: resetMarks[0].at, deltaPercent: resetMarks[0].deltaPercent }
    : null;
  const lowest = accounts.reduce<PoolAccount | null>(
    (low, account) =>
      low === null || (account.remainingPercent ?? 0) < (low.remainingPercent ?? 0) ? account : low,
    null
  );
  const lowestShare = lowest && capacity > 0 ? (100 * lowest.weight) / capacity : 0;
  return {
    ...pool,
    accounts,
    remainingPercent: remaining,
    capacityPercent: capacity,
    poolEmptyAt,
    firstEmpty:
      lowest && futureBurn > 0 && lowestShare > 0
        ? {
            account: lowest.id,
            at: new Date(
              now + (((lowest.remainingPercent ?? 0) * lowest.weight) / (futureBurn * lowestShare)) * HOUR_MS
            ).toISOString(),
          }
        : null,
    nextReset,
    burnPerHour: burn,
    recorded: recordedTimed.map((point) => ({ at: new Date(point.at).toISOString(), percent: point.value })),
    projected,
    resets: resetMarks,
    emptyMarks,
    coverage: {
      firstSampleAt: recordedTimed[0] ? new Date(recordedTimed[0].at).toISOString() : null,
      samples: recordedTimed.length,
    },
  };
};
