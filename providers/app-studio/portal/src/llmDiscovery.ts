export type LLMProviderPreset = 'openai' | 'google' | 'custom'

export const OPENAI_API_BASE_URL = 'https://api.openai.com/v1'
export const GOOGLE_AI_STUDIO_BASE_URL = 'https://generativelanguage.googleapis.com'

export interface LLMProviderSelection {
  provider: 'openai-compatible' | 'google-ai-studio'
  baseURL: string
}

export function inferLLMProviderPreset(provider: string, baseURL: string): LLMProviderPreset {
  if (provider.trim().toLowerCase() === 'google-ai-studio') return 'google'
  return normalizeBaseURL(baseURL) === OPENAI_API_BASE_URL ? 'openai' : 'custom'
}

export function llmProviderSelection(preset: LLMProviderPreset, currentBaseURL = ''): LLMProviderSelection {
  if (preset === 'google') {
    return { provider: 'google-ai-studio', baseURL: GOOGLE_AI_STUDIO_BASE_URL }
  }
  if (preset === 'openai') {
    return { provider: 'openai-compatible', baseURL: OPENAI_API_BASE_URL }
  }
  const normalizedCurrent = normalizeBaseURL(currentBaseURL)
  return {
    provider: 'openai-compatible',
    baseURL: normalizedCurrent === OPENAI_API_BASE_URL || normalizedCurrent === GOOGLE_AI_STUDIO_BASE_URL ? '' : currentBaseURL,
  }
}

function normalizeBaseURL(value: string): string {
  return value.trim().replace(/\/+$/, '').toLowerCase()
}
