/**
 * Per-tab sign-in record. Refreshing must not log you out even when "Remember me"
 * is off, so the current session always lives in sessionStorage. It disappears with
 * the tab; the remembered login in localStorage is separate.
 */

import { deobfuscateData, isObfuscated, obfuscateData } from '@/utils/encryption';

export const AUTH_SESSION_KEY = 'tpr-session';

export interface AuthSession {
  apiBase: string;
  managementKey: string;
  /** Signed in by Tailscale identity: there is no key to send. */
  identity: boolean;
}

type SessionStore = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;

const defaultStore = (): SessionStore | null => {
  try {
    return typeof sessionStorage === 'undefined' ? null : sessionStorage;
  } catch {
    return null;
  }
};

export const writeAuthSession = (session: AuthSession, store = defaultStore()): void => {
  try {
    store?.setItem(AUTH_SESSION_KEY, obfuscateData(JSON.stringify(session)));
  } catch {
    // Storage can be full or blocked; the session then simply does not survive a refresh.
  }
};

export const clearAuthSession = (store = defaultStore()): void => {
  try {
    store?.removeItem(AUTH_SESSION_KEY);
  } catch {
    // Nothing to clear when storage is blocked.
  }
};

export const readAuthSession = (store = defaultStore()): AuthSession | null => {
  try {
    const raw = store?.getItem(AUTH_SESSION_KEY);
    if (!raw || !isObfuscated(raw)) return null;
    const parsed: unknown = JSON.parse(deobfuscateData(raw));
    if (!parsed || typeof parsed !== 'object') return null;
    const { apiBase, managementKey, identity } = parsed as Partial<AuthSession>;
    if (typeof apiBase !== 'string' || !apiBase) return null;
    const session: AuthSession = {
      apiBase,
      managementKey: typeof managementKey === 'string' ? managementKey : '',
      identity: identity === true,
    };
    return session.identity || session.managementKey ? session : null;
  } catch {
    return null;
  }
};
