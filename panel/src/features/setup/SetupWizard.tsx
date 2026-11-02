import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import { IconCheck } from '@/components/ui/icons';
import { Badge, PrivateEmail, ProviderIcon, ProviderName, ResourceError } from '@/components/tpr/Ui';
import { useAccountLogin } from '@/features/accounts/useAccountLogin';
import { ConnectPanel } from '@/features/connect/ConnectPanel';
import { useTprResource } from '@/hooks/useTprResource';
import { TailnetToggle } from '@/pages/SystemPage';
import { tprApi } from '@/services/api/tpr';
import { useNotificationStore } from '@/stores';
import { getErrorMessage } from '@/utils/helpers';
import styles from './SetupWizard.module.scss';

const STEP_KEYS = ['accounts', 'tailnet', 'connect'] as const;

export function SetupWizard({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const [step, setStep] = useState(0);
  const [finishing, setFinishing] = useState(false);
  const health = useTprResource(() => tprApi.health(), [], { intervalMs: 5_000 });
  const tailscale = useTprResource(() => tprApi.tailscale(), []);
  const login = useAccountLogin({ onAdded: () => void health.refresh() });
  const accounts = health.data ?? [];

  /** Skipping also records completion: Quick Start stays available, so the wizard should not nag. */
  const finish = async () => {
    setFinishing(true);
    try {
      await tprApi.completeSetup();
    } catch (err) {
      showNotification(getErrorMessage(err, t('common.unknown_error')), 'warning');
    } finally {
      setFinishing(false);
      onClose();
    }
  };

  const last = step === STEP_KEYS.length - 1;

  return (
    <Modal
      open
      onClose={() => void finish()}
      width={640}
      title={t('tpr.wizard.title')}
      footer={
        <div className={styles.footer}>
          <Button variant="ghost" onClick={() => void finish()} disabled={finishing}>
            {t('tpr.wizard.skip')}
          </Button>
          <div className={styles.footerRight}>
            {step > 0 && (
              <Button variant="secondary" onClick={() => setStep(step - 1)}>
                {t('tpr.wizard.back')}
              </Button>
            )}
            {last ? (
              <Button onClick={() => void finish()} loading={finishing}>
                {t('tpr.wizard.finish')}
              </Button>
            ) : (
              <Button onClick={() => setStep(step + 1)}>{t('tpr.wizard.next')}</Button>
            )}
          </div>
        </div>
      }
    >
      <ol className={styles.progress} aria-label={t('tpr.wizard.progress', { step: step + 1, total: STEP_KEYS.length })}>
        {STEP_KEYS.map((key, index) => (
          <li
            key={key}
            className={styles.progressItem}
            data-state={index < step ? 'done' : index === step ? 'current' : undefined}
            aria-current={index === step ? 'step' : undefined}
          >
            <span className={styles.progressMark} aria-hidden="true">
              {index < step ? <IconCheck size={11} /> : index + 1}
            </span>
            <span className={styles.progressLabel}>{t(`tpr.wizard.step_${key}`)}</span>
          </li>
        ))}
      </ol>

      {step === 0 && (
        <div className={styles.body}>
          <p className={styles.lead}>{t('tpr.wizard.accounts_lead')}</p>
          <div className={styles.tiles}>
            {['claude', 'codex'].map((provider) => (
              <button
                key={provider}
                type="button"
                className={styles.tile}
                aria-haspopup="dialog"
                onClick={() => login.open(provider)}
              >
                <ProviderIcon provider={provider} size={22} />
                <span className={styles.tileName}>
                  <ProviderName provider={provider} />
                </span>
                <span className={styles.tileHint}>{t('tpr.wizard.signin_hint')}</span>
              </button>
            ))}
          </div>
          {accounts.length > 0 && (
            <ul className={styles.added}>
              {accounts.map((account) => (
                <li key={account.id}>
                  <ProviderIcon provider={account.provider} size={14} />
                  <PrivateEmail value={account.email} fallback={account.label || account.id} />
                  <Badge tone="good">{t('tpr.wizard.added')}</Badge>
                </li>
              ))}
            </ul>
          )}
          <p className={styles.note}>{t('tpr.wizard.accounts_note')}</p>
        </div>
      )}

      {step === 1 && (
        <div className={styles.body}>
          <p className={styles.lead}>{t('tpr.wizard.tailnet_lead')}</p>
          {tailscale.error ? (
            <ResourceError
              message={tailscale.error}
              status={tailscale.errorStatus}
              onRetry={() => void tailscale.refresh()}
            />
          ) : tailscale.data ? (
            <TailnetToggle
              data={tailscale.data}
              refresh={tailscale.refresh}
              onError={(err) =>
                showNotification(getErrorMessage(err, t('common.unknown_error')), 'error')
              }
            />
          ) : (
            <p className={styles.note}>{t('common.loading')}</p>
          )}
        </div>
      )}

      {step === 2 && (
        <div className={styles.body}>
          <p className={styles.lead}>{t('tpr.wizard.connect_lead')}</p>
          <ConnectPanel />
        </div>
      )}

      {login.dialog}
    </Modal>
  );
}
