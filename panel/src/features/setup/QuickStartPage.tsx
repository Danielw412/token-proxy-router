import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { IconCheck } from '@/components/ui/icons';
import { Skeleton } from '@/components/ui/Skeleton';
import { Page, PageHeader, Panel, ResourceError } from '@/components/tpr/Ui';
import { DAY_MS } from '@/features/quota/chart/chartModel';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useTprResource } from '@/hooks/useTprResource';
import { tprApi } from '@/services/api/tpr';
import { countDone, type QuickStartProgress } from './quickStartLogic';
import styles from './QuickStartPage.module.scss';

export function QuickStartPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const health = useTprResource(() => tprApi.health(), [], { intervalMs: 20_000 });
  const tailscale = useTprResource(() => tprApi.tailscale(), [], { intervalMs: 20_000 });
  const usage = useTprResource(
    () => {
      const end = Date.now();
      return tprApi.usageKeys({
        from: new Date(end - 30 * DAY_MS).toISOString(),
        to: new Date(end).toISOString(),
        bucket: 'day',
      });
    },
    [],
    { intervalMs: 20_000 }
  );
  useHeaderRefresh(async () => {
    await Promise.all([health.refresh(), tailscale.refresh(), usage.refresh()]);
  });

  const progress = useMemo<QuickStartProgress>(
    () => ({
      accounts: health.data?.length ?? 0,
      exposed: tailscale.data?.exposed ?? false,
      connected: (usage.data ?? []).some((key) => key.requests > 0),
    }),
    [health.data, tailscale.data, usage.data]
  );

  const loading = health.loading && !health.data;
  const firstError = health.error
    ? { message: health.error, status: health.errorStatus, retry: health.refresh }
    : null;

  const steps = [
    {
      key: 'accounts',
      done: progress.accounts > 0,
      title: t('tpr.quick.accounts_title'),
      detail:
        progress.accounts > 0
          ? t('tpr.quick.accounts_done', { count: progress.accounts })
          : t('tpr.quick.accounts_todo'),
      action: (
        <Link to="/accounts?add=1">
          <Button variant={progress.accounts > 0 ? 'secondary' : 'primary'} size="sm">
            {progress.accounts > 0 ? t('tpr.quick.accounts_more') : t('tpr.common.add_account')}
          </Button>
        </Link>
      ),
    },
    {
      key: 'tailnet',
      done: progress.exposed,
      title: t('tpr.quick.tailnet_title'),
      detail: progress.exposed
        ? t('tpr.quick.tailnet_done', { url: tailscale.data?.url ?? '' })
        : tailscale.data?.hint || t('tpr.quick.tailnet_todo'),
      action: (
        <Link to="/system">
          <Button variant={progress.exposed || progress.accounts === 0 ? 'secondary' : 'primary'} size="sm">
            {progress.exposed ? t('tpr.quick.tailnet_manage') : t('tpr.quick.tailnet_action')}
          </Button>
        </Link>
      ),
    },
    {
      key: 'connect',
      done: progress.connected,
      title: t('tpr.quick.connect_title'),
      detail: progress.connected ? t('tpr.quick.connect_done') : t('tpr.quick.connect_todo'),
      action: (
        <Link to="/connect">
          <Button variant="secondary" size="sm">
            {progress.connected ? t('tpr.quick.connect_more') : t('tpr.quick.connect_action')}
          </Button>
        </Link>
      ),
    },
  ];

  return (
    <Page>
      <PageHeader
        title={t('tpr.quick.title')}
        meta={t('tpr.quick.meta', { done: countDone(progress), total: steps.length })}
        actions={
          <Button variant="secondary" onClick={() => navigate('/quick-start?wizard=1')}>
            {t('tpr.quick.open_wizard')}
          </Button>
        }
      />
      {firstError && (
        <ResourceError
          message={firstError.message}
          status={firstError.status}
          onRetry={() => void firstError.retry()}
        />
      )}
      {loading ? (
        <Skeleton height={240} rounded={12} />
      ) : (
        <Panel>
          <ol className={styles.steps}>
            {steps.map((step, index) => (
              <li key={step.key} className={styles.step} data-done={step.done || undefined}>
                <span className={styles.marker} aria-hidden="true">
                  {step.done ? <IconCheck size={13} /> : index + 1}
                </span>
                <div className={styles.text}>
                  <div className={styles.title}>
                    {step.title}
                    <span className={styles.srOnly}>
                      {step.done ? t('tpr.quick.step_done') : t('tpr.quick.step_todo')}
                    </span>
                  </div>
                  <div className={styles.detail}>{step.detail}</div>
                </div>
                <div className={styles.action}>{step.action}</div>
              </li>
            ))}
          </ol>
        </Panel>
      )}
    </Page>
  );
}
