import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { Skeleton } from '@/components/ui/Skeleton';
import {
  Badge,
  Empty,
  Note,
  Page,
  PageHeader,
  Panel,
  PrivateEmail,
  PrivateText,
  ProviderIcon,
  ProviderName,
  ResourceError,
  Stat,
} from '@/components/tpr/Ui';
import { DAY_MS, formatBurn, formatDuration, formatPercentValue, formatSignedPercent, formatWhen, startOfLocalDay } from '@/features/quota/chart/chartModel';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useNow } from '@/hooks/useNow';
import { useTprResource } from '@/hooks/useTprResource';
import { tprApi, type QuotaPool, type RouterPool } from '@/services/api/tpr';
import { formatPercent } from '@/utils/format';
import { ThroughputChart } from './components/ThroughputChart';
import { useDashboardOverview } from './hooks/useDashboardOverview';
import { recentFailures } from './overviewLogic';
import styles from './OverviewPage.module.scss';

const REFRESH_MS = 60_000;

function PoolTile({
  pool,
  router,
  now,
  locale,
}: {
  pool: QuotaPool;
  router?: RouterPool;
  now: number;
  locale: string;
}) {
  const { t } = useTranslation();
  const routed = router?.accounts.find((account) => account.id === router.routing);
  return (
    <Link to="/quota" className={styles.pool}>
      <div className={styles.poolHead}>
        <ProviderIcon provider={pool.provider} size={18} />
        <span className={styles.poolName}>
          <ProviderName provider={pool.provider} />
        </span>
        <span className={styles.poolCount}>{t('tpr.quota.account_count', { count: pool.accounts.length })}</span>
      </div>
      <div className={styles.poolFigure}>
        <span className={styles.big}>{formatPercentValue(pool.remainingPercent)}</span>
        <span className={styles.of}>
          {t('tpr.quota.of_capacity', { capacity: formatPercentValue(pool.capacityPercent) })}
        </span>
      </div>
      <dl className={styles.poolStats}>
        <div>
          <dt>{t('tpr.dashboard.burn')}</dt>
          <dd>{formatBurn(pool.burnPerHour)}</dd>
        </div>
        <div>
          <dt>{t('tpr.quota.next_reset')}</dt>
          <dd>
            {pool.nextReset
              ? `${formatSignedPercent(pool.nextReset.deltaPercent)} ${formatWhen(Date.parse(pool.nextReset.at), locale)}`
              : t('tpr.quota.none_soon')}
          </dd>
        </div>
        <div className={styles.routed}>
          <dt>{t('tpr.dashboard.routed')}</dt>
          <dd>
            {routed ? (
              <PrivateEmail value={routed.email} fallback={routed.label} />
            ) : (
              t('tpr.dashboard.routed_none')
            )}
          </dd>
        </div>
      </dl>
      {pool.poolEmptyAt && (
        <div className={styles.poolWarn}>
          {t('tpr.dashboard.empties_in', { time: formatDuration(Date.parse(pool.poolEmptyAt) - now) })}
        </div>
      )}
    </Link>
  );
}

export function OverviewPage() {
  const { t, i18n } = useTranslation();
  const locale = i18n.language;
  const { connected, traffic, refresh: refreshTraffic } = useDashboardOverview();
  const [range] = useState(() => {
    const today = startOfLocalDay(Date.now());
    return {
      from: new Date(today - DAY_MS).toISOString(),
      to: new Date(today + 8 * DAY_MS).toISOString(),
    };
  });
  const tick = useNow();
  const pools = useTprResource(
    () => tprApi.quotaPools({ ...range, burn: '3h', window: 'weekly', future: 'same' }),
    [],
    { intervalMs: REFRESH_MS }
  );
  const router = useTprResource(() => tprApi.routerPools(1), [], { intervalMs: REFRESH_MS });
  const health = useTprResource(() => tprApi.health(), [], { intervalMs: REFRESH_MS });
  const events = useTprResource(() => tprApi.alertEvents(20), [], { intervalMs: REFRESH_MS });

  useHeaderRefresh(async () => {
    await Promise.all([
      pools.refresh(),
      router.refresh(),
      health.refresh(),
      events.refresh(),
      refreshTraffic(),
    ]);
  }, connected);

  const poolList = pools.data?.pools ?? [];
  const accountCount = poolList.reduce((sum, pool) => sum + pool.accounts.length, 0);
  const activeAlerts = (events.data ?? []).filter((event) => event.active);
  const failures = useMemo(() => recentFailures(health.data ?? []), [health.data]);
  const now = pools.data?.now ? Date.parse(pools.data.now) : tick;
  const zeroAccounts = !pools.loading && !pools.error && accountCount === 0;

  return (
    <Page>
      <PageHeader
        title={t('tpr.dashboard.title')}
        meta={t('tpr.dashboard.meta', { pools: poolList.length, accounts: accountCount })}
      />

      {pools.error && (
        <ResourceError message={pools.error} status={pools.errorStatus} onRetry={() => void pools.refresh()} />
      )}

      {zeroAccounts ? (
        <Panel>
          <Empty
            title={t('tpr.dashboard.empty_title')}
            description={t('tpr.dashboard.empty_description')}
            action={
              <Link to="/quick-start">
                <Button>{t('tpr.dashboard.empty_action')}</Button>
              </Link>
            }
          />
        </Panel>
      ) : pools.loading && !pools.data ? (
        <div className={styles.pools}>
          <Skeleton height={190} rounded={12} />
          <Skeleton height={190} rounded={12} />
        </div>
      ) : (
        <div className={styles.pools}>
          {poolList.map((pool) => (
            <PoolTile
              key={pool.provider}
              pool={pool}
              router={router.data?.find((item) => item.provider === pool.provider)}
              now={now}
              locale={locale}
            />
          ))}
        </div>
      )}

      <div className={styles.tiles}>
        <Panel>
          <Stat
            label={t('tpr.dashboard.requests')}
            value={traffic.total.toLocaleString(locale)}
            sub={t('tpr.dashboard.requests_sub', { minutes: traffic.windowMinutes || 200 })}
          />
        </Panel>
        <Panel>
          <Stat
            label={t('tpr.dashboard.success_rate')}
            value={traffic.successRate === null ? '—' : formatPercent(traffic.successRate)}
            tone={traffic.successRate !== null && traffic.successRate < 90 ? 'bad' : undefined}
            sub={t('tpr.dashboard.success_sub', { failures: traffic.totalFailure })}
          />
        </Panel>
        <Link to="/alerts" className={styles.tileLink}>
          <Panel>
            <Stat
              label={t('tpr.dashboard.alerts')}
              value={activeAlerts.length}
              tone={activeAlerts.length > 0 ? 'warn' : undefined}
              sub={activeAlerts[0] ? <PrivateText text={activeAlerts[0].message} /> : t('tpr.dashboard.alerts_none')}
            />
          </Panel>
        </Link>
      </div>

      <div className={styles.lower}>
        <Panel title={t('tpr.dashboard.traffic_title')} description={t('tpr.dashboard.traffic_description')}>
          {traffic.total === 0 ? (
            <Empty title={t('tpr.dashboard.traffic_empty')} description={t('tpr.dashboard.traffic_empty_hint')} />
          ) : (
            <ThroughputChart traffic={traffic} />
          )}
        </Panel>
        <Panel title={t('tpr.dashboard.failures_title')} description={t('tpr.dashboard.failures_description')}>
          {health.error ? (
            <ResourceError message={health.error} status={health.errorStatus} />
          ) : failures.length === 0 ? (
            <Note tone="good">{t('tpr.dashboard.failures_none')}</Note>
          ) : (
            <ul className={styles.failures}>
              {failures.map((account) => (
                <li key={account.id}>
                  <div className={styles.failureTop}>
                    <ProviderIcon provider={account.provider} size={14} />
                    <span className={styles.failureName}>
                      <PrivateEmail value={account.email} fallback={account.label || account.id} />
                    </span>
                    <Badge tone="bad">{t(`tpr.accounts.status_${account.status}`)}</Badge>
                    <span className={styles.failureWhen}>
                      {t('tpr.common.ago', { time: formatDuration(now - Date.parse(account.lastErrorAt ?? '')) })}
                    </span>
                  </div>
                  <div className={styles.failureMessage}>{account.lastError}</div>
                </li>
              ))}
            </ul>
          )}
        </Panel>
      </div>
    </Page>
  );
}
