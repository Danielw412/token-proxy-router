/**
 * Token Proxy Router management API (`/v8/management/tpr`), per docs/DESIGN.md.
 * Reads are normalized here so components never have to second-guess the backend's shape:
 * missing arrays become empty and unusable numbers become null.
 */

import { apiClient } from './client';
import { computeApiUrl } from '@/utils/connection';

const BASE = '/tpr';

export type TprProvider = 'claude' | 'codex';
export type BurnWindow = '1h' | '3h' | '6h';
export type QuotaWindow = 'weekly' | 'session';
export type FutureUsage = 'same' | 'idle' | 'double';

/* ---------------------------------- helpers --------------------------------- */

type RawRecord = Record<string, unknown>;

const isRecord = (value: unknown): value is RawRecord =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const asRecord = (value: unknown): RawRecord => (isRecord(value) ? value : {});
const asArray = (value: unknown): unknown[] => (Array.isArray(value) ? value : []);
const asString = (value: unknown): string => (typeof value === 'string' ? value : '');
const asBool = (value: unknown): boolean => value === true;
const asNumber = (value: unknown): number | null =>
  typeof value === 'number' && Number.isFinite(value) ? value : null;

/* ---------------------------------- quota ---------------------------------- */

export interface QuotaPoint {
  at: string;
  /** null marks a hole: the collector ran but had no usable reading. */
  percent: number | null;
}

export interface PoolAccount {
  id: string;
  label: string;
  email: string;
  plan: string;
  weight: number;
  remainingPercent: number | null;
  resetAt: string | null;
  status: string;
}

export interface PoolReset {
  at: string;
  deltaPercent: number;
  projected: boolean;
}

export interface QuotaPool {
  provider: string;
  accounts: PoolAccount[];
  remainingPercent: number;
  capacityPercent: number;
  weightNote: string;
  poolEmptyAt: string | null;
  firstEmpty: { account: string; at: string } | null;
  nextReset: { at: string; deltaPercent: number } | null;
  burnPerHour: number;
  recorded: QuotaPoint[];
  projected: QuotaPoint[];
  resets: PoolReset[];
  emptyMarks: Array<{ at: string; label: string }>;
  coverage: { firstSampleAt: string | null; samples: number };
}

export interface QuotaPoolsResponse {
  now: string;
  from: string;
  to: string;
  pools: QuotaPool[];
}

const normalizePoints = (value: unknown): QuotaPoint[] =>
  asArray(value)
    .map(asRecord)
    .filter((point) => asString(point.at))
    .map((point) => ({ at: asString(point.at), percent: asNumber(point.percent) }));

export const normalizePool = (raw: unknown): QuotaPool => {
  const pool = asRecord(raw);
  const firstEmpty = isRecord(pool.first_empty) ? pool.first_empty : null;
  const nextReset = isRecord(pool.next_reset) ? pool.next_reset : null;
  const coverage = asRecord(pool.coverage);
  return {
    provider: asString(pool.provider),
    accounts: asArray(pool.accounts)
      .map(asRecord)
      .map((account) => ({
        id: asString(account.id),
        label: asString(account.label),
        email: asString(account.email),
        plan: asString(account.plan),
        weight: asNumber(account.weight) ?? 1,
        remainingPercent: asNumber(account.remaining_percent),
        resetAt: asString(account.reset_at) || null,
        status: asString(account.status) || 'ok',
      })),
    remainingPercent: asNumber(pool.remaining_percent) ?? 0,
    capacityPercent: asNumber(pool.capacity_percent) ?? 0,
    weightNote: asString(pool.weight_note),
    poolEmptyAt: asString(pool.pool_empty_at) || null,
    firstEmpty:
      firstEmpty && asString(firstEmpty.at)
        ? { account: asString(firstEmpty.account), at: asString(firstEmpty.at) }
        : null,
    nextReset:
      nextReset && asString(nextReset.at)
        ? { at: asString(nextReset.at), deltaPercent: asNumber(nextReset.delta_percent) ?? 0 }
        : null,
    burnPerHour: asNumber(pool.burn_per_hour) ?? 0,
    recorded: normalizePoints(pool.recorded),
    projected: normalizePoints(pool.projected),
    resets: asArray(pool.resets)
      .map(asRecord)
      .filter((reset) => asString(reset.at))
      .map((reset) => ({
        at: asString(reset.at),
        deltaPercent: asNumber(reset.delta_percent) ?? 0,
        projected: asBool(reset.projected),
      })),
    emptyMarks: asArray(pool.empty_marks)
      .map(asRecord)
      .filter((mark) => asString(mark.at))
      .map((mark) => ({ at: asString(mark.at), label: asString(mark.label) || 'Empty' })),
    coverage: {
      firstSampleAt: asString(coverage.first_sample_at) || null,
      samples: asNumber(coverage.samples) ?? 0,
    },
  };
};

export const normalizePoolsResponse = (raw: unknown): QuotaPoolsResponse => {
  const data = asRecord(raw);
  return {
    now: asString(data.now) || new Date().toISOString(),
    from: asString(data.from),
    to: asString(data.to),
    pools: asArray(data.pools).map(normalizePool),
  };
};

/* ---------------------------------- router --------------------------------- */

export type AccountRole = 'normal' | 'reserve' | 'off';

export interface RouterWindowQuota {
  remaining: number | null;
  resetAt: string | null;
  estimated: boolean;
}

export interface WindowPlan {
  id: string;
  account: string;
  at: string;
  daily: boolean;
  source: string;
}

export interface RouterAccount {
  id: string;
  label: string;
  email: string;
  plan: string;
  role: AccountRole;
  /** ok, low, exhausted, unavailable, unknown or off. */
  state: string;
  detail: string;
  active: boolean;
  pinned: boolean;
  stale: boolean;
  /** The 5-hour window. */
  short: RouterWindowQuota | null;
  /** The weekly window. */
  long: RouterWindowQuota | null;
  sessions: number;
  busySessions: number;
  idleSessions: number;
  lastUsedAt: string | null;
}

export interface RouterSession {
  id: string;
  account: string;
  label: string;
  state: string;
  requests: number;
  boundAt: string | null;
  lastSeenAt: string | null;
}

export interface RouterHistoryEntry {
  at: string;
  kind: string;
  from: string;
  to: string;
  reason: string;
}

export interface RouterWindowRow {
  account: string;
  label: string;
  /** running, idle or unknown. */
  state: string;
  resetAt: string | null;
  autoStartAt: string | null;
  plans: WindowPlan[];
}

export interface RouterPause {
  since: string | null;
  until: string | null;
  autoResumeMinutes: number;
  nextAutoResume: string | null;
}

export interface RouterSettings {
  enabled: boolean;
  switch_threshold_percent: number;
  switch_back: boolean;
  min_dwell_minutes: number;
  cache_warm_minutes: number;
  strategy: string;
  rank_by: string[];
  reserve_accounts: string[];
  disabled_accounts: string[];
  manual_fallback: string;
  auto_start_windows: boolean;
}

export interface RouterPool {
  provider: string;
  enabled: boolean;
  mode: 'auto' | 'manual';
  pinned: string;
  /** The account new requests use right now. */
  routing: string;
  routingNote: string;
  accounts: RouterAccount[];
  sessions: RouterSession[];
  sessionCount: number;
  warmSessions: number;
  settings: RouterSettings;
  warnings: string[];
  history: RouterHistoryEntry[];
  pause: RouterPause | null;
  slowdownSeconds: number;
  waiting: number;
  windows: RouterWindowRow[];
  lastError: string;
}

const normalizeRole = (value: unknown): AccountRole => {
  const role = asString(value).toLowerCase();
  if (role === 'reserve' || role === 'off') return role;
  return 'normal';
};

const normalizeWindow = (raw: unknown): RouterWindowQuota | null => {
  if (!isRecord(raw)) return null;
  return {
    remaining: asNumber(raw.remaining_percent),
    resetAt: asString(raw.reset_at) || null,
    estimated: asBool(raw.estimated),
  };
};

const normalizePlans = (value: unknown): WindowPlan[] =>
  asArray(value)
    .map(asRecord)
    .map((plan) => ({
      id: asString(plan.id),
      account: asString(plan.account),
      at: asString(plan.at),
      daily: asBool(plan.daily),
      source: asString(plan.source),
    }));

const strings = (value: unknown): string[] =>
  asArray(value).filter((item): item is string => typeof item === 'string');

export const normalizeRouterPool = (raw: unknown): RouterPool => {
  const pool = asRecord(raw);
  const settings = asRecord(pool.settings);
  const pause = isRecord(pool.pause) ? pool.pause : null;
  const routing = asString(pool.routing) || asString(pool.active);
  return {
    provider: asString(pool.provider),
    enabled: pool.enabled === undefined ? true : asBool(pool.enabled),
    mode: asString(pool.mode) === 'manual' ? 'manual' : 'auto',
    pinned: asString(pool.pinned),
    routing,
    routingNote: asString(pool.routing_note) || asString(pool.active_reason),
    accounts: asArray(pool.accounts)
      .map(asRecord)
      .map((account) => {
        const id = asString(account.id);
        return {
          id,
          label: asString(account.label) || id,
          email: asString(account.email),
          plan: asString(account.plan),
          role: normalizeRole(account.role),
          state: asString(account.state) || 'unknown',
          detail: asString(account.detail),
          active: asBool(account.active) || (Boolean(id) && id === routing),
          pinned: asBool(account.pinned),
          stale: asBool(account.stale),
          short: normalizeWindow(account.short),
          long: normalizeWindow(account.long),
          sessions: asNumber(account.sessions) ?? 0,
          busySessions: asNumber(account.busy_sessions) ?? 0,
          idleSessions: asNumber(account.idle_sessions) ?? 0,
          lastUsedAt: asString(account.last_used) || null,
        };
      }),
    sessions: asArray(pool.sessions)
      .map(asRecord)
      .map((session) => ({
        id: asString(session.id),
        account: asString(session.account),
        label: asString(session.label),
        state: asString(session.state),
        requests: asNumber(session.requests) ?? 0,
        boundAt: asString(session.bound_at) || null,
        lastSeenAt: asString(session.last_seen) || null,
      })),
    sessionCount: asNumber(pool.session_count) ?? asArray(pool.sessions).length,
    warmSessions: asNumber(pool.warm_sessions) ?? 0,
    settings: {
      enabled: settings.enabled === undefined ? true : asBool(settings.enabled),
      switch_threshold_percent: asNumber(settings.switch_threshold_percent) ?? 0,
      switch_back: asBool(settings.switch_back),
      min_dwell_minutes: asNumber(settings.min_dwell_minutes) ?? 0,
      cache_warm_minutes: asNumber(settings.cache_warm_minutes) ?? 0,
      strategy: asString(settings.strategy),
      rank_by: strings(settings.rank_by),
      reserve_accounts: strings(settings.reserve_accounts),
      disabled_accounts: strings(settings.disabled_accounts),
      manual_fallback: asString(settings.manual_fallback) || 'auto',
      auto_start_windows: asBool(settings.auto_start_windows),
    },
    warnings: strings(pool.warnings),
    history: asArray(pool.history)
      .map(asRecord)
      .map((entry) => ({
        at: asString(entry.at),
        kind: asString(entry.kind),
        from: asString(entry.from),
        to: asString(entry.to),
        reason: asString(entry.reason),
      })),
    pause: pause
      ? {
          since: asString(pause.since) || null,
          until: asString(pause.until) || null,
          autoResumeMinutes: asNumber(pause.auto_resume_minutes) ?? 0,
          nextAutoResume: asString(pause.next_auto_resume) || null,
        }
      : null,
    slowdownSeconds: asNumber(asRecord(pool.slowdown).seconds) ?? asNumber(pool.slow_down_seconds) ?? 0,
    waiting: asNumber(pool.waiting) ?? 0,
    windows: asArray(pool.windows)
      .map(asRecord)
      .map((row) => ({
        account: asString(row.account),
        label: asString(row.label),
        state: asString(row.state),
        resetAt: asString(row.reset_at) || null,
        autoStartAt: asString(row.auto_start_at) || null,
        plans: normalizePlans(row.plans),
      })),
    lastError: asString(pool.last_error),
  };
};

/** Roles are stored as two lists in the settings; this returns both lists after one change. */
export const applyRoleChange = (
  settings: Pick<RouterSettings, 'reserve_accounts' | 'disabled_accounts'>,
  account: string,
  role: AccountRole
): Pick<RouterSettings, 'reserve_accounts' | 'disabled_accounts'> => {
  const reserve = settings.reserve_accounts.filter((id) => id !== account);
  const disabled = settings.disabled_accounts.filter((id) => id !== account);
  if (role === 'reserve') reserve.push(account);
  if (role === 'off') disabled.push(account);
  return { reserve_accounts: reserve, disabled_accounts: disabled };
};

/* --------------------------------- alerts ---------------------------------- */

export type AlertChannelType = 'ntfy' | 'discord' | 'slack' | 'webhook';

export interface AlertChannel {
  type: AlertChannelType;
  url: string;
  name: string;
}

export interface AlertsConfig {
  enabled: boolean;
  pool_empty_within_hours: number;
  account_below_percent: number;
  notify_on_reset: boolean;
  channels: AlertChannel[];
}

export interface AlertEvent {
  at: string;
  kind: string;
  provider: string;
  account: string;
  message: string;
  active: boolean;
}

const normalizeAlerts = (raw: unknown): AlertsConfig => {
  const data = asRecord(raw);
  return {
    enabled: asBool(data.enabled),
    pool_empty_within_hours: asNumber(data.pool_empty_within_hours) ?? 6,
    account_below_percent: asNumber(data.account_below_percent) ?? 10,
    notify_on_reset: asBool(data.notify_on_reset),
    channels: asArray(data.channels)
      .map(asRecord)
      .map((channel) => ({
        type: (asString(channel.type) || 'webhook') as AlertChannelType,
        url: asString(channel.url),
        name: asString(channel.name),
      })),
  };
};

/** Current conditions arrive beside the event log; they lead the list, flagged as active. */
export const normalizeAlertEvents = (raw: unknown): AlertEvent[] => {
  const data = asRecord(raw);
  const toEvent = (value: RawRecord, active: boolean, atKey: string): AlertEvent => ({
    at: asString(value[atKey]),
    kind: asString(value.kind),
    provider: asString(value.provider),
    account: asString(value.account),
    message: asString(value.message),
    active: active || asBool(value.active),
  });
  const conditions = asArray(data.active)
    .map(asRecord)
    .map((value) => toEvent(value, true, 'since'));
  const sameCondition = (a: AlertEvent, b: AlertEvent) =>
    a.kind === b.kind && a.provider === b.provider && a.account === b.account;
  const events = asArray(data.events)
    .map(asRecord)
    .map((value) => toEvent(value, false, 'at'))
    // The event that raised a live condition would otherwise show twice.
    .filter((event) => !conditions.some((item) => sameCondition(item, event) && item.at === event.at));
  return [...conditions, ...events];
};

/* --------------------------------- health ---------------------------------- */

export type HealthStatus = 'ok' | 'expiring' | 'expired' | 'failing' | 'cooling' | 'disabled';

export interface AccountHealth {
  id: string;
  provider: string;
  label: string;
  email: string;
  status: HealthStatus;
  reason: string;
  lastSuccessAt: string | null;
  lastErrorAt: string | null;
  lastError: string;
  tokenExpiresAt: string | null;
  reauthProvider: string | null;
}

const HEALTH_STATUSES: readonly string[] = [
  'ok',
  'expiring',
  'expired',
  'failing',
  'cooling',
  'disabled',
];

const normalizeHealth = (raw: unknown): AccountHealth[] =>
  asArray(asRecord(raw).accounts)
    .map(asRecord)
    .map((account) => {
      const status = asString(account.status);
      const reauth = asRecord(account.reauth);
      return {
        id: asString(account.id),
        provider: asString(account.provider),
        label: asString(account.label),
        email: asString(account.email),
        status: (HEALTH_STATUSES.includes(status) ? status : 'ok') as HealthStatus,
        reason: asString(account.reason),
        lastSuccessAt: asString(account.last_success_at) || null,
        lastErrorAt: asString(account.last_error_at) || null,
        lastError: asString(account.last_error),
        tokenExpiresAt: asString(account.token_expires_at) || null,
        reauthProvider: asString(reauth.provider) || null,
      };
    });

/* ---------------------------------- usage ---------------------------------- */

export interface KeyUsage {
  keyId: string;
  name: string;
  masked: string;
  requests: number;
  failures: number;
  inputTokens: number;
  outputTokens: number;
  cachedTokens: number;
  lastUsedAt: string | null;
  models: Array<{ model: string; requests: number; totalTokens: number }>;
  series: Array<{ at: string; requests: number; totalTokens: number }>;
}

const normalizeUsage = (raw: unknown): KeyUsage[] =>
  asArray(asRecord(raw).keys)
    .map(asRecord)
    .map((key) => ({
      keyId: asString(key.key_id),
      name: asString(key.name),
      masked: asString(key.masked),
      requests: asNumber(key.requests) ?? 0,
      failures: asNumber(key.failures) ?? 0,
      inputTokens: asNumber(key.input_tokens) ?? 0,
      outputTokens: asNumber(key.output_tokens) ?? 0,
      cachedTokens: asNumber(key.cached_tokens) ?? 0,
      lastUsedAt: asString(key.last_used_at) || null,
      models: asArray(key.models)
        .map(asRecord)
        .map((model) => ({
          model: asString(model.model),
          requests: asNumber(model.requests) ?? 0,
          totalTokens: asNumber(model.total_tokens) ?? 0,
        })),
      series: asArray(key.series)
        .map(asRecord)
        .map((point) => ({
          at: asString(point.at),
          requests: asNumber(point.requests) ?? 0,
          totalTokens: asNumber(point.total_tokens) ?? 0,
        })),
    }));

/* --------------------------------- system ---------------------------------- */

export interface TprSystem {
  version: string;
  commit: string;
  upstream_version: string;
  panel_version: string;
  data_dir: string;
  os: string;
  arch: string;
  uptime_seconds: number;
  service: { kind: string; installed: boolean; running: boolean; supervised: boolean };
  setup: { completed: boolean };
}

export interface TprUpdate {
  current: string;
  latest: string;
  available: boolean;
  notes_url: string;
  notes: string;
  checked_at: string;
  can_apply: boolean;
  reason: string;
  rollback_available: boolean;
  state: 'idle' | 'downloading' | 'verifying' | 'applying' | 'restarting' | 'failed' | string;
  error: string;
}

export interface TprTailscale {
  installed: boolean;
  logged_in: boolean;
  dns_name: string;
  exposed: boolean;
  url: string;
  port: number;
  can_expose: boolean;
  hint: string;
  identity: { enabled: boolean; allowed_logins: string[]; available: boolean };
}

const normalizeTailscale = (raw: unknown): TprTailscale => {
  const data = asRecord(raw);
  const identity = asRecord(data.identity);
  return {
    installed: asBool(data.installed),
    logged_in: asBool(data.logged_in),
    dns_name: asString(data.dns_name),
    exposed: asBool(data.exposed),
    url: asString(data.url),
    port: asNumber(data.port) ?? 8443,
    can_expose: asBool(data.can_expose),
    hint: asString(data.hint),
    identity: {
      enabled: asBool(identity.enabled),
      allowed_logins: asArray(identity.allowed_logins).filter(
        (login): login is string => typeof login === 'string'
      ),
      available: asBool(identity.available),
    },
  };
};

export interface TprConnect {
  hub_urls: Array<{ label: string; url: string }>;
  keys: Array<{ key_id: string; name: string; masked: string }>;
  models: { codex: string[]; claude: string[] };
  install: { linux: string; windows: string };
}

export interface TprSnippets {
  linux: string;
  windows: string;
  codex_toml: string;
  claude_env: string;
  claude_env_windows: string;
  t3: string;
}

const normalizeConnect = (raw: unknown): TprConnect => {
  const data = asRecord(raw);
  const models = asRecord(data.models);
  const install = asRecord(data.install);
  return {
    hub_urls: asArray(data.hub_urls)
      .map(asRecord)
      .map((hub) => ({ label: asString(hub.label), url: asString(hub.url) })),
    keys: asArray(data.keys)
      .map(asRecord)
      .map((key) => ({
        key_id: asString(key.key_id),
        name: asString(key.name),
        masked: asString(key.masked),
      })),
    models: { codex: strings(models.codex), claude: strings(models.claude) },
    install: { linux: asString(install.linux), windows: asString(install.windows) },
  };
};

export interface TprPublicSession {
  identity_login: boolean;
  login: string;
}

/* ---------------------------------- calls ---------------------------------- */

const providerParam = (provider: string) => ({ params: { provider } });

export const tprApi = {
  quotaPools: async (params: {
    from: string;
    to: string;
    burn: BurnWindow;
    window: QuotaWindow;
    future: FutureUsage;
  }) => normalizePoolsResponse(await apiClient.get(`${BASE}/quota/pools`, { params })),
  accountHistory: async (id: string, params: { from: string; to: string; window: QuotaWindow }) => {
    const data = asRecord(
      await apiClient.get(`${BASE}/quota/accounts/${encodeURIComponent(id)}/history`, { params })
    );
    return normalizePoints(data.samples ?? data.history ?? data.recorded);
  },
  refreshQuota: () => apiClient.post(`${BASE}/quota/refresh`),

  routerPools: async (history = 30) => {
    const data = asRecord(await apiClient.get(`${BASE}/router/state`, { params: { history } }));
    return asArray(data.pools).map(normalizeRouterPool);
  },
  routerSetMode: (provider: string, mode: 'auto' | 'manual', account?: string) =>
    apiClient.post(
      `${BASE}/router/mode`,
      mode === 'manual' ? { mode, account } : { mode },
      providerParam(provider)
    ),
  routerSelect: (provider: string, account: string) =>
    apiClient.post(`${BASE}/router/select`, { account }, providerParam(provider)),
  routerRefresh: (provider: string) =>
    apiClient.post(`${BASE}/router/refresh`, undefined, providerParam(provider)),
  routerPatchSettings: (provider: string, patch: Partial<RouterSettings>) =>
    apiClient.patch(`${BASE}/router/settings`, patch, providerParam(provider)),
  routerStartWindows: (
    provider: string,
    body: { delay_minutes: number; stagger_minutes: number; account: string }
  ) => apiClient.post(`${BASE}/router/windows/start`, body, providerParam(provider)),
  routerScheduleWindow: (
    provider: string,
    body: { account: string; at: string; daily: boolean }
  ) => apiClient.post(`${BASE}/router/windows/schedule`, body, providerParam(provider)),
  routerCancelWindows: (provider: string, body: { id?: string; account?: string }) =>
    apiClient.post(`${BASE}/router/windows/cancel`, body, providerParam(provider)),
  routerPause: (provider: string, body: { minutes: number; auto_resume_minutes: number }) =>
    apiClient.post(`${BASE}/router/pause`, body, providerParam(provider)),
  routerResume: (provider: string) =>
    apiClient.post(`${BASE}/router/resume`, undefined, providerParam(provider)),
  routerSlowdown: (provider: string, seconds: number) =>
    apiClient.post(`${BASE}/router/slowdown`, { seconds }, providerParam(provider)),

  getAlerts: async () => normalizeAlerts(await apiClient.get(`${BASE}/alerts`)),
  putAlerts: (config: AlertsConfig) => apiClient.put(`${BASE}/alerts`, config),
  testAlerts: async () => {
    const data = asRecord(await apiClient.post(`${BASE}/alerts/test`));
    return asArray(data.results)
      .map(asRecord)
      .map((result) => ({
        name: asString(result.name),
        ok: asBool(result.ok),
        error: asString(result.error),
      }));
  },
  alertEvents: async (limit = 50): Promise<AlertEvent[]> =>
    normalizeAlertEvents(await apiClient.get(`${BASE}/alerts/events`, { params: { limit } })),

  health: async () => normalizeHealth(await apiClient.get(`${BASE}/health/accounts`)),

  usageKeys: async (params: { from: string; to: string; bucket: 'hour' | 'day' }) =>
    normalizeUsage(await apiClient.get(`${BASE}/usage/keys`, { params })),

  system: () => apiClient.get<TprSystem>(`${BASE}/system`),
  update: () => apiClient.get<TprUpdate>(`${BASE}/update`),
  checkUpdate: () => apiClient.post(`${BASE}/update/check`),
  applyUpdate: (version?: string) =>
    apiClient.post(`${BASE}/update/apply`, version ? { version } : {}),
  rollbackUpdate: () => apiClient.post(`${BASE}/update/rollback`),

  setup: async () => {
    const data = asRecord(await apiClient.get(`${BASE}/setup`));
    return { completed: asBool(data.completed) };
  },
  completeSetup: () => apiClient.post(`${BASE}/setup/complete`),

  tailscale: async () => normalizeTailscale(await apiClient.get(`${BASE}/tailscale`)),
  expose: (port: number) => apiClient.post(`${BASE}/tailscale/expose`, { port }),
  unexpose: () => apiClient.delete(`${BASE}/tailscale/expose`),
  setIdentity: (enabled: boolean, allowedLogins: string[]) =>
    apiClient.put(`${BASE}/tailscale/identity`, { enabled, allowed_logins: allowedLogins }),

  backup: async (passphrase: string): Promise<Blob> => {
    const response = await apiClient.postRaw(
      `${BASE}/backup`,
      { passphrase },
      { responseType: 'blob', timeout: 0 }
    );
    return response.data as Blob;
  },
  restore: (file: File, passphrase: string) => {
    const form = new FormData();
    form.append('file', file);
    form.append('passphrase', passphrase);
    return apiClient.postForm(`${BASE}/restore`, form, { timeout: 0 });
  },

  connect: async () => normalizeConnect(await apiClient.get(`${BASE}/connect`)),
  /** Returns real keys. Fetched on demand and held only by the component that asked. */
  connectSnippets: (keyId: string, hubUrl: string) =>
    apiClient.post<TprSnippets>(`${BASE}/connect/snippets`, { key_id: keyId, hub_url: hubUrl }),
};

export const tprPublicApi = {
  /** Unauthenticated: tells the login page whether identity sign-in applies to this request. */
  session: async (apiBase: string): Promise<TprPublicSession | null> => {
    try {
      const response = await fetch(`${computeApiUrl(apiBase)}/tpr-public/session`, {
        credentials: 'same-origin',
      });
      if (!response.ok) return null;
      const data = asRecord(await response.json());
      return { identity_login: asBool(data.identity_login), login: asString(data.login) };
    } catch {
      return null;
    }
  },
};
