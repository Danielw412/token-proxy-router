import { useTranslation } from 'react-i18next';
import { QuotaBar } from '@/components/tpr/Ui';
import { formatPercentValue, formatWhen } from './chart/chartModel';
import styles from './WindowMeter.module.scss';

export interface WindowMeterProps {
  /** Shown above the bar, e.g. "5-hour". */
  label: string;
  /** Remaining percent of the window, or null when unknown. */
  remaining: number | null;
  resetAt?: string | null;
  color?: string;
  /** Names the meter for screen readers, e.g. "alice@example.com 5-hour". */
  ariaLabel?: string;
}

/** One quota window: label and percent left, a bar, and when it resets. */
export function WindowMeter({ label, remaining, resetAt, color, ariaLabel }: WindowMeterProps) {
  const { t, i18n } = useTranslation();
  const reset = resetAt ? Date.parse(resetAt) : Number.NaN;
  return (
    <div className={styles.meter}>
      <div className={styles.top}>
        <span className={styles.label}>{label}</span>
        <span className={styles.value}>
          {remaining === null ? '—' : formatPercentValue(remaining)}
        </span>
      </div>
      <QuotaBar value={remaining} color={color} label={ariaLabel ?? label} />
      <div className={styles.reset}>
        {Number.isFinite(reset)
          ? t('tpr.quota.resets', { when: formatWhen(reset, i18n.language) })
          : ' '}
      </div>
    </div>
  );
}
