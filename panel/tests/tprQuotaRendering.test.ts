import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { accountHistory, quotaPools } from '../dev/fixtures';
import i18n from '@/i18n';
import { PoolCard } from '@/features/quota/PoolCard';
import { PoolChart } from '@/features/quota/chart/PoolChart';
import { normalizePool, normalizePoolsResponse, type QuotaPool } from '@/services/api/tpr';
import { usePrivacyStore } from '@/stores/usePrivacyStore';

const DAY = 86_400_000;
const NOW = Date.UTC(2026, 9, 26, 12);
const FROM = NOW - 3 * DAY;
const TO = NOW + 11 * DAY;

const poolsFor = (future = 'same') =>
  normalizePoolsResponse(quotaPools(NOW, { from: FROM, to: TO, burn: '3h', future })).pools;

const renderCard = (pool: QuotaPool, mode: 'remaining' | 'drain' = 'remaining') =>
  renderToStaticMarkup(
    createElement(
      MemoryRouter,
      null,
      createElement(PoolCard, { pool, now: NOW, from: FROM, to: TO, mode, burn: '3h', future: 'same' })
    )
  );

let language = 'en';
beforeAll(async () => {
  language = i18n.language;
  await i18n.changeLanguage('en');
});
afterAll(async () => {
  await i18n.changeLanguage(language);
});

describe('pool card from fixtures', () => {
  const [claude, codex] = poolsFor();

  test('shows the headline figures', () => {
    const html = renderCard(claude);
    expect(html).toContain('555%');
    expect(html).toContain('of 600%');
    expect(html).toContain('6 accounts');
    expect(html).toContain('Pool empty');
    expect(html).toContain('First empty');
    expect(html).toContain('Next reset');
    expect(html).toContain('Burn, last 3h');
    expect(html).toContain('3.7%/h');
  });

  test('shows the 5-hour window for the pool and each account', () => {
    const html = renderCard(claude);
    expect(html).toContain('5-hour');
    expect(html).toContain(`${Math.round(claude.fiveHourRemainingPercent)}%`);
    for (const account of claude.accounts) {
      expect(html).toContain(`aria-label="${account.label} 5-hour"`);
      expect(html).toContain(`aria-label="${account.label} Weekly"`);
      expect(html).toContain(`aria-valuenow="${Math.round(account.fiveHourRemainingPercent ?? -1)}"`);
    }
  });

  test('a pool with no 5-hour data shows a dash, not 0%', () => {
    const html = renderCard(normalizePool({ provider: 'claude', accounts: [{ id: 'a', label: 'A' }] }));
    expect(html).toContain('5-hour');
    expect(html).not.toContain('NaN');
    expect(html).toContain('—');
  });

  test('shows the weight note and a dated pool-empty time', () => {
    const html = renderCard(codex);
    expect(html).toContain('421%');
    expect(html).toContain('of 450%');
    expect(html).toContain('Pro Max = 2.5x');
    expect(html).toContain('+450%');
  });

  test('draws the chart, with its legend and accessible summary', () => {
    const html = renderCard(claude);
    expect(html).toContain('role="img"');
    expect(html).toContain('Claude pool: 555% of 600% remaining');
    expect(html).toContain('Recorded');
    expect(html).toContain('Projected');
    expect(html).toContain('>Now<');
    expect(html).toContain('<path');
    expect(html).toMatch(/\+\d+%/);
  });

  test('marks the moment a pool runs out', () => {
    const html = renderCard(codex);
    expect(html).toMatch(/Empty \w+ \d+:\d+/);
  });

  test('blurs emails by default', () => {
    const html = renderCard(claude);
    expect(html).toContain('aria-label="Email hidden"');
    expect(usePrivacyStore.getInitialState().showEmails).toBe(false);
  });

  test('drain mode plots burn, with a per-hour axis', () => {
    expect(renderCard(claude, 'drain')).toContain('%/h');
  });
});

describe('thin data', () => {
  const [base] = poolsFor();

  test('no samples explains that recording has started', () => {
    const html = renderCard({
      ...base,
      recorded: [],
      coverage: { firstSampleAt: new Date(NOW - 60_000).toISOString(), samples: 0 },
    });
    expect(html).toContain('Recording started');
    expect(html).toContain('History builds as you use your accounts.');
    expect(html).not.toContain('NaN');
  });

  test('samples outside the range get their own message', () => {
    const html = renderCard({ ...base, recorded: [] });
    expect(html).toContain('No readings in this date range.');
  });

  test('a single sample draws a dot rather than an empty chart', () => {
    const at = new Date(NOW - 3_600_000).toISOString();
    const html = renderCard({
      ...base,
      recorded: [{ at, percent: 500 }],
      coverage: { firstSampleAt: at, samples: 1 },
    });
    expect(html).toContain('<circle');
    expect(html).not.toContain('Recording started');
    expect(html).not.toContain('NaN');
  });

  test('a hole in the data breaks the line instead of bridging it', () => {
    const points = [
      { at: new Date(NOW - 7_200_000).toISOString(), percent: 500 },
      { at: new Date(NOW - 5_400_000).toISOString(), percent: 495 },
      { at: new Date(NOW - 3_600_000).toISOString(), percent: null },
      { at: new Date(NOW - 1_800_000).toISOString(), percent: 480 },
      { at: new Date(NOW - 60_000).toISOString(), percent: 475 },
    ];
    const html = renderToStaticMarkup(
      createElement(PoolChart, {
        pool: { ...base, recorded: points },
        title: 'Claude',
        color: 'red',
        now: NOW,
        from: FROM,
        to: TO,
        mode: 'remaining',
        burnHours: 3,
        futureMultiplier: 1,
      })
    );
    const recordedPaths = html.match(/recorded[^"]*" *d?|d="M[^"]*"/g) ?? [];
    expect(recordedPaths.length).toBeGreaterThan(2);
    expect(html).not.toContain('NaN');
  });

  test('no capacity at all still renders', () => {
    const html = renderCard(normalizePool({ provider: 'claude' }));
    expect(html).toContain('0%');
    expect(html).not.toContain('NaN');
  });

  test('renders at phone width', () => {
    const html = renderToStaticMarkup(
      createElement(PoolChart, {
        pool: base,
        title: 'Claude',
        color: 'red',
        now: NOW,
        from: FROM,
        to: TO,
        mode: 'remaining',
        burnHours: 3,
        futureMultiplier: 1,
        initialWidth: 320,
      })
    );
    expect(html).toContain('width="320"');
    expect(html).not.toContain('NaN');
  });
});

describe('fixtures', () => {
  test('contain reset jumps, a hole, and two weeks of history', () => {
    const [claude] = normalizePoolsResponse(
      quotaPools(NOW, { from: NOW - 14 * DAY, to: TO, burn: '3h', future: 'same' })
    ).pools;
    expect(claude.recorded.some((point) => point.percent === null)).toBe(true);
    expect(claude.resets.some((reset) => !reset.projected)).toBe(true);
    expect(claude.resets.some((reset) => reset.projected)).toBe(true);
    const span = Date.parse(claude.recorded.at(-1)?.at ?? '') - Date.parse(claude.recorded[0].at);
    expect(span).toBeGreaterThan(13 * DAY);
    expect(claude.accounts).toHaveLength(6);
  });

  test('never show a real identity', () => {
    const text = JSON.stringify(quotaPools(NOW, { from: FROM, to: TO, burn: '3h', future: 'same' }));
    const domains = new Set(Array.from(text.matchAll(/@([a-z0-9.-]+)/g), (match) => match[1]));
    expect(Array.from(domains)).toEqual(['example.com']);
  });

  test('No usage removes the empty forecast', () => {
    const [, codex] = poolsFor('idle');
    expect(codex.poolEmptyAt).toBeNull();
    expect(codex.firstEmpty).toBeNull();
  });

  test('account history is well formed', () => {
    const { samples } = accountHistory('claude-alice', NOW, FROM, TO);
    expect(samples.length).toBeGreaterThan(10);
  });
});
