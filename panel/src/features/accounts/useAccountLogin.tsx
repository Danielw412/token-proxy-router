import { useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { OAuthFlowDialog, type DialogHeading } from '@/features/oauth/components/OAuthFlowDialog';
import { useOAuthFlows } from '@/features/oauth/hooks/useOAuthFlows';
import {
  OAUTH_PROVIDERS,
  resolveThemedIcon,
  supportsManualCallback,
  type OAuthProviderCard,
} from '@/features/oauth/providers';
import { useThemeStore } from '@/stores';

/** Health and the CLI say "claude"; the OAuth flow calls the same provider "anthropic". */
export const toOAuthProvider = (provider: string): string =>
  provider === 'claude' ? 'anthropic' : provider;

/**
 * The existing OAuth flow as a reusable dialog, including paste-the-callback for remote
 * machines. Pages get an `open` function and a `dialog` element to render.
 */
export function useAccountLogin(options: { onAdded?: () => void } = {}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const focusedRef = useRef<string | null>(null);
  const [providerId, setProviderId] = useState<string | null>(null);
  const [isOpen, setIsOpen] = useState(false);
  const cards = useMemo<OAuthProviderCard[]>(() => OAUTH_PROVIDERS, []);

  const flows = useOAuthFlows({
    providerCards: cards,
    isFocused: (provider) => focusedRef.current === provider,
  });

  const addedRef = useRef(flows.addedCount);
  if (addedRef.current !== flows.addedCount) {
    addedRef.current = flows.addedCount;
    options.onAdded?.();
  }

  const open = (provider: string) => {
    const id = toOAuthProvider(provider);
    focusedRef.current = id;
    setProviderId(id);
    setIsOpen(true);
    if (!flows.states[id]?.status) void flows.startAuth(id);
  };

  const close = () => {
    if (providerId) flows.dismissSuccess(providerId);
    focusedRef.current = null;
    setIsOpen(false);
  };

  const card = providerId ? cards.find((item) => item.id === providerId) : undefined;
  const heading = (item: OAuthProviderCard): DialogHeading => ({
    title:
      item.kind === 'plugin'
        ? t('auth_login.plugin_oauth_title', { name: item.title })
        : t(item.titleKey),
    caption:
      item.kind === 'plugin'
        ? t('auth_login.method_plugin')
        : t(item.flow === 'device' ? 'auth_login.method_device' : 'auth_login.method_browser'),
    icon: item.kind === 'plugin' ? item.icon : resolveThemedIcon(item.icon, resolvedTheme),
  });

  const dialog = card ? (
    <OAuthFlowDialog
      open={isOpen}
      providerId={card.id}
      heading={heading(card)}
      state={flows.states[card.id] ?? {}}
      text={(suffix) => flows.providerText(card.id, suffix)}
      supportsCallback={supportsManualCallback(card)}
      onStart={() => void flows.startAuth(card.id)}
      onCancel={() => void flows.cancelAuth(card.id)}
      onCallbackChange={(value) => flows.setCallbackInput(card.id, value)}
      onCallbackSubmit={() => void flows.submitCallback(card.id)}
      onViewAuthFiles={() => {
        close();
        navigate('/accounts');
      }}
      onClose={close}
    />
  ) : null;

  return { open, dialog, addedCount: flows.addedCount, states: flows.states };
}
