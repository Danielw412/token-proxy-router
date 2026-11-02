import { afterEach, beforeEach, describe, expect, test } from 'bun:test';
import { apiClient } from '@/services/api/client';
import { obfuscatedStorage } from '@/services/storage/secureStorage';
import {
  AUTH_SESSION_KEY,
  clearAuthSession,
  readAuthSession,
  writeAuthSession,
} from '@/stores/authSession';
import { useAuthStore } from '@/stores/useAuthStore';
import { useConfigStore } from '@/stores/useConfigStore';
import { STORAGE_KEY_AUTH } from '@/utils/constants';

const makeStorage = () => {
  const values = new Map<string, string>();
  return {
    values,
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => void values.set(key, value),
    removeItem: (key: string) => void values.delete(key),
  };
};

const descriptors = {
  local: Object.getOwnPropertyDescriptor(globalThis, 'localStorage'),
  session: Object.getOwnPropertyDescriptor(globalThis, 'sessionStorage'),
};
const restore = (name: string, descriptor?: PropertyDescriptor) => {
  if (descriptor) Object.defineProperty(globalThis, name, descriptor);
  else Reflect.deleteProperty(globalThis, name);
};

let local: ReturnType<typeof makeStorage>;
let session: ReturnType<typeof makeStorage>;

beforeEach(() => {
  local = makeStorage();
  session = makeStorage();
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: local });
  Object.defineProperty(globalThis, 'sessionStorage', { configurable: true, value: session });
  useAuthStore.setState({
    isAuthenticated: false,
    apiBase: '',
    managementKey: '',
    rememberPassword: true,
    connectionStatus: 'disconnected',
  });
});

afterEach(() => {
  restore('localStorage', descriptors.local);
  restore('sessionStorage', descriptors.session);
});

describe('session record', () => {
  test('round trips, and is not stored in the clear', () => {
    writeAuthSession({ apiBase: 'http://hub.example', managementKey: 'test-key', identity: false });
    expect(session.values.get(AUTH_SESSION_KEY)).not.toContain('test-key');
    expect(readAuthSession()).toEqual({
      apiBase: 'http://hub.example',
      managementKey: 'test-key',
      identity: false,
    });
    clearAuthSession();
    expect(readAuthSession()).toBeNull();
  });

  test('rejects garbage and records with nothing to authenticate with', () => {
    session.values.set(AUTH_SESSION_KEY, 'not obfuscated');
    expect(readAuthSession()).toBeNull();
    writeAuthSession({ apiBase: 'http://hub.example', managementKey: '', identity: false });
    expect(readAuthSession()).toBeNull();
    writeAuthSession({ apiBase: '', managementKey: 'test-key', identity: false });
    expect(readAuthSession()).toBeNull();
  });

  test('accepts an identity session with no key', () => {
    writeAuthSession({ apiBase: 'http://hub.example', managementKey: '', identity: true });
    expect(readAuthSession()?.identity).toBe(true);
  });

  test('works where sessionStorage does not exist', () => {
    Reflect.deleteProperty(globalThis, 'sessionStorage');
    expect(readAuthSession()).toBeNull();
    expect(() => writeAuthSession({ apiBase: 'a', managementKey: 'b', identity: false })).not.toThrow();
    expect(() => clearAuthSession()).not.toThrow();
  });
});

describe('signing in without Remember me', () => {
  const stubConfig = () => {
    const original = useConfigStore.getState().fetchConfig;
    useConfigStore.setState({ fetchConfig: (async () => ({})) as never });
    return () => useConfigStore.setState({ fetchConfig: original });
  };

  test('Remember me is the default', () => {
    expect(useAuthStore.getInitialState().rememberPassword).toBe(true);
  });

  test('survives a refresh through sessionStorage, never writing the key to localStorage', async () => {
    const restoreConfig = stubConfig();
    try {
      await useAuthStore.getState().login({
        apiBase: 'http://hub.example',
        managementKey: 'test-key',
        rememberPassword: false,
      });
      const saved = obfuscatedStorage.getItem<{ state: { managementKey?: string } }>(STORAGE_KEY_AUTH);
      expect(saved?.state.managementKey).toBeUndefined();
      expect(local.values.has('isLoggedIn')).toBe(false);
      expect(readAuthSession()?.managementKey).toBe('test-key');

      // What a reload sees: an empty store, with the tab's session still there.
      useAuthStore.setState({ isAuthenticated: false, managementKey: '', apiBase: '' });
      expect(useAuthStore.getState().hasRestorableSession()).toBe(true);
    } finally {
      restoreConfig();
    }
  });

  test('Remember me still persists across tabs', async () => {
    const restoreConfig = stubConfig();
    try {
      await useAuthStore.getState().login({
        apiBase: 'http://hub.example',
        managementKey: 'test-key',
        rememberPassword: true,
      });
      expect(local.values.get('isLoggedIn')).toBe('true');
      const saved = obfuscatedStorage.getItem<{ state: { managementKey?: string } }>(STORAGE_KEY_AUTH);
      expect(saved?.state.managementKey).toBe('test-key');
    } finally {
      restoreConfig();
    }
  });

  test('identity sign-in sends no key and remembers nothing', async () => {
    const restoreConfig = stubConfig();
    try {
      await useAuthStore.getState().login({
        apiBase: 'http://hub.example',
        managementKey: '',
        identity: true,
      });
      expect(local.values.has('isLoggedIn')).toBe(false);
      expect(readAuthSession()?.identity).toBe(true);
    } finally {
      restoreConfig();
    }
  });

  test('logging out clears the session', async () => {
    writeAuthSession({ apiBase: 'http://hub.example', managementKey: 'test-key', identity: false });
    useAuthStore.getState().logout();
    expect(readAuthSession()).toBeNull();
    expect(useAuthStore.getState().hasRestorableSession()).toBe(false);
    apiClient.setConfig({ apiBase: '', managementKey: '' });
  });
});
