import type { AccountHealth, HealthStatus } from '@/services/api/tpr';

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
