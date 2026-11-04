import { useEffect, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { Collapsible } from '@/components/ui/Collapsible';
import { Select } from '@/components/ui/Select';
import { Skeleton } from '@/components/ui/Skeleton';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  Badge,
  Empty,
  Note,
  Page,
  PageHeader,
  Panel,
  Pills,
  PrivateEmail,
  ProviderIcon,
  ProviderName,
  QuotaBar,
  ResourceError,
} from '@/components/tpr/Ui';
import { maskEmails } from '@/components/tpr/emailText';
import { formatDuration, formatWhen } from '@/features/quota/chart/chartModel';
import { providerColor } from '@/features/quota/providerColor';
import { RoutingPage } from '@/features/routing/RoutingPage';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useNow } from '@/hooks/useNow';
import { useTprResource } from '@/hooks/useTprResource';
import {
  applyRoleChange,
  tprApi,
  type AccountRole,
  type RouterAccount,
  type RouterPool,
} from '@/services/api/tpr';
import { useNotificationStore, usePrivacyStore } from '@/stores';
import { getErrorMessage } from '@/utils/helpers';
import styles from './RouterPage.module.scss';

const REFRESH_MS = 15_000;
/** The pool the page opens on. */
const DEFAULT_POOL = 'codex';
/** The route picker's value for automatic routing. Account IDs are never empty or this. */
const ROUTE_AUTO = '__auto__';

const stateTone = (state: string): 'good' | 'warn' | 'bad' | 'neutral' => {
  if (state === 'ok') return 'good';
  if (state === 'low' || state === 'unknown') return 'warn';
  if (state === 'exhausted' || state === 'unavailable') return 'bad';
  return 'neutral';
};

function AccountLabel({ pool, id }: { pool: RouterPool; id: string }) {
  const account = pool.accounts.find((item) => item.id === id);
  return <PrivateEmail value={account?.email ?? ''} fallback={account?.label || id || '—'} />;
}

export function RouterPage() {
  const { t, i18n } = useTranslation();
  const locale = i18n.language;
  const showNotification = useNotificationStore((state) => state.showNotification);
  const { data, loading, error, errorStatus, refresh } = useTprResource(
    () => tprApi.routerPools(30),
    [],
    { intervalMs: REFRESH_MS }
  );
  useHeaderRefresh(refresh);

  const [provider, setProvider] = useState(DEFAULT_POOL);
  const [busy, setBusy] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const pools = data ?? [];
  const pool = pools.find((item) => item.provider === provider) ?? pools[0] ?? null;
  const now = useNow();

  const act = async (action: () => Promise<unknown>, success?: string) => {
    setBusy(true);
    try {
      await action();
      if (success) showNotification(success, 'success');
      await refresh();
    } catch (err) {
      showNotification(getErrorMessage(err, t('common.unknown_error')), 'error');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Page>
      <PageHeader
        title={t('tpr.router.title')}
        meta={
          pool
            ? t('tpr.router.meta', {
                accounts: pool.accounts.length,
                sessions: pool.sessionCount,
              })
            : undefined
        }
        actions={
          pool ? (
            <Button
              variant="secondary"
              disabled={busy}
              onClick={() => void act(() => tprApi.routerRefresh(pool.provider))}
            >
              {t('tpr.router.recheck')}
            </Button>
          ) : undefined
        }
      />

      <p className={styles.intro}>{t('tpr.router.intro')}</p>

      {pools.length > 0 && (
        <Pills
          ariaLabel={t('tpr.router.pool_filter')}
          active={pool?.provider ?? provider}
          onChange={setProvider}
          options={pools.map((item) => ({
            id: item.provider,
            icon: <ProviderIcon provider={item.provider} />,
            label: <ProviderName provider={item.provider} />,
            count: item.accounts.length,
          }))}
        />
      )}

      {error && <ResourceError message={error} status={errorStatus} onRetry={() => void refresh()} />}

      {loading && !data ? (
        <div className={styles.skeletons} aria-busy="true" aria-label={t('common.loading')}>
          <Skeleton height={120} rounded={12} />
          <Skeleton height={320} rounded={12} />
        </div>
      ) : pool ? (
        pool.accounts.length === 0 ? (
          <Panel>
            <Empty
              title={t('tpr.router.no_accounts_title')}
              description={t('tpr.router.no_accounts_description')}
              action={
                <Link to="/accounts?add=1">
                  <Button variant="secondary">{t('tpr.common.add_account')}</Button>
                </Link>
              }
            />
          </Panel>
        ) : (
          <PoolControls pool={pool} now={now} locale={locale} busy={busy} act={act} />
        )
      ) : null}

      <Collapsible
        className={styles.advanced}
        label={t('tpr.router.advanced')}
        hint={t('tpr.router.advanced_hint')}
        onToggle={(event) => setAdvancedOpen(event.currentTarget.open)}
      >
        {/* Upstream's priority board does its own loading, so it only mounts once opened. */}
        {advancedOpen ? <RoutingPage embedded /> : null}
      </Collapsible>
    </Page>
  );
}

function PoolControls({
  pool,
  now,
  locale,
  busy,
  act,
}: {
  pool: RouterPool;
  now: number;
  locale: string;
  busy: boolean;
  act: (action: () => Promise<unknown>, success?: string) => Promise<void>;
}) {
  const { t } = useTranslation();
  const color = providerColor(pool.provider);
  const provider = pool.provider;
  const current = pool.accounts.find((account) => account.id === pool.routing);
  const showEmails = usePrivacyStore((state) => state.showEmails);
  const pinned = pool.mode === 'manual' ? pool.pinned || pool.routing : '';

  // A Select can only show plain text, so emails are masked here the way PrivateEmail blurs them.
  // Plan and quota keep the options apart while emails are hidden, and match the table below.
  const accountOption = (account: RouterAccount) => {
    const name = account.email || account.label || account.id;
    const percent = (value: number | null | undefined) =>
      value === null || value === undefined ? '—' : `${Math.round(value)}%`;
    const parts = [
      showEmails ? name : maskEmails(name),
      account.plan,
      t('tpr.router.route_quota', {
        short: percent(account.short?.remaining),
        long: percent(account.long?.remaining),
      }),
      account.state === 'ok' ? '' : t(`tpr.router.state_${account.state}`, { defaultValue: account.state }),
    ];
    return { value: account.id, label: parts.filter(Boolean).join(' · ') };
  };

  const route = (value: string) => {
    if (value === ROUTE_AUTO) {
      if (pool.mode !== 'auto') void act(() => tprApi.routerSetMode(provider, 'auto'));
      return;
    }
    if (value === pinned) return;
    void act(() => tprApi.routerSetMode(provider, 'manual', value));
  };

  return (
    <>
      <Panel className={styles.current}>
        <div className={styles.currentMain}>
          <div className={styles.currentLabel}>{t('tpr.router.current_account')}</div>
          <div className={styles.currentName}>
            {pool.routing ? <AccountLabel pool={pool} id={pool.routing} /> : t('tpr.router.none_selected')}
            {current?.pinned ? <Badge tone="info">{t('tpr.router.pinned')}</Badge> : null}
          </div>
          <p className={styles.currentReason}>{pool.routingNote || t('tpr.router.no_reason')}</p>
        </div>
        <div className={styles.currentSide}>
          <label className={styles.routeLabel} htmlFor={`route-${provider}`}>
            {t('tpr.router.route_all')}
          </label>
          <div className={styles.routeSelect}>
            <Select
              id={`route-${provider}`}
              value={pinned || ROUTE_AUTO}
              disabled={busy}
              onChange={route}
              dropdownMinWidth={380}
              options={[
                { value: ROUTE_AUTO, label: t('tpr.router.route_auto') },
                ...pool.accounts.map(accountOption),
              ]}
            />
          </div>
          <p className={styles.modeHint}>
            {pinned ? t('tpr.router.mode_pinned_hint') : t('tpr.router.mode_auto_hint')}
          </p>
          {pinned ? (
            <div className={styles.routeSelect}>
              <Select
                value={pool.settings.manual_fallback === 'strict' ? 'strict' : 'auto'}
                size="sm"
                ariaLabel={t('tpr.router.fallback_label')}
                disabled={busy}
                onChange={(value) =>
                  void act(() => tprApi.routerPatchSettings(provider, { manual_fallback: value }))
                }
                options={[
                  { value: 'auto', label: t('tpr.router.fallback_auto') },
                  { value: 'strict', label: t('tpr.router.fallback_strict') },
                ]}
              />
            </div>
          ) : null}
        </div>
      </Panel>

      {pool.pause && (
        <Note
          tone="warn"
          actions={
            <Button
              variant="secondary"
              size="sm"
              disabled={busy}
              onClick={() => void act(() => tprApi.routerResume(provider))}
            >
              {t('tpr.router.resume')}
            </Button>
          }
        >
          {pool.pause.until
            ? t('tpr.router.paused_until', { when: formatWhen(Date.parse(pool.pause.until), locale) })
            : t('tpr.router.paused')}
          {pool.waiting > 0 ? ` ${t('tpr.router.waiting_count', { count: pool.waiting })}` : ''}
        </Note>
      )}
      {pool.lastError && <Note tone="error">{pool.lastError}</Note>}
      {pool.warnings.map((warning) => (
        <Note key={warning} tone="warn">
          {warning}
        </Note>
      ))}

      <Panel title={t('tpr.router.accounts_title')} description={t('tpr.router.accounts_description')}>
        <div className={styles.accountHead} aria-hidden="true">
          <span>{t('tpr.router.col_account')}</span>
          <span>{t('tpr.router.col_five_hour')}</span>
          <span>{t('tpr.router.col_weekly')}</span>
          <span>{t('tpr.router.col_role')}</span>
          <span />
        </div>
        <ul className={styles.accounts}>
          {pool.accounts.map((account) => (
            <AccountRow
              key={account.id}
              account={account}
              pool={pool}
              color={color}
              locale={locale}
              busy={busy}
              act={act}
            />
          ))}
        </ul>
      </Panel>

      <div className={styles.twoColumn}>
        <TrafficPanel pool={pool} busy={busy} act={act} />
        <RankingPanel pool={pool} busy={busy} act={act} />
      </div>

      <WindowsPanel pool={pool} locale={locale} busy={busy} act={act} />

      <div className={styles.twoColumn}>
        <Panel
          title={t('tpr.router.sessions_title')}
          description={t('tpr.router.sessions_description', {
            sessions: pool.sessionCount,
            warm: pool.warmSessions,
            waiting: pool.waiting,
          })}
        >
          {pool.sessions.length === 0 ? (
            <p className={styles.quiet}>{t('tpr.router.no_sessions')}</p>
          ) : (
            <ul className={styles.rows}>
              {pool.sessions.slice(0, 8).map((session) => (
                <li key={session.id} className={styles.row}>
                  <Badge tone={session.state === 'busy' ? 'good' : session.state === 'warm' ? 'info' : 'neutral'}>
                    {t(`tpr.router.session_${session.state}`, { defaultValue: session.state })}
                  </Badge>
                  <span className={styles.rowMain}>
                    <AccountLabel pool={pool} id={session.account} />
                  </span>
                  <span className={styles.rowMeta}>
                    {t('tpr.router.session_requests', { count: session.requests })}
                    {session.lastSeenAt
                      ? ` · ${t('tpr.common.ago', { time: formatDuration(now - Date.parse(session.lastSeenAt)) })}`
                      : ''}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </Panel>

        <Panel title={t('tpr.router.switches_title')} description={t('tpr.router.switches_description')}>
          {pool.history.length === 0 ? (
            <p className={styles.quiet}>{t('tpr.router.no_switches')}</p>
          ) : (
            <ul className={styles.rows}>
              {pool.history.slice(0, 8).map((entry, index) => (
                <li key={`${entry.at}-${index}`} className={styles.switchRow}>
                  <div className={styles.switchTop}>
                    <span className={styles.rowMain}>
                      {entry.from ? (
                        <>
                          <AccountLabel pool={pool} id={entry.from} /> →{' '}
                        </>
                      ) : null}
                      <AccountLabel pool={pool} id={entry.to} />
                    </span>
                    <span className={styles.rowMeta}>
                      {entry.at ? formatWhen(Date.parse(entry.at), locale) : ''}
                    </span>
                  </div>
                  <div className={styles.switchReason}>{entry.reason}</div>
                </li>
              ))}
            </ul>
          )}
        </Panel>
      </div>
    </>
  );
}

function AccountRow({
  account,
  pool,
  color,
  locale,
  busy,
  act,
}: {
  account: RouterAccount;
  pool: RouterPool;
  color: string;
  locale: string;
  busy: boolean;
  act: (action: () => Promise<unknown>, success?: string) => Promise<void>;
}) {
  const { t } = useTranslation();
  const provider = pool.provider;
  const window = (value: number | null | undefined, resetAt: string | null | undefined, label: string) => (
    <div className={styles.window}>
      <div className={styles.windowTop}>
        <span className={styles.windowLabel}>{label}</span>
        <span>{value === null || value === undefined ? '—' : `${Math.round(value)}%`}</span>
      </div>
      <QuotaBar value={value ?? null} color={color} label={label} />
      <div className={styles.windowReset}>
        {resetAt ? t('tpr.quota.resets', { when: formatWhen(Date.parse(resetAt), locale) }) : ' '}
      </div>
    </div>
  );

  return (
    <li className={styles.account} data-active={account.active || undefined}>
      <div className={styles.accountName}>
        <div className={styles.accountTitle}>
          <PrivateEmail value={account.email} fallback={account.label} />
          {account.active ? <Badge tone="good">{t('tpr.router.in_use')}</Badge> : null}
          {account.pinned ? <Badge tone="info">{t('tpr.router.pinned')}</Badge> : null}
        </div>
        <div className={styles.accountMeta}>
          {account.plan ? <span className={styles.plan}>{account.plan}</span> : null}
          {account.state !== 'ok' ? (
            <Badge tone={stateTone(account.state)}>
              {t(`tpr.router.state_${account.state}`, { defaultValue: account.state })}
            </Badge>
          ) : null}
          {account.detail ? <span>{account.detail}</span> : null}
          {account.sessions > 0 ? (
            <span>{t('tpr.router.account_sessions', { count: account.sessions })}</span>
          ) : null}
        </div>
      </div>
      {window(account.short?.remaining, account.short?.resetAt, t('tpr.router.col_five_hour'))}
      {window(account.long?.remaining, account.long?.resetAt, t('tpr.router.col_weekly'))}
      <div className={styles.role}>
        <Select
          value={account.role}
          size="sm"
          ariaLabel={t('tpr.router.role_for', { account: account.label })}
          disabled={busy}
          onChange={(value) =>
            void act(() =>
              tprApi.routerPatchSettings(
                provider,
                applyRoleChange(pool.settings, account.id, value as AccountRole)
              )
            )
          }
          options={[
            { value: 'normal', label: t('tpr.router.role_normal') },
            { value: 'reserve', label: t('tpr.router.role_reserve') },
            { value: 'off', label: t('tpr.router.role_off') },
          ]}
        />
      </div>
      <div className={styles.accountActions}>
        <Button
          variant="secondary"
          size="sm"
          disabled={busy || account.active || account.role === 'off'}
          onClick={() => void act(() => tprApi.routerSelect(provider, account.id))}
        >
          {t('tpr.router.use_now')}
        </Button>
        {account.pinned ? (
          <Button
            variant="ghost"
            size="sm"
            disabled={busy}
            onClick={() => void act(() => tprApi.routerSetMode(provider, 'auto'))}
          >
            {t('tpr.router.unpin')}
          </Button>
        ) : (
          <Button
            variant="ghost"
            size="sm"
            disabled={busy || account.role === 'off'}
            onClick={() => void act(() => tprApi.routerSetMode(provider, 'manual', account.id))}
          >
            {t('tpr.router.pin')}
          </Button>
        )}
      </div>
    </li>
  );
}

function FieldRow({ label, hint, children }: { label: ReactNode; hint?: ReactNode; children: ReactNode }) {
  return (
    <div className={styles.field}>
      <div className={styles.fieldText}>
        <div className={styles.fieldLabel}>{label}</div>
        {hint ? <div className={styles.fieldHint}>{hint}</div> : null}
      </div>
      <div className={styles.fieldControl}>{children}</div>
    </div>
  );
}

function TrafficPanel({
  pool,
  busy,
  act,
}: {
  pool: RouterPool;
  busy: boolean;
  act: (action: () => Promise<unknown>, success?: string) => Promise<void>;
}) {
  const { t } = useTranslation();
  const provider = pool.provider;
  const [autoResume, setAutoResume] = useState('20');
  const [slowdown, setSlowdown] = useState(String(pool.slowdownSeconds || 30));

  useEffect(() => {
    setSlowdown(String(pool.slowdownSeconds || 30));
  }, [pool.slowdownSeconds, provider]);

  return (
    <Panel title={t('tpr.router.traffic_title')} description={t('tpr.router.traffic_description')}>
      <FieldRow label={t('tpr.router.pause_label')} hint={t('tpr.router.pause_hint')}>
        <label className={styles.inline}>
          <span>{t('tpr.router.auto_resume')}</span>
          <input
            className={`input ${styles.number}`}
            type="number"
            min={0}
            max={240}
            value={autoResume}
            aria-label={t('tpr.router.auto_resume')}
            onChange={(event) => setAutoResume(event.target.value)}
          />
          <span>{t('tpr.router.minutes_short')}</span>
        </label>
        {pool.pause ? (
          <Button
            variant="secondary"
            size="sm"
            disabled={busy}
            onClick={() => void act(() => tprApi.routerResume(provider))}
          >
            {t('tpr.router.resume')}
          </Button>
        ) : (
          <Button
            variant="secondary"
            size="sm"
            disabled={busy}
            onClick={() =>
              void act(() =>
                tprApi.routerPause(provider, {
                  minutes: 0,
                  auto_resume_minutes: Number(autoResume) || 0,
                })
              )
            }
          >
            {t('tpr.router.pause')}
          </Button>
        )}
      </FieldRow>
      <FieldRow
        label={t('tpr.router.slowdown_label')}
        hint={
          pool.slowdownSeconds > 0
            ? t('tpr.router.slowdown_on', { seconds: pool.slowdownSeconds })
            : t('tpr.router.slowdown_hint')
        }
      >
        <label className={styles.inline}>
          <input
            className={`input ${styles.number}`}
            type="number"
            min={0}
            max={600}
            value={slowdown}
            aria-label={t('tpr.router.slowdown_label')}
            onChange={(event) => setSlowdown(event.target.value)}
          />
          <span>{t('tpr.router.seconds_short')}</span>
        </label>
        {pool.slowdownSeconds > 0 ? (
          <Button
            variant="secondary"
            size="sm"
            disabled={busy}
            onClick={() => void act(() => tprApi.routerSlowdown(provider, 0))}
          >
            {t('tpr.router.slowdown_off')}
          </Button>
        ) : (
          <Button
            variant="secondary"
            size="sm"
            disabled={busy}
            onClick={() => void act(() => tprApi.routerSlowdown(provider, Number(slowdown) || 0))}
          >
            {t('tpr.router.slowdown_apply')}
          </Button>
        )}
      </FieldRow>
    </Panel>
  );
}

function RankingPanel({
  pool,
  busy,
  act,
}: {
  pool: RouterPool;
  busy: boolean;
  act: (action: () => Promise<unknown>, success?: string) => Promise<void>;
}) {
  const { t } = useTranslation();
  const provider = pool.provider;
  const [threshold, setThreshold] = useState(String(pool.settings.switch_threshold_percent));

  useEffect(() => {
    setThreshold(String(pool.settings.switch_threshold_percent));
  }, [pool.settings.switch_threshold_percent, provider]);

  return (
    <Panel title={t('tpr.router.ranking_title')} description={t('tpr.router.ranking_description')}>
      <FieldRow label={t('tpr.router.ranking_label')} hint={t(`tpr.router.strategy_${(pool.settings.strategy || 'balanced').replace('-', '_')}_hint`, { defaultValue: '' })}>
        <div className={styles.selectWide}>
          <Select
            value={pool.settings.strategy || 'balanced'}
            size="sm"
            ariaLabel={t('tpr.router.ranking_label')}
            disabled={busy}
            onChange={(value) =>
              // rank_by would override a preset, so it is cleared along with the change.
              void act(() => tprApi.routerPatchSettings(provider, { strategy: value, rank_by: [] }))
            }
            options={[
              { value: 'balanced', label: t('tpr.router.strategy_balanced') },
              { value: 'most-remaining', label: t('tpr.router.strategy_most_remaining') },
              { value: 'soonest-reset', label: t('tpr.router.strategy_soonest_reset') },
              { value: 'priority', label: t('tpr.router.strategy_priority') },
            ]}
          />
        </div>
      </FieldRow>
      <FieldRow label={t('tpr.router.threshold_label')} hint={t('tpr.router.threshold_hint')}>
        <label className={styles.inline}>
          <input
            className={`input ${styles.number}`}
            type="number"
            min={0}
            max={100}
            value={threshold}
            aria-label={t('tpr.router.threshold_label')}
            onChange={(event) => setThreshold(event.target.value)}
          />
          <span>%</span>
        </label>
        <Button
          variant="secondary"
          size="sm"
          disabled={busy || threshold === String(pool.settings.switch_threshold_percent)}
          onClick={() =>
            void act(() =>
              tprApi.routerPatchSettings(provider, {
                switch_threshold_percent: Number(threshold) || 0,
              })
            )
          }
        >
          {t('common.save')}
        </Button>
      </FieldRow>
    </Panel>
  );
}

function WindowsPanel({
  pool,
  locale,
  busy,
  act,
}: {
  pool: RouterPool;
  locale: string;
  busy: boolean;
  act: (action: () => Promise<unknown>, success?: string) => Promise<void>;
}) {
  const { t } = useTranslation();
  const provider = pool.provider;
  const [account, setAccount] = useState('');
  const [when, setWhen] = useState('');
  const [daily, setDaily] = useState(false);
  const scheduleAccount = account || pool.windows[0]?.account || '';

  return (
    <Panel
      title={t('tpr.router.windows_title')}
      description={t('tpr.router.windows_description')}
      actions={
        <>
          <ToggleSwitch
            label={t('tpr.router.auto_start')}
            checked={pool.settings.auto_start_windows}
            disabled={busy}
            onChange={(value) =>
              void act(() => tprApi.routerPatchSettings(provider, { auto_start_windows: value }))
            }
          />
          <Button
            variant="secondary"
            size="sm"
            disabled={busy}
            onClick={() =>
              void act(
                () =>
                  tprApi.routerStartWindows(provider, {
                    delay_minutes: 0,
                    stagger_minutes: 0,
                    account: '',
                  }),
                t('tpr.router.windows_started')
              )
            }
          >
            {t('tpr.router.start_all')}
          </Button>
        </>
      }
    >
      <ul className={styles.rows}>
        {pool.windows.map((row) => (
          <li key={row.account} className={styles.windowRow}>
            <span className={styles.rowMain}>
              <AccountLabel pool={pool} id={row.account} />
            </span>
            <span className={styles.windowState}>
              {row.state === 'running' && row.resetAt
                ? t('tpr.router.window_running', { when: formatWhen(Date.parse(row.resetAt), locale) })
                : row.state === 'idle'
                  ? t('tpr.router.window_idle')
                  : t('tpr.router.window_unknown')}
            </span>
            <span className={styles.rowMeta}>
              {row.plans[0]
                ? t(row.plans[0].daily ? 'tpr.router.next_start_daily' : 'tpr.router.next_start', {
                    when: formatWhen(Date.parse(row.plans[0].at), locale),
                  })
                : row.autoStartAt
                  ? t('tpr.router.next_auto', { when: formatWhen(Date.parse(row.autoStartAt), locale) })
                  : t('tpr.router.no_start')}
            </span>
            <span className={styles.windowActions}>
              {row.plans[0] ? (
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={busy}
                  onClick={() => void act(() => tprApi.routerCancelWindows(provider, { id: row.plans[0].id }))}
                >
                  {t('common.cancel')}
                </Button>
              ) : null}
              <Button
                variant="ghost"
                size="sm"
                disabled={busy || row.state === 'running'}
                onClick={() =>
                  void act(() =>
                    tprApi.routerStartWindows(provider, {
                      delay_minutes: 0,
                      stagger_minutes: 0,
                      account: row.account,
                    })
                  )
                }
              >
                {t('tpr.router.start')}
              </Button>
            </span>
          </li>
        ))}
      </ul>

      <form
        className={styles.schedule}
        onSubmit={(event) => {
          event.preventDefault();
          if (!when || !scheduleAccount) return;
          const at = new Date(when);
          if (Number.isNaN(at.getTime())) return;
          void act(() =>
            tprApi.routerScheduleWindow(provider, {
              account: scheduleAccount,
              at: at.toISOString(),
              daily,
            })
          ).then(() => setWhen(''));
        }}
      >
        <span className={styles.scheduleLabel}>{t('tpr.router.schedule_label')}</span>
        <div className={styles.selectWide}>
          <Select
            value={scheduleAccount}
            size="sm"
            ariaLabel={t('tpr.router.schedule_account')}
            onChange={setAccount}
            options={pool.windows.map((row) => ({
              value: row.account,
              label: row.label || row.account,
            }))}
          />
        </div>
        <input
          className={`input ${styles.when}`}
          type="datetime-local"
          value={when}
          aria-label={t('tpr.router.schedule_time')}
          onChange={(event) => setWhen(event.target.value)}
        />
        <label className={styles.inline}>
          <input type="checkbox" checked={daily} onChange={(event) => setDaily(event.target.checked)} />
          <span>{t('tpr.router.schedule_daily')}</span>
        </label>
        <Button variant="secondary" size="sm" type="submit" disabled={busy || !when}>
          {t('tpr.router.schedule')}
        </Button>
      </form>
    </Panel>
  );
}
