import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { IconPlus } from '@/components/ui/icons';
import { Skeleton } from '@/components/ui/Skeleton';
import {
  Badge,
  Empty,
  Page,
  PageHeader,
  Panel,
  PrivateEmail,
  ProviderIcon,
  ProviderName,
  ResourceError,
  Segmented,
} from '@/components/tpr/Ui';
import { AuthFilesPage } from '@/features/authFiles/AuthFilesPage';
import { AUTH_FILES_CHANGED_EVENT } from '@/features/authFiles/authFilesEvents';
import { OAuthPage } from '@/features/oauth/OAuthPage';
import { formatDuration, formatShortDate } from '@/features/quota/chart/chartModel';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useTprResource } from '@/hooks/useTprResource';
import { tprApi, type AccountHealth, type HealthStatus } from '@/services/api/tpr';
import { useAccountLogin } from './useAccountLogin';
import styles from './AccountsPage.module.scss';

type AccountsView = 'accounts' | 'files';

const HEALTH_TONE: Record<HealthStatus, 'good' | 'warn' | 'bad' | 'neutral'> = {
  ok: 'good',
  expiring: 'warn',
  cooling: 'warn',
  expired: 'bad',
  failing: 'bad',
  disabled: 'neutral',
};

/** Worst first, so anything needing attention sits at the top of its provider's list. */
const HEALTH_ORDER: Record<HealthStatus, number> = {
  failing: 0,
  expired: 1,
  expiring: 2,
  cooling: 3,
  disabled: 4,
  ok: 5,
};

const PROVIDER_ORDER = ['claude', 'codex'];

export const groupAccountsByProvider = (accounts: AccountHealth[]) => {
  const groups = new Map<string, AccountHealth[]>();
  accounts.forEach((account) => {
    const list = groups.get(account.provider) ?? [];
    list.push(account);
    groups.set(account.provider, list);
  });
  const rank = (provider: string) => {
    const index = PROVIDER_ORDER.indexOf(provider);
    return index === -1 ? PROVIDER_ORDER.length : index;
  };
  return Array.from(groups.entries())
    .sort(([a], [b]) => rank(a) - rank(b) || a.localeCompare(b))
    .map(([provider, list]) => ({
      provider,
      accounts: [...list].sort((a, b) => HEALTH_ORDER[a.status] - HEALTH_ORDER[b.status]),
    }));
};

export function AccountsPage() {
  const { t, i18n } = useTranslation();
  const locale = i18n.language;
  const [params, setParams] = useSearchParams();
  const view: AccountsView = params.get('view') === 'files' ? 'files' : 'accounts';
  const [addOpen, setAddOpen] = useState(params.get('add') === '1');
  const [moreOpen, setMoreOpen] = useState(false);

  const { data, loading, error, errorStatus, refresh } = useTprResource(() => tprApi.health(), [], {
    intervalMs: 30_000,
  });
  useHeaderRefresh(refresh, view === 'accounts');

  const login = useAccountLogin();

  useEffect(() => {
    const onChange = () => void refresh();
    window.addEventListener(AUTH_FILES_CHANGED_EVENT, onChange);
    return () => window.removeEventListener(AUTH_FILES_CHANGED_EVENT, onChange);
  }, [refresh]);

  const accounts = useMemo(() => data ?? [], [data]);
  const groups = useMemo(() => groupAccountsByProvider(accounts), [accounts]);
  const healthy = accounts.filter((account) => account.status === 'ok').length;
  const now = Date.now();

  const setView = (next: AccountsView) => {
    const nextParams = new URLSearchParams(params);
    if (next === 'files') nextParams.set('view', 'files');
    else nextParams.delete('view');
    setParams(nextParams, { replace: true });
  };

  const relative = (at: string | null) =>
    at ? t('tpr.common.ago', { time: formatDuration(now - Date.parse(at)) }) : '—';

  return (
    <Page>
      <PageHeader
        title={t('tpr.accounts.title')}
        meta={t('tpr.accounts.meta', { count: accounts.length, healthy })}
        actions={
          <>
            <Segmented<AccountsView>
              ariaLabel={t('tpr.accounts.view')}
              value={view}
              onChange={setView}
              options={[
                { value: 'accounts', label: t('tpr.accounts.view_accounts') },
                { value: 'files', label: t('tpr.accounts.view_files') },
              ]}
            />
            <Button onClick={() => setAddOpen((value) => !value)} aria-expanded={addOpen}>
              <IconPlus size={15} />
              {t('tpr.common.add_account')}
            </Button>
          </>
        }
      />

      {addOpen && (
        <Panel title={t('tpr.accounts.add_title')} description={t('tpr.accounts.add_description')}>
          <div className={styles.addRow}>
            {['claude', 'codex'].map((provider) => (
              <button
                key={provider}
                type="button"
                className={styles.addTile}
                aria-haspopup="dialog"
                onClick={() => login.open(provider)}
              >
                <ProviderIcon provider={provider} size={22} />
                <span className={styles.addName}>
                  <ProviderName provider={provider} />
                </span>
                <span className={styles.addHint}>{t('auth_login.method_browser')}</span>
              </button>
            ))}
          </div>
          <button
            type="button"
            className={styles.moreToggle}
            aria-expanded={moreOpen}
            onClick={() => setMoreOpen((value) => !value)}
          >
            {t('tpr.common.more_providers')}
          </button>
          {moreOpen && (
            <div className={styles.more}>
              <OAuthPage embedded />
              <p className={styles.moreNote}>
                {t('tpr.accounts.api_key_note')}{' '}
                <Link to="/ai-providers">{t('tpr.accounts.api_key_link')}</Link>
              </p>
            </div>
          )}
        </Panel>
      )}

      {view === 'files' ? (
        <AuthFilesPage />
      ) : (
        <>
          {error && (
            <ResourceError message={error} status={errorStatus} onRetry={() => void refresh()} />
          )}
          {loading && !data ? (
            <Skeleton height={360} rounded={12} />
          ) : groups.length === 0 && !error ? (
            <Panel>
              <Empty
                title={t('tpr.accounts.empty_title')}
                description={t('tpr.accounts.empty_description')}
                action={<Button onClick={() => setAddOpen(true)}>{t('tpr.common.add_account')}</Button>}
              />
            </Panel>
          ) : (
            groups.map((group) => (
              <Panel
                key={group.provider}
                title={
                  <span className={styles.groupTitle}>
                    <ProviderIcon provider={group.provider} size={18} />
                    <ProviderName provider={group.provider} />
                    <span className={styles.groupCount}>
                      {t('tpr.quota.account_count', { count: group.accounts.length })}
                    </span>
                  </span>
                }
              >
                <div className={styles.head} aria-hidden="true">
                  <span>{t('tpr.accounts.col_account')}</span>
                  <span>{t('tpr.accounts.col_status')}</span>
                  <span>{t('tpr.accounts.col_success')}</span>
                  <span>{t('tpr.accounts.col_error')}</span>
                  <span>{t('tpr.accounts.col_expires')}</span>
                  <span />
                </div>
                <ul>
                  {group.accounts.map((account) => {
                    const needsLogin = account.status === 'expired' || account.status === 'failing';
                    const reauth = account.reauthProvider ?? account.provider;
                    return (
                      <li key={account.id} className={styles.row}>
                        <div className={styles.name}>
                          <PrivateEmail value={account.email} fallback={account.label || account.id} />
                          {account.label && account.email ? (
                            <span className={styles.label}>{account.label}</span>
                          ) : null}
                        </div>
                        <div className={styles.status}>
                          <Badge tone={HEALTH_TONE[account.status]}>
                            {t(`tpr.accounts.status_${account.status}`)}
                          </Badge>
                          {account.reason ? <span className={styles.reason}>{account.reason}</span> : null}
                        </div>
                        <div className={styles.cell}>
                          <span className={styles.cellLabel}>{t('tpr.accounts.col_success')}</span>
                          {relative(account.lastSuccessAt)}
                        </div>
                        <div className={styles.cell}>
                          <span className={styles.cellLabel}>{t('tpr.accounts.col_error')}</span>
                          {account.lastError ? (
                            <span title={account.lastError}>
                              {relative(account.lastErrorAt)}
                              <span className={styles.errorText}>{account.lastError}</span>
                            </span>
                          ) : (
                            '—'
                          )}
                        </div>
                        <div className={styles.cell}>
                          <span className={styles.cellLabel}>{t('tpr.accounts.col_expires')}</span>
                          {account.tokenExpiresAt
                            ? formatShortDate(Date.parse(account.tokenExpiresAt), locale)
                            : '—'}
                        </div>
                        <div className={styles.action}>
                          <Button
                            variant={needsLogin ? 'primary' : 'secondary'}
                            size="sm"
                            aria-haspopup="dialog"
                            onClick={() => login.open(reauth)}
                          >
                            {t('tpr.accounts.relogin')}
                          </Button>
                        </div>
                      </li>
                    );
                  })}
                </ul>
              </Panel>
            ))
          )}
        </>
      )}

      {login.dialog}
    </Page>
  );
}
