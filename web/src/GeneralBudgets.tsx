import { useState, type FormEvent } from 'react'
import {
  api,
  ApiError,
  type GeneralBudgetCreate,
  type GeneralBudgetPolicy,
  type GeneralBudgetProtocol,
  type GovernanceScopeKind,
} from './api'
import { Button, EmptyState, Field, FormError } from './ui'

type ScopeTarget = { id: string; label: string }

const protocols: Array<{ value: GeneralBudgetProtocol | ''; label: string }> = [
  { value: '', label: '所有协议' },
  { value: 'openai-chat-completions', label: 'OpenAI Chat Completions' },
  { value: 'openai-responses', label: 'OpenAI Responses' },
  { value: 'anthropic-messages', label: 'Anthropic Messages' },
  { value: 'gemini-generate-content', label: 'Gemini generateContent' },
]

function validSafeLimit(value: string) {
  return /^[1-9][0-9]*$/.test(value) && BigInt(value) <= 9007199254740991n
}

function validCost(value: string) {
  return /^[1-9][0-9]*$/.test(value) && BigInt(value) <= 9223372036854775807n
}

function budgetError(error: unknown) {
  if (error instanceof ApiError && error.status === 404) return '当前服务尚未提供 selector-aware 通用预算。旧治理预算仍保持原行为。'
  if (error instanceof ApiError && error.code === 'operation_conflict') return '该操作编号已绑定其他写入；请保留当前载荷并核对原回执。'
  if (error instanceof ApiError && error.code === 'revision_conflict') return '预算策略已被更新，请重新读取后再编辑。'
  if (error instanceof ApiError && error.code === 'resource_conflict') return '作用范围不存在，或重叠策略使用了不同币种。'
  return '通用预算操作未完成；未知写入结果不会自动更换操作编号。'
}

export function GeneralBudgets({ csrf, employees, keys, groups }: {
  csrf: string
  employees: ScopeTarget[]
  keys: ScopeTarget[]
  groups: ScopeTarget[]
}) {
  const [loaded, setLoaded] = useState(false)
  const [items, setItems] = useState<GeneralBudgetPolicy[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState<GeneralBudgetCreate | null>(null)
  const [kind, setKind] = useState<GovernanceScopeKind>('employee')
  const [scopeID, setScopeID] = useState('')
  const [protocol, setProtocol] = useState<GeneralBudgetProtocol | ''>('')
  const [model, setModel] = useState('')
  const [tokens, setTokens] = useState('')
  const [cost, setCost] = useState('')
  const [currency, setCurrency] = useState('')

  const targets = kind === 'employee' ? employees : kind === 'key' ? keys : groups

  async function load() {
    setLoading(true); setError(null)
    try {
      const page = await api.generalBudgets(undefined, 100)
      setItems(page.items); setLoaded(true)
    } catch (caught) { setError(budgetError(caught)) }
    finally { setLoading(false) }
  }

  async function send(body: GeneralBudgetCreate) {
    setLoading(true); setError(null); setPending(body)
    try {
      const receipt = await api.createGeneralBudget(body, csrf)
      await api.generalBudget(receipt.resource_id)
      setPending(null); setTokens(''); setCost(''); setCurrency(''); setModel('')
      await load()
    } catch (caught) { setError(budgetError(caught)) }
    finally { setLoading(false) }
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    if (!scopeID || tokens === '' && cost === '') { setError('请选择作用对象，并至少配置一个 Token 或成本限制。'); return }
    if (tokens !== '' && !validSafeLimit(tokens)) { setError('Token 限制必须是 1 到 9007199254740991 的整数。'); return }
    if (cost !== '' && (!validCost(cost) || !/^[A-Z]{3}$/.test(currency))) { setError('成本限制必须是规范正整数，并配三个大写 ASCII 字母币种。'); return }
    const body: GeneralBudgetCreate = {
      operation_id: crypto.randomUUID(), scope_kind: kind, scope_id: scopeID,
      protocol: protocol || null, model: model || null, enabled: true,
      token_limit: tokens ? Number(tokens) : null, cost_limit_micro: cost || null, currency: cost ? currency : null,
    }
    void send(body)
  }

  return <section className="content-panel governance-panel" aria-labelledby="general-budget-title">
    <div className="section-heading"><div><h2 id="general-budget-title">通用预算（selector-aware）</h2><p>复用现有预留与结算引擎；同一请求命中的员工、Key、治理组和协议/模型策略全部取最严格结果。Strict 请求无法证明上界时会在派发前拒绝。</p></div>{!loaded ? <Button variant="secondary" disabled={loading} onClick={() => void load()}>{loading ? '读取中…' : '读取通用预算'}</Button> : null}</div>
    <FormError error={error} />
    {pending && error ? <div className="governance-recovery" role="status"><strong>写入结果尚未确认</strong><code>{pending.operation_id}</code><Button type="button" variant="secondary" disabled={loading} onClick={() => void send(pending)}>用原操作与原载荷重试</Button></div> : null}
    {loaded ? <>
      {items.length === 0 ? <EmptyState title="还没有通用预算策略" body="旧治理预算保持兼容；新策略可进一步按协议和公开模型收窄。" /> : <div className="table-scroll"><table className="governance-table"><thead><tr><th>作用范围</th><th>Selector</th><th>Token</th><th>成本</th><th>状态 / 版本</th></tr></thead><tbody>{items.map((item) => <tr key={item.id}><td>{item.scope.kind} · <code>{item.scope.id}</code></td><td>{item.scope.protocol ?? '所有协议'}<small>{item.scope.model ?? '所有公开模型'}</small></td><td>{item.token.limit ?? '未配置'}<small>{item.token.window ?? ''}</small></td><td>{item.cost.limit_micro ? `${item.cost.limit_micro} μ ${item.cost.currency}` : '未配置'}<small>{item.cost.window ?? ''}</small></td><td>{item.enabled ? 'strict 启用' : '停用'}<small>r{item.revision}</small></td></tr>)}</tbody></table></div>}
      <form className="governance-budget-form" onSubmit={submit}>
        <Field label="作用范围"><select value={kind} onChange={(event) => { setKind(event.target.value as GovernanceScopeKind); setScopeID('') }}><option value="employee">员工</option><option value="key">Key</option><option value="group">治理组</option></select></Field>
        <Field label="作用对象"><select value={scopeID} onChange={(event) => setScopeID(event.target.value)}><option value="">请选择</option>{targets.map((target) => <option key={target.id} value={target.id}>{target.label}</option>)}</select></Field>
        <Field label="协议 selector"><select value={protocol} onChange={(event) => setProtocol(event.target.value as GeneralBudgetProtocol | '')}>{protocols.map((item) => <option key={item.value || 'all'} value={item.value}>{item.label}</option>)}</select></Field>
        <Field label="公开模型 selector" hint="留空表示所有公开模型。"><input value={model} onChange={(event) => setModel(event.target.value)} /></Field>
        <Field label="60 秒 Token 上限"><input inputMode="numeric" value={tokens} onChange={(event) => setTokens(event.target.value)} placeholder="不配置" /></Field>
        <Field label="24 小时成本上限（micro）"><input inputMode="numeric" value={cost} onChange={(event) => setCost(event.target.value)} placeholder="不配置" /></Field>
        <Field label="币种"><input maxLength={3} value={currency} disabled={!cost} onChange={(event) => setCurrency(event.target.value)} placeholder={cost ? 'USD' : '未配置成本'} /></Field>
        <Button type="submit" disabled={loading || Boolean(pending)}>{loading ? '保存中…' : '创建 strict 预算'}</Button>
      </form>
    </> : null}
  </section>
}
