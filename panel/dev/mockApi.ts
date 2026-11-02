/**
 * Vite dev-server mock of the management API. `apply: 'serve'` keeps this out of
 * production builds, and nothing under src/ imports it.
 *
 * Scenarios: GET /__mock/scenario/<full|empty|firstrun> switches the data set.
 * Sign in with any key except "wrong".
 */

import type { IncomingMessage, ServerResponse } from 'node:http';
import type { Plugin } from 'vite';
import {
  accountHistory,
  alertEvents,
  authFiles,
  configJson,
  configYaml,
  connectInfo,
  connectSnippets,
  health,
  iso,
  logLines,
  quotaPools,
  routerPools,
  systemInfo,
  usageKeys,
  type RouterMutable,
} from './fixtures';

const PREFIX = '/v8/management';
const DAY = 86_400_000;

interface MockState {
  scenario: 'full' | 'empty' | 'firstrun';
  setupCompleted: boolean;
  identity: boolean;
  router: RouterMutable;
  alerts: Record<string, unknown>;
  tailscale: Record<string, unknown>;
  updateStartedAt: number | null;
  version: string;
}

const initialState = (scenario: MockState['scenario'] = 'full'): MockState => ({
  scenario,
  setupCompleted: scenario === 'full',
  identity: false,
  router: {},
  alerts: {
    enabled: true,
    pool_empty_within_hours: 6,
    account_below_percent: 10,
    notify_on_reset: false,
    channels: [
      { type: 'ntfy', url: 'https://ntfy.sh/••••••', name: 'Phone' },
      { type: 'discord', url: 'https://discord.com/api/webhooks/••••••', name: 'Team channel' },
    ],
  },
  tailscale:
    scenario === 'full'
      ? {
          installed: true,
          logged_in: true,
          dns_name: 'hub.example.ts.net',
          exposed: true,
          url: 'https://hub.example.ts.net:8443',
          port: 8443,
          can_expose: true,
          hint: '',
          identity: { enabled: false, allowed_logins: [], available: true },
        }
      : {
          installed: true,
          logged_in: false,
          dns_name: '',
          exposed: false,
          url: '',
          port: 8443,
          can_expose: false,
          hint: 'Tailscale is installed but not signed in. Run: tailscale up',
          identity: { enabled: false, allowed_logins: [], available: false },
        },
  updateStartedAt: null,
  version: 'v1.0.0',
});

let state = initialState();

const readBody = (req: IncomingMessage): Promise<string> =>
  new Promise((resolve) => {
    const chunks: Buffer[] = [];
    req.on('data', (chunk: Buffer) => chunks.push(chunk));
    req.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
    req.on('error', () => resolve(''));
  });

const parseJson = (raw: string): Record<string, unknown> => {
  try {
    const parsed: unknown = JSON.parse(raw);
    return parsed && typeof parsed === 'object' ? (parsed as Record<string, unknown>) : {};
  } catch {
    return {};
  }
};

const send = (res: ServerResponse, status: number, body: unknown, type = 'application/json') => {
  res.statusCode = status;
  res.setHeader('Content-Type', type);
  res.setHeader('X-CPA-Version', '8.0.42');
  res.setHeader('X-CPA-Build-Date', '2026-10-30T00:00:00Z');
  res.setHeader('X-CPA-Support-Plugin', 'false');
  res.end(typeof body === 'string' ? body : JSON.stringify(body));
};

const updateInfo = (now: number) => {
  const started = state.updateStartedAt;
  let phase = 'idle';
  if (started !== null) {
    const elapsed = now - started;
    phase =
      elapsed < 1500 ? 'downloading' : elapsed < 3000 ? 'verifying' : elapsed < 4500 ? 'applying' : 'restarting';
    if (elapsed > 6000) {
      state.updateStartedAt = null;
      state.version = 'v1.1.0';
      phase = 'idle';
    }
  }
  const current = state.version;
  return {
    current,
    latest: 'v1.1.0',
    available: current !== 'v1.1.0',
    notes_url: 'https://github.com/Danielw412/token-proxy-router/releases/tag/v1.1.0',
    notes: 'Faster quota refresh.\n\nFixes a rare pin not persisting across restarts.',
    checked_at: iso(now - 40 * 60_000),
    can_apply: true,
    reason: '',
    rollback_available: true,
    state: phase,
    error: '',
  };
};

const handleTpr = async (
  req: IncomingMessage,
  res: ServerResponse,
  path: string,
  url: URL
): Promise<boolean> => {
  const now = Date.now();
  const zero = state.scenario !== 'full';
  const method = req.method ?? 'GET';
  const provider = url.searchParams.get('provider') ?? '';
  const pool = () => (state.router[provider] ??= {});

  if (path === '/quota/pools') {
    send(
      res,
      200,
      quotaPools(
        now,
        {
          from: Date.parse(url.searchParams.get('from') ?? '') || now - 3 * DAY,
          to: Date.parse(url.searchParams.get('to') ?? '') || now + 11 * DAY,
          burn: url.searchParams.get('burn') ?? '3h',
          future: url.searchParams.get('future') ?? 'same',
        },
        zero
      )
    );
    return true;
  }
  const history = path.match(/^\/quota\/accounts\/([^/]+)\/history$/);
  if (history) {
    send(
      res,
      200,
      accountHistory(
        decodeURIComponent(history[1]),
        now,
        Date.parse(url.searchParams.get('from') ?? '') || now - 3 * DAY,
        Date.parse(url.searchParams.get('to') ?? '') || now
      )
    );
    return true;
  }
  if (path === '/quota/refresh') {
    send(res, 200, { ok: true });
    return true;
  }

  if (path === '/router/state') {
    const pools = routerPools(now, zero, state.router);
    send(res, 200, provider ? pools.pools.find((item) => item.provider === provider) ?? {} : pools);
    return true;
  }
  if (path === '/router/mode' && method === 'POST') {
    const body = parseJson(await readBody(req));
    pool().mode = body.mode === 'manual' ? 'manual' : 'auto';
    if (typeof body.account === 'string' && body.account) {
      pool().active = body.account;
      pool().reason = 'Pinned by you.';
    } else {
      pool().reason = 'Back to automatic: picked the account with the most quota left.';
    }
    send(res, 200, { ok: true });
    return true;
  }
  if (path === '/router/select' && method === 'POST') {
    const body = parseJson(await readBody(req));
    if (typeof body.account === 'string') {
      pool().active = body.account;
      pool().reason = 'You chose this account with Use now.';
    }
    send(res, 200, { ok: true });
    return true;
  }
  if (path === '/router/pause') {
    pool().paused = true;
    send(res, 200, { ok: true });
    return true;
  }
  if (path === '/router/resume') {
    pool().paused = false;
    send(res, 200, { ok: true });
    return true;
  }
  if (path === '/router/slowdown') {
    const body = parseJson(await readBody(req));
    pool().slowdown = typeof body.seconds === 'number' ? body.seconds : 0;
    send(res, 200, { ok: true });
    return true;
  }
  if (path === '/router/settings') {
    if (method === 'PATCH') {
      const body = parseJson(await readBody(req));
      if (typeof body.auto_start_windows === 'boolean') pool().autoStart = body.auto_start_windows;
      if (typeof body.switch_threshold_percent === 'number') pool().threshold = body.switch_threshold_percent;
      if (typeof body.strategy === 'string') pool().strategy = body.strategy;
      if (Array.isArray(body.reserve_accounts)) pool().reserve = body.reserve_accounts as string[];
      if (Array.isArray(body.disabled_accounts)) pool().disabled = body.disabled_accounts as string[];
    }
    send(res, 200, {
      provider,
      settings: routerPools(now, zero, state.router).pools.find((item) => item.provider === provider)?.settings ?? {},
      warnings: [],
    });
    return true;
  }
  if (path === '/router/windows/schedule') {
    const body = parseJson(await readBody(req));
    const plan = {
      id: `plan-${now}`,
      account: String(body.account ?? ''),
      at: String(body.at ?? iso(now + 3_600_000)),
      daily: body.daily === true,
      source: 'manual',
    };
    pool().plans = [...(pool().plans ?? []), plan];
    send(res, 200, { ok: true });
    return true;
  }
  if (path === '/router/windows/cancel') {
    const body = parseJson(await readBody(req));
    pool().plans = (pool().plans ?? []).filter((plan) => plan.id !== body.id);
    send(res, 200, { ok: true });
    return true;
  }
  if (path.startsWith('/router/')) {
    send(res, 200, { ok: true });
    return true;
  }

  if (path === '/alerts') {
    if (method === 'PUT') state.alerts = parseJson(await readBody(req));
    send(res, 200, state.alerts);
    return true;
  }
  if (path === '/alerts/test') {
    const channels = (state.alerts.channels as Array<{ name: string }>) ?? [];
    send(res, 200, {
      results: channels.map((channel, index) => ({
        name: channel.name,
        ok: index === 0,
        error: index === 0 ? '' : 'The channel answered 403 Forbidden.',
      })),
    });
    return true;
  }
  if (path === '/alerts/events') {
    send(res, 200, alertEvents(now, zero));
    return true;
  }

  if (path === '/health/accounts') {
    send(res, 200, health(now, zero));
    return true;
  }
  if (path === '/usage/keys') {
    send(
      res,
      200,
      usageKeys(
        now,
        Date.parse(url.searchParams.get('from') ?? '') || now - 7 * DAY,
        Date.parse(url.searchParams.get('to') ?? '') || now,
        url.searchParams.get('bucket') ?? 'day',
        zero
      )
    );
    return true;
  }

  if (path === '/system') {
    const info = systemInfo(now, state.setupCompleted);
    send(res, 200, { ...info, version: state.version });
    return true;
  }
  if (path === '/update') {
    send(res, 200, updateInfo(now));
    return true;
  }
  if (path === '/update/check') {
    send(res, 200, updateInfo(now));
    return true;
  }
  if (path === '/update/apply') {
    state.updateStartedAt = now;
    send(res, 202, { ok: true });
    return true;
  }
  if (path === '/update/rollback') {
    state.version = 'v1.0.0';
    send(res, 202, { ok: true });
    return true;
  }
  if (path === '/setup') {
    send(res, 200, { completed: state.setupCompleted });
    return true;
  }
  if (path === '/setup/complete') {
    state.setupCompleted = true;
    send(res, 200, { completed: true });
    return true;
  }
  if (path === '/tailscale') {
    send(res, 200, state.tailscale);
    return true;
  }
  if (path === '/tailscale/expose') {
    if (method === 'POST') {
      const body = parseJson(await readBody(req));
      state.tailscale = {
        ...state.tailscale,
        exposed: true,
        port: body.port ?? 8443,
        url: `https://hub.example.ts.net:${String(body.port ?? 8443)}`,
      };
    } else {
      state.tailscale = { ...state.tailscale, exposed: false, url: '' };
    }
    send(res, 200, state.tailscale);
    return true;
  }
  if (path === '/tailscale/identity') {
    const body = parseJson(await readBody(req));
    state.tailscale = {
      ...state.tailscale,
      identity: { available: true, enabled: body.enabled === true, allowed_logins: body.allowed_logins ?? [] },
    };
    send(res, 200, state.tailscale);
    return true;
  }
  if (path === '/backup') {
    res.statusCode = 200;
    res.setHeader('Content-Type', 'application/octet-stream');
    res.setHeader('Cache-Control', 'no-store');
    res.end('demo-encrypted-backup');
    return true;
  }
  if (path === '/restore') {
    await readBody(req);
    send(res, 200, { ok: true });
    return true;
  }
  if (path === '/connect') {
    send(res, 200, connectInfo(zero));
    return true;
  }
  if (path === '/connect/snippets') {
    const body = parseJson(await readBody(req));
    res.setHeader('Cache-Control', 'no-store');
    send(res, 200, connectSnippets(String(body.key_id ?? ''), String(body.hub_url ?? '')));
    return true;
  }
  send(res, 404, { error: `No mock for ${path}` });
  return true;
};

const handleUpstream = async (
  req: IncomingMessage,
  res: ServerResponse,
  path: string
): Promise<void> => {
  const now = Date.now();
  const zero = state.scenario !== 'full';
  const method = req.method ?? 'GET';
  if (method !== 'GET') await readBody(req);

  if (path === '/config') return send(res, 200, configJson(zero));
  if (path === '/config.yaml') return send(res, 200, configYaml(zero), 'text/yaml');
  if (path === '/config/access/api-keys') {
    return send(res, 200, zero ? [] : ['sk-demo-a1b2c3d4', 'sk-demo-e5f6a7b8']);
  }
  if (path === '/credentials' || path === '/credentials/refresh') {
    return send(res, 200, authFiles(now, zero));
  }
  if (path === '/plugins') return send(res, 200, { plugins: [] });
  if (path === '/plugins/store') return send(res, 200, { plugins: [] });
  if (path === '/observability/logs') {
    return send(res, 200, { lines: logLines(now), 'latest-timestamp': Math.round(now / 1000) });
  }
  if (path === '/observability/logs/errors') return send(res, 200, { files: [] });
  if (path === '/observability/usage/api-keys') return send(res, 200, {});
  if (path === '/server/latest-version') return send(res, 200, { 'latest-version': '8.0.42' });
  if (path === '/oauth/auth-url') {
    return send(res, 200, {
      status: 'ok',
      url: 'https://claude.ai/oauth/authorize?client_id=demo&state=demo-state',
      state: 'demo-state',
    });
  }
  if (path === '/oauth/status') return send(res, 200, { status: 'wait' });
  if (path === '/oauth/callback') return send(res, 200, { status: 'ok' });
  send(res, 200, {});
};

export const mockApiPlugin = (): Plugin => ({
  name: 'tpr-dev-mock-api',
  apply: 'serve',
  configureServer(server) {
    server.middlewares.use((req, res, next) => {
      void (async () => {
        const url = new URL(req.url ?? '/', 'http://mock.local');
        const path = url.pathname;

        const scenario = path.match(/^\/__mock\/scenario\/(full|empty|firstrun)$/);
        if (scenario) {
          state = initialState(scenario[1] as MockState['scenario']);
          return send(res, 200, { scenario: state.scenario });
        }
        if (path === '/__mock/identity') {
          state.identity = url.searchParams.get('on') === '1';
          return send(res, 200, { identity: state.identity });
        }
        if (path === '/v1/models') {
          return send(res, 200, {
            object: 'list',
            data: ['claude-opus-5-5', 'claude-sonnet-5-5', 'gpt-5.1-codex'].map((id) => ({ id })),
          });
        }
        if (!path.startsWith(PREFIX)) return next();
        const route = path.slice(PREFIX.length);

        if (route === '/tpr-public/session') {
          return send(res, 200, {
            identity_login: state.identity,
            login: state.identity ? 'alice@example.com' : '',
          });
        }
        const auth = req.headers.authorization ?? '';
        if (auth === 'Bearer wrong' || (!auth && !state.identity)) {
          return send(res, 401, { error: 'invalid management key' });
        }
        if (route.startsWith('/tpr/')) {
          await handleTpr(req, res, route.slice('/tpr'.length), url);
          return;
        }
        await handleUpstream(req, res, route);
      })().catch((error: unknown) => {
        send(res, 500, { error: String(error) });
      });
    });
  },
});
