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
import { useNow } from '@/hooks/useNow';
import { useTprResource } from '@/hooks/useTprResource';
import {
  tprApi,
  type BurnWindow,
  type FutureUsage,
  type QuotaPool,
  type QuotaPoolsResponse,
} from '@/services/api/tpr';
import {
  DAY_MS,
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
  const [allPools, setAllPools] = useState<QuotaPool[]>([]);

  useEffect(() => {
    writePoolsUi({ mode, burn, future });
  }, [burn, future, mode]);

  const tick = useNow();
  const today = startOfLocalDay(tick);
  const from = today + (offsetDays - DAYS_BACK) * DAY_MS;
  const to = today + (offsetDays + DAYS_FORWARD) * DAY_MS;

  // The server forecasts a chosen subset the same way it forecasts a whole pool.
  const selectedIds = useMemo(() => Array.from(selected).sort().join(','), [selected]);

  const fetchPools = (accounts: string) =>
    tprApi.quotaPools({
      from: new Date(from).toISOString(),
      to: new Date(to).toISOString(),
      burn,
      window: 'weekly',
      future,
      ...(accounts ? { accounts } : {}),
    });

  const resource = useTprResource(
    // Tagged with its filter, because old data stays on screen while new data loads.
    async () => ({ response: await fetchPools(selectedIds), filter: selectedIds }),
    [from, to, burn, future, refreshToken, selectedIds],
    { intervalMs: REFRESH_MS }
  );
  // Page-wide counts and the account list always describe every account, so while a filter
  // is on they come from their own request. A new account can be chosen without clearing it.
  const unfiltered = useTprResource(
    () => fetchPools(''),
    [from, to, burn, future, refreshToken],
    { intervalMs: REFRESH_MS, enabled: Boolean(selectedIds) }
  );
  const { loading, error, errorStatus, refresh } = resource;
  const data = resource.data?.response ?? null;
  const everyAccount = resource.data?.filter === '' ? resource.data.response : unfiltered.data;

  useEffect(() => {
    if (everyAccount) {
      onLoaded?.(everyAccount);
      setAllPools(everyAccount.pools);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [everyAccount]);

  const accountOptions = useMemo(
    () =>
      allPools
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
    [allPools, provider]
  );

  const now = data?.now ? Date.parse(data.now) : tick;

  const pools = useMemo<QuotaPool[]>(
    () => (data?.pools ?? []).filter((pool) => provider === 'all' || pool.provider === provider),
    [data, provider]
  );

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
