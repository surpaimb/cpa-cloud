import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import { messageFor } from '../hooks'
import { CreateModel } from '../pages/ModelsPage'
import { CreateUpstream } from '../pages/UpstreamsPage'
import { presetForEndpoint } from '../providerPresets'

type Route = { status?: number; body?: unknown }
const response = (route: Route) => Promise.resolve(new Response(JSON.stringify(route.body ?? {}), { status: route.status ?? 200, headers: { 'Content-Type': 'application/json' } }))

describe('provider presets', () => {
  it('matches only exact HTTPS origins and paths', () => {
    expect(presetForEndpoint('https://api.anthropic.com/')?.providerKind).toBe('anthropic-api-key')
    expect(presetForEndpoint('https://api.openai.com/v1/')?.id).toBe('openai')
    expect(presetForEndpoint('https://api.openai.com.evil.example/v1')).toBeNull()
    expect(presetForEndpoint('https://api.openai.com/v1/extra')).toBeNull()
    expect(presetForEndpoint('https://api.openai.com/v1?key=value')).toBeNull()
    expect(presetForEndpoint('http://api.openai.com/v1')).toBeNull()
  })

  it.each([
    [409, 'upstream_disabled', '该上游已停用，请先启用后再同步模型。'],
    [502, 'upstream_authentication_failed', '上游拒绝了当前 API Key，请更新凭据后重试。'],
    [502, 'model_discovery_unsupported', '该上游不支持自动读取模型列表，请在模型路由中手动输入。'],
    [429, 'upstream_rate_limited', '上游请求过于频繁，请稍后重试。'],
    [504, 'model_discovery_timeout', '读取模型列表超时，请检查上游后重试。'],
    [502, 'invalid_model_response', '上游返回的模型列表格式无效。'],
    [502, 'model_discovery_failed', '无法从上游读取模型列表，请稍后重试。'],
  ])('maps discovery error %s/%s to actionable copy', (status, code, expected) => {
    expect(messageFor(new ApiError(status, code, 'redacted'))).toBe(expected)
  })
})

describe('upstream and model discovery flows', () => {
  beforeEach(() => { vi.restoreAllMocks() })
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('autofills provider details and clears a stale key when provider or URL changes', async () => {
    render(<CreateUpstream csrf="csrf" onClose={() => undefined} onSaved={() => undefined} />)
    const provider = screen.getByLabelText('服务商')
    const name = screen.getByLabelText('显示名称')
    const endpoint = screen.getByLabelText('API 端点')
    const key = screen.getByLabelText('API Key')

    expect(provider).toHaveValue('deepseek')
    expect(name).toHaveValue('DeepSeek')
    expect(endpoint).toHaveValue('https://api.deepseek.com/v1')

    await userEvent.type(key, 'secret-for-deepseek')
    await userEvent.selectOptions(provider, 'groq')
    expect(name).toHaveValue('Groq')
    expect(endpoint).toHaveValue('https://api.groq.com/openai/v1')
    expect(key).toHaveValue('')

    await userEvent.type(key, 'secret-for-groq')
    await userEvent.clear(endpoint)
    await userEvent.type(endpoint, 'https://gateway.example.com/v1')
    expect(provider).toHaveValue('custom')
    expect(key).toHaveValue('')
    expect(screen.getByText('当前地址按自定义服务处理。修改服务商或地址后，API Key 必须重新填写。')).toBeInTheDocument()
  })

  it('creates the Anthropic preset as an Anthropic API-key upstream', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/upstreams') && init?.method === 'POST') return response({ body: { id: 'anthropic-1', name: 'Anthropic', provider_kind: 'anthropic-api-key', endpoint: 'https://api.anthropic.com/v1', enabled: true, revision: 1 } })
      if (url.endsWith('/upstreams/anthropic-1/discover-models')) return response({ body: { items: [] } })
      throw new Error(`unexpected request ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<CreateUpstream csrf="csrf" onClose={() => undefined} onSaved={() => undefined} />)
    await userEvent.selectOptions(screen.getByLabelText('服务商'), 'anthropic')
    const endpoint = screen.getByLabelText('API 端点')
    await userEvent.clear(endpoint)
    await userEvent.type(endpoint, 'https://gateway.example.com/anthropic')
    await userEvent.type(screen.getByLabelText('API Key'), 'sk-ant-test')
    await userEvent.click(screen.getByRole('button', { name: '保存并同步模型' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalled())
    const createCall = fetchMock.mock.calls.find(([input]) => String(input).endsWith('/upstreams'))
    expect(JSON.parse(String((createCall?.[1] as RequestInit).body))).toMatchObject({
      provider_kind: 'anthropic-api-key',
      endpoint: 'https://gateway.example.com/anthropic',
    })
  })

  it('distinguishes native Gemini from its OpenAI-compatible endpoint and fixes the native origin', async () => {
    expect(presetForEndpoint('https://generativelanguage.googleapis.com')?.id).toBe('gemini-native')
    expect(presetForEndpoint('https://generativelanguage.googleapis.com/v1beta/openai')?.id).toBe('gemini-openai')
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/upstreams') && init?.method === 'POST') return response({ body: { id: 'gemini-1', name: 'Google Gemini（原生 API）', provider_kind: 'gemini-api-key', endpoint: 'https://generativelanguage.googleapis.com', enabled: true, revision: 1 } })
      if (url.endsWith('/upstreams/gemini-1/discover-models')) return response({ body: { items: [] } })
      throw new Error(`unexpected request ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<CreateUpstream csrf="csrf" onClose={() => undefined} onSaved={() => undefined} />)
    await userEvent.selectOptions(screen.getByLabelText('服务商'), 'gemini-native')
    expect(screen.getByLabelText('API 端点')).toHaveValue('https://generativelanguage.googleapis.com')
    expect(screen.getByLabelText('API 端点')).toHaveAttribute('readonly')
    await userEvent.type(screen.getByLabelText('API Key'), 'gemini-secret')
    await userEvent.click(screen.getByRole('button', { name: '保存并同步模型' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalled())
    const createCall = fetchMock.mock.calls.find(([url, init]) => String(url).endsWith('/upstreams') && (init as RequestInit)?.method === 'POST')
    expect(JSON.parse(String((createCall?.[1] as RequestInit).body))).toMatchObject({
      provider_kind: 'gemini-api-key',
      endpoint: 'https://generativelanguage.googleapis.com',
    })
  })

  it('retries discovery after saving without creating the upstream twice', async () => {
    let discoveryAttempts = 0
    const onSaved = vi.fn()
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/upstreams') && init?.method === 'POST') return response({ body: { id: 'up-1', name: 'DeepSeek', provider_kind: 'openai-compatible', endpoint: 'https://api.deepseek.com/v1', enabled: true, revision: 1 } })
      if (url.endsWith('/upstreams/up-1/discover-models') && init?.method === 'POST') {
        discoveryAttempts += 1
        if (discoveryAttempts === 1) return response({ status: 502, body: { error: { code: 'model_discovery_failed', message: 'redacted' } } })
        return response({ body: { items: [{ id: 'deepseek-chat' }] } })
      }
      if (url.endsWith('/models') && init?.method === 'POST') return response({ body: { id: 'deepseek-chat', upstream_id: 'up-1', upstream_model: 'deepseek-chat', enabled: true } })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<CreateUpstream csrf="csrf" onClose={() => undefined} onSaved={onSaved} />)
    await userEvent.type(screen.getByLabelText('API Key'), 'new-secret')
    await userEvent.click(screen.getByRole('button', { name: '保存并同步模型' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('上游已保存，但模型同步失败：无法从上游读取模型列表，请稍后重试。')
    await userEvent.click(screen.getByRole('button', { name: '重试同步' }))
    expect(await screen.findByText('已同步 1 个模型。不会自动创建或启用任何模型路由。')).toBeInTheDocument()

    const createCalls = fetchMock.mock.calls.filter(([url, init]) => String(url).endsWith('/upstreams') && (init as RequestInit)?.method === 'POST')
    const discoveryCalls = fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/discover-models'))
    expect(createCalls).toHaveLength(1)
    expect(discoveryCalls).toHaveLength(2)
    expect(onSaved).toHaveBeenCalledTimes(1)
    const discoveryInit = discoveryCalls[1][1] as RequestInit
    expect(discoveryInit.body).toBeUndefined()
    expect(new Headers(discoveryInit.headers).get('X-CSRF-Token')).toBe('csrf')

    await userEvent.click(screen.getByLabelText('选择 deepseek-chat'))
    expect(screen.getByLabelText('deepseek-chat 对外模型 ID')).toHaveValue('deepseek-chat')
    await userEvent.click(screen.getByRole('button', { name: '创建所选路由（1）' }))
    expect(await screen.findByText('已创建')).toBeInTheDocument()
    const routeCalls = fetchMock.mock.calls.filter(([url, init]) => String(url).endsWith('/models') && (init as RequestInit)?.method === 'POST')
    expect(routeCalls).toHaveLength(1)
    expect(JSON.parse(String((routeCalls[0][1] as RequestInit).body))).toEqual({ id: 'deepseek-chat', upstream_id: 'up-1', upstream_model: 'deepseek-chat' })
    expect(screen.getByRole('button', { name: '创建所选路由' })).toBeDisabled()
  })

  it('ignores a stale discovery response after switching upstreams and suggests the selected model name', async () => {
    let resolveFirst!: (value: Response) => void
    const firstDiscovery = new Promise<Response>((resolve) => { resolveFirst = resolve })
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/upstreams') && !init?.method) return response({ body: { items: [
        { id: 'up-1', name: 'Alpha', provider_kind: 'openai-compatible', endpoint: 'https://alpha.example/v1', enabled: true, revision: 1 },
        { id: 'up-2', name: 'Beta', provider_kind: 'openai-compatible', endpoint: 'https://beta.example/v1', enabled: true, revision: 1 },
      ] } })
      if (url.endsWith('/upstreams/up-1/discover-models')) return firstDiscovery
      if (url.endsWith('/upstreams/up-2/discover-models')) return response({ body: { items: [{ id: 'beta-model' }] } })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<CreateModel csrf="csrf" onClose={() => undefined} onCreated={() => undefined} />)

    const upstream = await screen.findByLabelText('上游连接')
    await waitFor(() => expect(upstream).toHaveValue('up-1'))
    await userEvent.selectOptions(upstream, 'up-2')
    await waitFor(() => expect(document.querySelector('datalist option[value="beta-model"]')).not.toBeNull())

    resolveFirst(new Response(JSON.stringify({ items: [{ id: 'alpha-model' }] }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    await waitFor(() => expect(document.querySelector('datalist option[value="alpha-model"]')).toBeNull())

    await userEvent.type(screen.getByLabelText('上游模型名称'), 'beta-model')
    expect(screen.getByLabelText('对外模型 ID')).toHaveValue('beta-model')
    await userEvent.clear(screen.getByLabelText('对外模型 ID'))
    await userEvent.type(screen.getByLabelText('对外模型 ID'), 'public-beta')
    expect(screen.getByLabelText('上游模型名称')).toHaveValue('beta-model')
  })

  it('discovers membership models while keeping manual model entry available', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/upstreams') && !init?.method) return response({ body: { items: [
        { id: 'codex-1', name: 'Codex 会员', provider_kind: 'codex-membership', endpoint: 'https://chatgpt.com/backend-api/codex', enabled: true, revision: 1, credential_state: 'imported_unverified', verified_at: null },
      ] } })
      if (url.endsWith('/upstreams/codex-1/discover-models') && init?.method === 'POST') return response({ body: { items: [{ id: 'gpt-5-codex', display_name: 'GPT-5 Codex', upstream_capabilities: { input_modalities: ['text', 'image'] } }] } })
      if (url.endsWith('/models') && init?.method === 'POST') return response({ body: { id: 'codex-model', upstream_id: 'codex-1', upstream_model: 'codex-model', enabled: true } })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const onCreated = vi.fn()
    render(<CreateModel csrf="csrf" onClose={() => undefined} onCreated={onCreated} />)

    expect(await screen.findByText('已同步 1 个模型；请选择或手动输入。')).toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/discover-models'))).toHaveLength(1)
    expect(screen.queryByText(/image/)).not.toBeInTheDocument()
    await userEvent.type(screen.getByLabelText('上游模型名称'), 'codex-model')
    await userEvent.type(screen.getByLabelText('对外模型 ID'), 'codex-model')
    await userEvent.selectOptions(screen.getByLabelText('上游 Wire 协议'), 'openai-responses')
    await userEvent.click(screen.getByRole('button', { name: '添加路由' }))
    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1))
    const create = fetchMock.mock.calls.find(([url, init]) => String(url).endsWith('/models') && (init as RequestInit)?.method === 'POST')
    expect(JSON.parse(String((create?.[1] as RequestInit).body))).toEqual({ id: 'codex-model', upstream_id: 'codex-1', upstream_model: 'codex-model', wire_protocol: 'openai-responses' })
  })
})
