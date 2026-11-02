/**
 * Realistic fixtures for the dev mock and tests. Everything is derived from `now`, so
 * the charts always look current. All identities are placeholders: this repo is public.
 */

const HOUR = 3_600_000;
const DAY = 24 * HOUR;

export const iso = (ms: number): string => new Date(ms).toISOString();

/** Small deterministic generator so fixtures do not change between requests. */
const seeded = (seed: number) => () => {
  seed = (seed * 16807) % 2147483647;
  return (seed - 1) / 2147483646;
};

interface AccountSeed {
  id: string;
  label: string;
  email: string;
  plan: string;
  weight: number;
  remaining: number;
  resetInHours: number;
  status?: string;
}

const CLAUDE_ACCOUNTS: AccountSeed[] = [
  { id: 'claude-alice', label: 'Alice', email: 'alice@example.com', plan: 'max', weight: 1, remaining: 91, resetInHours: 46 },
  { id: 'claude-bob', label: 'Bob', email: 'bob@example.com', plan: 'max', weight: 1, remaining: 96, resetInHours: 70 },
  { id: 'claude-carol', label: 'Carol', email: 'carol@example.com', plan: 'max', weight: 1, remaining: 88, resetInHours: 94 },
  { id: 'claude-dana', label: 'Dana', email: 'dana@example.com', plan: 'pro', weight: 1, remaining: 100, resetInHours: 118 },
  { id: 'claude-erin', label: 'Erin', email: 'erin@example.com', plan: 'pro', weight: 1, remaining: 94, resetInHours: 142 },
  { id: 'claude-frank', label: 'Frank', email: 'frank@example.com', plan: 'pro', weight: 1, remaining: 86, resetInHours: 16 },
];

const CODEX_ACCOUNTS: AccountSeed[] = [
  { id: 'codex-alice', label: 'Alice', email: 'alice@example.com', plan: 'pro', weight: 1, remaining: 96, resetInHours: 167 },
  { id: 'codex-grace', label: 'Grace', email: 'grace@example.com', plan: 'plus', weight: 1, remaining: 85, resetInHours: 167 },
  { id: 'codex-heidi', label: 'Heidi', email: 'heidi@example.com', plan: 'pro max', weight: 2.5, remaining: 96, resetInHours: 167 },
];

export interface QuotaQuery {
  from: number;
  to: number;
  burn: string;
  future: string;
}

const burnMultiplier = (future: string): number =>
  future === 'idle' ? 0 : future === 'double' ? 2 : 1;

interface PoolShape {
  provider: 'claude' | 'codex';
  accounts: AccountSeed[];
  burnPerHour: number;
  weightNote: string;
  recordedShape: (now: number, emit: (at: number, percent: number | null) => void) => void;
}

const claudeRecorded = (now: number, emit: (at: number, percent: number | null) => void) => {
  // Built backwards from the current value so the line always lands exactly on "now".
  const random = seeded(4242);
  const STEP = 45 * 60_000;
  const resets = [
    { at: now - 0.55 * DAY, delta: 120 },
    { at: now - 1.9 * DAY, delta: 139 },
    { at: now - 2.8 * DAY, delta: 200 },
    { at: now - 6.2 * DAY, delta: 110 },
    { at: now - 10.4 * DAY, delta: 100 },
  ];
  const hole = now - 4 * DAY;
  const points: Array<{ at: number; percent: number | null }> = [{ at: now, percent: 555 }];
  let value = 555;
  for (let at = now - STEP; at > now - 14 * DAY; at -= STEP) {
    const reset = resets.find((item) => item.at >= at && item.at < at + STEP);
    if (reset) {
      // Just before a reset the line was lower by the amount it jumped.
      points.push({ at: reset.at + 60_000, percent: value });
      value = Math.max(60, value - reset.delta);
      points.push({ at: reset.at, percent: value });
    }
    value = Math.min(600, value + 0.6 + random() * 3.6);
    points.push({ at, percent: Math.abs(at - hole) < 30 * 60_000 ? null : value });
  }
  points.sort((a, b) => a.at - b.at).forEach((point) => emit(point.at, point.percent));
};

const codexRecorded = (now: number, emit: (at: number, percent: number | null) => void) => {
  const random = seeded(90210);
  let value = 450;
  for (let at = now - 14 * DAY; at < now - 3 * DAY; at += 2 * HOUR) {
    value = Math.max(170, value - random() * 26);
    if (random() > 0.86) value = 450;
    emit(at, value);
  }
  value = 188;
  for (let at = now - 3 * DAY; at < now - 1.3 * DAY; at += HOUR) {
    value = Math.max(160, value - random() * 1.2);
    emit(at, value);
  }
  emit(now - 1.3 * DAY, value);
  value = 415;
  for (let at = now - 1.3 * DAY + 60_000; at < now - 0.34 * DAY; at += HOUR) {
    value = Math.max(330, value - random() * 5);
    emit(at, value);
  }
  emit(now - 0.34 * DAY, value);
  value = 450;
  for (let at = now - 0.34 * DAY + 60_000; at < now; at += HOUR) {
    value = Math.max(421, value - random() * 8);
    emit(at, value);
  }
  emit(now, 421);
};

/** Saw-tooth forecast: burn down linearly, jump at every account reset, then weekly. */
const buildProjection = (shape: PoolShape, now: number, to: number, future: string) => {
  const capacity = shape.accounts.reduce((sum, account) => sum + 100 * account.weight, 0);
  const burn = shape.burnPerHour * burnMultiplier(future);
  const resetTimes: Array<{ at: number; delta: number }> = [];
  shape.accounts.forEach((account) => {
    for (let at = now + account.resetInHours * HOUR; at <= to; at += 7 * DAY) {
      resetTimes.push({ at, delta: 100 * account.weight });
    }
  });
  resetTimes.sort((a, b) => a.at - b.at);
  const mergedResets: Array<{ at: number; delta: number }> = [];
  resetTimes.forEach((reset) => {
    const last = mergedResets[mergedResets.length - 1];
    if (last && last.at === reset.at) last.delta += reset.delta;
    else mergedResets.push({ ...reset });
  });

  const remaining = shape.accounts.reduce(
    (sum, account) => sum + (account.remaining * account.weight),
    0
  );
  const projected: Array<{ at: string; percent: number }> = [{ at: iso(now), percent: remaining }];
  const resets: Array<{ at: string; delta_percent: number; projected: boolean }> = [];
  const emptyMarks: Array<{ at: string; label: string }> = [];
  let value = remaining;
  let cursor = now;
  let poolEmptyAt: number | null = null;

  const burnTo = (target: number) => {
    const needed = ((target - cursor) * burn) / HOUR;
    if (value - needed <= 0 && burn > 0) {
      const emptyAt = cursor + (value / burn) * HOUR;
      projected.push({ at: iso(emptyAt), percent: 0 });
      if (poolEmptyAt === null) poolEmptyAt = emptyAt;
      emptyMarks.push({ at: iso(emptyAt), label: 'Empty' });
      projected.push({ at: iso(target), percent: 0 });
      value = 0;
    } else {
      value -= needed;
      projected.push({ at: iso(target), percent: value });
    }
    cursor = target;
  };

  for (const reset of mergedResets) {
    burnTo(reset.at);
    const next = Math.min(capacity, value + reset.delta);
    resets.push({ at: iso(reset.at), delta_percent: Math.round(next - value), projected: true });
    value = next;
    projected.push({ at: iso(reset.at), percent: value });
  }
  if (cursor < to) burnTo(to);
  return { projected, resets, emptyMarks, poolEmptyAt, remaining, capacity };
};

const buildPool = (shape: PoolShape, now: number, query: QuotaQuery) => {
  const recorded: Array<{ at: string; percent: number | null }> = [];
  const pastResets: Array<{ at: string; delta_percent: number; projected: boolean }> = [];
  let previous: { at: number; percent: number } | null = null;
  shape.recordedShape(now, (at, percent) => {
    if (at < query.from || at > query.to) return;
    recorded.push({ at: iso(at), percent });
    if (percent !== null) {
      if (previous && percent - previous.percent > 20) {
        pastResets.push({
          at: iso(at),
          delta_percent: Math.round(percent - previous.percent),
          projected: false,
        });
      }
      previous = { at, percent };
    }
  });
  const forecast = buildProjection(shape, now, query.to, query.future);
  const firstEmptyAccount = shape.accounts.reduce((lowest, account) =>
    account.remaining < lowest.remaining ? account : lowest
  );
  const burnFactor = query.burn === '1h' ? 1.15 : query.burn === '6h' ? 0.9 : 1;
  return {
    provider: shape.provider,
    accounts: shape.accounts.map((account) => ({
      id: account.id,
      label: account.label,
      email: account.email,
      plan: account.plan,
      weight: account.weight,
      remaining_percent: account.remaining,
      reset_at: iso(now + account.resetInHours * HOUR),
      status: account.status ?? 'ok',
    })),
    remaining_percent: Math.round(forecast.remaining),
    capacity_percent: forecast.capacity,
    weight_note: shape.weightNote,
    pool_empty_at: forecast.poolEmptyAt ? iso(forecast.poolEmptyAt) : null,
    first_empty:
      burnMultiplier(query.future) > 0
        ? {
            account: firstEmptyAccount.id,
            at: iso(now + (firstEmptyAccount.remaining / (shape.burnPerHour / 2.1)) * HOUR),
          }
        : null,
    next_reset: forecast.resets[0]
      ? { at: forecast.resets[0].at, delta_percent: forecast.resets[0].delta_percent }
      : null,
    burn_per_hour: Math.round(shape.burnPerHour * burnFactor * 10) / 10,
    recorded,
    projected: forecast.projected.filter((point) => {
      const at = Date.parse(point.at);
      return at >= now && at <= query.to;
    }),
    resets: [...pastResets, ...forecast.resets].filter((reset) => {
      const at = Date.parse(reset.at);
      return at >= query.from && at <= query.to;
    }),
    empty_marks: forecast.emptyMarks,
    coverage: { first_sample_at: iso(now - 14 * DAY), samples: 448 },
  };
};

export const quotaPools = (now: number, query: QuotaQuery, zero = false) => ({
  now: iso(now),
  from: iso(query.from),
  to: iso(query.to),
  pools: zero
    ? []
    : [
        buildPool(
          {
            provider: 'claude',
            accounts: CLAUDE_ACCOUNTS,
            burnPerHour: 3.7,
            weightNote: '',
            recordedShape: claudeRecorded,
          },
          now,
          query
        ),
        buildPool(
          {
            provider: 'codex',
            accounts: CODEX_ACCOUNTS,
            burnPerHour: 2.7,
            weightNote: 'Pro Max = 2.5x',
            recordedShape: codexRecorded,
          },
          now,
          query
        ),
      ],
});

/** One account's samples, built backwards so they end exactly on its current value. */
export const accountHistory = (id: string, now: number, from: number, to: number) => {
  const seed = [...CLAUDE_ACCOUNTS, ...CODEX_ACCOUNTS].find((account) => account.id === id);
  const random = seeded(id.length * 7919 + id.charCodeAt(id.length - 1));
  const samples: Array<{ at: string; percent: number | null }> = [];
  const end = Math.min(to, now);
  let value = seed?.remaining ?? 80;
  for (let at = end; at >= Math.max(from, now - 14 * DAY); at -= 2 * HOUR) {
    samples.unshift({ at: iso(at), percent: Math.round(value * 10) / 10 });
    value += 0.3 + random() * 1.4;
    // Going back in time past a weekly reset, the account was much lower.
    if (value > 100) value = 18 + random() * 20;
  }
  return { samples };
};

export const routerPools = (now: number, zero = false, mutable?: RouterMutable) => {
  const makePool = (provider: 'claude' | 'codex', seeds: AccountSeed[]) => {
    const state = mutable?.[provider];
    const routing = state?.active ?? seeds[0]?.id ?? '';
    const mode = state?.mode ?? 'auto';
    const random = seeded(provider === 'claude' ? 77 : 99);
    const reserve = state?.reserve ?? (seeds.length ? [seeds[seeds.length - 1].id] : []);
    const disabled = state?.disabled ?? [];
    const accounts = seeds.map((seed, index) => {
      const short = Math.round(30 + random() * 70);
      const off = disabled.includes(seed.id);
      return {
        id: seed.id,
        label: seed.label,
        email: seed.email,
        plan: seed.plan,
        role: off ? 'off' : reserve.includes(seed.id) ? 'reserve' : 'normal',
        order: index,
        state: off ? 'off' : seed.remaining < 10 ? 'low' : 'ok',
        detail: off ? 'Excluded from automatic routing' : '',
        active: seed.id === routing,
        pinned: mode === 'manual' && seed.id === routing,
        short: { remaining_percent: short, window_minutes: 300, reset_at: iso(now + (1 + index) * HOUR) },
        long: { remaining_percent: seed.remaining, window_minutes: 10080, reset_at: iso(now + seed.resetInHours * HOUR) },
        observed_at: iso(now - 3 * 60_000),
        sessions: seed.id === routing ? 2 : index === 1 ? 1 : 0,
        warm_sessions: seed.id === routing ? 2 : 0,
        busy_sessions: seed.id === routing ? 1 : 0,
        idle_sessions: seed.id === routing ? 1 : 0,
        last_used: iso(now - (index + 1) * 90_000),
        window: index < 2 ? 'running' : 'idle',
        window_reset_at: index < 2 ? iso(now + (index + 1) * HOUR) : undefined,
        plans: [],
      };
    });
    return {
      provider,
      enabled: true,
      now: iso(now),
      mode,
      pinned: mode === 'manual' ? routing : undefined,
      active: routing,
      active_reason:
        state?.reason ??
        (provider === 'claude'
          ? 'Most weekly quota left, and its 5-hour window is open.'
          : 'Keeps conversations on the account that started them.'),
      routing,
      routing_note:
        state?.reason ??
        (provider === 'claude'
          ? 'Most weekly quota left, and its 5-hour window is open.'
          : 'Keeps conversations on the account that started them.'),
      accounts,
      sessions: seeds.length
        ? [
            { id: `${provider}-session-1`, account: routing, label: seeds[0].label, bound_at: iso(now - 42 * 60_000), last_seen: iso(now - 60_000), requests: 118, state: 'busy' },
            { id: `${provider}-session-2`, account: routing, label: seeds[0].label, bound_at: iso(now - 11 * 60_000), last_seen: iso(now - 4 * 60_000), requests: 37, state: 'warm' },
            { id: `${provider}-session-3`, account: seeds[1]?.id ?? routing, label: seeds[1]?.label ?? seeds[0].label, bound_at: iso(now - 3 * HOUR), last_seen: iso(now - 80 * 60_000), requests: 12, state: 'cold' },
          ]
        : [],
      session_count: seeds.length ? 3 : 0,
      warm_sessions: seeds.length ? 2 : 0,
      settings: {
        enabled: true,
        switch_threshold_percent: state?.threshold ?? 15,
        switch_back: true,
        switch_back_margin_percent: 10,
        min_dwell_minutes: 10,
        cache_warm_minutes: 60,
        strategy: state?.strategy ?? 'balanced',
        rank_by: ['account_order', 'weekly_reset', 'five_hour_reset', 'most_remaining'],
        account_order: seeds.map((seed) => seed.id),
        reserve_accounts: reserve,
        disabled_accounts: disabled,
        manual_fallback: 'auto',
        quota_poll_minutes: 5,
        stale_after_minutes: 30,
        model_level_limits: true,
        window_start_model: provider === 'claude' ? 'claude-haiku-5-5' : 'gpt-5.1-codex-mini',
        window_start_effort: 'low',
        auto_start_windows: state?.autoStart ?? false,
      },
      history: seeds.length
        ? [
            { at: iso(now - 3 * HOUR), kind: 'switch', from: seeds[1]?.id ?? '', to: seeds[0].id, reason: 'Below the 15% switch threshold.' },
            { at: iso(now - 20 * HOUR), kind: 'switch', from: seeds[0].id, to: seeds[1]?.id ?? '', reason: 'Hit a 5-hour limit.' },
            { at: iso(now - 2 * DAY), kind: 'start', to: seeds[0].id, reason: 'Server started.' },
          ]
        : [],
      pause: state?.paused
        ? { since: iso(now - 2 * 60_000), until: iso(now + 18 * 60_000), auto_resume_minutes: 20, next_auto_resume: iso(now + 18 * 60_000) }
        : null,
      slowdown: { seconds: state?.slowdown ?? 0 },
      waiting: state?.paused ? 2 : 0,
      windows: seeds.map((seed, index) => {
        const plans = state?.plans?.filter((plan) => plan.account === seed.id) ?? (index === 2
          ? [{ id: `${provider}-plan-${index}`, account: seed.id, at: iso(now + 2 * HOUR), daily: true, source: 'manual' }]
          : []);
        return {
          account: seed.id,
          label: seed.label,
          state: index < 2 ? 'running' : 'idle',
          reset_at: index < 2 ? iso(now + (index + 1) * HOUR) : undefined,
          auto_start_at: state?.autoStart && index >= 2 ? iso(now + 30 * 60_000) : undefined,
          plans,
        };
      }),
      starter: { model: '', effort: '', plans: [], log: [] },
      last_error: '',
    };
  };
  return {
    pools: zero
      ? [makePool('claude', []), makePool('codex', [])]
      : [makePool('claude', CLAUDE_ACCOUNTS), makePool('codex', CODEX_ACCOUNTS)],
  };
};

export type RouterMutable = Record<
  string,
  {
    active?: string;
    mode?: 'auto' | 'manual';
    reason?: string;
    paused?: boolean;
    slowdown?: number;
    autoStart?: boolean;
    threshold?: number;
    strategy?: string;
    reserve?: string[];
    disabled?: string[];
    plans?: Array<{ id: string; account: string; at: string; daily: boolean; source: string }>;
  }
>;

export const health = (now: number, zero = false) => ({
  accounts: zero
    ? []
    : [
        ...CLAUDE_ACCOUNTS.map((account, index) => ({
          id: account.id,
          provider: 'claude',
          label: account.label,
          email: account.email,
          status: index === 4 ? 'expiring' : index === 5 ? 'failing' : 'ok',
          reason:
            index === 4
              ? 'Sign-in expires in 2 days.'
              : index === 5
                ? 'Refusing requests: session token was revoked.'
                : '',
          last_success_at: iso(now - (index + 1) * 11 * 60_000),
          last_error_at: index === 5 ? iso(now - 26 * 60_000) : '',
          last_error: index === 5 ? 'Upstream returned 401 Unauthorized.' : '',
          token_expires_at: iso(now + (index === 4 ? 2 : 20 + index) * DAY),
          reauth: { provider: 'claude' },
        })),
        ...CODEX_ACCOUNTS.map((account, index) => ({
          id: account.id,
          provider: 'codex',
          label: account.label,
          email: account.email,
          status: index === 1 ? 'cooling' : 'ok',
          reason: index === 1 ? 'Waiting out a rate limit for 4 more minutes.' : '',
          last_success_at: iso(now - (index + 2) * 7 * 60_000),
          last_error_at: index === 1 ? iso(now - 6 * 60_000) : '',
          last_error: index === 1 ? 'Upstream returned 429 Too Many Requests.' : '',
          token_expires_at: iso(now + (12 + index) * DAY),
          reauth: { provider: 'codex' },
        })),
      ],
});

export const usageKeys = (now: number, from: number, to: number, bucket: string, zero = false) => {
  if (zero) return { keys: [] };
  const step = bucket === 'hour' ? HOUR : DAY;
  const buildSeries = (seed: number, scale: number) => {
    const random = seeded(seed);
    const series: Array<{ at: string; requests: number; total_tokens: number }> = [];
    for (let at = from; at <= Math.min(to, now); at += step) {
      const requests = Math.round(random() * scale);
      series.push({ at: iso(at), requests, total_tokens: requests * Math.round(1800 + random() * 2400) });
    }
    return series;
  };
  const build = (id: string, name: string, masked: string, seed: number, scale: number) => {
    const series = buildSeries(seed, scale);
    const requests = series.reduce((sum, point) => sum + point.requests, 0);
    const tokens = series.reduce((sum, point) => sum + point.total_tokens, 0);
    return {
      key_id: id,
      name,
      masked,
      requests,
      failures: Math.round(requests * 0.012),
      input_tokens: Math.round(tokens * 0.62),
      output_tokens: Math.round(tokens * 0.2),
      cached_tokens: Math.round(tokens * 0.18),
      last_used_at: iso(now - seed * 1000),
      models: [
        { model: 'claude-sonnet-5-5', requests: Math.round(requests * 0.6), total_tokens: Math.round(tokens * 0.55) },
        { model: 'gpt-5.1-codex', requests: Math.round(requests * 0.3), total_tokens: Math.round(tokens * 0.33) },
        { model: 'claude-opus-5-5', requests: Math.round(requests * 0.1), total_tokens: Math.round(tokens * 0.12) },
      ],
      series,
    };
  };
  return {
    keys: [
      build('a1b2c3d4', 'Workstation', 'sk-...a1b2', 31, 90),
      build('e5f6a7b8', 'Laptop', 'sk-...e5f6', 57, 46),
      build('c9d0e1f2', 'T3 Code', 'sk-...c9d0', 83, 28),
      build('13579bdf', 'CI runner', 'sk-...1357', 9, 6),
    ],
  };
};

export const alertEvents = (now: number, zero = false) => ({
  active: zero
    ? []
    : [
        { key: 'account_failing:claude-frank', kind: 'account_failing', provider: 'claude', account: 'claude-frank', message: 'Frank is failing: session token was revoked.', since: iso(now - 26 * 60_000) },
        { key: 'pool_empty:codex', kind: 'pool_empty_soon', provider: 'codex', message: 'Codex pool empties in about 5 hours at the current burn.', since: iso(now - 40 * 60_000) },
      ],
  events: zero
    ? []
    : [
        { at: iso(now - 26 * 60_000), kind: 'account_failing', provider: 'claude', account: 'claude-frank', message: 'Frank is failing: session token was revoked.' },
        { at: iso(now - 5 * HOUR), kind: 'account_low', provider: 'claude', account: 'claude-frank', message: 'Frank dropped below 10% weekly quota.' },
        { at: iso(now - 26 * HOUR), kind: 'reset', provider: 'codex', account: 'codex-grace', message: 'Grace reset: +100%.' },
        { at: iso(now - 50 * HOUR), kind: 'test', provider: '', account: '', message: 'Test notification sent to Phone.' },
      ],
});

export const systemInfo = (now: number, setupCompleted: boolean) => ({
  version: 'v1.0.0',
  commit: 'abc1234',
  upstream_version: 'v8.0.42',
  panel_version: 'v1.0.0',
  data_dir: '~/.token-proxy-router',
  os: 'linux',
  arch: 'amd64',
  uptime_seconds: Math.round((now % (9 * DAY)) / 1000),
  service: { kind: 'systemd', installed: true, running: true, supervised: true },
  setup: { completed: setupCompleted },
});

export const connectInfo = (zero = false) => ({
  hub_urls: [
    { label: 'Tailnet', url: 'https://hub.example.ts.net:8443' },
    { label: 'This machine', url: 'http://127.0.0.1:8317' },
  ],
  keys: zero
    ? []
    : [
        { key_id: 'a1b2c3d4', name: 'Workstation', masked: 'sk-...a1b2' },
        { key_id: 'e5f6a7b8', name: 'Laptop', masked: 'sk-...e5f6' },
      ],
  models: {
    codex: ['gpt-5.1-codex', 'gpt-5.1-codex-mini'],
    claude: ['claude-opus-5-5', 'claude-sonnet-5-5', 'claude-haiku-5-5'],
  },
  install: {
    linux:
      'curl -fsSL https://github.com/Danielw412/token-proxy-router/releases/latest/download/install.sh | sh',
    windows:
      'irm https://github.com/Danielw412/token-proxy-router/releases/latest/download/install.ps1 | iex',
  },
});

export const connectSnippets = (keyId: string, hubUrl: string) => {
  const key = `sk-demo-${keyId || 'key'}`;
  const hub = hubUrl || 'https://hub.example.ts.net:8443';
  return {
    linux: `tpr connect --hub ${hub} --key ${key} --codex --claude --t3`,
    windows: `tpr.exe connect --hub ${hub} --key ${key} --codex --claude --t3`,
    codex_toml: `model_provider = "tpr"\n\n[model_providers.tpr]\nname = "TokenProxyRouter"\nbase_url = "${hub}/v1"\nenv_key = "TPR_API_KEY"\nwire_api = "responses"\n\n# In your shell profile:\n# export TPR_API_KEY=${key}`,
    claude_env: `export ANTHROPIC_BASE_URL=${hub}\nexport ANTHROPIC_AUTH_TOKEN=${key}`,
    claude_env_windows: `$env:ANTHROPIC_BASE_URL = "${hub}"\n$env:ANTHROPIC_AUTH_TOKEN = "${key}"`,
    t3: `tpr connect --hub ${hub} --key ${key} --t3`,
  };
};

const recentRequests = (now: number, seed: number) => {
  const random = seeded(seed);
  return Array.from({ length: 20 }, (_, index) => {
    const start = new Date(now - (20 - index) * 10 * 60_000);
    const end = new Date(start.getTime() + 10 * 60_000);
    const label = (date: Date) => `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`;
    return {
      time: `${label(start)}-${label(end)}`,
      success: Math.round(random() * 9),
      failed: random() > 0.92 ? 1 : 0,
    };
  });
};

const authFile = (
  provider: string,
  account: AccountSeed,
  now: number,
  index: number,
  status: { disabled?: boolean; unavailable?: boolean; message?: string } = {}
) => ({
  id: `${provider}-${account.label.toLowerCase()}.json`,
  name: `${provider}-${account.email}.json`,
  type: provider,
  provider,
  email: account.email,
  size: 2048 + index * 16,
  auth_index: `${provider}-${index}`,
  status: status.unavailable ? 'error' : 'active',
  status_message: status.message ?? '',
  disabled: status.disabled ?? false,
  unavailable: status.unavailable ?? false,
  last_refresh: iso(now - (index + 1) * HOUR),
  modtime: iso(now - (index + 1) * DAY),
  success: 40 + index * 3,
  failed: index % 4 === 0 ? 1 : 0,
  recent_requests: recentRequests(now, 11 + index),
});

export const authFiles = (now: number, zero = false) => ({
  files: zero
    ? []
    : [
        ...CLAUDE_ACCOUNTS.map((account, index) =>
          authFile('claude', account, now, index, index === 5 ? { unavailable: true, message: 'Session token was revoked.' } : {})
        ),
        ...CODEX_ACCOUNTS.map((account, index) => authFile('codex', account, now, index + 6)),
        authFile('xai', { id: 'xai-judy', label: 'Judy', email: 'judy@example.com', plan: '', weight: 1, remaining: 80, resetInHours: 1 }, now, 10),
      ],
});

export const configJson = (zero = false) => ({
  server: { host: '127.0.0.1', port: 8317 },
  observability: {
    logs: { debug: false, 'logging-to-file': true, 'request-log': false, 'logs-max-total-size-mb': 512 },
  },
  access: { 'api-keys': zero ? [] : ['sk-demo-a1b2c3d4', 'sk-demo-e5f6a7b8'] },
  routing: {
    strategy: 'round-robin',
    'session-affinity': true,
    retry: { 'request-retry': 3, 'max-retry-credentials': 0, 'max-retry-interval': 30 },
  },
  'api-keys': {},
});

export const configYaml = (zero = false) => `# Token Proxy Router configuration (demo data)
server:
  host: "127.0.0.1"
  port: 8317
observability:
  logs:
    debug: false
    logging-to-file: true
    request-log: false
access:
  api-keys:${zero ? ' []' : '\n    - sk-demo-a1b2c3d4\n    - sk-demo-e5f6a7b8'}
routing:
  strategy: round-robin
  session-affinity: true
  retry:
    request-retry: 3
    max-retry-credentials: 0
    max-retry-interval: 30
`;

export const logLines = (now: number) =>
  Array.from({ length: 40 }, (_, index) => {
    const at = new Date(now - (40 - index) * 7000).toISOString().replace('T', ' ').slice(0, 19);
    const status = index % 13 === 5 ? 429 : 200;
    const level = status === 200 ? 'INFO' : 'WARN';
    return `[${at}] [${level}] [gin_logger.go:92] ${status} | ${(index % 7) + 1}.2s | 127.0.0.1 | POST "/v1/messages" | claude-sonnet-5-5`;
  });
