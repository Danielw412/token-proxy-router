import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Collapsible } from '@/components/ui/Collapsible';
import { Modal } from '@/components/ui/Modal';
import { Skeleton } from '@/components/ui/Skeleton';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconExternalLink } from '@/components/ui/icons';
import {
  Badge,
  Note,
  Page,
  PageHeader,
  Panel,
  PrivateEmail,
  ResourceError,
  Stat,
} from '@/components/tpr/Ui';
import { formatDuration, formatWhen } from '@/features/quota/chart/chartModel';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useTprResource } from '@/hooks/useTprResource';
import { useApiKeysForModels } from '@/hooks/useApiKeysForModels';
import { tprApi, type TprTailscale } from '@/services/api/tpr';
import { useAuthStore, useModelsStore, useNotificationStore } from '@/stores';
import { STORAGE_KEY_AUTH } from '@/utils/constants';
import { downloadBlob } from '@/utils/download';
import { getErrorMessage } from '@/utils/helpers';
import { classifyModels } from '@/utils/models';
import styles from './SystemPage.module.scss';

const UPDATE_POLL_MS = 1500;
const MIN_PASSPHRASE = 12;

export function SystemPage() {
  const { t, i18n } = useTranslation();
  const locale = i18n.language;
  const showNotification = useNotificationStore((state) => state.showNotification);
  const system = useTprResource(() => tprApi.system(), []);
  const tailscale = useTprResource(() => tprApi.tailscale(), []);
  useHeaderRefresh(async () => {
    await Promise.all([system.refresh(), tailscale.refresh()]);
  });

  const notify = (err: unknown) =>
    showNotification(getErrorMessage(err, t('common.unknown_error')), 'error');

  return (
    <Page>
      <PageHeader
        title={t('tpr.system.title')}
        meta={
          system.data
            ? t('tpr.system.meta', {
                version: system.data.version,
                uptime: formatDuration(system.data.uptime_seconds * 1000),
              })
            : undefined
        }
      />

      {system.error && (
        <ResourceError
          message={system.error}
          status={system.errorStatus}
          onRetry={() => void system.refresh()}
        />
      )}

      <UpdateCard
        systemVersion={system.data?.version ?? ''}
        refreshSystem={system.refresh}
        onError={notify}
        locale={locale}
      />

      <TailscaleCard
        data={tailscale.data}
        error={tailscale.error}
        errorStatus={tailscale.errorStatus}
        loading={tailscale.loading}
        refresh={tailscale.refresh}
        onError={notify}
      />

      <BackupCard onError={notify} />

      <Panel title={t('tpr.system.service_title')} description={t('tpr.system.service_description')}>
        {system.loading && !system.data ? (
          <Skeleton height={64} />
        ) : system.data ? (
          <div className={styles.stats}>
            <Stat
              label={t('tpr.system.service_kind')}
              value={t(`tpr.system.service_kind_${system.data.service.kind.replace('-', '_')}`, {
                defaultValue: system.data.service.kind,
              })}
            />
            <Stat
              label={t('tpr.system.service_state')}
              value={system.data.service.running ? t('tpr.system.running') : t('tpr.system.stopped')}
              tone={system.data.service.running ? 'good' : 'warn'}
            />
            <Stat label={t('tpr.system.data_dir')} value={<code>{system.data.data_dir}</code>} />
            <Stat label={t('tpr.system.platform')} value={`${system.data.os} / ${system.data.arch}`} />
          </div>
        ) : null}
      </Panel>

      <Panel title={t('tpr.system.about_title')} description={t('tpr.system.about_description')}>
        <div className={styles.stats}>
          <Stat label={t('tpr.system.version')} value={system.data?.version ?? '—'} sub={system.data?.commit} />
          <Stat label={t('tpr.system.upstream')} value={system.data?.upstream_version ?? '—'} />
          <Stat label={t('tpr.system.panel')} value={system.data?.panel_version || __APP_VERSION__ || '—'} />
        </div>
        <p className={styles.credits}>{t('tpr.system.credits')}</p>
        <div className={styles.links}>
          <a href="https://github.com/Danielw412/token-proxy-router" target="_blank" rel="noopener noreferrer">
            Token Proxy Router <IconExternalLink size={12} />
          </a>
          <a href="https://github.com/router-for-me/CLIProxyAPI" target="_blank" rel="noopener noreferrer">
            CLIProxyAPI <IconExternalLink size={12} />
          </a>
          <a
            href="https://github.com/router-for-me/Cli-Proxy-API-Management-Center"
            target="_blank"
            rel="noopener noreferrer"
          >
            Management Center <IconExternalLink size={12} />
          </a>
        </div>
      </Panel>

      <Collapsible className={styles.more} label={t('tpr.system.models_title')} hint={t('tpr.system.models_hint')}>
        <ModelsList />
      </Collapsible>

      <Collapsible className={styles.more} label={t('tpr.system.this_browser_title')}>
        <ThisBrowser />
      </Collapsible>
    </Page>
  );
}

function UpdateCard({
  systemVersion,
  refreshSystem,
  onError,
  locale,
}: {
  systemVersion: string;
  refreshSystem: () => Promise<void>;
  onError: (err: unknown) => void;
  locale: string;
}) {
  const { t } = useTranslation();
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const [polling, setPolling] = useState(false);
  const update = useTprResource(() => tprApi.update(), [], {
    intervalMs: polling ? UPDATE_POLL_MS : 5 * 60_000,
  });
  const [checking, setChecking] = useState(false);
  const startVersionRef = useRef('');
  const data = update.data;
  const inFlight = Boolean(data && data.state !== 'idle' && data.state !== 'failed');
  const STEPS = ['downloading', 'verifying', 'applying', 'restarting'];
  const stepIndex = data ? STEPS.indexOf(data.state) : -1;

  useEffect(() => {
    if (inFlight) setPolling(true);
  }, [inFlight]);

  // During a restart the server may be briefly gone. Keep asking, and reload once it is back on a new version.
  useEffect(() => {
    if (!polling) return;
    const timer = window.setInterval(() => {
      void tprApi
        .system()
        .then((info) => {
          if (startVersionRef.current && info.version !== startVersionRef.current) {
            window.location.reload();
          } else if (!startVersionRef.current) {
            startVersionRef.current = info.version;
          }
        })
        .catch(() => undefined);
    }, UPDATE_POLL_MS);
    return () => window.clearInterval(timer);
  }, [polling]);

  useEffect(() => {
    if (data?.state === 'failed') setPolling(false);
  }, [data?.state]);

  const check = async () => {
    setChecking(true);
    try {
      await tprApi.checkUpdate();
      await update.refresh();
    } catch (err) {
      onError(err);
    } finally {
      setChecking(false);
    }
  };

  const apply = async () => {
    startVersionRef.current = systemVersion;
    try {
      await tprApi.applyUpdate();
      setPolling(true);
      await update.refresh();
    } catch (err) {
      onError(err);
    }
  };

  const rollback = () => {
    showConfirmation({
      title: t('tpr.system.rollback_title'),
      message: t('tpr.system.rollback_message'),
      variant: 'danger',
      confirmText: t('tpr.system.rollback'),
      onConfirm: async () => {
        startVersionRef.current = systemVersion;
        try {
          await tprApi.rollbackUpdate();
          setPolling(true);
          await update.refresh();
          await refreshSystem();
        } catch (err) {
          onError(err);
        }
      },
    });
  };

  if (update.error && !data) {
    return (
      <Panel title={t('tpr.system.update_title')}>
        <ResourceError message={update.error} status={update.errorStatus} onRetry={() => void update.refresh()} />
      </Panel>
    );
  }

  return (
    <Panel
      title={t('tpr.system.update_title')}
      description={
        data
          ? data.available
            ? t('tpr.system.update_available', { current: data.current, latest: data.latest })
            : data.error
              ? t('tpr.system.update_unknown', { current: data.current })
              : t('tpr.system.update_current', { current: data.current })
          : undefined
      }
      actions={
        <>
          <Button variant="secondary" size="sm" onClick={() => void check()} loading={checking} disabled={inFlight}>
            {t('tpr.system.update_check')}
          </Button>
          {data?.rollback_available && (
            <Button variant="secondary" size="sm" onClick={rollback} disabled={inFlight}>
              {t('tpr.system.rollback')}
            </Button>
          )}
          {data?.available && (
            <Button size="sm" onClick={() => void apply()} disabled={inFlight || !data.can_apply}>
              {t('tpr.system.update_apply', { version: data.latest })}
            </Button>
          )}
        </>
      }
    >
      {!data ? (
        <Skeleton height={40} />
      ) : (
        <>
          {inFlight && (
            <div className={styles.progress} role="status" aria-live="polite">
              <ol className={styles.steps}>
                {STEPS.map((step, index) => (
                  <li key={step} data-state={index < stepIndex ? 'done' : index === stepIndex ? 'active' : undefined}>
                    {t(`tpr.system.update_step_${step}`)}
                  </li>
                ))}
              </ol>
            </div>
          )}
          {data.error && <Note tone={data.state === 'failed' ? 'error' : 'warn'}>{data.error}</Note>}
          {!data.can_apply && data.available && data.reason && <Note tone="warn">{data.reason}</Note>}
          {data.available && data.notes && (
            <div className={styles.notes}>
              <div className={styles.notesTitle}>{t('tpr.system.release_notes')}</div>
              <p className={styles.notesBody}>{data.notes}</p>
              {data.notes_url && (
                <a href={data.notes_url} target="_blank" rel="noopener noreferrer">
                  {t('tpr.system.release_notes_link')} <IconExternalLink size={12} />
                </a>
              )}
            </div>
          )}
          {data.checked_at && (
            <p className={styles.checked}>
              {t('tpr.system.checked_at', { when: formatWhen(Date.parse(data.checked_at), locale) })}
            </p>
          )}
        </>
      )}
    </Panel>
  );
}

export function TailnetToggle({
  data,
  refresh,
  onError,
}: {
  data: TprTailscale;
  refresh: () => Promise<void>;
  onError: (err: unknown) => void;
}) {
  const { t } = useTranslation();
  const [busy, setBusy] = useState(false);

  const toggle = async (next: boolean) => {
    setBusy(true);
    try {
      if (next) await tprApi.expose(data.port || 8443);
      else await tprApi.unexpose();
      await refresh();
    } catch (err) {
      onError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className={styles.tailnet}>
      <div className={styles.tailnetRow}>
        <div className={styles.tailnetText}>
          <div className={styles.tailnetLabel}>{t('tpr.tailnet.expose')}</div>
          {data.exposed && data.url ? (
            <a className={styles.tailnetUrl} href={data.url} target="_blank" rel="noopener noreferrer">
              {data.url}
            </a>
          ) : (
            <div className={styles.tailnetHint}>{t('tpr.tailnet.expose_hint')}</div>
          )}
        </div>
        <ToggleSwitch
          ariaLabel={t('tpr.tailnet.expose')}
          checked={data.exposed}
          disabled={busy || (!data.can_expose && !data.exposed)}
          onChange={(next) => void toggle(next)}
        />
      </div>
      {!data.can_expose && data.hint && <Note tone="warn">{data.hint}</Note>}
    </div>
  );
}

function TailscaleCard({
  data,
  error,
  errorStatus,
  loading,
  refresh,
  onError,
}: {
  data: TprTailscale | null;
  error: string;
  errorStatus: number | null;
  loading: boolean;
  refresh: () => Promise<void>;
  onError: (err: unknown) => void;
}) {
  const { t } = useTranslation();
  const [login, setLogin] = useState('');
  const [busy, setBusy] = useState(false);

  const saveIdentity = async (enabled: boolean, logins: string[]) => {
    setBusy(true);
    try {
      await tprApi.setIdentity(enabled, logins);
      await refresh();
    } catch (err) {
      onError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Panel
      title={t('tpr.tailnet.title')}
      description={t('tpr.tailnet.description')}
      actions={
        data ? (
          <Badge tone={data.logged_in ? 'good' : 'warn'}>
            {data.logged_in
              ? t('tpr.tailnet.signed_in')
              : data.installed
                ? t('tpr.tailnet.not_signed_in')
                : t('tpr.tailnet.not_installed')}
          </Badge>
        ) : undefined
      }
    >
      {error && !data ? (
        <ResourceError message={error} status={errorStatus} onRetry={() => void refresh()} />
      ) : loading && !data ? (
        <Skeleton height={70} />
      ) : data ? (
        <>
          <TailnetToggle data={data} refresh={refresh} onError={onError} />
          <div className={styles.identity}>
            <div className={styles.tailnetRow}>
              <div className={styles.tailnetText}>
                <div className={styles.tailnetLabel}>{t('tpr.tailnet.identity')}</div>
                <div className={styles.tailnetHint}>{t('tpr.tailnet.identity_hint')}</div>
              </div>
              <ToggleSwitch
                ariaLabel={t('tpr.tailnet.identity')}
                checked={data.identity.enabled}
                disabled={busy || !data.identity.available}
                onChange={(next) => void saveIdentity(next, data.identity.allowed_logins)}
              />
            </div>
            <Note tone="warn">{t('tpr.tailnet.identity_warning')}</Note>
            {data.identity.available ? (
              <>
                <ul className={styles.logins}>
                  {data.identity.allowed_logins.map((item) => (
                    <li key={item}>
                      <PrivateEmail value={item} />
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={busy}
                        onClick={() =>
                          void saveIdentity(
                            data.identity.enabled,
                            data.identity.allowed_logins.filter((value) => value !== item)
                          )
                        }
                      >
                        {t('tpr.tailnet.remove_login')}
                      </Button>
                    </li>
                  ))}
                </ul>
                <form
                  className={styles.loginForm}
                  onSubmit={(event) => {
                    event.preventDefault();
                    const value = login.trim();
                    if (!value) return;
                    void saveIdentity(data.identity.enabled, [...data.identity.allowed_logins, value]).then(
                      () => setLogin('')
                    );
                  }}
                >
                  <input
                    className="input"
                    type="email"
                    value={login}
                    placeholder="alice@example.com"
                    aria-label={t('tpr.tailnet.allowed_login')}
                    onChange={(event) => setLogin(event.target.value)}
                  />
                  <Button variant="secondary" size="sm" type="submit" disabled={!login.trim() || busy}>
                    {t('tpr.tailnet.add_login')}
                  </Button>
                </form>
              </>
            ) : (
              <p className={styles.tailnetHint}>{t('tpr.tailnet.identity_unavailable')}</p>
            )}
          </div>
        </>
      ) : null}
    </Panel>
  );
}

function BackupCard({ onError }: { onError: (err: unknown) => void }) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const [passphrase, setPassphrase] = useState('');
  const [busy, setBusy] = useState(false);
  const [restoreOpen, setRestoreOpen] = useState(false);
  const [restorePassphrase, setRestorePassphrase] = useState('');
  const [restoreFile, setRestoreFile] = useState<File | null>(null);
  const tooShort = passphrase.length > 0 && passphrase.length < MIN_PASSPHRASE;

  const backup = async () => {
    setBusy(true);
    try {
      const blob = await tprApi.backup(passphrase);
      const stamp = new Date().toISOString().slice(0, 10);
      downloadBlob({ filename: `tpr-backup-${stamp}.tprbak`, blob });
      setPassphrase('');
      showNotification(t('tpr.system.backup_done'), 'success');
    } catch (err) {
      onError(err);
    } finally {
      setBusy(false);
    }
  };

  const restore = () => {
    if (!restoreFile) return;
    showConfirmation({
      title: t('tpr.system.restore_title'),
      message: t('tpr.system.restore_message'),
      variant: 'danger',
      confirmText: t('tpr.system.restore'),
      onConfirm: async () => {
        setBusy(true);
        try {
          const result = await tprApi.restore(restoreFile, restorePassphrase);
          setRestoreOpen(false);
          setRestorePassphrase('');
          setRestoreFile(null);
          // Only a server run by the service restarts by itself.
          if (result?.restart_required) {
            showNotification(t('tpr.system.restore_needs_restart'), 'warning');
          } else {
            showNotification(t('tpr.system.restore_done'), 'success');
          }
        } catch (err) {
          onError(err);
        } finally {
          setBusy(false);
        }
      },
    });
  };

  return (
    <Panel
      title={t('tpr.system.backup_title')}
      description={t('tpr.system.backup_description')}
      actions={
        <Button variant="secondary" size="sm" onClick={() => setRestoreOpen(true)}>
          {t('tpr.system.restore')}
        </Button>
      }
    >
      <form
        className={styles.backupForm}
        onSubmit={(event) => {
          event.preventDefault();
          void backup();
        }}
      >
        <input
          className="input"
          type="password"
          value={passphrase}
          autoComplete="new-password"
          placeholder={t('tpr.system.passphrase')}
          aria-label={t('tpr.system.passphrase')}
          aria-invalid={tooShort || undefined}
          onChange={(event) => setPassphrase(event.target.value)}
        />
        <Button variant="secondary" size="sm" type="submit" loading={busy} disabled={passphrase.length < MIN_PASSPHRASE}>
          {t('tpr.system.backup')}
        </Button>
      </form>
      <p className={styles.passHint} data-bad={tooShort || undefined}>
        {t('tpr.system.passphrase_hint', { count: MIN_PASSPHRASE })}
      </p>

      <Modal
        open={restoreOpen}
        onClose={() => setRestoreOpen(false)}
        title={t('tpr.system.restore_title')}
        footer={
          <>
            <Button variant="secondary" onClick={() => setRestoreOpen(false)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={restore} disabled={!restoreFile || !restorePassphrase} loading={busy}>
              {t('tpr.system.restore')}
            </Button>
          </>
        }
      >
        <div className={styles.restore}>
          <p>{t('tpr.system.restore_hint')}</p>
          <label className={styles.restoreField}>
            <span>{t('tpr.system.restore_file')}</span>
            <input
              type="file"
              onChange={(event) => setRestoreFile(event.target.files?.[0] ?? null)}
            />
          </label>
          <label className={styles.restoreField}>
            <span>{t('tpr.system.passphrase')}</span>
            <input
              className="input"
              type="password"
              autoComplete="off"
              value={restorePassphrase}
              onChange={(event) => setRestorePassphrase(event.target.value)}
            />
          </label>
        </div>
      </Modal>
    </Panel>
  );
}

function ModelsList() {
  const { t } = useTranslation();
  const apiBase = useAuthStore((state) => state.apiBase);
  const models = useModelsStore((state) => state.models);
  const loading = useModelsStore((state) => state.loading);
  const error = useModelsStore((state) => state.error);
  const fetchModels = useModelsStore((state) => state.fetchModels);
  const resolveKeys = useApiKeysForModels();

  useEffect(() => {
    void resolveKeys()
      .then((keys) => fetchModels(apiBase, keys[0]))
      .catch(() => undefined);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apiBase]);

  const groups = classifyModels(models, { otherLabel: t('tpr.system.models_other') });
  if (loading) return <p className={styles.checked}>{t('common.loading')}</p>;
  if (error) return <Note tone="error">{error}</Note>;
  if (models.length === 0) return <p className={styles.checked}>{t('system_info.models_empty')}</p>;
  return (
    <ul className={styles.modelGroups}>
      {groups.map((group) => (
        <li key={group.id}>
          <div className={styles.modelGroupName}>{group.label}</div>
          <div className={styles.modelTags}>
            {group.items.map((model) => (
              <code key={`${model.name}-${model.alias ?? ''}`}>{model.name}</code>
            ))}
          </div>
        </li>
      ))}
    </ul>
  );
}

function ThisBrowser() {
  const { t } = useTranslation();
  const logout = useAuthStore((state) => state.logout);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const showNotification = useNotificationStore((state) => state.showNotification);
  return (
    <div className={styles.browser}>
      <p>{t('tpr.system.this_browser_description')}</p>
      <Button
        variant="danger"
        size="sm"
        onClick={() =>
          showConfirmation({
            title: t('system_info.clear_login_title'),
            message: t('system_info.clear_login_confirm'),
            variant: 'danger',
            confirmText: t('common.confirm'),
            onConfirm: () => {
              logout();
              [STORAGE_KEY_AUTH, 'isLoggedIn', 'apiBase', 'apiUrl', 'managementKey'].forEach((key) =>
                localStorage.removeItem(key)
              );
              showNotification(t('notification.login_storage_cleared'), 'success');
            },
          })
        }
      >
        {t('system_info.clear_login_button')}
      </Button>
    </div>
  );
}
