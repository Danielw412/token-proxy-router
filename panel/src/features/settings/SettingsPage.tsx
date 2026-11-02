import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router-dom';
import { parseDocument } from 'yaml';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import { Skeleton } from '@/components/ui/Skeleton';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconPlus, IconTrash2 } from '@/components/ui/icons';
import { Note, Page, PageHeader, Panel, ResourceError, Segmented } from '@/components/tpr/Ui';
import { ConfigPage } from '@/features/config/ConfigPage';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useTprResource } from '@/hooks/useTprResource';
import { apiClient, apiKeysApi, configFileApi } from '@/services/api';
import {
  applyConfigPatch,
  buildConfigPatch,
  hasConfigPatchChanges,
} from '@/services/api/configPatch';
import { tprApi } from '@/services/api/tpr';
import { useConfigStore, useNotificationStore } from '@/stores';
import { maskApiKey } from '@/utils/format';
import { getErrorMessage } from '@/utils/helpers';
import styles from './SettingsPage.module.scss';

type SettingsView = 'basic' | 'advanced';

export interface BasicSettings {
  port: number;
  debug: boolean;
  loggingToFile: boolean;
  requestLog: boolean;
  routingStrategy: string;
  sessionAffinity: boolean;
  requestRetry: number;
}

const toNumber = (value: unknown, fallback: number): number =>
  typeof value === 'number' && Number.isFinite(value) ? value : fallback;

const PORT = ['server', 'port'];
const LOGS = ['observability', 'logs'];
const RETRY = ['routing', 'retry', 'request-retry'];

/** The essentials, read from the same v8 YAML document the advanced editor writes. */
export const readBasicSettings = (yaml: string): BasicSettings => {
  const doc = parseDocument(yaml);
  const strategy = doc.getIn(['routing', 'strategy']);
  return {
    port: toNumber(doc.getIn(PORT), 8317),
    debug: doc.getIn([...LOGS, 'debug']) === true,
    loggingToFile: doc.getIn([...LOGS, 'logging-to-file']) === true,
    requestLog: doc.getIn([...LOGS, 'request-log']) === true,
    routingStrategy: typeof strategy === 'string' ? strategy : 'round-robin',
    sessionAffinity: doc.getIn(['routing', 'session-affinity']) === true,
    requestRetry: toNumber(doc.getIn(RETRY), 3),
  };
};

export const writeBasicSettings = (yaml: string, next: BasicSettings): string => {
  const doc = parseDocument(yaml);
  doc.setIn(PORT, next.port);
  doc.setIn([...LOGS, 'debug'], next.debug);
  doc.setIn([...LOGS, 'logging-to-file'], next.loggingToFile);
  doc.setIn([...LOGS, 'request-log'], next.requestLog);
  doc.setIn(['routing', 'strategy'], next.routingStrategy);
  doc.setIn(['routing', 'session-affinity'], next.sessionAffinity);
  doc.setIn(RETRY, next.requestRetry);
  return doc.toString();
};

export function SettingsPage() {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const wantsField = params.has('field');
  const view: SettingsView = params.get('view') === 'advanced' || wantsField ? 'advanced' : 'basic';

  const setView = (next: SettingsView) => {
    const nextParams = new URLSearchParams(params);
    if (next === 'advanced') nextParams.set('view', 'advanced');
    else nextParams.delete('view');
    setParams(nextParams, { replace: true });
  };

  return (
    <Page>
      <PageHeader
        title={t('tpr.settings.title')}
        meta={view === 'basic' ? t('tpr.settings.meta_basic') : t('tpr.settings.meta_advanced')}
        actions={
          <Segmented<SettingsView>
            ariaLabel={t('tpr.settings.view')}
            value={view}
            onChange={setView}
            options={[
              { value: 'basic', label: t('tpr.settings.view_basic') },
              { value: 'advanced', label: t('tpr.settings.view_advanced') },
            ]}
          />
        }
      />
      {view === 'basic' ? <BasicSettingsView /> : <ConfigPage />}
    </Page>
  );
}

function BasicSettingsView() {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const clearCache = useConfigStore((state) => state.clearCache);
  const yaml = useTprResource(() => configFileApi.fetchConfigYaml(), []);
  const keys = useTprResource(() => apiKeysApi.list(), []);
  const tailscale = useTprResource(() => tprApi.tailscale(), []);
  useHeaderRefresh(async () => {
    await Promise.all([yaml.refresh(), keys.refresh(), tailscale.refresh()]);
  });

  const loaded = useMemo(() => (yaml.data !== null ? readBasicSettings(yaml.data) : null), [yaml.data]);
  const [draft, setDraft] = useState<BasicSettings | null>(null);
  const [saving, setSaving] = useState(false);
  const [newKey, setNewKey] = useState('');
  const [keysBusy, setKeysBusy] = useState(false);

  useEffect(() => {
    if (loaded) setDraft(loaded);
  }, [loaded]);

  const dirty = Boolean(draft && loaded && JSON.stringify(draft) !== JSON.stringify(loaded));
  const portChanged = Boolean(draft && loaded && draft.port !== loaded.port);
  const patch = (next: Partial<BasicSettings>) => draft && setDraft({ ...draft, ...next });

  const save = async () => {
    if (!draft || yaml.data === null) return;
    setSaving(true);
    try {
      const plan = buildConfigPatch(yaml.data, writeBasicSettings(yaml.data, draft));
      if (hasConfigPatchChanges(plan)) {
        await applyConfigPatch(plan, apiClient.getConnectionRevision());
      }
      clearCache();
      showNotification(t('tpr.settings.saved'), 'success');
      await yaml.refresh();
    } catch (err) {
      showNotification(getErrorMessage(err, t('common.unknown_error')), 'error');
    } finally {
      setSaving(false);
    }
  };

  const replaceKeys = async (next: string[]) => {
    setKeysBusy(true);
    try {
      await apiKeysApi.replace(next);
      await keys.refresh();
    } catch (err) {
      showNotification(getErrorMessage(err, t('common.unknown_error')), 'error');
    } finally {
      setKeysBusy(false);
    }
  };

  const generateKey = () => {
    const bytes = crypto.getRandomValues(new Uint8Array(24));
    const body = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
    setNewKey(`sk-${body}`);
  };

  if (yaml.error && !yaml.data) {
    return <ResourceError message={yaml.error} status={yaml.errorStatus} onRetry={() => void yaml.refresh()} />;
  }
  if (!draft) return <Skeleton height={420} rounded={12} />;

  return (
    <div className={styles.stack}>
      <Panel title={t('tpr.settings.server_title')} description={t('tpr.settings.server_description')}>
        <div className={styles.row}>
          <label htmlFor="settings-port">{t('tpr.settings.port')}</label>
          <input
            id="settings-port"
            className={`input ${styles.number}`}
            type="number"
            min={1}
            max={65535}
            value={draft.port}
            onChange={(event) => patch({ port: Number(event.target.value) })}
          />
        </div>
        {portChanged && <Note tone="warn">{t('tpr.settings.port_warning')}</Note>}
      </Panel>

      <Panel
        title={t('tpr.settings.keys_title')}
        description={t('tpr.settings.keys_description')}
        actions={
          <Link className={styles.link} to="/connect">
            {t('tpr.settings.keys_connect')}
          </Link>
        }
      >
        {keys.error ? (
          <ResourceError message={keys.error} status={keys.errorStatus} onRetry={() => void keys.refresh()} />
        ) : (keys.data ?? []).length === 0 ? (
          <p className={styles.quiet}>{t('tpr.settings.keys_empty')}</p>
        ) : (
          <ul className={styles.keys}>
            {(keys.data ?? []).map((key, index) => (
              <li key={`${index}-${key.length}`} className={styles.key}>
                <span className={styles.keyValue}>{maskApiKey(key)}</span>
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={keysBusy}
                  aria-label={t('tpr.settings.keys_remove', { key: maskApiKey(key) })}
                  onClick={() => void replaceKeys((keys.data ?? []).filter((_, position) => position !== index))}
                >
                  <IconTrash2 size={14} />
                </Button>
              </li>
            ))}
          </ul>
        )}
        <form
          className={styles.keyForm}
          onSubmit={(event) => {
            event.preventDefault();
            const value = newKey.trim();
            if (!value) return;
            void replaceKeys([...(keys.data ?? []), value]).then(() => setNewKey(''));
          }}
        >
          <input
            className={`input ${styles.keyInput}`}
            value={newKey}
            spellCheck={false}
            autoComplete="off"
            placeholder={t('tpr.settings.keys_placeholder')}
            aria-label={t('tpr.settings.keys_placeholder')}
            onChange={(event) => setNewKey(event.target.value)}
          />
          <Button variant="secondary" size="sm" type="button" onClick={generateKey}>
            {t('tpr.settings.keys_generate')}
          </Button>
          <Button variant="secondary" size="sm" type="submit" disabled={!newKey.trim() || keysBusy}>
            <IconPlus size={13} />
            {t('tpr.settings.keys_add')}
          </Button>
        </form>
      </Panel>

      <Panel
        title={t('tpr.settings.tailnet_title')}
        description={t('tpr.settings.tailnet_description')}
        actions={
          <Link className={styles.link} to="/system">
            {t('tpr.settings.tailnet_manage')}
          </Link>
        }
      >
        {tailscale.error ? (
          <ResourceError message={tailscale.error} status={tailscale.errorStatus} />
        ) : tailscale.data?.exposed ? (
          <p className={styles.quiet}>{t('tpr.settings.tailnet_on', { url: tailscale.data.url })}</p>
        ) : (
          <p className={styles.quiet}>{tailscale.data?.hint || t('tpr.settings.tailnet_off')}</p>
        )}
      </Panel>

      <Panel title={t('tpr.settings.logging_title')} description={t('tpr.settings.logging_description')}>
        <div className={styles.row}>
          <span>{t('tpr.settings.logging_file')}</span>
          <ToggleSwitch
            ariaLabel={t('tpr.settings.logging_file')}
            checked={draft.loggingToFile}
            onChange={(loggingToFile) => patch({ loggingToFile })}
          />
        </div>
        <div className={styles.row}>
          <span>{t('tpr.settings.logging_requests')}</span>
          <ToggleSwitch
            ariaLabel={t('tpr.settings.logging_requests')}
            checked={draft.requestLog}
            onChange={(requestLog) => patch({ requestLog })}
          />
        </div>
        <div className={styles.row}>
          <span>{t('tpr.settings.logging_debug')}</span>
          <ToggleSwitch
            ariaLabel={t('tpr.settings.logging_debug')}
            checked={draft.debug}
            onChange={(debug) => patch({ debug })}
          />
        </div>
      </Panel>

      <Panel title={t('tpr.settings.routing_title')} description={t('tpr.settings.routing_description')}>
        <div className={styles.row}>
          <label id="settings-strategy-label">{t('tpr.settings.routing_strategy')}</label>
          <div className={styles.select}>
            <Select
              value={draft.routingStrategy}
              size="sm"
              ariaLabelledBy="settings-strategy-label"
              onChange={(routingStrategy) => patch({ routingStrategy })}
              options={[
                { value: 'round-robin', label: t('basic_settings.routing_strategy_round_robin') },
                { value: 'weighted-round-robin', label: t('basic_settings.routing_strategy_weighted_round_robin') },
                { value: 'fill-first', label: t('basic_settings.routing_strategy_fill_first') },
              ]}
            />
          </div>
        </div>
        <div className={styles.row}>
          <span>{t('tpr.settings.routing_affinity')}</span>
          <ToggleSwitch
            ariaLabel={t('tpr.settings.routing_affinity')}
            checked={draft.sessionAffinity}
            onChange={(sessionAffinity) => patch({ sessionAffinity })}
          />
        </div>
        <div className={styles.row}>
          <label htmlFor="settings-retry">{t('tpr.settings.routing_retry')}</label>
          <input
            id="settings-retry"
            className={`input ${styles.number}`}
            type="number"
            min={0}
            max={100}
            value={draft.requestRetry}
            onChange={(event) => patch({ requestRetry: Number(event.target.value) })}
          />
        </div>
        <p className={styles.footnote}>
          {t('tpr.settings.routing_note')}{' '}
          <Link className={styles.link} to="/routing">
            {t('tpr.settings.routing_link')}
          </Link>
        </p>
      </Panel>

      <p className={styles.footnote}>
        {t('tpr.settings.providers_note')}{' '}
        <Link className={styles.link} to="/ai-providers">
          {t('tpr.common.more_providers')}
        </Link>
      </p>

      {dirty && (
        <div className={styles.saveBar} role="status">
          <span>{t('tpr.settings.unsaved')}</span>
          <div className={styles.saveActions}>
            <Button variant="ghost" size="sm" onClick={() => setDraft(loaded)} disabled={saving}>
              {t('tpr.settings.discard')}
            </Button>
            <Button size="sm" onClick={() => void save()} loading={saving}>
              {t('common.save')}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
