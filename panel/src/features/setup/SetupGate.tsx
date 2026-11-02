import { useEffect, useState } from 'react';
import { useLocation } from 'react-router-dom';
import { useAuthStore } from '@/stores';
import { tprApi } from '@/services/api/tpr';
import { SetupWizard } from './SetupWizard';

/** Opens the first-run wizard once per sign-in while the server says setup is not complete. */
export function SetupGate() {
  const connected = useAuthStore((state) => state.connectionStatus === 'connected');
  const location = useLocation();
  const [open, setOpen] = useState(false);

  useEffect(() => {
    if (!connected) return;
    let cancelled = false;
    tprApi
      .setup()
      .then((setup) => {
        if (!cancelled && !setup.completed) setOpen(true);
      })
      .catch(() => {
        // An older server has no setup route: nothing to show.
      });
    return () => {
      cancelled = true;
    };
  }, [connected]);

  useEffect(() => {
    if (location.search.includes('wizard=1')) setOpen(true);
  }, [location.search]);

  if (!open) return null;
  return <SetupWizard onClose={() => setOpen(false)} />;
}
