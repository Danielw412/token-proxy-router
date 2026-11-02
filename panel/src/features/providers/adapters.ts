import type { GeminiKeyConfig, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import { hasDisableAllModelsRule, stripDisableAllModelsRule } from '@/components/providers/utils';
import { maskApiKey } from '@/utils/format';
import type {
  ProviderBrand,
  ProviderResource,
  ProviderResourceSelector,
} from './types';

const countHeaders = (headers?: Record<string, string>): number =>
  headers ? Object.keys(headers).length : 0;

const collectModelNames = (models?: Array<{ name?: string }>): string[] => {
  const seen = new Set<string>();
  (models ?? []).forEach((model) => {
    const name = (model?.name ?? '').trim();
    if (name) seen.add(name);
  });
  return Array.from(seen);
};

const normalizePriority = (priority?: number): number =>
  typeof priority === 'number' && Number.isFinite(priority) ? priority : 0;

const buildId = (brand: ProviderBrand, index: number, fragment: string) =>
  `${brand}:${index}:${fragment || 'item'}`;

const truncateForId = (value: string | undefined | null): string => {
  const trimmed = String(value ?? '').trim();
  if (!trimmed) return '';
  if (trimmed.length <= 12) return trimmed;
  return trimmed.slice(0, 8);
};

function providerKeyToResource(
  brand: 'gemini' | 'interactions' | 'codex' | 'meta' | 'xai' | 'claude' | 'vertex',
  config: GeminiKeyConfig | ProviderKeyConfig,
  index: number
): ProviderResource {
  const apiKey = config.apiKey ?? '';
  const disabled = hasDisableAllModelsRule(config.excludedModels);
  const flags: ProviderResource['flags'] = {};
  if (brand === 'codex' || brand === 'xai') {
    flags.websockets = (config as ProviderKeyConfig).websockets === true;
  }
  if (brand === 'claude') {
    const claudeConfig = config as ProviderKeyConfig;
    flags.cloakEnabled = Boolean(claudeConfig.cloak?.mode?.trim());
    flags.claudeCodeCliProfile = claudeConfig.fingerprintProfile === 'claude-code-cli';
  }

  const selector: ProviderResourceSelector = {
    brand,
    apiKey,
    baseUrl: config.baseUrl,
    index,
  } as ProviderResourceSelector;

  return {
    id: buildId(brand, index, truncateForId(apiKey)),
    brand,
    originalIndex: index,
    name: typeof config.source?.group.name === 'string' ? config.source.group.name : null,
    identifier: maskApiKey(apiKey) || `#${index + 1}`,
    apiKeyPreview: apiKey ? maskApiKey(apiKey) : null,
    apiKey: apiKey || null,
    authIndex: config.authIndex ?? null,
    baseUrl: config.baseUrl ?? null,
    proxyUrl: config.proxyUrl ?? null,
    prefix: config.prefix ?? null,
    modelCount: config.models?.length ?? 0,
    models: collectModelNames(config.models),
    priority: normalizePriority(config.priority),
    headerCount: countHeaders(config.headers),
    excludedModelCount: stripDisableAllModelsRule(config.excludedModels).length,
    apiKeyEntryCount: 0,
    disabled,
    flags,
    selector,
    raw: config,
  };
}

export function geminiToResource(config: GeminiKeyConfig, index: number): ProviderResource {
  return providerKeyToResource('gemini', config, index);
}

export function interactionsToResource(config: GeminiKeyConfig, index: number): ProviderResource {
  return providerKeyToResource('interactions', config, index);
}

export function codexToResource(config: ProviderKeyConfig, index: number): ProviderResource {
  return providerKeyToResource('codex', config, index);
}

export function metaToResource(config: ProviderKeyConfig, index: number): ProviderResource {
  return providerKeyToResource('meta', config, index);
}

export function xaiToResource(config: ProviderKeyConfig, index: number): ProviderResource {
  return providerKeyToResource('xai', config, index);
}

export function claudeToResource(config: ProviderKeyConfig, index: number): ProviderResource {
  return providerKeyToResource('claude', config, index);
}

export function vertexToResource(config: ProviderKeyConfig, index: number): ProviderResource {
  return providerKeyToResource('vertex', config, index);
}

export function openaiToResource(config: OpenAIProviderConfig, index: number): ProviderResource {
  const sourceIndex = config.sourceIndex ?? index;
  const name = (config.name ?? '').trim();
  const firstEntry = config.apiKeyEntries?.[0];
  const previewApiKey = firstEntry?.apiKey ? maskApiKey(firstEntry.apiKey) : null;
  return {
    id: buildId('openaiCompatibility', sourceIndex, truncateForId(name) || `#${sourceIndex}`),
    brand: 'openaiCompatibility',
    originalIndex: sourceIndex,
    name: name || null,
    identifier: name || `#${sourceIndex + 1}`,
    apiKeyPreview: previewApiKey,
    apiKey: null,
    authIndex: config.authIndex ?? null,
    baseUrl: config.baseUrl ?? null,
    proxyUrl: null,
    prefix: config.prefix ?? null,
    modelCount: config.models?.length ?? 0,
    models: collectModelNames(config.models),
    priority: normalizePriority(config.priority),
    headerCount: countHeaders(config.headers),
    excludedModelCount: 0,
    apiKeyEntryCount: config.apiKeyEntries?.length ?? 0,
    disabled: config.disabled === true,
    flags: {},
    selector: { brand: 'openaiCompatibility', name, index: sourceIndex },
    raw: config,
  };
}
