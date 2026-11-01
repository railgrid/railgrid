export type LLMProviderPreset = 'openai' | 'anthropic' | 'openrouter' | 'google' | 'custom'

export const OPENAI_API_BASE_URL = 'https://api.openai.com/v1'
export const ANTHROPIC_API_BASE_URL = 'https://api.anthropic.com/v1'
export const OPENROUTER_API_BASE_URL = 'https://openrouter.ai/api/v1'
export const GOOGLE_AI_STUDIO_BASE_URL = 'https://generativelanguage.googleapis.com'

export interface LLMProviderSelection {
  provider: 'openai-compatible' | 'google-ai-studio'
  baseURL: string
}

export function inferLLMProviderPreset(provider: string, baseURL: string): LLMProviderPreset {
  if (provider.trim().toLowerCase() === 'google-ai-studio') return 'google'
  const normalized = normalizeBaseURL(baseURL)
  if (normalized === OPENAI_API_BASE_URL) return 'openai'
  if (normalized === ANTHROPIC_API_BASE_URL) return 'anthropic'
  if (normalized === OPENROUTER_API_BASE_URL) return 'openrouter'
  return 'custom'
}

export function llmProviderSelection(preset: LLMProviderPreset, currentBaseURL = ''): LLMProviderSelection {
  if (preset === 'google') {
    return { provider: 'google-ai-studio', baseURL: GOOGLE_AI_STUDIO_BASE_URL }
  }
  if (preset === 'openai') {
    return { provider: 'openai-compatible', baseURL: OPENAI_API_BASE_URL }
  }
  if (preset === 'anthropic') return { provider: 'openai-compatible', baseURL: ANTHROPIC_API_BASE_URL }
  if (preset === 'openrouter') return { provider: 'openai-compatible', baseURL: OPENROUTER_API_BASE_URL }
  const normalizedCurrent = normalizeBaseURL(currentBaseURL)
  return {
    provider: 'openai-compatible',
    baseURL: [OPENAI_API_BASE_URL, ANTHROPIC_API_BASE_URL, OPENROUTER_API_BASE_URL, GOOGLE_AI_STUDIO_BASE_URL].includes(normalizedCurrent) ? '' : currentBaseURL,
  }
}

function normalizeBaseURL(value: string): string {
  return value.trim().replace(/\/+$/, '').toLowerCase()
}
