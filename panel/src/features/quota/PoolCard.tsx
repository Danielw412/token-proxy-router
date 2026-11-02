import { useTranslation } from 'react-i18next';
import {
  PrivateEmail,
  ProviderIcon,
  ProviderName,
  QuotaBar,
  Badge,
} from '@/components/tpr/Ui';
import type { BurnWindow, FutureUsage, QuotaPool } from '@/services/api/tpr';
import {
  formatBurn,
  formatDuration,
  formatPercentValue,
  formatShortDate,
  formatSignedPercent,
  formatWhen,
} from './chart/chartModel';
import { PoolChart, type ChartMode } from './chart/PoolChart';
import styles from './PoolCard.module.scss';

export const providerColor = (provider: string): string =>
  provider === 'claude'
    ? 'var(--provider-claude)'
    : provider === 'codex'
      ? 'var(--provider-codex)'
      : 'var(--text-secondary)';

export interface PoolCardProps {
  pool: QuotaPool;
  now: number;
  from: number;
  to: number;
  mode: ChartMode;
  burn: BurnWindow;
  future: FutureUsage;
  initialChartWidth?: number;
}

export function PoolCard({
  pool,
  now,
  from,
  to,
  mode,
  burn,
  future,
  initialChartWidth,
}: PoolCardProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.language;
  const color = providerColor(pool.provider);
  const firstEmptyAccount = pool.firstEmpty
    ? pool.accounts.find((account) => account.id === pool.firstEmpty?.account)
    : undefined;
  const burnHours = Number.parseInt(burn, 10) || 3;
  const futureMultiplier = future === 'idle' ? 0 : future === 'double' ? 2 : 1;
  const providerTitle = pool.provider === 'claude' ? 'Claude' : pool.provider === 'codex' ? 'Codex' : pool.provider;

  return (
    <article className={styles.card}>
      <header className={styles.header}>
        <ProviderIcon provider={pool.provider} size={20} />
        <h2 className={styles.name}>
          <ProviderName provider={pool.provider} />
        </h2>
        <span className={styles.count}>
          {t('tpr.quota.account_count', { count: pool.accounts.length })}
        </span>
      </header>

      <div className={styles.summary}>
        <div className={styles.figure}>
          <span className={styles.big}>{formatPercentValue(pool.remainingPercent)}</span>
          <span className={styles.of}>
            {t('tpr.quota.of_capacity', { capacity: formatPercentValue(pool.capacityPercent) })}
          </span>
          {pool.weightNote ? <span className={styles.weight}>{pool.weightNote}</span> : null}
        </div>
        <dl className={styles.stats}>
          <div>
            <dt>{t('tpr.quota.pool_empty')}</dt>
            <dd>
              {pool.poolEmptyAt
                ? formatWhen(Date.parse(pool.poolEmptyAt), locale)
                : t('tpr.quota.not_by', { date: formatShortDate(to, locale) })}
            </dd>
          </div>
          <div>
            <dt>{t('tpr.quota.first_empty')}</dt>
            <dd>
              {pool.firstEmpty ? (
                <>
                  <span className={styles.firstAccount}>
                    <PrivateEmail
                      value={firstEmptyAccount?.email ?? ''}
                      fallback={firstEmptyAccount?.label ?? pool.firstEmpty.account}
                    />
                  </span>{' '}
                  {t('tpr.quota.in_time', {
                    time: formatDuration(Date.parse(pool.firstEmpty.at) - now),
                  })}
                </>
              ) : (
                t('tpr.quota.none_soon')
              )}
            </dd>
          </div>
          <div>
            <dt>{t('tpr.quota.next_reset')}</dt>
            <dd>
              {pool.nextReset ? (
                <>
                  <span>{formatSignedPercent(pool.nextReset.deltaPercent)}</span>{' '}
                  <span className={styles.resetWhen}>
                    {formatWhen(Date.parse(pool.nextReset.at), locale)}
                  </span>
                </>
              ) : (
                t('tpr.quota.none_soon')
              )}
            </dd>
          </div>
          <div>
            <dt>{t('tpr.quota.burn_last', { window: burn })}</dt>
            <dd>{formatBurn(pool.burnPerHour)}</dd>
          </div>
        </dl>
      </div>

      <PoolChart
        pool={pool}
        title={providerTitle}
        color={color}
        now={now}
        from={from}
        to={to}
        mode={mode}
        burnHours={burnHours}
        futureMultiplier={futureMultiplier}
        initialWidth={initialChartWidth}
      />

      <div className={styles.legend} style={{ '--chart-color': color } as React.CSSProperties}>
        <span className={styles.legendTitle}>{t('tpr.quota.legend_pool')}</span>
        <span className={styles.legendItem}>
          <span className={styles.swatch} aria-hidden="true" />
          {t('tpr.quota.chart_recorded')}
        </span>
        <span className={styles.legendItem}>
          <span className={`${styles.swatch} ${styles.swatchDashed}`} aria-hidden="true" />
          {t('tpr.quota.chart_projected')}
        </span>
      </div>

      {pool.accounts.length > 0 && (
        <ul className={styles.accounts} aria-label={t('tpr.quota.pool_accounts', { pool: providerTitle })}>
          {pool.accounts.map((account) => (
            <li key={account.id} className={styles.account}>
              <div className={styles.accountTop}>
                <span className={styles.accountName}>
                  <PrivateEmail value={account.email} fallback={account.label || account.id} />
                </span>
                <span className={styles.accountValue}>
                  {account.remainingPercent === null
                    ? '—'
                    : formatPercentValue(account.remainingPercent)}
                </span>
              </div>
              <QuotaBar
                value={account.remainingPercent}
                color={color}
                label={account.label || account.id}
              />
              <div className={styles.accountMeta}>
                <span>
                  {account.plan ? <span className={styles.plan}>{account.plan}</span> : null}
                  {account.weight !== 1 ? ` · ${account.weight}x` : ''}
                </span>
                {account.status !== 'ok' ? (
                  <Badge tone="warn">{account.status}</Badge>
                ) : account.resetAt ? (
                  <span>{t('tpr.quota.resets', { when: formatWhen(Date.parse(account.resetAt), locale) })}</span>
                ) : null}
              </div>
            </li>
          ))}
        </ul>
      )}
    </article>
  );
}
