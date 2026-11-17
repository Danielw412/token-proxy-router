// Secondary text on the logs and plugins pages and the main layout (TPR replaces upstream's
// dashboard and system pages, so their checks are left out):
// definitions live in InfoTips after their labels, restating subtitles and descriptions are
// gone, and warnings / destructive-action notes stay visible. Static markup and source
// contracts only; hover, focus and tap behaviour and the visual layout need a browser.

import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';

const read = (path: string) => readFileSync(path, 'utf8');

describe('logs page', () => {
  const source = read('src/features/logs/LogsPage.tsx');

  test('the error-log caveat is an InfoTip on the tab card title', () => {
    expect(source).toMatch(
      /\{t\('logs\.error_logs_modal_title'\)\}\s*<InfoTip\s+content=\{t\('logs\.error_logs_description'\)\}/
    );
    expect(source).not.toContain(`<div className="hint">{t('logs.error_logs_description')}</div>`);
    // The "request logging is on, so this list stays empty" warning remains visible.
    expect(source).toContain("{t('logs.error_logs_request_log_enabled')}");
  });

  test('the raw-log explanation moved from a title to an InfoTip beside the switch', () => {
    expect(source).not.toMatch(/title=\{t\('logs\.show_raw_logs_hint'/);
    // A sibling of the switch, not inside its <label>.
    expect(source).toMatch(
      /checked=\{showRawLogs\}[\s\S]*?<\/span>\s*\}\s*\/>\s*<InfoTip\s+content=\{t\('logs\.show_raw_logs_hint'/
    );
  });
});

describe('plugins page and install flow', () => {
  test('the page header is the title alone; warnings stay', () => {
    const page = read('src/features/plugins/PluginsPage.tsx');
    // Only the h1 in the header.
    expect(page).toMatch(
      /<header className=\{styles\.header\}>\s*<h1[^>]*>[^<]*<\/h1>\s*<\/header>/
    );
    expect(page).toContain("t('plugin_management.global_disabled_hint')");
    expect(read('src/features/plugins/components/PluginStoreView.tsx')).toContain(
      "t('plugin_store.security_banner_text')"
    );
  });

  test('install-version options keep only the explanations that change the choice', () => {
    const modal = read('src/features/plugins/components/PluginInstallOptionsModal.tsx');
    // "Choose release" only shows a second line while releases are loading.
    expect(modal).toMatch(
      /'release',\s*t\('plugin_store\.install_version_release_mode'\),[\s\S]*?releaseLoading \? t\('plugin_store\.install_versions_loading'\) : null,/
    );
    expect(modal).toContain("t('plugin_store.install_version_latest_hint'");
    expect(modal).toContain("t('plugin_store.install_version_manual_hint')");
  });

  test('the trust gate keeps every risk statement', () => {
    const gate = read('src/features/plugins/components/PluginInstallGateModal.tsx');
    for (const key of [
      'gate_warning',
      'gate_effect_runs_code',
      'gate_effect_no_review',
      'gate_effect_restart',
      'gate_untrusted_alert',
      'gate_step3_prompt',
    ]) {
      expect(gate).toContain(`t('plugin_store.${key}'`);
    }
    // Step 3 is the prompt, the input and the button; nothing restates the prompt below it.
    expect(gate).toMatch(
      /aria-label=\{t\('plugin_store\.gate_step3_prompt', \{ token \}\)\}\s*\/>\s*<\/div>/
    );
  });
});

describe('main layout', () => {
  // TPR shows its own name in the sidebar, with no subtitle under it.
  test('the sidebar brand is the name alone, with no subtitle', () => {
    const layout = read('src/components/layout/MainLayout.tsx');
    expect(layout).toMatch(
      /<span className="sidebar-brand-text">\s*<span className="sidebar-brand-title">\{BRAND_NAME\}<\/span>\s*<\/span>/
    );
    expect(layout).toContain('className="sidebar-brand" title={BRAND_NAME}');
  });
});
