import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { I18nextProvider } from 'react-i18next';
import { createInstance } from 'i18next';
import en from '@/i18n/locales/en.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import zhTW from '@/i18n/locales/zh-TW.json';
import ru from '@/i18n/locales/ru.json';
import ko from '@/i18n/locales/ko.json';
import vi from '@/i18n/locales/vi.json';
import tr from '@/i18n/locales/tr.json';
import { OAuthPage } from '@/features/oauth/OAuthPage';
import {
  OAuthFlowDialog,
  type OAuthFlowDialogProps,
} from '@/features/oauth/components/OAuthFlowDialog';
import { VertexImportDialog } from '@/features/oauth/components/VertexImportDialog';
import type { ProviderFlowState } from '@/features/oauth/hooks/useOAuthFlows';
import { resolveCallbackUrl, XAI_CALLBACK_URL } from '@/features/oauth/callbackUrl';
import { resolveFlowView, splitAuthUrl } from '@/features/oauth/flowView';
import { buildPluginOAuthProviderCards, OAUTH_PROVIDERS } from '@/features/oauth/providers';
import type { PluginListEntry } from '@/types';

describe('OAuth dialog view resolution', () => {
  test('maps every flow state to exactly one view', () => {
    expect(resolveFlowView({})).toBe('idle');
    // 正在生成链接：已进入步骤视图（骨架占位）
    expect(resolveFlowView({ status: 'waiting' })).toBe('flow');
    expect(resolveFlowView({ status: 'waiting', url: 'https://a.test' })).toBe('flow');
    expect(resolveFlowView({ status: 'success' })).toBe('success');
    // 轮询网络失败但链接仍在：留在步骤视图，手动回调仍可恢复
    expect(resolveFlowView({ status: 'error', url: 'https://a.test' })).toBe('flow');
    // 链接已失效（启动失败 / Devin 终态失败）：失败视图
    expect(resolveFlowView({ status: 'error', error: 'denied' })).toBe('failed');
  });

  test('highlights the host of the authorization link', () => {
    expect(splitAuthUrl('https://auth.openai.com/oauth/authorize?x=1')).toEqual({
      host: 'auth.openai.com',
      rest: '/oauth/authorize?x=1',
    });
    expect(splitAuthUrl('http://127.0.0.1:8317/cb')).toEqual({
      host: '127.0.0.1:8317',
      rest: '/cb',
    });
    expect(splitAuthUrl('not a url')).toEqual({ host: '', rest: 'not a url' });
  });
});

describe('manual callback input', () => {
  test('submits non-xAI input verbatim (trimmed)', () => {
    expect(resolveCallbackUrl('codex', '  http://localhost:1455/auth/callback?code=c  ')).toBe(
      'http://localhost:1455/auth/callback?code=c'
    );
  });

  test('passes absolute xAI callback URLs through', () => {
    const url = 'http://127.0.0.1:56121/callback?code=c&state=s';
    expect(resolveCallbackUrl('xai', url, 'other')).toBe(url);
  });

  test('builds the xAI loopback URL from a displayed code and the attempt state', () => {
    const built = new URL(resolveCallbackUrl('xai', 'abc123', 'st-1')!);
    expect(`${built.origin}${built.pathname}`).toBe(XAI_CALLBACK_URL);
    expect(built.searchParams.get('code')).toBe('abc123');
    expect(built.searchParams.get('state')).toBe('st-1');
    expect(new URL(resolveCallbackUrl('xai', 'code: xyz', 'st-1')!).searchParams.get('code')).toBe(
      'xyz'
    );
  });

  test('prefers the pasted query state and keeps provider errors', () => {
    const built = new URL(
      resolveCallbackUrl('xai', '?error=access_denied&error_description=no&state=pasted', 'st')!
    );
    expect(built.searchParams.get('state')).toBe('pasted');
    expect(built.searchParams.get('error')).toBe('access_denied');
    expect(built.searchParams.get('error_description')).toBe('no');
  });

  test('refuses to invent a state for xAI', () => {
    expect(resolveCallbackUrl('xai', 'abc123')).toBeNull();
    expect(resolveCallbackUrl('xai', 'code=abc123')).toBeNull();
    expect(resolveCallbackUrl('xai', '   ', 'st')).toBeNull();
  });
});

const plugin = (overrides: Partial<PluginListEntry>): PluginListEntry => ({
  id: 'example',
  path: '/plugins/example',
  configured: true,
  registered: true,
  enabled: true,
  effectiveEnabled: true,
  supportsOAuth: true,
  oauthProvider: 'example',
  supportsQuota: false,
  logo: '',
  configFields: [],
  menus: [],
  metadata: null,
  ...overrides,
});

describe('plugin OAuth tiles', () => {
  test('appends enabled OAuth plugins once and never shadows built-in providers', () => {
    const cards = buildPluginOAuthProviderCards(
      [
        plugin({ id: 'a', oauthProvider: 'acme' }),
        plugin({ id: 'a-dup', oauthProvider: 'acme' }),
        plugin({ id: 'b', oauthProvider: 'codex' }),
        plugin({ id: 'c', oauthProvider: 'off', effectiveEnabled: false }),
        plugin({ id: 'd', oauthProvider: 'nooauth', supportsOAuth: false }),
        plugin({ id: 'e', oauthProvider: undefined }),
      ],
      'http://127.0.0.1:8317'
    );
    expect(cards.map((card) => [card.id, card.title])).toEqual([['acme', 'a']]);
  });
});

describe('OAuth provider gallery', () => {
  const locales = { en, 'zh-CN': zhCN, 'zh-TW': zhTW, ru, ko, vi } as const;

  for (const [lng, translation] of Object.entries(locales)) {
    test(`renders every provider tile without raw keys (${lng})`, async () => {
      const i18n = createInstance();
      await i18n.init({ lng, resources: { [lng]: { translation } } });
      const markup = renderToStaticMarkup(
        createElement(
          I18nextProvider,
          { i18n },
          createElement(MemoryRouter, null, createElement(OAuthPage))
        )
      );
      for (const provider of OAUTH_PROVIDERS) {
        const label =
          typeof provider.label === 'string' ? provider.label : i18n.t(provider.label.key);
        expect(markup).toContain(label);
      }
      expect(markup).toContain('Vertex AI');
      expect(markup).toContain(i18n.t('siwc.bind'));
      expect(markup).not.toMatch(/auth_login\.|vertex_import\.|siwc\./);
      // 每个磁贴都是打开对话框的按钮（提供商 + Vertex + SIWC 绑定）
      expect(markup.match(/aria-haspopup="dialog"/g)?.length).toBe(OAUTH_PROVIDERS.length + 2);
    });
  }

  test('lists Claude and Codex first and has no promotional links', async () => {
    const i18n = createInstance();
    await i18n.init({ lng: 'en', resources: { en: { translation: en } } });
    const markup = renderToStaticMarkup(
      createElement(
        I18nextProvider,
        { i18n },
        createElement(MemoryRouter, null, createElement(OAuthPage))
      )
    );
    expect(OAUTH_PROVIDERS.slice(0, 2).map((provider) => provider.id)).toEqual([
      'anthropic',
      'codex',
    ]);
    expect(markup).not.toContain('aff=');
    expect(markup).not.toContain('target="_blank"');
  });
});

describe('OAuth dialog secondary text', () => {
  const renderEn = async (element: ReturnType<typeof createElement>) => {
    const i18n = createInstance();
    await i18n.init({ lng: 'en', resources: { en: { translation: en } } });
    return {
      i18n,
      markup: renderToStaticMarkup(createElement(I18nextProvider, { i18n }, element)),
    };
  };

  const dialog = (providerId: string, state: ProviderFlowState, extra = {}) =>
    createElement(OAuthFlowDialog, {
      open: true,
      providerId,
      heading: { title: `${providerId} OAuth`, caption: 'Browser sign-in', icon: '' },
      state,
      text: (suffix: string) => `auth_login.${providerId}_${suffix}`,
      supportsCallback: false,
      onStart: () => {},
      onCancel: () => {},
      onCallbackChange: () => {},
      onCallbackSubmit: () => {},
      onViewAuthFiles: () => {},
      onClose: () => {},
      ...extra,
    } satisfies OAuthFlowDialogProps);

  test('only providers with something non-obvious to say carry a sign-in note', () => {
    const withNotes = OAUTH_PROVIDERS.filter((provider) => provider.signInNoteKey);
    expect(withNotes.map((provider) => provider.id).sort()).toEqual(
      ['antigravity', 'devin', 'kimi', 'kimi-ai'].sort()
    );
    for (const translation of [en, zhCN, zhTW, ru, ko, vi, tr]) {
      const messages = translation.auth_login as Record<string, string>;
      for (const provider of withNotes) {
        expect(messages[provider.signInNoteKey!.replace('auth_login.', '')]).toBeTruthy();
      }
    }
  });

  test('no longer opens with the provider sentence and puts the note behind the step title', async () => {
    const waiting = { status: 'waiting' as const, url: 'https://accounts.example/authorize' };
    const plain = await renderEn(dialog('codex', waiting));
    // The dialog no longer renders `<provider>_oauth_hint` at all.
    expect(plain.markup).not.toContain('oauth_hint');
    expect(plain.markup).not.toContain('data-info-tip');

    const note = en.auth_login.antigravity_oauth_hint;
    const noted = await renderEn(dialog('antigravity', waiting, { signInNote: note }));
    const title = en.auth_login.step_open_auth_page;
    expect(noted.markup).toContain(`${title}</span><button type="button" data-info-tip=""`);
    expect(noted.markup).toContain(
      `aria-label="${noted.i18n.t('common.more_info_about', { label: title })}"`
    );
    // The note stays reachable to assistive tech through the InfoTip's hidden description.
    expect(noted.markup).toMatch(/<span id="[^"]+" hidden="">[^<]*Google[^<]*<\/span>/);
  });

  test('the success view relies on its actions instead of a saved-credential sentence', async () => {
    const { markup } = await renderEn(dialog('codex', { status: 'success' }));
    expect(markup).toContain(en.auth_login.view_auth_files);
    // Literal on purpose: `auth_login.success_saved_hint` is slated for removal from the locales.
    expect(markup).not.toContain('manage it in Auth Files');
  });

  test('the Vertex import form drops its description and moves the file hint into an InfoTip', async () => {
    const { markup } = await renderEn(
      createElement(VertexImportDialog, {
        open: true,
        heading: { title: en.vertex_import.title, caption: en.vertex_import.file_label, icon: '' },
        vertex: { location: '', loading: false },
        onPickFile: () => {},
        onLocationChange: () => {},
        onImport: () => {},
        onImportAnother: () => {},
        onViewAuthFiles: () => {},
        onClose: () => {},
      })
    );
    // Literal on purpose: `vertex_import.description` is slated for removal from the locales.
    expect(markup).not.toContain('vertex-import helper');
    expect(markup).toContain(en.vertex_import.drop_hint);
    // The file hint lives in one hidden element that describes both the drop zone and the InfoTip.
    const hint = new RegExp(`<span id="([^"]+)" hidden="">${en.vertex_import.file_hint}</span>`);
    const hintId = hint.exec(markup)?.[1];
    expect(hintId).toBeTruthy();
    expect(markup).toContain(
      `data-info-tip="" aria-label="More information about ${en.vertex_import.file_label}" aria-describedby="${hintId}"`
    );
    expect(markup).toMatch(
      new RegExp(
        `aria-describedby="${hintId}"[^>]*>(?:(?!</button>).)*${en.vertex_import.drop_hint}`
      )
    );
    // The region field keeps its explanation as the shared Input's InfoTip.
    expect(markup).toContain(
      `aria-label="More information about ${en.vertex_import.location_label}"`
    );
  });
});
