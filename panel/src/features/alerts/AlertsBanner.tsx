import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useLocation } from 'react-router-dom';
import { IconAlertTriangle, IconX } from '@/components/ui/icons';
import { PrivateText } from '@/components/tpr/Ui';
import { useTprResource } from '@/hooks/useTprResource';
import { tprApi } from '@/services/api/tpr';
import styles from './AlertsBanner.module.scss';

const REFRESH_MS = 60_000;

/** Active alerts, on every page but Alerts itself. Dismissal lasts until a new alert appears. */
export function AlertsBanner() {
  const { t } = useTranslation();
  const location = useLocation();
  const { data } = useTprResource(() => tprApi.alertEvents(20), [], { intervalMs: REFRESH_MS });
  const [dismissed, setDismissed] = useState('');

  const active = (data ?? []).filter((event) => event.active);
  const fingerprint = active.map((event) => `${event.at}:${event.kind}`).join('|');
  if (active.length === 0 || dismissed === fingerprint || location.pathname === '/alerts') {
    return null;
  }

  return (
    <div className={styles.banner} role="status">
      <IconAlertTriangle size={15} className={styles.icon} />
      <span className={styles.text}>
        <PrivateText text={active[0].message} />
        {active.length > 1 ? (
          <span className={styles.more}>
            {' '}
            {t('tpr.alerts.banner_more', { count: active.length - 1 })}
          </span>
        ) : null}
      </span>
      <Link className={styles.link} to="/alerts">
        {t('tpr.alerts.banner_view')}
      </Link>
      <button
        type="button"
        className={styles.close}
        aria-label={t('tpr.alerts.banner_dismiss')}
        onClick={() => setDismissed(fingerprint)}
      >
        <IconX size={14} />
      </button>
    </div>
  );
}
