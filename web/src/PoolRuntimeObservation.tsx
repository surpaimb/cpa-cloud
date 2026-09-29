// Independently authored UI for docs/account-pool-runtime-observation-contract.md.
import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type PoolRuntimeBlockReason, type PoolRuntimeObservation } from './api'
import { Button } from './ui'

const reasonLabels: Record<PoolRuntimeBlockReason, string> = {
  model_disabled: '模型已停用',
  upstream_disabled: '账号已停用',
  cooldown_active: '冷却中',
  recovery_isolated: '恢复隔离中',
  membership_disabled: '会员能力未启用',
  reauth_required: '凭据需重新授权',
  capacity_reserved: '本机持久槽位已占满',
}

function validObservation(value: PoolRuntimeObservation, modelID: string) {
  if (!value || value.model_id !== modelID || !['explicit_pool', 'legacy_no_pool', 'model_disabled'].includes(value.pool_status) || !Number.isSafeInteger(value.model_revision) || !Number.isSafeInteger(value.pool_revision) || Number.isNaN(Date.parse(value.as_of)) || !Array.isArray(value.items) || value.items.length > 64) return false
  if (value.pool_status === 'legacy_no_pool') return value.pool_revision === 0 && value.items.length === 0
  if (value.pool_revision < 1 || value.items.length === 0) return false
  return value.items.every((item) => item && typeof item.upstream_id === 'string' && item.upstream_id.length > 0 && Number.isSafeInteger(item.account_revision) && item.account_revision > 0 && Number.isInteger(item.configured_max_concurrency) && item.configured_max_concurrency >= 1 && item.configured_max_concurrency <= 1024 && (item.global_max_concurrency === null || Number.isInteger(item.global_max_concurrency) && item.global_max_concurrency >= 1) && Number.isSafeInteger(item.request_reservations) && item.request_reservations >= 0 && Number.isSafeInteger(item.maintenance_reservations) && item.maintenance_reservations >= 0 && (item.remaining_local_slots === null || Number.isInteger(item.remaining_local_slots) && item.remaining_local_slots >= 0) && Array.isArray(item.block_reasons) && item.block_reasons.every((reason) => Object.prototype.hasOwnProperty.call(reasonLabels, reason)) && (item.cooldown_until === null || typeof item.cooldown_until === 'string' && !Number.isNaN(Date.parse(item.cooldown_until))))
}

function utcLabel(value: string) { return new Date(value).toISOString().replace('.000Z', 'Z') }

export function PoolRuntimeObservationPanel({ modelID, savedRevision }: { modelID: string; savedRevision: number }) {
  const [snapshot, setSnapshot] = useState<PoolRuntimeObservation | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(false)
  const requestVersion = useRef(0)
  const activeController = useRef<AbortController | null>(null)

  const reload = useCallback(async () => {
    const version = ++requestVersion.current
    activeController.current?.abort()
    const controller = new AbortController()
    activeController.current = controller
    setSnapshot(null)
    setLoading(true)
    setError(false)
    try {
      const next = await api.modelPoolRuntime(modelID, controller.signal)
      if (requestVersion.current !== version) return
      if (!validObservation(next, modelID)) throw new Error('invalid pool runtime observation')
      setSnapshot(next)
    } catch (caught) {
      if (requestVersion.current === version && !(caught instanceof DOMException && caught.name === 'AbortError')) setError(true)
    } finally {
      if (requestVersion.current === version) setLoading(false)
    }
  }, [modelID])

  useEffect(() => {
    void reload()
    return () => { requestVersion.current += 1; activeController.current?.abort() }
  }, [reload, savedRevision])

  return <section className="pool-runtime-observation" aria-label="本机账号池容量快照">
    <div className="pool-runtime-heading"><div><h3>本机账号池容量快照</h3><p>仅展示持久租约与已知本地阻断；不是实时 HTTP 请求数，也不是供应商额度或账号可用性。</p></div><Button type="button" variant="secondary" onClick={() => void reload()} disabled={loading}>刷新快照</Button></div>
    {loading ? <p role="status" className="muted-copy">正在读取本机快照…</p> : null}
    {error ? <p role="alert" className="pool-runtime-error">快照读取失败，未显示旧数据。请重试。</p> : null}
    {snapshot ? <>
      <p className="pool-runtime-meta">快照时间（UTC）：{utcLabel(snapshot.as_of)} · 模型 r{snapshot.model_revision} · 池 r{snapshot.pool_revision}{snapshot.pool_revision !== savedRevision ? ' · 池配置已在别处改变，请重新加载编辑器' : ''}</p>
      {snapshot.pool_status === 'legacy_no_pool' ? <p className="pool-runtime-limited">此模型仍使用旧单路由，没有显式账号池；不推算容量或余额。</p> : null}
      {snapshot.pool_status === 'model_disabled' ? <p className="pool-runtime-limited">模型已停用，以下仅为已保存路由与持久记录；没有可派发槽位结论。</p> : null}
      {snapshot.items.length ? <div className="pool-runtime-list">{snapshot.items.map((item) => <article className="pool-runtime-route" key={item.upstream_id}>
        <header><strong><code>{item.upstream_id}</code></strong><small>账号 r{item.account_revision}</small></header>
        <dl><div><dt>本模型配置并发</dt><dd>{item.configured_max_concurrency}</dd></div><div><dt>跨模型全局上限</dt><dd>{item.global_max_concurrency ?? '不适用'}</dd></div><div><dt>请求预留</dt><dd>{item.request_reservations}</dd></div><div><dt>维护预留</dt><dd>{item.maintenance_reservations}</dd></div><div><dt>未预留本机槽位</dt><dd>{item.remaining_local_slots ?? '不适用'}</dd></div></dl>
        <p className="pool-runtime-reasons"><span>本地阻断：</span>{item.block_reasons.length ? item.block_reasons.map((reason) => reasonLabels[reason]).join('、') : '未发现列出的本地阻断（仍须经过权限与实际派发检查）'}</p>
        {item.cooldown_until ? <p className="pool-runtime-cooldown">冷却截至（UTC）：{utcLabel(item.cooldown_until)}</p> : null}
      </article>)}</div> : null}
    </> : null}
  </section>
}
