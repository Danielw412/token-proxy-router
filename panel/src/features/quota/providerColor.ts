export const providerColor = (provider: string): string =>
  provider === 'claude'
    ? 'var(--provider-claude)'
    : provider === 'codex'
      ? 'var(--provider-codex)'
      : 'var(--text-secondary)';
