import { useCallback, useEffect, useRef, useState } from 'react';
import { useAuthStore } from '@/stores';
import { getErrorMessage } from '@/utils/helpers';

export interface TprResource<T> {
  data: T | null;
  loading: boolean;
  /** Empty when fine. A failed background refresh keeps the last good data. */
  error: string;
  /** HTTP status of the last failure; 404 means this server build lacks the route. */
  errorStatus: number | null;
  refresh: () => Promise<void>;
  setData: (next: T) => void;
}

/**
 * Loads one management API read with stale-response protection, optional polling, and a
 * pause while the tab is hidden. Late responses can never overwrite a newer request.
 */
export function useTprResource<T>(
  fetcher: () => Promise<T>,
  deps: ReadonlyArray<unknown>,
  options: { intervalMs?: number; enabled?: boolean } = {}
): TprResource<T> {
  const { intervalMs, enabled = true } = options;
  const connected = useAuthStore((state) => state.connectionStatus === 'connected');
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [errorStatus, setErrorStatus] = useState<number | null>(null);
  const requestRef = useRef(0);
  const fetcherRef = useRef(fetcher);
  useEffect(() => {
    fetcherRef.current = fetcher;
  });

  const refresh = useCallback(async () => {
    const id = ++requestRef.current;
    try {
      const next = await fetcherRef.current();
      if (id !== requestRef.current) return;
      setData(next);
      setError('');
      setErrorStatus(null);
    } catch (err) {
      if (id !== requestRef.current) return;
      setError(getErrorMessage(err));
      const status = (err as { status?: unknown })?.status;
      setErrorStatus(typeof status === 'number' ? status : null);
    } finally {
      if (id === requestRef.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!enabled || !connected) {
      setLoading(false);
      return;
    }
    setLoading(true);
    void refresh();
    return () => {
      requestRef.current += 1;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connected, enabled, refresh, ...deps]);

  useEffect(() => {
    if (!intervalMs || !enabled || !connected) return;
    const timer = window.setInterval(() => {
      if (typeof document === 'undefined' || document.visibilityState === 'visible') {
        void refresh();
      }
    }, intervalMs);
    return () => window.clearInterval(timer);
  }, [connected, enabled, intervalMs, refresh]);

  return { data, loading, error, errorStatus, refresh, setData };
}
