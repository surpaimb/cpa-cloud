import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, type DiscoveredModel, type Upstream } from './api'
import { messageFor } from './hooks'
import { Button, FormError } from './ui'

type ModelDiscoveryProps = {
  upstream: Pick<Upstream, 'id' | 'name'>
  csrf: string
  savedNotice?: boolean
  onRoutesCreated?: () => void
}

export function ModelDiscovery({ upstream, csrf, savedNotice = false, onRoutesCreated }: ModelDiscoveryProps) {
  const [models, setModels] = useState<DiscoveredModel[]>([])
  const [syncing, setSyncing] = useState(true)
  const [syncError, setSyncError] = useState<string | null>(null)
  const [selected, setSelected] = useState<Set<string>>(() => new Set())
  const [routeNames, setRouteNames] = useState<Record<string, string>>({})
  const [created, setCreated] = useState<Set<string>>(() => new Set())
  const [creatingRoutes, setCreatingRoutes] = useState(false)
  const [routeError, setRouteError] = useState<string | null>(null)
  const requestVersion = useRef(0)

  const sync = useCallback(async () => {
    const version = ++requestVersion.current
    setSyncing(true)
    setSyncError(null)
    try {
      const result = await api.discoverUpstreamModels(upstream.id, csrf)
      if (requestVersion.current !== version) return
      const seen = new Set<string>()
      const unique = result.items.filter((item) => {
        if (!item.id || seen.has(item.id)) return false
        seen.add(item.id)
        return true
      })
      setModels(unique)
      setSelected(new Set())
      setRouteNames(Object.fromEntries(unique.map((item) => [item.id, item.id])))
    } catch (caught) {
      if (requestVersion.current === version) setSyncError(messageFor(caught))
    } finally {
      if (requestVersion.current === version) setSyncing(false)
    }
  }, [csrf, upstream.id])

  useEffect(() => {
    void sync()
    return () => { requestVersion.current += 1 }
  }, [sync])

  const pending = useMemo(
    () => models.filter((model) => selected.has(model.id) && !created.has(model.id)),
    [created, models, selected],
  )

  async function createSelectedRoutes() {
    setCreatingRoutes(true)
    setRouteError(null)
    let createdAny = false
    for (const model of pending) {
      try {
        await api.createModel({
          id: routeNames[model.id]?.trim() || model.id,
          upstream_id: upstream.id,
          upstream_model: model.id,
        }, csrf)
        createdAny = true
        setCreated((current) => new Set(current).add(model.id))
      } catch (caught) {
        setRouteError(`模型 ${model.id} 创建失败：${messageFor(caught)}`)
        break
      }
    }
    if (createdAny) onRoutesCreated?.()
    setCreatingRoutes(false)
  }

  if (syncing) return <div className="model-sync-state" role="status"><span className="spinner" />正在同步 {upstream.name} 的模型列表…</div>

  if (syncError) return <div className="model-sync-result">
    <FormError error={`${savedNotice ? '上游已保存，但' : ''}模型同步失败：${syncError}`} />
    <Button type="button" variant="secondary" onClick={() => void sync()}>重试同步</Button>
  </div>

  return <div className="model-sync-result">
    <div className="success-note" role="status">已同步 {models.length} 个模型。不会自动创建或启用任何模型路由。</div>
    {models.length === 0 ? <p className="muted-copy">该上游没有返回可用模型。</p> : <>
      <div className="discovery-list" aria-label="同步到的模型">
        {models.map((model) => {
          const isCreated = created.has(model.id)
          return <div className={`discovery-row ${isCreated ? 'discovery-row--created' : ''}`} key={model.id}>
            <label>
              <input
                type="checkbox"
                aria-label={`选择 ${model.id}`}
                checked={selected.has(model.id) || isCreated}
                disabled={isCreated || creatingRoutes}
                onChange={(event) => setSelected((current) => {
                  const next = new Set(current)
                  if (event.target.checked) next.add(model.id); else next.delete(model.id)
                  return next
                })}
              />
              <code>{model.id}</code>
            </label>
            <input
              aria-label={`${model.id} 对外模型 ID`}
              value={routeNames[model.id] ?? model.id}
              disabled={isCreated || creatingRoutes}
              onChange={(event) => setRouteNames((current) => ({ ...current, [model.id]: event.target.value }))}
            />
            <span>{isCreated ? '已创建' : '待选择'}</span>
          </div>
        })}
      </div>
      <FormError error={routeError} />
      <div className="dialog__actions model-sync-actions">
        <Button type="button" disabled={pending.length === 0 || creatingRoutes} onClick={() => void createSelectedRoutes()}>
          {creatingRoutes ? '正在创建…' : `创建所选路由${pending.length ? `（${pending.length}）` : ''}`}
        </Button>
      </div>
    </>}
  </div>
}
