export type ProviderPresetId = 'deepseek' | 'openai' | 'groq' | 'mistral' | 'openrouter'

export type ProviderPreset = {
  id: ProviderPresetId
  name: string
  endpoint: string
}

export const providerPresets: readonly ProviderPreset[] = [
  { id: 'deepseek', name: 'DeepSeek', endpoint: 'https://api.deepseek.com/v1' },
  { id: 'openai', name: 'OpenAI', endpoint: 'https://api.openai.com/v1' },
  { id: 'groq', name: 'Groq', endpoint: 'https://api.groq.com/openai/v1' },
  { id: 'mistral', name: 'Mistral', endpoint: 'https://api.mistral.ai/v1' },
  { id: 'openrouter', name: 'OpenRouter', endpoint: 'https://openrouter.ai/api/v1' },
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
