import { describe, expect, test } from 'bun:test';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { createInstance } from 'i18next';
import { routerPools } from '../dev/fixtures';
import { countDone } from '@/features/setup/quickStartLogic';
import { groupAccountsByProvider } from '@/features/accounts/accountGroups';
import { readBasicSettings, writeBasicSettings } from '@/features/settings/basicSettings';
import { recentFailures } from '@/features/dashboard/overviewLogic';
import { maskEmails, splitEmails } from '@/components/tpr/emailText';
import en from '@/i18n/locales/en.json';
import ko from '@/i18n/locales/ko.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import {
  applyRoleChange,
  normalizeAlertEvents,
  normalizePool,
  normalizeRouterPool,
  type AccountHealth,
} from '@/services/api/tpr';

const walk = (dir: string): string[] =>
  readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    return statSync(path).isDirectory() ? walk(path) : [path];
  });

describe('router state', () => {
  test('reads the live engine shape', () => {
    const pool = normalizeRouterPool({
      provider: 'codex',
      enabled: true,
      mode: 'manual',
      pinned: 'b',
      active: 'a',
      routing: 'b',
      routing_note: 'Pinned manually',
      accounts: [
        {
          id: 'b',
          label: 'B',
          role: 'reserve',
          state: 'low',
          detail: '9% left',
          pinned: true,
          short: { remaining_percent: 40, reset_at: '2026-10-26T12:00:00Z' },
          long: { remaining_percent: 9 },
        },
      ],
      pause: { since: '2026-10-26T11:00:00Z', auto_resume_minutes: 20 },
      slowdown: { seconds: 30 },
      windows: [{ account: 'b', state: 'idle', plans: [{ id: 'p', at: '2026-10-26T13:00:00Z', daily: true }] }],
    });
    expect(pool.routing).toBe('b');
    expect(pool.routingNote).toBe('Pinned manually');
    expect(pool.accounts[0].active).toBe(true);
    expect(pool.accounts[0].short?.remaining).toBe(40);
    expect(pool.accounts[0].long?.remaining).toBe(9);
    expect(pool.pause?.autoResumeMinutes).toBe(20);
    expect(pool.slowdownSeconds).toBe(30);
    expect(pool.windows[0].plans[0].daily).toBe(true);
  });

  test('tolerates an empty or missing response', () => {
    const pool = normalizeRouterPool(undefined);
    expect(pool.accounts).toEqual([]);
    expect(pool.mode).toBe('auto');
    expect(pool.pause).toBeNull();
    expect(pool.settings.reserve_accounts).toEqual([]);
  });

  test('the dev fixtures match the live shape', () => {
    const [claude] = routerPools(Date.UTC(2026, 9, 26)).pools.map(normalizeRouterPool);
    expect(claude.accounts).toHaveLength(6);
    expect(claude.accounts[0].short?.remaining).not.toBeNull();
    expect(claude.routing).toBe('claude-alice');
  });

  test('a role change moves the account between the two settings lists', () => {
    const settings = { reserve_accounts: ['a'], disabled_accounts: ['b'] };
    expect(applyRoleChange(settings, 'a', 'off')).toEqual({
      reserve_accounts: [],
      disabled_accounts: ['b', 'a'],
    });
    expect(applyRoleChange(settings, 'b', 'reserve')).toEqual({
      reserve_accounts: ['a', 'b'],
      disabled_accounts: [],
    });
    expect(applyRoleChange(settings, 'a', 'normal')).toEqual({
      reserve_accounts: [],
      disabled_accounts: ['b'],
    });
  });
});

describe('quota normalization', () => {
  test('keeps holes, drops points without a time, and defaults the rest', () => {
    const pool = normalizePool({
      provider: 'claude',
      recorded: [{ at: '2026-10-26T12:00:00Z', percent: null }, { percent: 5 }],
      first_empty: {},
    });
    expect(pool.recorded).toEqual([{ at: '2026-10-26T12:00:00Z', percent: null }]);
    expect(pool.firstEmpty).toBeNull();
    expect(pool.capacityPercent).toBe(0);
  });
});

describe('alert events', () => {
  test('live conditions lead and are flagged, without repeating their raising event', () => {
    const list = normalizeAlertEvents({
      active: [{ kind: 'low', provider: 'claude', account: 'a', message: 'now', since: '2026-10-26T10:00:00Z' }],
      events: [
        { at: '2026-10-26T10:00:00Z', kind: 'low', provider: 'claude', account: 'a', message: 'now' },
        { at: '2026-10-25T10:00:00Z', kind: 'reset', provider: 'codex', message: 'older' },
      ],
    });
    expect(list.map((event) => [event.message, event.active])).toEqual([
      ['now', true],
      ['older', false],
    ]);
    expect(normalizeAlertEvents(undefined)).toEqual([]);
  });
});

describe('basic settings', () => {
  const YAML = `# keep me
server:
  host: "127.0.0.1"
  port: 8317
observability:
  logs:
    debug: false
    logging-to-file: true
routing:
  strategy: fill-first
  retry:
    request-retry: 5
    max-retry-interval: 30
`;

  test('reads the v8 layout', () => {
    expect(readBasicSettings(YAML)).toEqual({
      port: 8317,
      debug: false,
      loggingToFile: true,
      requestLog: false,
      routingStrategy: 'fill-first',
      sessionAffinity: false,
      requestRetry: 5,
    });
  });

  test('writes only what it owns, keeping comments and other keys', () => {
    const next = writeBasicSettings(YAML, { ...readBasicSettings(YAML), port: 9000, debug: true });
    expect(next).toContain('# keep me');
    expect(next).toContain('port: 9000');
    expect(next).toContain('host: "127.0.0.1"');
    expect(next).toContain('max-retry-interval: 30');
    expect(readBasicSettings(next).debug).toBe(true);
  });

  test('copes with an empty config', () => {
    expect(readBasicSettings('').port).toBe(8317);
  });
});

describe('page logic', () => {
  const health = (partial: Partial<AccountHealth>): AccountHealth => ({
    id: 'x',
    provider: 'claude',
    label: '',
    email: '',
    status: 'ok',
    reason: '',
    lastSuccessAt: null,
    lastErrorAt: null,
    lastError: '',
    tokenExpiresAt: null,
    reauthProvider: null,
    ...partial,
  });

  test('accounts list Claude and Codex first, worst status first', () => {
    const groups = groupAccountsByProvider([
      health({ id: 'x1', provider: 'xai' }),
      health({ id: 'c1', provider: 'codex' }),
      health({ id: 'a1', status: 'ok' }),
      health({ id: 'a2', status: 'failing' }),
    ]);
    expect(groups.map((group) => group.provider)).toEqual(['claude', 'codex', 'xai']);
    expect(groups[0].accounts.map((account) => account.id)).toEqual(['a2', 'a1']);
  });

  test('recent failures are newest first', () => {
    const failures = recentFailures([
      health({ id: 'old', lastError: 'x', lastErrorAt: '2026-10-25T00:00:00Z' }),
      health({ id: 'new', lastError: 'y', lastErrorAt: '2026-10-26T00:00:00Z' }),
      health({ id: 'fine' }),
    ]);
    expect(failures.map((account) => account.id)).toEqual(['new', 'old']);
  });

  test('quick start counts finished steps', () => {
    expect(countDone({ accounts: 0, exposed: false, connected: false })).toBe(0);
    expect(countDone({ accounts: 2, exposed: true, connected: false })).toBe(2);
  });

  test('emails are found inside other text', () => {
    expect(splitEmails('claude-alice@example.com.json')).toEqual([
      { text: 'claude-alice@example.com', email: true },
      { text: '.json', email: false },
    ]);
    expect(splitEmails('no address')).toEqual([{ text: 'no address', email: false }]);
    expect(maskEmails('file-alice@example.com.json')).toBe('•••@•••.json');
  });
});

describe('strings', () => {
  const keysUsed = () => {
    const keys = new Set<string>();
    for (const file of walk('src')) {
      if (!/\.tsx?$/.test(file)) continue;
      for (const match of readFileSync(file, 'utf8').matchAll(/['"`](tpr\.[a-z_0-9.]+)['"`]/g)) {
        keys.add(match[1]);
      }
    }
    return keys;
  };

  const lookup = (key: string, suffix = ''): unknown =>
    (key + suffix)
      .split('.')
      .reduce<unknown>(
        (value, part) => (value as Record<string, unknown> | undefined)?.[part],
        en
      );

  test('every tpr key used in the source exists in English', () => {
    const missing = Array.from(keysUsed()).filter(
      (key) => !lookup(key) && !lookup(key, '_other')
    );
    expect(missing).toEqual([]);
  });

  test('other locales fall back to English without noise', async () => {
    for (const translation of [ko, zhCN]) {
      const i18n = createInstance();
      const warnings: unknown[] = [];
      await i18n.init({
        lng: 'test',
        fallbackLng: 'en',
        resources: { en: { translation: en }, test: { translation } },
        logger: undefined,
        saveMissing: false,
        missingKeyHandler: (...args: unknown[]) => warnings.push(args),
      });
      expect(i18n.t('tpr.quota.pool_empty')).toBe('Pool empty');
      expect(warnings).toEqual([]);
    }
    const source = readFileSync('src/i18n/index.ts', 'utf8');
    expect(source).toContain("fallbackLng: 'en'");
    expect(source).not.toContain('debug: true');
  });

  test('no sponsor or promo content remains', () => {
    const banned = /sponsor|apikey.?fun|bestproxy|double 11|join open platform|affiliate/i;
    const offenders = [...walk('src'), 'index.html']
      .filter((file) => /\.(tsx?|scss|json|html)$/.test(file))
      .filter((file) => banned.test(readFileSync(file, 'utf8')));
    expect(offenders).toEqual([]);
  });

  test('the product is named everywhere users see it', () => {
    expect(readFileSync('index.html', 'utf8')).toContain('<title>Token Proxy Router</title>');
    expect(en.title.login).toBe('Token Proxy Router');
    expect(JSON.stringify(en)).not.toContain('CPAMC');
  });
});

describe('dev mock', () => {
  test('is serve-only and nothing in src imports it', () => {
    expect(readFileSync('dev/mockApi.ts', 'utf8')).toContain("apply: 'serve'");
    const offenders = walk('src')
      .filter((file) => /\.tsx?$/.test(file))
      .filter((file) => /from ['"][^'"]*dev\/(mockApi|fixtures)/.test(readFileSync(file, 'utf8')));
    expect(offenders).toEqual([]);
  });
});
