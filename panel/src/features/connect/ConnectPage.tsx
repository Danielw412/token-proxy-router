import { useTranslation } from 'react-i18next';
import { Page, PageHeader, Panel } from '@/components/tpr/Ui';
import { ConnectPanel } from './ConnectPanel';

export function ConnectPage() {
  const { t } = useTranslation();
  return (
    <Page>
      <PageHeader title={t('tpr.connect.title')} meta={t('tpr.connect.meta')} />
      <Panel>
        <ConnectPanel />
      </Panel>
    </Page>
  );
}
