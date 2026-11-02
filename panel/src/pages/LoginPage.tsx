import { useCallback, useEffect, useMemo, useState } from 'react';
import { Navigate, useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import { BRAND_LOGO, BRAND_NAME } from '@/assets/logoInline';
import { useAuthStore, useLanguageStore, useNotificationStore } from '@/stores';
import { tprPublicApi, type TprPublicSession } from '@/services/api/tpr';
import { detectApiBaseFromLocation, normalizeApiBase } from '@/utils/connection';
import { LANGUAGE_LABEL_KEYS, LANGUAGE_ORDER } from '@/utils/constants';
import { isSupportedLanguage } from '@/utils/language';
import type { ApiError } from '@/types';
import { LegacyBackendError } from '@/services/api/legacyBackendProbe';
import { LoginForm } from './login/LoginForm';
import { resolveLoginRedirect } from '@/router/loginRedirect';
import styles from './LoginPage.module.scss';

export type LoginPhase = 'idle' | 'connecting' | 'success';

const LANGUAGE_DROPDOWN_MIN_WIDTH = 180;

/** Turns an API error into a friendly localized message. */
function getLocalizedErrorMessage(error: unknown, t: (key: string) => string): string {
  if (error instanceof LegacyBackendError) return t('login.error_legacy_backend');
  const apiError = error as Partial<ApiError>;
  const status = typeof apiError.status === 'number' ? apiError.status : undefined;
  const code = typeof apiError.code === 'string' ? apiError.code : undefined;
  const message =
    error instanceof Error
      ? error.message
      : typeof apiError.message === 'string'
        ? apiError.message
        : typeof error === 'string'
          ? error
          : '';

  const withHttpStatus = (summary: string) => {
    if (!status) {
      return summary;
    }

    const genericAxiosMessage = `Request failed with status code ${status}`;
    const detail = message.trim();
    const backendDetail =
      detail && detail !== genericAxiosMessage
        ? ` (${t('login.error_backend_detail')}: ${detail})`
        : '';

    return `HTTP ${status}: ${summary}${backendDetail}`;
  };

  if (status === 401) return withHttpStatus(t('login.error_unauthorized'));
  if (status === 403) return withHttpStatus(t('login.error_forbidden'));
  if (status === 404) return withHttpStatus(t('login.error_not_found'));
  if (status && status >= 500) return withHttpStatus(t('login.error_server'));

  if (code === 'ECONNABORTED' || message.toLowerCase().includes('timeout')) {
    return t('login.error_timeout');
  }
  if (code === 'ERR_NETWORK' || message.toLowerCase().includes('network error')) {
    return t('login.error_network');
  }
  if (code === 'ERR_CERT_AUTHORITY_INVALID' || message.toLowerCase().includes('certificate')) {
    return t('login.error_ssl');
  }
  if (message.toLowerCase().includes('cors') || message.toLowerCase().includes('cross-origin')) {
    return t('login.error_cors');
  }

  return withHttpStatus(t('login.error_invalid'));
}

export function LoginPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const { showNotification } = useNotificationStore();
  const language = useLanguageStore((state) => state.language);
  const setLanguage = useLanguageStore((state) => state.setLanguage);
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);
  const login = useAuthStore((state) => state.login);
  const restoreSession = useAuthStore((state) => state.restoreSession);

  const detectedBase = useMemo(() => detectApiBaseFromLocation(), []);
  const [apiBase, setApiBase] = useState(() => useAuthStore.getState().apiBase || detectedBase);
  const [managementKey, setManagementKey] = useState(
    () => useAuthStore.getState().managementKey || ''
  );
  const [showAdvanced, setShowAdvanced] = useState(() => {
    const stored = useAuthStore.getState().apiBase;
    return Boolean(stored) && normalizeApiBase(stored) !== detectedBase;
  });
  const [showKey, setShowKey] = useState(false);
  const [rememberPassword, setRememberPassword] = useState(
    () => useAuthStore.getState().rememberPassword
  );
  const [phase, setPhase] = useState<LoginPhase>('idle');
  const [restoring, setRestoring] = useState(() => useAuthStore.getState().hasRestorableSession());
  const [error, setError] = useState('');
  const [identity, setIdentity] = useState<TprPublicSession | null>(null);

  const redirectTarget = resolveLoginRedirect(location.state);

  const languageOptions = useMemo(
    () =>
      LANGUAGE_ORDER.map((lang) => ({
        value: lang,
        label: t(LANGUAGE_LABEL_KEYS[lang]),
      })),
    [t]
  );
  const handleLanguageChange = useCallback(
    (selectedLanguage: string) => {
      if (isSupportedLanguage(selectedLanguage)) setLanguage(selectedLanguage);
    },
    [setLanguage]
  );

  useEffect(() => {
    let cancelled = false;
    const init = async () => {
      try {
        const restored = await restoreSession();
        if (cancelled) return;
        if (restored) {
          navigate(redirectTarget, { replace: true });
          return;
        }
        // A rejected restore logs out and clears storage: keep what the form already
        // holds so a corrected key can be retried without retyping the address.
        const stored = useAuthStore.getState();
        setApiBase((previous) => stored.apiBase || previous || detectedBase);
        setManagementKey((previous) => stored.managementKey || previous);
      } finally {
        if (!cancelled) setRestoring(false);
      }
    };
    void init();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    let cancelled = false;
    void tprPublicApi.session(detectedBase).then((session) => {
      if (!cancelled) setIdentity(session);
    });
    return () => {
      cancelled = true;
    };
  }, [detectedBase]);

  const finishLogin = useCallback(
    async (attempt: () => Promise<void>) => {
      if (phase !== 'idle') return;
      setPhase('connecting');
      setError('');
      try {
        await attempt();
      } catch (err: unknown) {
        const message = getLocalizedErrorMessage(err, t);
        setPhase('idle');
        setError(message);
        showNotification(`${t('notification.login_failed')}: ${message}`, 'error');
        return;
      }
      setPhase('success');
      navigate(redirectTarget, { replace: true });
    },
    [navigate, phase, redirectTarget, showNotification, t]
  );

  const handleSubmit = useCallback(() => {
    if (!managementKey.trim()) {
      setError(t('login.error_required'));
      return;
    }
    const baseToUse = apiBase ? normalizeApiBase(apiBase) : detectedBase;
    void finishLogin(() =>
      login({ apiBase: baseToUse, managementKey: managementKey.trim(), rememberPassword })
    );
  }, [apiBase, detectedBase, finishLogin, login, managementKey, rememberPassword, t]);

  const handleIdentityLogin = useCallback(() => {
    void finishLogin(() => login({ apiBase: detectedBase, managementKey: '', identity: true }));
  }, [detectedBase, finishLogin, login]);

  if (isAuthenticated && !restoring && phase === 'idle') {
    return <Navigate to={redirectTarget} replace />;
  }

  const loading = phase === 'connecting';
  const identityLogin = identity?.identity_login ? identity.login : '';

  return (
    <div className={styles.container}>
      <header className={styles.topBar}>
        {!restoring && (
          <Select
            className={styles.languageSelect}
            dropdownMinWidth={LANGUAGE_DROPDOWN_MIN_WIDTH}
            value={language}
            options={languageOptions}
            onChange={handleLanguageChange}
            fullWidth={false}
            size="sm"
            ariaLabel={t('language.switch')}
          />
        )}
      </header>
      <main className={styles.main}>
        {restoring ? (
          <div className={styles.splash} role="status" aria-live="polite">
            <img src={BRAND_LOGO} alt="" className={styles.splashLogo} />
            {t('tpr.login.restoring')}
          </div>
        ) : (
          <div className={styles.column}>
            <div className={styles.brand}>
              <img src={BRAND_LOGO} alt="" className={styles.brandLogo} />
              <div>
                <h1 className={styles.title}>{BRAND_NAME}</h1>
                <p className={styles.subtitle}>{t('tpr.login.subtitle')}</p>
              </div>
            </div>
            {identityLogin && (
              <div className={styles.identity}>
                <Button fullWidth onClick={handleIdentityLogin} loading={loading}>
                  {t('tpr.login.continue_as', { login: identityLogin })}
                </Button>
                <p className={styles.identityHint}>{t('tpr.login.identity_hint')}</p>
              </div>
            )}
            <LoginForm
              apiBase={apiBase}
              detectedBase={detectedBase}
              editingBase={showAdvanced}
              managementKey={managementKey}
              showKey={showKey}
              rememberPassword={rememberPassword}
              loading={loading}
              success={phase === 'success'}
              error={error}
              onApiBaseChange={setApiBase}
              onEditingBaseChange={setShowAdvanced}
              onManagementKeyChange={setManagementKey}
              onShowKeyChange={setShowKey}
              onRememberPasswordChange={setRememberPassword}
              onSubmit={handleSubmit}
              quietSubmit={Boolean(identityLogin)}
            />
          </div>
        )}
      </main>
    </div>
  );
}
