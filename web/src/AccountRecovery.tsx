// Independent implementation of CPA Cloud's bounded account recovery controls.
import { useCallback, useState } from 'react'
import { api, ApiError, type AccountRecoveryState } from './api'
import { useResource } from './hooks'
import { Button, FormError, PageState } from './ui'

const attention: Record<string, string> = {
  authentication_required: '需要检查凭据或重新授权',
  configuration_changed: '账号或模型路径已变更，请核对配置',
  unsupported: '当前协议不支持自动恢复',
  protocol_error: '生成响应未通过协议校验',
  retry_limit: '本次事件已达到三次探测上限',
  history_full: '探测历史已达上限',
  settlement_pending: '结算结果待确认，不会重发当前请求',
  snapshot_missing: '缺少可验证的生成路径',
  storage_unavailable: '存储状态需要核对，自动探测已暂停',
}
const results: Record<string, string> = {
  generation_ok: '生成检查通过', rate_limited: '上游限流', upstream_unavailable: '上游不可用',
  upstream_timeout: '上游超时', cancelled: '已取消', interrupted: '执行已中断',
  authentication_failed: '凭据未通过检查', configuration_changed: '配置已变化',
  unsupported: '协议不支持', protocol_error: '响应格式无效',
}
function when(value: string | null) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '时间不可用' : date.toLocaleString('zh-CN')
}
export function RecoveryStateSummary({ state }: { state: AccountRecoveryState }) {
  return <div className="recovery-state">
    <strong>恢复隔离中</strong>
    <small>冷却到期仍需验证恢复；清除隔离不代表生成成功。异常重启后的占用可能仍需等待租约到期。</small>
    <small>{state.attention_code ? (attention[state.attention_code] ?? '需要管理员核对恢复状态') : state.state === 'in_progress' ? '探测或结算进行中' : state.auto_eligible ? '等待后台检查与容量准入' : '自动恢复当前不可执行'}</small>
    <small>本事件尝试 {state.attempt_count} / 3 · 下次检查 {when(state.next_probe_at)}</small>
    {state.last_result_code ? <small>最近结果：{results[state.last_result_code] ?? '请核对服务端状态'} · {when(state.last_finished_at)}</small> : null}
  </div>
}

export function AccountRecoveryPanel({ csrf }: { csrf: string }) {
  const load = useCallback(async () => {
    const [status, accounts] = await Promise.all([api.accountRecovery(), api.accountRecoveryAccounts()])
    return { status, accounts: accounts.items }
  }, [])
  const { data, setData, loading, error, reload } = useResource(load)
  const [busy, setBusy] = useState(false)
  const [writeError, setWriteError] = useState<string | null>(null)
  const [uncertain, setUncertain] = useState(false)
  async function refresh() {
    setBusy(true)
    try { const next = await load(); setData(next); setUncertain(false); setWriteError(null) }
    catch { setWriteError('无法读取当前设置，请稍后重新核对。') }
    finally { setBusy(false) }
  }
  async function toggle() {
    if (!data || busy || uncertain) return
    setBusy(true); setWriteError(null)
    try {
      const status = await api.setAccountRecovery(!data.status.enabled, data.status.setting_revision, csrf)
      setData({ ...data, status })
      // The setting response is authoritative. Refresh isolation independently
      // without interpreting a failed read as a failed mutation.
      try { const accounts = await api.accountRecoveryAccounts(); setData({ status, accounts: accounts.items }) }
      catch { setWriteError('设置已保存，但隔离列表未刷新，请重新读取状态。') }
    } catch (caught) {
      setUncertain(true)
      setWriteError(caught instanceof ApiError && caught.code === 'revision_conflict'
        ? '设置已被其他操作更新，请重新读取状态后再操作。'
        : caught instanceof ApiError && caught.code === 'recovery_not_allowed'
          ? '服务尚未允许自动恢复，请核对启动参数后重新读取状态。'
          : '设置结果未确认，请重新读取状态；不要假定开关已经改变。')
    } finally { setBusy(false) }
  }
  return <section className="content-panel recovery-panel" aria-labelledby="recovery-title">
    <h2 id="recovery-title">账号自动恢复</h2>
    <p>对显式账号池中失败的生成路径进行有界探测，成功后解除隔离。探测会消耗上游用量，独立记账；每个冷却事件最多三次。</p>
    <PageState loading={loading} error={error} onRetry={() => void reload()} />
    {data ? <>
      <dl className="recovery-facts">
        <div><dt>服务启动许可</dt><dd>{data.status.cli_allowed ? '已允许' : '未允许'}</dd></div>
        <div><dt>管理员设置</dt><dd>{data.status.enabled ? '已启用' : '已关闭'}</dd></div>
        <div><dt>后台任务</dt><dd>{data.status.running ? '运行中' : '未运行'}</dd></div>
        <div><dt>探测历史</dt><dd>{data.status.history_count} / 10000{data.status.history_full ? ' · 已满' : ''}</dd></div>
      </dl>
      {!data.status.cli_allowed ? <p>需要以 <code>--allow-account-recovery</code> 启动服务，才能在此开启。</p> : null}
      <p className="muted-copy">关闭后保留历史与隔离状态。下次唤醒：{when(data.status.next_wake_at)}。列表时间：{when(data.status.server_time)}。</p>
      <div className="page-header-buttons">
        <Button disabled={busy || uncertain || (!data.status.cli_allowed && !data.status.enabled)} onClick={() => void toggle()}>{busy ? '处理中…' : data.status.enabled ? '关闭自动恢复' : '启用自动恢复'}</Button>
        <Button variant="secondary" disabled={busy} onClick={() => void refresh()}>重新读取状态</Button>
      </div>
      <FormError error={writeError} />
      <h3>恢复隔离账号</h3>
      {data.accounts.length === 0 ? <p>当前没有恢复隔离记录。</p> : <div className="table-scroll"><table><thead><tr><th>账号 / 模型</th><th>状态</th></tr></thead><tbody>{data.accounts.map(state => <tr key={state.account_id}>
        <td><code>{state.account_id}</code><div>{state.public_model} → {state.upstream_model}</div><small>{state.protocol}</small></td>
        <td><RecoveryStateSummary state={state} /></td>
      </tr>)}</tbody></table></div>}
    </> : null}
  </section>
}
