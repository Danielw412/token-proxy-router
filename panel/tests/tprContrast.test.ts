import { describe, expect, test } from 'bun:test';

const css = await Bun.file('src/styles/themes.scss').text();

const block = (selector: string): Record<string, string> => {
  const start = css.indexOf(selector);
  if (start < 0) throw new Error(`missing ${selector}`);
  const open = css.indexOf('{', start);
  const close = css.indexOf('\n}', open);
  const tokens: Record<string, string> = {};
  for (const match of css.slice(open, close).matchAll(/(--[a-z0-9-]+):\s*(#[0-9a-f]{6})\s*;/gi)) {
    tokens[match[1]] = match[2];
  }
  return tokens;
};

const luminance = (hex: string): number => {
  const channel = (start: number) => {
    const value = Number.parseInt(hex.slice(start, start + 2), 16) / 255;
    return value <= 0.03928 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(1) + 0.7152 * channel(3) + 0.0722 * channel(5);
};

const contrast = (a: string, b: string): number => {
  const [light, dark] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (light + 0.05) / (dark + 0.05);
};

describe.each([
  ['light', ':root,'],
  ['dark', "[data-theme='dark']"],
])('%s theme contrast', (_name, selector) => {
  const tokens = block(selector);
  const surfaces = ['--bg-primary', '--bg-secondary', '--bg-tertiary'];

  test.each(['--text-primary', '--text-secondary', '--text-tertiary'])('%s meets AA text', (name) => {
    for (const surface of surfaces) {
      expect(contrast(tokens[name], tokens[surface])).toBeGreaterThanOrEqual(4.5);
    }
  });

  test('the primary button is readable', () => {
    expect(contrast(tokens['--primary-contrast'], tokens['--primary-color'])).toBeGreaterThanOrEqual(7);
  });

  test.each(['--success-color', '--warning-text', '--error-color'])('%s reads on a card', (name) => {
    expect(contrast(tokens[name], tokens['--bg-primary'])).toBeGreaterThanOrEqual(4.5);
  });

  test.each(['--provider-claude', '--provider-codex', '--focus-ring'])(
    '%s stands out as a graphic against cards',
    (name) => {
      expect(contrast(tokens[name], tokens['--bg-primary'])).toBeGreaterThanOrEqual(3);
    }
  );
});
