import { describe, expect, test } from 'bun:test';
import type { QuotaPool } from '@/services/api/tpr';
import {
  DAY_MS,
  HOUR_MS,
  aggregateAccounts,
  areaPath,
  createScales,
  dayTicks,
  drainSeries,
  formatAxisBurn,
  formatBurn,
  formatDayTick,
  formatDuration,
  formatPercentValue,
  formatShortDate,
  formatSignedPercent,
  formatWhen,
  layoutResetLabels,
  niceDrainMax,
  runPath,
  splitRuns,
  steppedRun,
  toTimed,
  valueAtTime,
  yTicks,
} from '@/features/quota/chart/chartModel';

const box = { width: 1000, height: 300, left: 50, right: 10, top: 20, bottom: 30 };
const FROM = Date.UTC(2026, 9, 23);
const TO = FROM + 14 * DAY_MS;
const scales = createScales(box, FROM, TO, 600);

describe('scales', () => {
  test('maps the domain onto the plot area', () => {
    expect(scales.x(FROM)).toBe(50);
    expect(scales.x(TO)).toBe(990);
    expect(scales.y(600)).toBe(20);
    expect(scales.y(0)).toBe(270);
    expect(scales.y(300)).toBe(145);
  });

  test('clamps stray readings to the plot', () => {
    expect(scales.y(900)).toBe(20);
    expect(scales.y(-40)).toBe(270);
  });

  test('inverts x and clamps pointer positions outside the plot', () => {
    expect(scales.invertX(scales.x(FROM + DAY_MS))).toBeCloseTo(FROM + DAY_MS, 0);
    expect(scales.invertX(-100)).toBe(FROM);
    expect(scales.invertX(5000)).toBe(TO);
  });

  test('survives degenerate domains without NaN', () => {
    const flat = createScales(box, FROM, FROM, 0);
    expect(Number.isFinite(flat.x(FROM))).toBe(true);
    expect(flat.max).toBe(100);
    expect(Number.isFinite(flat.y(50))).toBe(true);
  });

  test('labels zero, half and capacity', () => {
    expect(yTicks(600)).toEqual([0, 300, 600]);
    expect(yTicks(0)).toEqual([0, 50, 100]);
  });
});

describe('day ticks', () => {
  test('one grid line per day, all labeled when there is room', () => {
    const ticks = dayTicks(FROM, TO, 1400);
    expect(ticks.length).toBeGreaterThanOrEqual(14);
    expect(ticks.filter((tick) => tick.showLabel).length).toBeGreaterThanOrEqual(13);
  });

  test('labels thin out on narrow charts but grid lines stay', () => {
    const ticks = dayTicks(FROM, TO, 300, 46);
    expect(ticks.length).toBeGreaterThanOrEqual(14);
    const shown = ticks.filter((tick) => tick.showLabel).length;
    expect(shown).toBeGreaterThan(2);
    expect(shown).toBeLessThanOrEqual(7);
  });
});

describe('formatting', () => {
  const at = Date.UTC(2026, 9, 25, 12, 0);
  const options = ['en-US', 'UTC'] as const;

  test('dates and times', () => {
    expect(formatDayTick(at, ...options)).toBe('Sun 25');
    expect(formatWhen(at, ...options).replace(/\s/g, ' ')).toBe('Sun 12:00 PM');
    expect(formatShortDate(at, ...options)).toBe('Oct 25');
  });

  test('durations read like the reference design', () => {
    expect(formatDuration((15 * 60 + 49) * 60_000)).toBe('15h 49m');
    expect(formatDuration(2 * DAY_MS + 18 * HOUR_MS)).toBe('2d 18h');
    expect(formatDuration(5 * 60_000)).toBe('5m');
    expect(formatDuration(3 * HOUR_MS)).toBe('3h');
    expect(formatDuration(3 * DAY_MS)).toBe('3d');
    expect(formatDuration(20_000)).toBe('under 1m');
    expect(formatDuration(-1)).toBe('now');
    expect(formatDuration(Number.NaN)).toBe('now');
  });

  test('percents and burn', () => {
    expect(formatPercentValue(554.6)).toBe('555%');
    expect(formatSignedPercent(100.2)).toBe('+100%');
    expect(formatSignedPercent(-27)).toBe('-27%');
    expect(formatBurn(3.74)).toBe('3.7%/h');
    expect(formatAxisBurn(0.30000000000000004)).toBe('0.3%/h');
  });
});

describe('paths', () => {
  const points = [
    { at: FROM, value: 500 },
    { at: FROM + DAY_MS, value: 400 },
    { at: FROM + 2 * DAY_MS, value: null },
    { at: FROM + 3 * DAY_MS, value: 300 },
    { at: FROM + 4 * DAY_MS, value: null },
    { at: FROM + 5 * DAY_MS, value: 200 },
    { at: FROM + 6 * DAY_MS, value: 100 },
  ];

  test('a hole breaks the line, and a lone reading becomes a dot', () => {
    const { runs, isolated } = splitRuns(points);
    expect(runs).toHaveLength(2);
    expect(isolated).toEqual([{ at: FROM + 3 * DAY_MS, value: 300 }]);
    expect(runs[0]).toHaveLength(2);
  });

  test('builds a line and an area closed to the baseline', () => {
    const run = points.slice(0, 2);
    expect(runPath(run, scales).startsWith('M50 ')).toBe(true);
    const area = areaPath(run, scales);
    expect(area.endsWith('Z')).toBe(true);
    expect(area).toContain(` ${scales.plotBottom}`);
    expect(areaPath(points.slice(0, 1), scales)).toBe('');
  });

  test('rising forecast segments become vertical jumps', () => {
    const stepped = steppedRun([
      { at: 0, value: 300 },
      { at: 100, value: 200 },
      { at: 200, value: 400 },
    ]);
    expect(stepped).toEqual([
      { at: 0, value: 300 },
      { at: 100, value: 200 },
      { at: 200, value: 200 },
      { at: 200, value: 400 },
    ]);
  });
});

describe('values under the crosshair', () => {
  const now = FROM + 2 * DAY_MS;
  const recorded = toTimed([
    { at: new Date(FROM).toISOString(), percent: 500 },
    { at: new Date(FROM + DAY_MS).toISOString(), percent: null },
    { at: new Date(now).toISOString(), percent: 400 },
  ]);
  const projected = toTimed([
    { at: new Date(now).toISOString(), percent: 400 },
    { at: new Date(now + DAY_MS).toISOString(), percent: 300 },
  ]);

  test('interpolates recorded and projected points', () => {
    expect(valueAtTime(recorded, projected, now + DAY_MS / 2, now)).toEqual({
      value: 350,
      kind: 'projected',
    });
    expect(valueAtTime(recorded, projected, now, now)?.kind).toBe('recorded');
  });

  test('reports no value across a hole or outside the data', () => {
    expect(valueAtTime(recorded, projected, FROM + DAY_MS / 2, now)).toBeNull();
    expect(valueAtTime(recorded, projected, now + 3 * DAY_MS, now)).toBeNull();
  });
});

describe('reset labels', () => {
  const reset = (at: number, deltaPercent: number, top = 500) => ({
    at,
    deltaPercent,
    projected: true,
    top,
  });

  test('merges accounts that reset at the same moment', () => {
    const at = FROM + 5 * DAY_MS;
    const labels = layoutResetLabels([reset(at, 100), reset(at, 100), reset(at, 250)], scales);
    expect(labels).toHaveLength(1);
    expect(labels[0].text).toBe('+450%');
  });

  test('lifts a label rather than overlapping, and stays inside the plot', () => {
    const labels = layoutResetLabels(
      [reset(FROM + 5 * DAY_MS, 100), reset(FROM + 5 * DAY_MS + 3 * HOUR_MS, 50)],
      scales
    );
    expect(labels).toHaveLength(2);
    expect(labels[0].y).not.toBe(labels[1].y);
    const edge = layoutResetLabels([reset(TO - 60_000, 100)], scales);
    expect(edge[0].x).toBeLessThanOrEqual(scales.plotRight);
  });

  test('compact mode drops crowded labels instead of stacking them', () => {
    const narrow = createScales({ ...box, width: 300 }, FROM, TO, 600);
    const many = Array.from({ length: 10 }, (_, index) => reset(FROM + index * DAY_MS, 100));
    const labels = layoutResetLabels(many, narrow, { compact: true });
    expect(labels.length).toBeLessThan(many.length);
    labels.forEach((label, index) => {
      if (index > 0) expect(label.x - labels[index - 1].x).toBeGreaterThanOrEqual(38);
    });
  });

  test('ignores resets outside the range and zero jumps', () => {
    expect(layoutResetLabels([reset(TO + DAY_MS, 100), reset(FROM + DAY_MS, 0)], scales)).toEqual([]);
  });
});

describe('drain rate', () => {
  const series = (values: Array<number | null>) =>
    values.map((value, index) => ({ at: FROM + index * HOUR_MS, value }));

  test('measures quota used per hour', () => {
    const drain = drainSeries(series([100, 98, 96, 94, 92]), 1, 3);
    expect(drain.at(-1)?.value).toBeCloseTo(2, 5);
  });

  test('a reset is not negative usage', () => {
    const drain = drainSeries(series([50, 48, 148, 146]), 3, 2);
    expect(drain.at(-1)?.value).toBeCloseTo(4 / 3, 5);
  });

  test('needs at least two readings', () => {
    expect(drainSeries(series([100]), 1)).toEqual([]);
    expect(drainSeries(series([null, null]), 1)).toEqual([]);
  });

  test('the axis top is tidy and never below 1%/h', () => {
    expect(niceDrainMax([0.2, null])).toBe(1);
    expect(niceDrainMax([3.7])).toBe(4);
    expect(niceDrainMax([17])).toBe(20);
  });
});

describe('filtering a pool to chosen accounts', () => {
  const now = FROM + 2 * DAY_MS;
  const pool: QuotaPool = {
    provider: 'codex',
    accounts: [
      { id: 'a', label: 'A', email: '', plan: '', weight: 1, remainingPercent: 50, resetAt: new Date(now + DAY_MS).toISOString(), status: 'ok' },
      { id: 'b', label: 'B', email: '', plan: '', weight: 2.5, remainingPercent: 80, resetAt: null, status: 'ok' },
    ],
    remainingPercent: 250,
    capacityPercent: 350,
    weightNote: '',
    poolEmptyAt: null,
    firstEmpty: null,
    nextReset: null,
    burnPerHour: 1,
    recorded: [],
    projected: [],
    resets: [],
    emptyMarks: [],
    coverage: { firstSampleAt: null, samples: 0 },
  };
  const at = (offsetHours: number) => new Date(now - offsetHours * HOUR_MS).toISOString();
  const histories = {
    a: [
      { at: at(3), percent: 56 },
      { at: at(0), percent: 50 },
    ],
    b: [{ at: at(3), percent: 80 }],
  };
  const build = (selected: string[], futureMultiplier = 1) =>
    aggregateAccounts({
      pool,
      selected: new Set(selected),
      histories,
      now,
      to: now + 10 * DAY_MS,
      burnHours: 3,
      futureMultiplier,
    });

  test('recomputes weighted figures for the subset', () => {
    const filtered = build(['b']);
    expect(filtered.capacityPercent).toBe(250);
    expect(filtered.remainingPercent).toBe(200);
    expect(filtered.accounts.map((account) => account.id)).toEqual(['b']);
  });

  test('derives burn from the subset and forecasts its resets', () => {
    const filtered = build(['a']);
    expect(filtered.burnPerHour).toBeCloseTo(2, 5);
    expect(filtered.nextReset?.deltaPercent).toBeGreaterThan(0);
    expect(filtered.poolEmptyAt).not.toBeNull();
    expect(filtered.emptyMarks).toHaveLength(filtered.emptyMarks.length);
  });

  test('no usage ahead means the pool never empties', () => {
    const filtered = build(['a'], 0);
    expect(filtered.poolEmptyAt).toBeNull();
    expect(filtered.firstEmpty).toBeNull();
  });
});
