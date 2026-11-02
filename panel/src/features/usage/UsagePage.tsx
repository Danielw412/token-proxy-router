import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Skeleton } from '@/components/ui/Skeleton';
import {
  Empty,
  Page,
  PageHeader,
  Panel,
  ResourceError,
  Segmented,
  Stat,
} from '@/components/tpr/Ui';
import { DAY_MS, formatDuration, formatDayTick } from '@/features/quota/chart/chartModel';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useNow } from '@/hooks/useNow';
import { useTprResource } from '@/hooks/useTprResource';
import { tprApi, type KeyUsage } from '@/services/api/tpr';
import { formatCompactNumber } from '@/utils/format';
import { Bars } from './Bars';
import styles from './UsagePage.module.scss';

type UsageRange = '1d' | '7d' | '30d';

const RANGE_DAYS: Record<UsageRange, number> = { '1d': 1, '7d': 7, '30d': 30 };

const totalTokens = (key: KeyUsage): number => key.totalTokens;

export function UsagePage() {
  const { t, i18n } = useTranslation();
  const locale = i18n.language;
  const [range, setRange] = useState<UsageRange>('7d');
  const now = useNow();
  const { from, to, bucket } = useMemo(
    () => ({
      from: new Date(now - RANGE_DAYS[range] * DAY_MS).toISOString(),
      to: new Date(now).toISOString(),
      bucket: range === '1d' ? ('hour' as const) : ('day' as const),
    }),
    [now, range]
  );
  const { data, loading, error, errorStatus, refresh } = useTprResource(
    () => tprApi.usageKeys({ from, to, bucket }),
    [from, to, bucket],
    { intervalMs: 60_000 }
  );
  useHeaderRefresh(refresh);

  const keys = useMemo(() => [...(data ?? [])].sort((a, b) => b.requests - a.requests), [data]);
  const totals = useMemo(
    () =>
      keys.reduce(
        (sum, key) => ({
          requests: sum.requests + key.requests,
          failures: sum.failures + key.failures,
          tokens: sum.tokens + totalTokens(key),
        }),
        { requests: 0, failures: 0, tokens: 0 }
      ),
    [keys]
  );
  return (
    <Page>
      <PageHeader
        title={t('tpr.usage.title')}
        meta={t('tpr.usage.meta', { count: keys.length })}
        actions={
          <Segmented<UsageRange>
            ariaLabel={t('tpr.usage.range')}
            value={range}
            onChange={setRange}
            options={[
              { value: '1d', label: t('tpr.usage.range_1d') },
              { value: '7d', label: t('tpr.usage.range_7d') },
              { value: '30d', label: t('tpr.usage.range_30d') },
            ]}
          />
        }
      />

      {error && <ResourceError message={error} status={errorStatus} onRetry={() => void refresh()} />}

      {loading && !data ? (
        <Skeleton height={340} rounded={12} />
      ) : keys.length === 0 && !error ? (
        <Panel>
          <Empty title={t('tpr.usage.empty_title')} description={t('tpr.usage.empty_description')} />
        </Panel>
      ) : (
        <>
          <Panel>
            <div className={styles.totals}>
              <Stat label={t('tpr.usage.requests')} value={totals.requests.toLocaleString(locale)} />
              <Stat
                label={t('tpr.usage.success_rate')}
                value={
                  totals.requests
                    ? `${(((totals.requests - totals.failures) / totals.requests) * 100).toFixed(1)}%`
                    : '—'
                }
              />
              <Stat label={t('tpr.usage.tokens')} value={formatCompactNumber(totals.tokens)} />
            </div>
          </Panel>

          <Panel title={t('tpr.usage.keys_title')}>
            <div className={styles.tableWrap}>
              <table className={styles.table}>
                <thead>
                  <tr>
                    <th scope="col">{t('tpr.usage.col_key')}</th>
                    <th scope="col" className={styles.num}>{t('tpr.usage.requests')}</th>
                    <th scope="col" className={styles.num}>{t('tpr.usage.failures')}</th>
                    <th scope="col" className={styles.num}>{t('tpr.usage.col_input')}</th>
                    <th scope="col" className={styles.num}>{t('tpr.usage.col_output')}</th>
                    <th scope="col" className={styles.num}>{t('tpr.usage.col_cached')}</th>
                    <th scope="col">{t('tpr.usage.col_last')}</th>
                    <th scope="col">{t('tpr.usage.col_activity')}</th>
                  </tr>
                </thead>
                <tbody>
                  {keys.map((key) => (
                    <tr key={key.keyId}>
                      <td>
                        <div className={styles.keyName}>{key.name || key.keyId}</div>
                        <div className={styles.keyMasked}>{key.masked}</div>
                      </td>
                      <td className={styles.num}>{key.requests.toLocaleString(locale)}</td>
                      <td className={styles.num} data-bad={key.failures > 0 || undefined}>
                        {key.failures.toLocaleString(locale)}
                      </td>
                      <td className={styles.num}>{formatCompactNumber(key.inputTokens)}</td>
                      <td className={styles.num}>{formatCompactNumber(key.outputTokens)}</td>
                      <td className={styles.num}>{formatCompactNumber(key.cachedTokens)}</td>
                      <td className={styles.when}>
                        {key.lastUsedAt
                          ? t('tpr.common.ago', { time: formatDuration(now - Date.parse(key.lastUsedAt)) })
                          : '—'}
                      </td>
                      <td className={styles.spark}>
                        <Bars
                          values={key.series.map((point) => point.requests)}
                          label={t('tpr.usage.activity_label', {
                            name: key.name || key.keyId,
                            requests: key.requests,
                          })}
                        />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Panel>

          <div className={styles.cards}>
            {keys.map((key) => (
              <Panel key={key.keyId} title={key.name || key.keyId} description={key.masked}>
                <Bars
                  height={72}
                  values={key.series.map((point) => point.totalTokens)}
                  label={t('tpr.usage.tokens_label', { name: key.name || key.keyId })}
                />
                <div className={styles.axis}>
                  <span>{key.series[0] ? formatDayTick(Date.parse(key.series[0].at), locale) : ''}</span>
                  <span>
                    {key.series.length > 1
                      ? formatDayTick(Date.parse(key.series[key.series.length - 1].at), locale)
                      : ''}
                  </span>
                </div>
                {key.models.length > 0 && (
                  <ul className={styles.models}>
                    {[...key.models]
                      .sort((a, b) => b.requests - a.requests)
                      .slice(0, 4)
                      .map((model) => (
                        <li key={model.model}>
                          <span className={styles.modelName}>{model.model}</span>
                          <span className={styles.modelValue}>
                            {t('tpr.usage.model_value', {
                              requests: model.requests.toLocaleString(locale),
                              tokens: formatCompactNumber(model.totalTokens),
                            })}
                          </span>
                        </li>
                      ))}
                  </ul>
                )}
              </Panel>
            ))}
          </div>
        </>
      )}
    </Page>
  );
}
