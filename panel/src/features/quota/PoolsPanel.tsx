import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Select } from '@/components/ui/Select';
import { IconChevronLeft, IconChevronRight } from '@/components/ui/icons';
import { Skeleton } from '@/components/ui/Skeleton';
import {
  Empty,
  MultiSelect,
  PrivateEmail,
  ProviderIcon,
  ResourceError,
  Segmented,
  ToolbarLabel,
} from '@/components/tpr/Ui';
import { useTprResource } from '@/hooks/useTprResource';
import {
  tprApi,
  type BurnWindow,
  type FutureUsage,
  type QuotaPoint,
  type QuotaPool,
  type QuotaPoolsResponse,
} from '@/services/api/tpr';
import {
  DAY_MS,
  aggregateAccounts,
  formatShortDate,
  startOfLocalDay,
} from './chart/chartModel';
import type { ChartMode } from './chart/PoolChart';
import { PoolCard } from './PoolCard';
import { readPoolsUi, writePoolsUi } from './poolsUi';
import styles from './PoolsPanel.module.scss';

const DAYS_BACK = 3;
const DAYS_FORWARD = 11;
const SHIFT_DAYS = 7;
const REFRESH_MS = 60_000;

export const burnHoursOf = (burn: BurnWindow): number => Number.parseInt(burn, 10) || 3;

export const futureMultiplierOf = (future: FutureUsage): number =>
  future === 'idle' ? 0 : future === 'double' ? 2 : 1;

export interface PoolsPanelProps {
  /** Provider filter from the page's pills. "all" shows every pool. */
  provider: string;
  /** Bumped by the page when the user asks for a refresh. */
  refreshToken: number;
  onLoaded?: (data: QuotaPoolsResponse) => void;
}

export function PoolsPanel({ provider, refreshToken, onLoaded }: PoolsPanelProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.language;
  const [ui] = useState(readPoolsUi);
  const [mode, setMode] = useState<ChartMode>(ui.mode);
  const [burn, setBurn] = useState<BurnWindow>(ui.burn);
  const [future, setFuture] = useState<FutureUsage>(ui.future);
  const [offsetDays, setOffsetDays] = useState(0);
  const [selected, setSelected] = useState<Set<string>>(new Set());

  useEffect(() => {
    writePoolsUi({ mode, burn, future });
  }, [burn, future, mode]);

  const { from, to } = useMemo(() => {
    const today = startOfLocalDay(Date.now());
    return {
      from: today + (offsetDays - DAYS_BACK) * DAY_MS,
      to: today + (offsetDays + DAYS_FORWARD) * DAY_MS,
    };
  }, [offsetDays]);

  const resource = useTprResource(
    () =>
      tprApi.quotaPools({
        from: new Date(from).toISOString(),
        to: new Date(to).toISOString(),
        burn,
        window: 'weekly',
        future,
      }),
    [from, to, burn, future, refreshToken],
    { intervalMs: REFRESH_MS }
  );
  const { data, loading, error, errorStatus, refresh } = resource;

  useEffect(() => {
    if (data) onLoaded?.(data);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data]);

  const accountOptions = useMemo(
    () =>
      (data?.pools ?? [])
        .filter((pool) => provider === 'all' || pool.provider === provider)
        .flatMap((pool) =>
          pool.accounts.map((account) => ({
            value: account.id,
            label: (
              <span className={styles.accountOption}>
                <ProviderIcon provider={pool.provider} size={13} />
                <PrivateEmail value={account.email} fallback={account.label || account.id} />
              </span>
            ),
          }))
        ),
    [data, provider]
  );

  // A subset cannot come from the aggregated endpoint, so its accounts' histories are fetched.
  const selectedIds = useMemo(() => Array.from(selected).sort().join(','), [selected]);
  const [histories, setHistories] = useState<Record<string, QuotaPoint[]>>({});
  useEffect(() => {
    if (!selectedIds) {
      setHistories({});
      return;
    }
    let cancelled = false;
    const ids = selectedIds.split(',');
    void Promise.all(
      ids.map(async (id) => {
        try {
          const samples = await tprApi.accountHistory(id, {
            from: new Date(from).toISOString(),
            to: new Date(to).toISOString(),
            window: 'weekly',
          });
          return [id, samples] as const;
        } catch {
          return [id, [] as QuotaPoint[]] as const;
        }
      })
    ).then((entries) => {
      if (!cancelled) setHistories(Object.fromEntries(entries));
    });
    return () => {
      cancelled = true;
    };
  }, [from, selectedIds, to]);

  const now = data?.now ? Date.parse(data.now) : Date.now();

  const pools = useMemo<QuotaPool[]>(() => {
    const all = (data?.pools ?? []).filter(
      (pool) => provider === 'all' || pool.provider === provider
    );
    if (selected.size === 0) return all;
    return all
      .filter((pool) => pool.accounts.some((account) => selected.has(account.id)))
      .map((pool) =>
        aggregateAccounts({
          pool,
          selected,
          histories,
          now,
          to,
          burnHours: burnHoursOf(burn),
          futureMultiplier: futureMultiplierOf(future),
        })
      );
  }, [burn, data, future, histories, now, provider, selected, to]);

  const shift = useCallback((days: number) => setOffsetDays((value) => value + days), []);

  const rangeLabel = t('tpr.quota.range', {
    from: formatShortDate(from, locale),
    to: formatShortDate(to - 1, locale),
  });

  const accountSummary =
    selected.size === 0
      ? t('tpr.quota.accounts_filter')
      : t('tpr.quota.accounts_selected', { count: selected.size });

  return (
    <section className={styles.panel} aria-label={t('tpr.quota.pools_label')}>
      <div className={styles.toolbar}>
        <Segmented<ChartMode>
          ariaLabel={t('tpr.quota.chart_mode')}
          value={mode}
          onChange={setMode}
          options={[
            { value: 'remaining', label: t('tpr.quota.mode_remaining') },
            { value: 'drain', label: t('tpr.quota.mode_drain') },
          ]}
        />
        <div className={styles.toolbarGroup}>
          <ToolbarLabel>{t('tpr.quota.burn_rate')}</ToolbarLabel>
          <Segmented<BurnWindow>
            ariaLabel={t('tpr.quota.burn_rate')}
            value={burn}
            onChange={setBurn}
            options={[
              { value: '1h', label: '1h' },
              { value: '3h', label: '3h' },
              { value: '6h', label: '6h' },
            ]}
          />
        </div>
        <div className={styles.toolbarGroup}>
          <ToolbarLabel>{t('tpr.quota.future_usage')}</ToolbarLabel>
          <div className={styles.futureSelect}>
            <Select
              value={future}
              onChange={(value) => setFuture(value as FutureUsage)}
              ariaLabel={t('tpr.quota.future_usage')}
              size="sm"
              options={[
                { value: 'same', label: t('tpr.quota.future_same') },
                { value: 'idle', label: t('tpr.quota.future_idle') },
                { value: 'double', label: t('tpr.quota.future_double') },
              ]}
            />
          </div>
        </div>
        <MultiSelect
          label={t('tpr.quota.accounts_filter')}
          summary={accountSummary}
          options={accountOptions}
          selected={selected}
          onChange={setSelected}
        />
        <div className={styles.range}>
          <span className={styles.rangeLabel}>{rangeLabel}</span>
          <button
            type="button"
            className={styles.rangeNav}
            aria-label={t('tpr.quota.range_prev')}
            onClick={() => shift(-SHIFT_DAYS)}
          >
            <IconChevronLeft size={15} />
          </button>
          <button
            type="button"
            className={styles.today}
            onClick={() => setOffsetDays(0)}
            aria-pressed={offsetDays === 0}
          >
            {t('tpr.quota.today')}
          </button>
          <button
            type="button"
            className={styles.rangeNav}
            aria-label={t('tpr.quota.range_next')}
            onClick={() => shift(SHIFT_DAYS)}
          >
            <IconChevronRight size={15} />
          </button>
        </div>
      </div>

      {error && (
        <ResourceError message={error} status={errorStatus} onRetry={() => void refresh()} />
      )}

      {loading && !data ? (
        <div className={styles.cards} aria-busy="true" aria-label={t('common.loading')}>
          <Skeleton height={430} rounded={12} />
          <Skeleton height={430} rounded={12} />
        </div>
      ) : pools.length === 0 && !error ? (
        <div className={styles.emptyCard}>
          <Empty
            title={t('tpr.quota.no_pools_title')}
            description={t('tpr.quota.no_pools_description')}
            action={
              <Link className={styles.emptyLink} to="/quick-start">
                {t('tpr.quota.open_quick_start')}
              </Link>
            }
          />
        </div>
      ) : (
        <div className={styles.cards}>
          {pools.map((pool) => (
            <PoolCard
              key={pool.provider}
              pool={pool}
              now={now}
              from={from}
              to={to}
              mode={mode}
              burn={burn}
              future={future}
            />
          ))}
        </div>
      )}
    </section>
  );
}
