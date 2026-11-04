import type { RouterPool } from '@/services/api/tpr';

export interface SessionCounts {
  /** Sent a request in the last few minutes. */
  running: number;
  /** Not working now, but their prompt cache is still warm. */
  idle: number;
  /** Idle past the cache window. The router remembers them for a day. */
  cold: number;
}

/**
 * Splits a pool's sessions by state. The server's `session_count` includes every session it
 * remembers, cold ones included, so it overstates what is running.
 */
export const sessionCounts = (pool: RouterPool): SessionCounts => {
  const running = pool.accounts.reduce((sum, account) => sum + account.busySessions, 0);
  const idle = pool.accounts.reduce((sum, account) => sum + account.idleSessions, 0);
  return { running, idle, cold: Math.max(0, pool.sessionCount - running - idle) };
};
