const OPENAI_COMPATIBLE_PROVIDER = 'openai-compatible'
const GOOGLE_AI_STUDIO_PROVIDER = 'google-ai-studio'
const CHAT_COMPLETIONS_SUFFIX = '/chat/completions'
const UNSUPPORTED_OPERATION_SUFFIXES = ['/responses', '/messages']

export type LLMCredentialMode = 'api-key' | 'service-account-json'

export interface LLMModelFormCandidate {
  id: string
  name: string
}

export interface LLMModelFormInput {
  name: string
  provider: string
  credentialMode: LLMCredentialMode
  baseURL: string
  model: string
  credential: string
  credentialRequired?: boolean
  editingModelID?: string | null
  existingModels?: LLMModelFormCandidate[]
}

export interface LLMModelFormErrors {
  name: string
  baseURL: string
  model: string
  credential: string
}

export function validateLLMBaseURL(provider: string, value: string): string {
  const raw = value.trim()
  const openAICompatible = provider.trim().toLowerCase() === OPENAI_COMPATIBLE_PROVIDER
  if (!raw) return openAICompatible ? 'Base URL is required.' : ''

  let parsed: URL
  try {
    parsed = new URL(raw)
  } catch {
    return 'Enter an absolute HTTP(S) base URL.'
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return 'Base URL must use HTTP or HTTPS.'
  }
  if (!openAICompatible) return ''

  const pathname = parsed.pathname.replace(/\/+$/, '')
  const lowerPathname = pathname.toLowerCase()
  if (lowerPathname.endsWith(CHAT_COMPLETIONS_SUFFIX)) {
    const basePath = pathname.slice(0, -CHAT_COMPLETIONS_SUFFIX.length)
    const suggestedBaseURL = `${parsed.origin}${basePath}`
    return `Enter the API base URL, not the chat completions endpoint. Use ${suggestedBaseURL}; App Studio adds /chat/completions automatically.`
  }
  const unsupportedSuffix = UNSUPPORTED_OPERATION_SUFFIXES.find((suffix) => lowerPathname.endsWith(suffix))
  if (unsupportedSuffix) {
    return `This endpoint uses ${unsupportedSuffix}, but the OpenAI-compatible provider requires a /chat/completions model. Choose a compatible model and enter its base URL.`
  }
  return ''
}

export function validateLLMModelForm(input: LLMModelFormInput): LLMModelFormErrors {
  const name = input.name.trim()
  const model = input.model.trim()
  const credential = input.credential.trim()
  const credentialRequired = input.credentialRequired ?? !input.editingModelID
  const errors: LLMModelFormErrors = {
    name: '',
    baseURL: validateLLMBaseURL(input.provider, input.baseURL),
    model: '',
    credential: '',
  }

  if (!name) {
    errors.name = 'Enter a display name.'
  } else if (name.length > 80) {
    errors.name = 'Use 80 characters or fewer.'
  } else {
    const candidateID = modelConfigurationID(name)
    const duplicate = (input.existingModels ?? []).some((saved) =>
      saved.id !== input.editingModelID && modelConfigurationID(saved.name) === candidateID,
    )
    if (duplicate) errors.name = 'A model with this display name already exists.'
  }

  if (!model) errors.model = 'Enter the provider’s exact model ID.'

  if (!credential) {
    if (credentialRequired) errors.credential = 'Enter a credential to connect this model.'
    return errors
  }

  if (input.provider.trim().toLowerCase() !== GOOGLE_AI_STUDIO_PROVIDER) return errors

  if (input.credentialMode === 'service-account-json') {
    errors.credential = validateGoogleServiceAccountJSON(credential)
  } else if (looksLikeOAuthOrJWT(credential)) {
    errors.credential = 'Enter a Gemini API key, not an OAuth or JWT token.'
  }
  return errors
}

export function hasLLMModelFormErrors(errors: LLMModelFormErrors): boolean {
  return Boolean(errors.name || errors.baseURL || errors.model || errors.credential)
}

function modelConfigurationID(value: string): string {
  const normalized = value.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')
  return normalized.slice(0, 63).replace(/-+$/g, '') || 'default'
}

function validateGoogleServiceAccountJSON(raw: string): string {
  let value: unknown
  try {
    value = JSON.parse(raw)
  } catch {
    return 'Paste valid Google service-account JSON.'
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return 'Paste a Google service-account JSON object.'
  }
  const credential = value as Record<string, unknown>
  const missing = ['project_id', 'client_email', 'private_key', 'token_uri']
    .filter((field) => typeof credential[field] !== 'string' || !String(credential[field]).trim())
  if (credential.type !== 'service_account' || missing.length > 0) {
    const detail = missing.length > 0 ? ` Missing: ${missing.join(', ')}.` : ''
    return `Paste a complete Google service-account key.${detail}`
  }
  return ''
}

function looksLikeOAuthOrJWT(value: string): boolean {
  if (value.startsWith('ya29.')) return true
  const parts = value.split('.')
  return parts.length === 3 && parts.every(Boolean) && parts[0].startsWith('eyJ')
}
