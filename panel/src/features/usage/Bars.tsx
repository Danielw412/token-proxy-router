import styles from './Bars.module.scss';

/** Minimal bar spark chart. The label carries the meaning for screen readers. */
export function Bars({
  values,
  height = 28,
  label,
}: {
  values: number[];
  height?: number;
  label: string;
}) {
  const max = Math.max(1, ...values);
  return (
    <div className={styles.bars} style={{ height }} role="img" aria-label={label}>
      {values.map((value, index) => (
        <span
          key={index}
          className={styles.bar}
          data-zero={value === 0 || undefined}
          style={{ height: `${Math.max(value === 0 ? 2 : 6, (value / max) * 100)}%` }}
        />
      ))}
    </div>
  );
}
