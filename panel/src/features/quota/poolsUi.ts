/** Persisted chart toolbar choices. */

import type { BurnWindow, FutureUsage } from '@/services/api/tpr';
import type { ChartMode } from './chart/PoolChart';

const KEY = 'tpr-quota-pools-ui';

export interface PoolsUi {
  mode: ChartMode;
  burn: BurnWindow;
  future: FutureUsage;
}

const DEFAULTS: PoolsUi = { mode: 'remaining', burn: '3h', future: 'same' };

export const readPoolsUi = (): PoolsUi => {
  try {
    const raw = typeof localStorage === 'undefined' ? null : localStorage.getItem(KEY);
    if (!raw) return DEFAULTS;
    const parsed = JSON.parse(raw) as Partial<PoolsUi>;
    return {
      mode: parsed.mode === 'drain' ? 'drain' : 'remaining',
      burn: parsed.burn === '1h' || parsed.burn === '6h' ? parsed.burn : '3h',
      future: parsed.future === 'idle' || parsed.future === 'double' ? parsed.future : 'same',
    };
  } catch {
    return DEFAULTS;
  }
};

export const writePoolsUi = (value: PoolsUi): void => {
  try {
    localStorage.setItem(KEY, JSON.stringify(value));
  } catch {
    // Preferences are optional.
  }
};
