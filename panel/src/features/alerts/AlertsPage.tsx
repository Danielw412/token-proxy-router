import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import { Skeleton } from '@/components/ui/Skeleton';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconPlus, IconTrash2 } from '@/components/ui/icons';
import {
  Badge,
  Empty,
  Note,
  Page,
  PageHeader,
  Panel,
  PrivateText,
  ProviderIcon,
  ResourceError,
} from '@/components/tpr/Ui';
import { formatWhen } from '@/features/quota/chart/chartModel';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useTprResource } from '@/hooks/useTprResource';
import { tprApi, type AlertChannelType, type AlertsConfig } from '@/services/api/tpr';
import { useNotificationStore } from '@/stores';
import { getErrorMessage } from '@/utils/helpers';
import styles from './AlertsPage.module.scss';

const CHANNEL_TYPES: AlertChannelType[] = ['ntfy', 'discord', 'slack', 'webhook'];

export function AlertsPage() {
  const { t, i18n } = useTranslation();
  const locale = i18n.language;
  const showNotification = useNotificationStore((state) => state.showNotification);
  const config = useTprResource(() => tprApi.getAlerts(), []);
  const events = useTprResource(() => tprApi.alertEvents(50), [], { intervalMs: 60_000 });
  const refreshAll = async () => {
    await Promise.all([config.refresh(), events.refresh()]);
  };
  useHeaderRefresh(refreshAll);

  const [draft, setDraft] = useState<AlertsConfig | null>(null);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [results, setResults] = useState<Array<{ name: string; ok: boolean; error: string }> | null>(
    null
  );

  useEffect(() => {
    if (config.data) setDraft(config.data);
  }, [config.data]);

  const dirty = draft && config.data ? JSON.stringify(draft) !== JSON.stringify(config.data) : false;
  const patch = (next: Partial<AlertsConfig>) => draft && setDraft({ ...draft, ...next });

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    try {
      await tprApi.putAlerts(draft);
      showNotification(t('tpr.alerts.saved'), 'success');
      await config.refresh();
    } catch (err) {
      showNotification(getErrorMessage(err, t('common.unknown_error')), 'error');
    } finally {
      setSaving(false);
    }
  };

  const test = async () => {
    setTesting(true);
    setResults(null);
    try {
      setResults(await tprApi.testAlerts());
    } catch (err) {
      showNotification(getErrorMessage(err, t('common.unknown_error')), 'error');
    } finally {
      setTesting(false);
    }
  };

  const active = (events.data ?? []).filter((event) => event.active);

  return (
    <Page>
      <PageHeader
        title={t('tpr.alerts.title')}
        meta={t('tpr.alerts.meta', { count: active.length })}
        actions={
          draft ? (
            <>
              <ToggleSwitch
                label={t('tpr.alerts.enabled')}
                checked={draft.enabled}
                onChange={(enabled) => patch({ enabled })}
              />
              <Button onClick={() => void save()} disabled={!dirty} loading={saving}>
                {t('common.save')}
              </Button>
            </>
          ) : undefined
        }
      />

      {config.error && (
        <ResourceError
          message={config.error}
          status={config.errorStatus}
          onRetry={() => void config.refresh()}
        />
      )}

      {config.loading && !draft ? (
        <Skeleton height={260} rounded={12} />
      ) : draft ? (
        <div className={styles.columns}>
          <Panel title={t('tpr.alerts.thresholds_title')} description={t('tpr.alerts.thresholds_description')}>
            <div className={styles.field}>
              <label htmlFor="alert-pool-hours">{t('tpr.alerts.pool_empty_label')}</label>
              <div className={styles.fieldControl}>
                <input
                  id="alert-pool-hours"
                  className={`input ${styles.number}`}
                  type="number"
                  min={0}
                  value={draft.pool_empty_within_hours}
                  onChange={(event) => patch({ pool_empty_within_hours: Number(event.target.value) })}
                />
                <span>{t('tpr.alerts.hours')}</span>
              </div>
            </div>
            <div className={styles.field}>
              <label htmlFor="alert-account-percent">{t('tpr.alerts.account_below_label')}</label>
              <div className={styles.fieldControl}>
                <input
                  id="alert-account-percent"
                  className={`input ${styles.number}`}
                  type="number"
                  min={0}
                  max={100}
                  value={draft.account_below_percent}
                  onChange={(event) => patch({ account_below_percent: Number(event.target.value) })}
                />
                <span>%</span>
              </div>
            </div>
            <div className={styles.field}>
              <span>{t('tpr.alerts.notify_reset_label')}</span>
              <ToggleSwitch
                ariaLabel={t('tpr.alerts.notify_reset_label')}
                checked={draft.notify_on_reset}
                onChange={(notify_on_reset) => patch({ notify_on_reset })}
              />
            </div>
          </Panel>

          <Panel
            title={t('tpr.alerts.channels_title')}
            description={t('tpr.alerts.channels_description')}
            actions={
              <>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => void test()}
                  loading={testing}
                  disabled={draft.channels.length === 0 || dirty}
                  title={dirty ? t('tpr.alerts.save_first') : undefined}
                >
                  {t('tpr.alerts.test')}
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() =>
                    patch({
                      channels: [...draft.channels, { type: 'ntfy', url: '', name: '' }],
                    })
                  }
                >
                  <IconPlus size={13} />
                  {t('tpr.alerts.add_channel')}
                </Button>
              </>
            }
          >
            {draft.channels.length === 0 ? (
              <p className={styles.quiet}>{t('tpr.alerts.no_channels')}</p>
            ) : (
              <ul className={styles.channels}>
                {draft.channels.map((channel, index) => {
                  const update = (next: Partial<typeof channel>) =>
                    patch({
                      channels: draft.channels.map((item, position) =>
                        position === index ? { ...item, ...next } : item
                      ),
                    });
                  return (
                    <li key={index} className={styles.channel}>
                      <div className={styles.channelType}>
                        <Select
                          value={channel.type}
                          size="sm"
                          ariaLabel={t('tpr.alerts.channel_type')}
                          onChange={(type) => update({ type: type as AlertChannelType })}
                          options={CHANNEL_TYPES.map((type) => ({
                            value: type,
                            label: t(`tpr.alerts.channel_${type}`),
                          }))}
                        />
                      </div>
                      <input
                        className={`input ${styles.channelName}`}
                        value={channel.name}
                        placeholder={t('tpr.alerts.channel_name')}
                        aria-label={t('tpr.alerts.channel_name')}
                        onChange={(event) => update({ name: event.target.value })}
                      />
                      <input
                        className={`input ${styles.channelUrl}`}
                        value={channel.url}
                        placeholder={t('tpr.alerts.channel_url')}
                        aria-label={t('tpr.alerts.channel_url')}
                        spellCheck={false}
                        onChange={(event) => update({ url: event.target.value })}
                      />
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label={t('tpr.alerts.remove_channel', { name: channel.name || channel.type })}
                        onClick={() =>
                          patch({ channels: draft.channels.filter((_, position) => position !== index) })
                        }
                      >
                        <IconTrash2 size={14} />
                      </Button>
                    </li>
                  );
                })}
              </ul>
            )}
            {results && (
              <div className={styles.results}>
                {results.map((result) => (
                  <Note key={result.name} tone={result.ok ? 'good' : 'error'}>
                    {result.ok
                      ? t('tpr.alerts.test_ok', { name: result.name })
                      : t('tpr.alerts.test_failed', { name: result.name, error: result.error })}
                  </Note>
                ))}
              </div>
            )}
          </Panel>
        </div>
      ) : null}

      <Panel title={t('tpr.alerts.events_title')}>
        {events.error ? (
          <ResourceError
            message={events.error}
            status={events.errorStatus}
            onRetry={() => void events.refresh()}
          />
        ) : (events.data ?? []).length === 0 ? (
          <Empty title={t('tpr.alerts.no_events_title')} description={t('tpr.alerts.no_events_description')} />
        ) : (
          <ul className={styles.events}>
            {(events.data ?? []).map((event, index) => (
              <li key={`${event.at}-${index}`} className={styles.event}>
                <span className={styles.eventWhen}>{formatWhen(Date.parse(event.at), locale)}</span>
                {event.provider ? <ProviderIcon provider={event.provider} size={15} /> : null}
                <span className={styles.eventMessage}>
                  <PrivateText text={event.message} />
                </span>
                {event.active ? <Badge tone="warn">{t('tpr.alerts.active')}</Badge> : null}
              </li>
            ))}
          </ul>
        )}
      </Panel>
    </Page>
  );
}
