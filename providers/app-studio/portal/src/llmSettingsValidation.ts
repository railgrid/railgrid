const OPENAI_COMPATIBLE_PROVIDER = 'openai-compatible'
const CHAT_COMPLETIONS_SUFFIX = '/chat/completions'
const UNSUPPORTED_OPERATION_SUFFIXES = ['/responses', '/messages']

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
