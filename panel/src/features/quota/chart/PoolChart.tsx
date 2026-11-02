import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent,
} from 'react';
import { useTranslation } from 'react-i18next';
import type { QuotaPool } from '@/services/api/tpr';
import {
  areaPath,
  createScales,
  dayTicks,
  drainSeries,
  formatAxisBurn,
  formatBurn,
  formatDayTick,
  formatPercentValue,
  formatWhen,
  layoutResetLabels,
  niceDrainMax,
  runPath,
  splitRuns,
  steppedRun,
  toTimed,
  valueAtTime,
  yTicks,
  type ChartBox,
  type TimedValue,
} from './chartModel';
import styles from './PoolChart.module.scss';

export type ChartMode = 'remaining' | 'drain';

export interface PoolChartProps {
  pool: QuotaPool;
  title: string;
  /** CSS color, typically a provider token. */
  color: string;
  now: number;
  from: number;
  to: number;
  mode: ChartMode;
  burnHours: number;
  /** Multiplier applied to the drain forecast: 0 for no usage, 1 for same, 2 for double. */
  futureMultiplier: number;
  /** Width used before the first measurement, including server rendering. */
  initialWidth?: number;
}

const WIDE_BOX = { left: 52, right: 10, top: 24, bottom: 30 };
const NARROW_BOX = { left: 44, right: 6, top: 24, bottom: 30 };
const NARROW_WIDTH = 560;
const KEY_STEPS = 56;

export function PoolChart({
  pool,
  title,
  color,
  now,
  from,
  to,
  mode,
  burnHours,
  futureMultiplier,
  initialWidth = 960,
}: PoolChartProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.language;
  const gradientId = useId();
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const [width, setWidth] = useState(initialWidth);
  const [hoverAt, setHoverAt] = useState<number | null>(null);

  useLayoutEffect(() => {
    const node = wrapRef.current;
    if (!node) return;
    const measure = () => {
      const next = Math.round(node.getBoundingClientRect().width);
      if (next > 0) setWidth(next);
    };
    measure();
    if (typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(measure);
    observer.observe(node);
    return () => observer.disconnect();
  }, []);

  useEffect(() => setHoverAt(null), [from, to, mode]);

  const narrow = width < NARROW_WIDTH;
  const height = narrow ? 232 : 300;

  const model = useMemo(() => {
    const recorded = toTimed(pool.recorded);
    const projected = toTimed(pool.projected);
    if (mode === 'remaining') {
      return {
        recorded,
        projected,
        max: pool.capacityPercent > 0 ? pool.capacityPercent : 100,
        format: formatPercentValue,
        axis: formatPercentValue,
      };
    }
    const drain = drainSeries(recorded, burnHours);
    const lastBurn = pool.burnPerHour * futureMultiplier;
    const forecast: TimedValue[] = [
      { at: Math.max(now, from), value: lastBurn },
      { at: to, value: lastBurn },
    ];
    return {
      recorded: drain,
      projected: forecast,
      max: niceDrainMax([...drain.map((point) => point.value), lastBurn]),
      format: formatBurn,
      axis: formatAxisBurn,
    };
  }, [burnHours, from, futureMultiplier, mode, now, pool, to]);

  const scales = useMemo(() => {
    const box: ChartBox = { width, height, ...(narrow ? NARROW_BOX : WIDE_BOX) };
    return createScales(box, from, to, model.max);
  }, [from, height, model.max, narrow, to, width]);
  const plotWidth = scales.plotRight - scales.plotLeft;

  const visibleRecorded = useMemo(
    () => model.recorded.filter((point) => point.at >= from && point.at <= Math.min(now, to)),
    [from, model.recorded, now, to]
  );
  const visibleProjected = useMemo(
    () => model.projected.filter((point) => point.at >= from && point.at <= to),
    [from, model.projected, to]
  );

  const recordedRuns = useMemo(() => splitRuns(visibleRecorded), [visibleRecorded]);
  const projectedRuns = useMemo(
    () => splitRuns(mode === 'remaining' ? steppedRun(visibleProjected) : visibleProjected),
    [mode, visibleProjected]
  );

  const ticks = useMemo(
    () => dayTicks(from, to, plotWidth, narrow ? 46 : 58),
    [from, narrow, plotWidth, to]
  );

  const labels = useMemo(() => {
    if (mode !== 'remaining') return [];
    const resets = pool.resets.map((reset) => {
      const at = Date.parse(reset.at);
      const after = reset.projected
        ? [...visibleProjected].reverse().find((point) => point.at === at)?.value
        : visibleRecorded.find((point) => point.at >= at)?.value;
      return {
        at,
        deltaPercent: reset.deltaPercent,
        projected: reset.projected,
        top: after ?? null,
      };
    });
    return layoutResetLabels(resets, scales);
  }, [mode, pool.resets, scales, visibleProjected, visibleRecorded]);

  const nowVisible = now >= from && now <= to;
  const nowValue = useMemo(() => {
    if (mode === 'remaining') return pool.remainingPercent;
    const last = [...visibleRecorded].reverse().find((point) => point.value !== null);
    return last?.value ?? null;
  }, [mode, pool.remainingPercent, visibleRecorded]);

  const emptyMarks = useMemo(
    () =>
      mode === 'remaining'
        ? pool.emptyMarks
            .map((mark) => ({ at: Date.parse(mark.at), label: mark.label }))
            .filter((mark) => mark.at >= from && mark.at <= to)
        : [],
    [from, mode, pool.emptyMarks, to]
  );

  const hover = useMemo(() => {
    if (hoverAt === null) return null;
    const value = valueAtTime(model.recorded, model.projected, hoverAt, now);
    return { at: hoverAt, value };
  }, [hoverAt, model.projected, model.recorded, now]);

  const tooltipText = hover
    ? [
        formatWhen(hover.at, locale),
        hover.value
          ? mode === 'remaining'
            ? t('tpr.quota.chart_left', { value: model.format(hover.value.value) })
            : model.format(hover.value.value)
          : t('tpr.quota.chart_no_reading'),
        hover.value
          ? hover.value.kind === 'projected'
            ? t('tpr.quota.chart_projected').toLowerCase()
            : t('tpr.quota.chart_recorded').toLowerCase()
          : null,
      ]
        .filter(Boolean)
        .join(' · ')
    : '';

  const hoverX = hover ? scales.x(hover.at) : 0;

  const handlePointer = useCallback(
    (event: PointerEvent<SVGSVGElement>) => {
      const rect = event.currentTarget.getBoundingClientRect();
      const at = scales.invertX(event.clientX - rect.left);
      setHoverAt(at);
    },
    [scales]
  );

  const handleKey = useCallback(
    (event: KeyboardEvent<HTMLDivElement>) => {
      const step = (to - from) / KEY_STEPS;
      if (event.key === 'ArrowRight' || event.key === 'ArrowLeft') {
        event.preventDefault();
        setHoverAt((current) => {
          const base = current ?? (now >= from && now <= to ? now : from);
          const next = base + (event.key === 'ArrowRight' ? step : -step);
          return Math.min(to, Math.max(from, next));
        });
      } else if (event.key === 'Escape') {
        setHoverAt(null);
      }
    },
    [from, now, to]
  );

  const hasReadings = visibleRecorded.some((point) => point.value !== null);
  const noHistoryAtAll = pool.coverage.samples === 0 && pool.recorded.length === 0;
  const summary = t(mode === 'remaining' ? 'tpr.quota.chart_summary' : 'tpr.quota.chart_summary_drain', {
    pool: title,
    remaining: formatPercentValue(pool.remainingPercent),
    capacity: formatPercentValue(pool.capacityPercent),
    burn: formatBurn(pool.burnPerHour),
    samples: pool.coverage.samples,
    from: formatDayTick(from, locale),
    to: formatDayTick(to, locale),
  });

  const firstSample = pool.coverage.firstSampleAt ? Date.parse(pool.coverage.firstSampleAt) : now;

  return (
    <figure className={styles.figure} style={{ '--chart-color': color } as React.CSSProperties}>
      <div
        ref={wrapRef}
        className={styles.plot}
        tabIndex={0}
        role="group"
        aria-label={t('tpr.quota.chart_keyboard_hint', { pool: title })}
        onKeyDown={handleKey}
        onBlur={() => setHoverAt(null)}
      >
        <svg
          role="img"
          aria-label={summary}
          width={width}
          height={height}
          viewBox={`0 0 ${width} ${height}`}
          className={styles.svg}
          onPointerMove={handlePointer}
          onPointerLeave={() => setHoverAt(null)}
        >
          <defs>
            <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={color} stopOpacity={0.26} />
              <stop offset="100%" stopColor={color} stopOpacity={0.01} />
            </linearGradient>
          </defs>

          {ticks
            .filter((tick) => tick.at >= from && tick.at <= to)
            .map((tick) => (
              <line
                key={tick.at}
                className={styles.grid}
                x1={scales.x(tick.at)}
                x2={scales.x(tick.at)}
                y1={scales.plotTop}
                y2={scales.plotBottom}
              />
            ))}
          {yTicks(model.max).map((tick) => (
            <g key={tick}>
              <line
                className={tick === 0 ? styles.axis : styles.grid}
                x1={scales.plotLeft}
                x2={scales.plotRight}
                y1={scales.y(tick)}
                y2={scales.y(tick)}
              />
              <text className={styles.axisLabel} x={scales.plotLeft - 10} y={scales.y(tick) + 4} textAnchor="end">
                {model.axis(tick)}
              </text>
            </g>
          ))}
          {ticks
            .filter((tick) => tick.showLabel)
            .map((tick) => (
              <text
                key={tick.labelAt}
                className={styles.axisLabel}
                x={scales.x(tick.labelAt)}
                y={scales.plotBottom + 20}
                textAnchor="middle"
              >
                {formatDayTick(tick.labelAt, locale)}
              </text>
            ))}

          {projectedRuns.runs.map((run) => (
            <path key={`pa-${run[0].at}`} d={areaPath(run, scales)} fill={`url(#${gradientId})`} opacity={0.42} />
          ))}
          {recordedRuns.runs.map((run) => (
            <path key={`ra-${run[0].at}`} d={areaPath(run, scales)} fill={`url(#${gradientId})`} />
          ))}

          {nowVisible && (
            <g>
              <line
                className={styles.nowLine}
                x1={scales.x(now)}
                x2={scales.x(now)}
                y1={scales.plotTop - 8}
                y2={scales.plotBottom + 26}
              />
              <text className={styles.nowLabel} x={scales.x(now)} y={scales.plotTop - 13} textAnchor="middle">
                {t('tpr.quota.chart_now')}
              </text>
            </g>
          )}

          {projectedRuns.runs.map((run) => (
            <path key={`pl-${run[0].at}`} d={runPath(run, scales)} className={styles.projected} />
          ))}
          {recordedRuns.runs.map((run) => (
            <path key={`rl-${run[0].at}`} d={runPath(run, scales)} className={styles.recorded} />
          ))}
          {recordedRuns.isolated.map((point) => (
            <circle
              key={`iso-${point.at}`}
              cx={scales.x(point.at)}
              cy={scales.y(point.value ?? 0)}
              r={3}
              className={styles.dot}
            />
          ))}

          {labels.map((label) => (
            <text key={label.key} className={styles.resetLabel} x={label.x} y={label.y} textAnchor="middle">
              {label.text}
            </text>
          ))}

          {nowVisible && nowValue !== null && hasReadings && (
            <circle cx={scales.x(now)} cy={scales.y(nowValue)} r={4} className={styles.dot} />
          )}

          {emptyMarks.map((mark) => {
            const x = scales.x(mark.at);
            const y = scales.y(0);
            const text = `${mark.label} ${formatWhen(mark.at, locale)}`;
            const anchor =
              x > scales.plotRight - 90 ? 'end' : x < scales.plotLeft + 90 ? 'start' : 'middle';
            return (
              <g key={mark.at}>
                <text className={styles.emptyLabel} x={x} y={y - 12} textAnchor={anchor}>
                  {text}
                </text>
                <circle cx={x} cy={y} r={5} className={styles.emptyRing} />
              </g>
            );
          })}

          {hover && (
            <g pointerEvents="none">
              <line
                className={styles.crosshair}
                x1={hoverX}
                x2={hoverX}
                y1={scales.plotTop}
                y2={scales.plotBottom}
              />
              {hover.value && (
                <circle cx={hoverX} cy={scales.y(hover.value.value)} r={4} className={styles.dot} />
              )}
            </g>
          )}
        </svg>

        {!hasReadings && mode === 'remaining' && (
          <p className={styles.emptyNote}>
            {noHistoryAtAll
              ? t('tpr.quota.chart_recording', { time: formatWhen(firstSample, locale) })
              : t('tpr.quota.chart_no_range')}
          </p>
        )}
        {!hasReadings && mode === 'drain' && (
          <p className={styles.emptyNote}>{t('tpr.quota.chart_no_drain')}</p>
        )}

        {hover && (
          <div
            className={styles.tooltip}
            aria-live="polite"
            style={hoverX > width * 0.6 ? { right: Math.max(4, width - hoverX + 10) } : { left: hoverX + 10 }}
          >
            {tooltipText}
          </div>
        )}
      </div>
      <figcaption className={styles.srOnly}>{summary}</figcaption>
    </figure>
  );
}
