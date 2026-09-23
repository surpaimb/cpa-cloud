export type ProviderPresetId = 'anthropic' | 'gemini-native' | 'gemini-openai' | 'deepseek' | 'openai' | 'groq' | 'mistral' | 'openrouter'

export type ProviderPreset = {
  id: ProviderPresetId
  name: string
  endpoint: string
  providerKind: 'openai-compatible' | 'anthropic-api-key' | 'gemini-api-key'
  endpointLocked?: boolean
}

export const providerPresets: readonly ProviderPreset[] = [
  { id: 'anthropic', name: 'Anthropic', endpoint: 'https://api.anthropic.com', providerKind: 'anthropic-api-key' },
  { id: 'gemini-native', name: 'Google Gemini（原生 API）', endpoint: 'https://generativelanguage.googleapis.com', providerKind: 'gemini-api-key', endpointLocked: true },
  { id: 'gemini-openai', name: 'Google Gemini（OpenAI 兼容）', endpoint: 'https://generativelanguage.googleapis.com/v1beta/openai', providerKind: 'openai-compatible' },
  { id: 'deepseek', name: 'DeepSeek', endpoint: 'https://api.deepseek.com/v1', providerKind: 'openai-compatible' },
  { id: 'openai', name: 'OpenAI', endpoint: 'https://api.openai.com/v1', providerKind: 'openai-compatible' },
  { id: 'groq', name: 'Groq', endpoint: 'https://api.groq.com/openai/v1', providerKind: 'openai-compatible' },
  { id: 'mistral', name: 'Mistral', endpoint: 'https://api.mistral.ai/v1', providerKind: 'openai-compatible' },
  { id: 'openrouter', name: 'OpenRouter', endpoint: 'https://openrouter.ai/api/v1', providerKind: 'openai-compatible' },
] as const

export type ProviderChoice = ProviderPresetId | 'custom'

function canonicalEndpoint(value: string) {
  try {
    const parsed = new URL(value.trim())
    if (parsed.protocol !== 'https:' || parsed.username || parsed.password || parsed.search || parsed.hash) return null
    const path = parsed.pathname.replace(/\/+$/, '') || '/'
    return `${parsed.origin}${path}`
  } catch {
    return null
  }
}

export function presetForEndpoint(value: string): ProviderPreset | null {
  const candidate = canonicalEndpoint(value)
  if (!candidate) return null
  return providerPresets.find((preset) => canonicalEndpoint(preset.endpoint) === candidate) ?? null
}

export function providerPreset(id: ProviderPresetId) {
  return providerPresets.find((preset) => preset.id === id)!
}
