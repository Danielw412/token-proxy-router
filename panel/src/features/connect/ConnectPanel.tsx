import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import { CodeBlock, Note, ResourceError, Segmented } from '@/components/tpr/Ui';
import { useTprResource } from '@/hooks/useTprResource';
import { tprApi, type TprSnippets } from '@/services/api/tpr';
import { useNotificationStore } from '@/stores';
import { getErrorMessage } from '@/utils/helpers';
import styles from './ConnectPanel.module.scss';

type ConnectTab = 'linux' | 'windows' | 'codex' | 'claude' | 't3';

/**
 * Hub and key pickers plus copy-ready commands. Snippets carry the real key, so they
 * are fetched only on request and held only in this component's state.
 */
export function ConnectPanel() {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const { data, loading, error, errorStatus, refresh } = useTprResource(() => tprApi.connect(), []);
  const [hub, setHub] = useState('');
  const [keyId, setKeyId] = useState('');
  const [tab, setTab] = useState<ConnectTab>('linux');
  const [snippets, setSnippets] = useState<TprSnippets | null>(null);
  const [fetching, setFetching] = useState(false);

  const hubUrl = hub || data?.hub_urls[0]?.url || '';
  const selectedKey = keyId || data?.keys[0]?.key_id || '';

  // A new hub or key makes the old snippets wrong, and they should not linger.
  useEffect(() => {
    setSnippets(null);
  }, [hubUrl, selectedKey]);

  const reveal = async () => {
    setFetching(true);
    try {
      setSnippets(await tprApi.connectSnippets(selectedKey, hubUrl));
    } catch (err) {
      showNotification(getErrorMessage(err, t('common.unknown_error')), 'error');
    } finally {
      setFetching(false);
    }
  };

  if (error) {
    return <ResourceError message={error} status={errorStatus} onRetry={() => void refresh()} />;
  }
  if (loading || !data) return <p className={styles.quiet}>{t('common.loading')}</p>;

  const noKeys = data.keys.length === 0;

  return (
    <div className={styles.root}>
      <div className={styles.pickers}>
        <div className={styles.picker}>
          <span className={styles.pickerLabel} id="connect-hub-label">
            {t('tpr.connect.hub_url')}
          </span>
          <Select
            value={hubUrl}
            ariaLabelledBy="connect-hub-label"
            size="sm"
            onChange={setHub}
            options={data.hub_urls.map((item) => ({
              value: item.url,
              label: `${item.label}: ${item.url}`,
            }))}
          />
        </div>
        <div className={styles.picker}>
          <span className={styles.pickerLabel} id="connect-key-label">
            {t('tpr.connect.api_key')}
          </span>
          <Select
            value={selectedKey}
            ariaLabelledBy="connect-key-label"
            size="sm"
            disabled={noKeys}
            placeholder={t('tpr.connect.no_keys')}
            onChange={setKeyId}
            options={data.keys.map((item) => ({
              value: item.key_id,
              label: `${item.name || item.key_id} (${item.masked})`,
            }))}
          />
        </div>
      </div>

      {noKeys && (
        <Note tone="warn">
          {t('tpr.connect.no_keys_hint')} <Link to="/settings">{t('tpr.connect.no_keys_link')}</Link>
        </Note>
      )}

      <Segmented<ConnectTab>
        ariaLabel={t('tpr.connect.tabs')}
        value={tab}
        onChange={setTab}
        options={[
          { value: 'linux', label: t('tpr.connect.tab_linux') },
          { value: 'windows', label: t('tpr.connect.tab_windows') },
          { value: 'codex', label: t('tpr.connect.tab_codex') },
          { value: 'claude', label: t('tpr.connect.tab_claude') },
          { value: 't3', label: t('tpr.connect.tab_t3') },
        ]}
      />

      {tab === 'linux' && <p className={styles.tabNote}>{t('tpr.connect.linux_note')}</p>}
      {tab === 'windows' && <p className={styles.tabNote}>{t('tpr.connect.windows_note')}</p>}
      {tab === 'codex' && <p className={styles.tabNote}>{t('tpr.connect.codex_note')}</p>}
      {tab === 'claude' && <Note tone="warn">{t('tpr.connect.claude_warning')}</Note>}
      {tab === 't3' && <p className={styles.tabNote}>{t('tpr.connect.t3_note')}</p>}

      {!snippets ? (
        <div className={styles.reveal}>
          <Button onClick={() => void reveal()} loading={fetching} disabled={noKeys}>
            {t('tpr.connect.show_commands')}
          </Button>
          <span className={styles.quiet}>{t('tpr.connect.show_commands_hint')}</span>
        </div>
      ) : (
        <div className={styles.snippets}>
          {tab === 'linux' && <CodeBlock code={snippets.linux} />}
          {tab === 'windows' && <CodeBlock code={snippets.windows} />}
          {tab === 'codex' && (
            <>
              <CodeBlock code={snippets.codex_toml} label={t('tpr.connect.codex_file')} />
              <CodeBlock code={snippets.codex_env} label={t('tpr.connect.codex_key_linux')} />
              <CodeBlock code={snippets.codex_env_windows} label={t('tpr.connect.codex_key_windows')} />
              <CodeBlock code={snippets.codex_run} label={t('tpr.connect.codex_run')} />
            </>
          )}
          {tab === 'claude' && (
            <>
              <CodeBlock code={snippets.claude_env} label={t('tpr.connect.claude_linux')} />
              <CodeBlock code={snippets.claude_env_windows} label={t('tpr.connect.claude_windows')} />
            </>
          )}
          {tab === 't3' && <CodeBlock code={snippets.t3} />}
        </div>
      )}
    </div>
  );
}
