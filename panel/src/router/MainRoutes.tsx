import { Navigate, useLocation, useRoutes, type Location } from 'react-router-dom';
import { OverviewPage } from '@/features/dashboard/OverviewPage';
import { ProvidersWorkbenchPage } from '@/features/providers/ProvidersWorkbenchPage';
import { AuthFilesOAuthExcludedEditPage } from '@/pages/AuthFilesOAuthExcludedEditPage';
import { AuthFilesOAuthModelAliasEditPage } from '@/pages/AuthFilesOAuthModelAliasEditPage';
import { AccountsPage } from '@/features/accounts/AccountsPage';
import { AlertsPage } from '@/features/alerts/AlertsPage';
import { ConnectPage } from '@/features/connect/ConnectPage';
import { QuickStartPage } from '@/features/setup/QuickStartPage';
import { QuotaPage } from '@/features/quota/QuotaPage';
import { RouterPage } from '@/features/tprRouter/RouterPage';
import { PluginResourcePage } from '@/features/plugins/PluginResourcePage';
import { PluginsPage } from '@/features/plugins/PluginsPage';
import { SettingsPage } from '@/features/settings/SettingsPage';
import { LogsPage } from '@/features/logs/LogsPage';
import { SystemPage } from '@/pages/SystemPage';
import { UsagePage } from '@/features/usage/UsagePage';
import { useAuthStore } from '@/stores';

/** Keeps `?field=...` deep links into the advanced editor working from the old route. */
function ConfigRedirect() {
  const location = useLocation();
  const params = new URLSearchParams(location.search);
  params.set('view', 'advanced');
  return <Navigate to={`/settings?${params.toString()}`} replace />;
}

const createMainRoutes = (supportsPlugin: boolean) => [
  { path: '/', element: <OverviewPage /> },
  { path: '/dashboard', element: <OverviewPage /> },
  { path: '/settings', element: <SettingsPage /> },
  { path: '/config', element: <ConfigRedirect /> },
  { path: '/api-keys', element: <Navigate to="/settings" replace /> },
  { path: '/quick-start', element: <QuickStartPage /> },
  { path: '/quick-start/*', element: <Navigate to="/quick-start" replace /> },
  { path: '/ai-providers', element: <ProvidersWorkbenchPage /> },
  { path: '/ai-providers/*', element: <Navigate to="/ai-providers" replace /> },
  { path: '/accounts', element: <AccountsPage /> },
  { path: '/auth-files', element: <Navigate to="/accounts?view=files" replace /> },
  { path: '/auth-files/oauth-excluded', element: <AuthFilesOAuthExcludedEditPage /> },
  { path: '/auth-files/oauth-model-alias', element: <AuthFilesOAuthModelAliasEditPage /> },
  { path: '/oauth', element: <Navigate to="/accounts?add=1" replace /> },
  { path: '/routing', element: <RouterPage /> },
  { path: '/quota', element: <QuotaPage /> },
  { path: '/usage', element: <UsagePage /> },
  { path: '/connect', element: <ConnectPage /> },
  { path: '/alerts', element: <AlertsPage /> },
  ...(supportsPlugin
    ? [
        { path: '/plugin-pages/:pluginId/:menuIndex', element: <PluginResourcePage /> },
        { path: '/plugins', element: <PluginsPage /> },
        { path: '/plugin-store', element: <Navigate to="/plugins" replace /> },
        { path: '/plugins/*', element: <Navigate to="/plugins" replace /> },
      ]
    : [
        { path: '/plugin-pages/*', element: <Navigate to="/" replace /> },
        { path: '/plugins/*', element: <Navigate to="/" replace /> },
        { path: '/plugin-store', element: <Navigate to="/" replace /> },
      ]),
  { path: '/logs', element: <LogsPage /> },
  { path: '/system', element: <SystemPage /> },
  { path: '*', element: <Navigate to="/" replace /> },
];

export function MainRoutes({ location }: { location?: Location }) {
  const supportsPlugin = useAuthStore((state) => state.supportsPlugin);
  return useRoutes(createMainRoutes(supportsPlugin), location);
}
