/**
 * Small shared kit for Token Proxy Router pages: compact page header, segmented
 * controls, pills, badges, stats, quota bars, and privacy-aware email text.
 */

import {
  useEffect,
  useId,
  useRef,
  useState,
  type PropsWithChildren,
  type ReactNode,
} from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { IconCheck, IconChevronDown, IconCopy } from '@/components/ui/icons';
import { getAuthFileIcon, getTypeLabel } from '@/features/authFiles/constants';
import { usePrivacyStore, useNotificationStore, useThemeStore } from '@/stores';
import { copyToClipboard } from '@/utils/clipboard';
import styles from './Ui.module.scss';

/* ----------------------------- page structure ------------------------------ */

export function PageHeader({
  title,
  meta,
  actions,
}: {
  title: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className={styles.pageHeader}>
      <div className={styles.pageHeading}>
        <h1 className={styles.pageTitle}>{title}</h1>
        {meta ? <p className={styles.pageMeta}>{meta}</p> : null}
      </div>
      {actions ? <div className={styles.pageActions}>{actions}</div> : null}
    </header>
  );
}

export function Page({ children, className }: PropsWithChildren<{ className?: string }>) {
  return <div className={[styles.page, className].filter(Boolean).join(' ')}>{children}</div>;
}

export function Panel({
  title,
  description,
  actions,
  children,
  className,
}: PropsWithChildren<{
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  className?: string;
}>) {
  return (
    <section className={[styles.panel, className].filter(Boolean).join(' ')}>
      {title || actions ? (
        <div className={styles.panelHeader}>
          <div className={styles.panelHeading}>
            {title ? <h2 className={styles.panelTitle}>{title}</h2> : null}
            {description ? <p className={styles.panelDescription}>{description}</p> : null}
          </div>
          {actions ? <div className={styles.panelActions}>{actions}</div> : null}
        </div>
      ) : null}
      {children}
    </section>
  );
}

export function Stat({
  label,
  value,
  sub,
  tone,
}: {
  label: ReactNode;
  value: ReactNode;
  sub?: ReactNode;
  tone?: 'good' | 'warn' | 'bad';
}) {
  return (
    <div className={styles.stat}>
      <div className={styles.statLabel}>{label}</div>
      <div className={styles.statValue} data-tone={tone}>
        {value}
      </div>
      {sub ? <div className={styles.statSub}>{sub}</div> : null}
    </div>
  );
}

export function Note({
  tone = 'info',
  children,
  actions,
}: PropsWithChildren<{ tone?: 'info' | 'warn' | 'error' | 'good'; actions?: ReactNode }>) {
  return (
    <div className={styles.note} data-tone={tone} role={tone === 'error' ? 'alert' : undefined}>
      <div className={styles.noteBody}>{children}</div>
      {actions ? <div className={styles.noteActions}>{actions}</div> : null}
    </div>
  );
}

export function Empty({
  title,
  description,
  action,
}: {
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className={styles.empty}>
      <div className={styles.emptyTitle}>{title}</div>
      {description ? <div className={styles.emptyDescription}>{description}</div> : null}
      {action ? <div className={styles.emptyAction}>{action}</div> : null}
    </div>
  );
}

/** Shown when a route is missing, so an older server reads as such rather than as a crash. */
export function ResourceError({
  message,
  status,
  onRetry,
}: {
  message: string;
  status: number | null;
  onRetry?: () => void;
}) {
  const { t } = useTranslation();
  return (
    <Note
      tone="error"
      actions={
        onRetry ? (
          <Button variant="secondary" size="sm" onClick={onRetry}>
            {t('tpr.common.retry')}
          </Button>
        ) : undefined
      }
    >
      {status === 404 ? t('tpr.common.route_missing') : message}
    </Note>
  );
}

/* --------------------------------- controls -------------------------------- */

export interface SegmentOption<T extends string> {
  value: T;
  label: ReactNode;
}

export function Segmented<T extends string>({
  options,
  value,
  onChange,
  ariaLabel,
}: {
  options: ReadonlyArray<SegmentOption<T>>;
  value: T;
  onChange: (value: T) => void;
  ariaLabel: string;
}) {
  return (
    <div className={styles.segmented} role="radiogroup" aria-label={ariaLabel}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          role="radio"
          aria-checked={option.value === value}
          className={styles.segment}
          data-active={option.value === value || undefined}
          onClick={() => onChange(option.value)}
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}

export function ToolbarLabel({ children }: PropsWithChildren) {
  return <span className={styles.toolbarLabel}>{children}</span>;
}

export function ProviderIcon({ provider, size = 16 }: { provider: string; size?: number }) {
  const theme = useThemeStore((state) => state.resolvedTheme);
  const src = getAuthFileIcon(provider === 'anthropic' ? 'claude' : provider, theme);
  if (!src) return null;
  return <img src={src} alt="" width={size} height={size} className={styles.providerIcon} />;
}

export function ProviderName({ provider }: { provider: string }) {
  const { t } = useTranslation();
  return <>{getTypeLabel(t, provider === 'anthropic' ? 'claude' : provider)}</>;
}

export interface PillOption {
  id: string;
  label: ReactNode;
  icon?: ReactNode;
  count?: number;
}

export function Pills({
  options,
  active,
  onChange,
  ariaLabel,
}: {
  options: PillOption[];
  active: string;
  onChange: (id: string) => void;
  ariaLabel: string;
}) {
  return (
    <div className={styles.pills} role="tablist" aria-label={ariaLabel}>
      {options.map((option) => (
        <button
          key={option.id}
          type="button"
          role="tab"
          aria-selected={option.id === active}
          className={styles.pill}
          data-active={option.id === active || undefined}
          onClick={() => onChange(option.id)}
        >
          {option.icon}
          <span>{option.label}</span>
          {typeof option.count === 'number' ? (
            <span className={styles.pillCount}>{option.count}</span>
          ) : null}
        </button>
      ))}
    </div>
  );
}

export function Badge({
  tone = 'neutral',
  children,
}: PropsWithChildren<{ tone?: 'neutral' | 'good' | 'warn' | 'bad' | 'info' }>) {
  return (
    <span className={styles.badge} data-tone={tone}>
      {children}
    </span>
  );
}

export function QuotaBar({
  value,
  label,
  color,
}: {
  value: number | null;
  label?: string;
  color?: string;
}) {
  const safe = value === null ? 0 : Math.min(100, Math.max(0, value));
  const tone = value === null ? 'none' : safe <= 10 ? 'bad' : safe <= 25 ? 'warn' : 'ok';
  return (
    <div
      className={styles.quotaBar}
      role="meter"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={value === null ? undefined : Math.round(safe)}
      aria-label={label}
    >
      <div
        className={styles.quotaFill}
        data-tone={tone}
        style={{ width: `${safe}%`, ...(color && tone === 'ok' ? { background: color } : {}) }}
      />
    </div>
  );
}

export interface MultiSelectOption {
  value: string;
  label: ReactNode;
}

/** Compact checklist popover. An empty selection means "all". */
export function MultiSelect({
  label,
  options,
  selected,
  onChange,
  summary,
}: {
  label: string;
  options: MultiSelectOption[];
  selected: ReadonlySet<string>;
  onChange: (next: Set<string>) => void;
  summary?: string;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement | null>(null);
  const menuId = useId();

  useEffect(() => {
    if (!open) return;
    const onPointer = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', onPointer);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onPointer);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  return (
    <div className={styles.multi} ref={rootRef}>
      <button
        type="button"
        className={styles.toolbarButton}
        aria-haspopup="true"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => setOpen((value) => !value)}
      >
        {summary ?? label}
        <IconChevronDown size={14} />
      </button>
      {open ? (
        <div className={styles.multiMenu} id={menuId} role="group" aria-label={label}>
          <button
            type="button"
            className={styles.multiClear}
            onClick={() => onChange(new Set())}
            disabled={selected.size === 0}
          >
            {t('tpr.common.all_accounts')}
          </button>
          {options.map((option) => {
            const checked = selected.has(option.value);
            return (
              <label key={option.value} className={styles.multiOption}>
                <input
                  type="checkbox"
                  checked={checked}
                  onChange={() => {
                    const next = new Set(selected);
                    if (checked) next.delete(option.value);
                    else next.add(option.value);
                    onChange(next);
                  }}
                />
                <span className={styles.multiLabel}>{option.label}</span>
              </label>
            );
          })}
        </div>
      ) : null}
    </div>
  );
}

/* --------------------------------- privacy --------------------------------- */

const EMAIL_PATTERN = /[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g;

/** Renders one email, blurred until "Show emails" is on. */
export function PrivateEmail({ value, fallback }: { value: string; fallback?: ReactNode }) {
  const { t } = useTranslation();
  const show = usePrivacyStore((state) => state.showEmails);
  if (!value) return <>{fallback ?? null}</>;
  if (show) return <span>{value}</span>;
  return (
    <span className={styles.blurred} aria-label={t('tpr.common.email_hidden')} role="img">
      <span aria-hidden="true">{value}</span>
    </span>
  );
}

export const splitEmails = (text: string): Array<{ text: string; email: boolean }> => {
  const parts: Array<{ text: string; email: boolean }> = [];
  let last = 0;
  for (const match of text.matchAll(EMAIL_PATTERN)) {
    const start = match.index ?? 0;
    if (start > last) parts.push({ text: text.slice(last, start), email: false });
    parts.push({ text: match[0], email: true });
    last = start + match[0].length;
  }
  if (last < text.length) parts.push({ text: text.slice(last), email: false });
  return parts;
};

/** Plain text in which any email found is treated like PrivateEmail, such as file names. */
export function PrivateText({ text }: { text: string }) {
  return (
    <>
      {splitEmails(text).map((part, index) =>
        part.email ? <PrivateEmail key={index} value={part.text} /> : <span key={index}>{part.text}</span>
      )}
    </>
  );
}

/* ----------------------------------- code ---------------------------------- */

export function CopyButton({ text, label }: { text: string; label?: string }) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 1600);
    return () => window.clearTimeout(timer);
  }, [copied]);
  return (
    <Button
      variant="secondary"
      size="sm"
      onClick={() => {
        void copyToClipboard(text).then((ok) => {
          if (ok) setCopied(true);
          else showNotification(t('tpr.common.copy_failed'), 'error');
        });
      }}
    >
      {copied ? <IconCheck size={13} /> : <IconCopy size={13} />}
      {copied ? t('tpr.common.copied') : (label ?? t('common.copy'))}
    </Button>
  );
}

export function CodeBlock({ code, label }: { code: string; label?: string }) {
  return (
    <div className={styles.code}>
      {label ? <div className={styles.codeLabel}>{label}</div> : null}
      <div className={styles.codeBody}>
        <pre tabIndex={0}>{code}</pre>
        <div className={styles.codeCopy}>
          <CopyButton text={code} />
        </div>
      </div>
    </div>
  );
}
