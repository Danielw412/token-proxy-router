import type { AccountHealth } from '@/services/api/tpr';

export const recentFailures = (accounts: AccountHealth[], limit = 6): AccountHealth[] =>
  accounts
    .filter((account) => account.lastError && account.lastErrorAt)
    .sort((a, b) => Date.parse(b.lastErrorAt ?? '') - Date.parse(a.lastErrorAt ?? ''))
    .slice(0, limit);
