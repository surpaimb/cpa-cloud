import { useEffect, useState, type FormEvent } from 'react'
import { api, ApiError, type BillingOneShotRenewal, type BillingOwner, type BillingPlan, type BillingPlanWrite, type BillingSubscription } from '../api'
import { Button, Dialog, EmptyState, Field, FormError, PageState } from '../ui'
import { ConfirmWrite, OwnerFields, billingError, formatMicro, ownerLabel, ownerValid, validMicro } from './common'
import { cleanBillingOwner } from './BillingWallet'

export function BillingPlans({ csrf, commercialEnabled, oneShotRenewal = false }: { csrf: string; commercialEnabled: boolean; oneShotRenewal?: boolean }) {
  const [plans, setPlans] = useState<BillingPlan[]>([])
  const [subscriptions, setSubscriptions] = useState<BillingSubscription[]>([])
  const [planCursor, setPlanCursor] = useState<string | null>(null)
  const [subscriptionCursor, setSubscriptionCursor] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<BillingPlan | 'new' | null>(null)
  const [subscribing, setSubscribing] = useState(false)
  const [cancelling, setCancelling] = useState<BillingSubscription | null>(null)
  const [renewing, setRenewing] = useState<BillingSubscription | null>(null)
  const [managingOneShot, setManagingOneShot] = useState<BillingSubscription | null>(null)

  async function load() {
    setLoading(true); setError(null)
    try {
      const [planPage, subscriptionPage] = await Promise.all([api.billingPlans(undefined, 50), api.billingSubscriptions(undefined, 50)])
      setPlans(planPage.items); setPlanCursor(planPage.next_cursor)
      setSubscriptions(subscriptionPage.items); setSubscriptionCursor(subscriptionPage.next_cursor)
    } catch (caught) { setError(billingError(caught)) }
    finally { setLoading(false) }
  }
  useEffect(() => { void load() }, [])

  async function morePlans() {
    if (!planCursor) return
    setLoading(true)
    try { const page = await api.billingPlans(planCursor, 50); setPlans((items) => [...items, ...page.items]); setPlanCursor(page.next_cursor) }
    catch (caught) { setError(billingError(caught)) } finally { setLoading(false) }
  }
  async function moreSubscriptions() {
    if (!subscriptionCursor) return
    setLoading(true)
    try { const page = await api.billingSubscriptions(subscriptionCursor, 50); setSubscriptions((items) => [...items, ...page.items]); setSubscriptionCursor(page.next_cursor) }
    catch (caught) { setError(billingError(caught)) } finally { setLoading(false) }
  }

  return <div className="billing-stack">
    <PageState loading={loading && plans.length === 0 && subscriptions.length === 0} error={error} onRetry={() => void load()} />
    <section className="content-panel billing-panel">
      <div className="section-heading"><div><h2>套餐</h2><p>套餐保存价格和授予额度的快照；金额是 microcurrency 整数字符串。</p></div><Button onClick={() => setEditing('new')}>创建套餐</Button></div>
      {plans.length === 0 && !loading && !error ? <EmptyState title="还没有套餐" body="创建套餐后，管理员可为指定归属对象购买。" /> : plans.length ? <div className="table-scroll"><table><thead><tr><th>套餐</th><th>价格</th><th>授予额度</th><th>周期 / 状态</th><th>操作</th></tr></thead><tbody>{plans.map((plan) => <tr key={plan.id}><td><strong>{plan.name}</strong><small><code>{plan.id}</code> · r{plan.revision}</small></td><td>{formatMicro(plan.price_micro, plan.currency)}</td><td>{formatMicro(plan.credit_micro, plan.currency)}</td><td>{plan.interval === 'monthly' ? oneShotRenewal ? '每月（仅显式单次预约）' : '每月（当前不自动续费）' : '一次性'}<small>{plan.enabled ? '可购买' : '已停用'}</small></td><td><button className="link-button" onClick={() => setEditing(plan)}>编辑</button></td></tr>)}</tbody></table></div> : null}
      {planCursor ? <div className="billing-load-more"><Button variant="secondary" disabled={loading} onClick={() => void morePlans()}>加载更多套餐</Button></div> : null}
    </section>
    <section className="content-panel billing-panel">
      <div className="section-heading"><div><h2>订阅</h2><p>管理员购买会先从钱包扣套餐价格，再授予套餐额度；{oneShotRenewal ? '只有显式预约的月订阅才会在到期后尝试一次钱包续购。' : '本里程碑不运行自动续费任务。'}</p></div><Button disabled={!commercialEnabled || !plans.some((item) => item.enabled)} onClick={() => setSubscribing(true)}>购买套餐</Button></div>
      {!commercialEnabled ? <div className="billing-disabled-inline">商业执行总开关已关闭；仍可配置套餐，既有月订阅仍会按冻结时间到期，但不能购买、手工续购或新预约。已预约的到期执行会记录未成交终态，可在提交前解除预约。</div> : null}
      {subscriptions.length === 0 && !loading && !error ? <EmptyState title="还没有订阅" body="购买动作需要相同币种的钱包余额足够。" /> : subscriptions.length ? <div className="table-scroll"><table className="billing-subscription-table"><thead><tr><th>订阅 / 来源</th><th>价格 / 额度</th><th>周期 / 新一期</th><th>状态</th><th>操作</th></tr></thead><tbody>{subscriptions.map((item) => <tr key={item.id}><td><code>{item.id}</code><small>套餐 {item.plan_id} r{item.plan_revision}</small>{item.predecessor_id ? <small>来源：<code>{item.predecessor_id}</code></small> : null}{item.successor_id ? <small>续购到：<code>{item.successor_id}</code></small> : null}</td><td>{formatMicro(item.price_micro, item.currency)}<small>额度 {formatMicro(item.credit_micro, item.currency)}</small></td><td>{item.interval === 'monthly' ? oneShotRenewal ? '单月（可单次预约）' : '单月（不自动续费）' : '一次性'}<small>开始 {new Date(item.started_at).toLocaleString()}</small><small>{item.period_end_at ? `到期 ${new Date(item.period_end_at).toLocaleString()}` : '无到期日'}</small></td><td>{item.status === 'active' ? '有效' : item.status === 'expired' ? '已到期' : '已取消'}<small>r{item.revision}</small></td><td><div className="billing-row-actions">{item.status === 'active' ? <button className="link-button" onClick={() => setCancelling(item)}>取消</button> : null}{item.status === 'expired' && item.interval === 'monthly' && !item.successor_id && commercialEnabled && plans.some((plan) => plan.id === item.plan_id && plan.enabled && plan.interval === 'monthly') ? <button className="link-button" onClick={() => setRenewing(item)}>手工续购一期</button> : null}{oneShotRenewal && item.interval === 'monthly' ? <button className="link-button" onClick={() => setManagingOneShot(item)}>一次性预约状态</button> : null}{item.status !== 'active' && !(item.status === 'expired' && item.interval === 'monthly' && !item.successor_id && commercialEnabled && plans.some((plan) => plan.id === item.plan_id && plan.enabled && plan.interval === 'monthly')) && !(oneShotRenewal && item.interval === 'monthly') ? '—' : null}</div></td></tr>)}</tbody></table></div> : null}
      {subscriptionCursor ? <div className="billing-load-more"><Button variant="secondary" disabled={loading} onClick={() => void moreSubscriptions()}>加载更多订阅</Button></div> : null}
    </section>
    {editing ? <PlanEditor csrf={csrf} current={editing === 'new' ? undefined : editing} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); void load() }} /> : null}
    {subscribing ? <SubscriptionEditor csrf={csrf} plans={plans.filter((item) => item.enabled)} onClose={() => setSubscribing(false)} onSaved={() => { setSubscribing(false); void load() }} /> : null}
    {cancelling ? <CancelSubscription csrf={csrf} item={cancelling} onClose={() => setCancelling(null)} onSaved={() => { setCancelling(null); void load() }} /> : null}
    {renewing ? <RenewSubscriptionDialog csrf={csrf} item={renewing} plan={plans.find((plan) => plan.id === renewing.plan_id)} onClose={() => setRenewing(null)} onSaved={() => { setRenewing(null); void load() }} /> : null}
    {managingOneShot ? <OneShotRenewalDialog csrf={csrf} item={managingOneShot} commercialEnabled={commercialEnabled} plan={plans.find((plan) => plan.id === managingOneShot.plan_id)} onClose={() => setManagingOneShot(null)} onSaved={() => void load()} /> : null}
  </div>
}

function PlanEditor({ csrf, current, onClose, onSaved }: { csrf: string; current?: BillingPlan; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(current?.name ?? '')
  const [currency, setCurrency] = useState(current?.currency ?? 'USD')
  const [price, setPrice] = useState(current?.price_micro ?? '')
  const [credit, setCredit] = useState(current?.credit_micro ?? '')
  const [interval, setInterval] = useState<BillingPlan['interval']>(current?.interval ?? 'one_time')
  const [enabled, setEnabled] = useState(current?.enabled ?? true)
  const [pending, setPending] = useState<(BillingPlanWrite & { expected_revision?: number }) | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  function submit(event: FormEvent) {
    event.preventDefault(); setError(null)
    const nextCurrency = currency.trim().toUpperCase()
    if (!name.trim() || !/^[A-Z]{3}$/.test(nextCurrency) || !validMicro(price) || !validMicro(credit)) { setError('请填写套餐名称、三个大写字母币种，以及 int64 范围内的正整数价格和额度。'); return }
    const body = { operation_id: crypto.randomUUID(), name: name.trim(), currency: nextCurrency, price_micro: price, credit_micro: credit, interval, enabled, ...(current ? { expected_revision: current.revision } : {}) }
    void save(body)
  }
  async function save(body: BillingPlanWrite & { expected_revision?: number }) {
    setBusy(true); setError(null); setPending(body)
    try {
      if (current) await api.updateBillingPlan(current.id, body as BillingPlanWrite & { expected_revision: number }, csrf)
      else await api.createBillingPlan(body, csrf)
      setPending(null); onSaved()
    } catch (caught) { setError(billingError(caught)); if (caught instanceof ApiError) setPending(null) }
    finally { setBusy(false) }
  }
  return <Dialog title={current ? `编辑套餐：${current.name}` : '创建套餐'} description={current ? `expected revision：${current.revision}` : '创建后可由管理员购买；不会开放员工自助。'} onClose={onClose} closeDisabled={busy}>
    <form className="form-grid" onSubmit={submit}>
      <Field label="套餐名称"><input value={name} disabled={busy || pending !== null} onChange={(event) => setName(event.target.value)} autoFocus /></Field>
      <div className="billing-two-columns"><Field label="币种"><input maxLength={3} value={currency} disabled={busy || pending !== null} onChange={(event) => setCurrency(event.target.value.toUpperCase())} /></Field><Field label="周期"><select value={interval} disabled={busy || pending !== null} onChange={(event) => setInterval(event.target.value as BillingPlan['interval'])}><option value="one_time">一次性</option><option value="monthly">每月（不自动续费）</option></select></Field></div>
      <div className="billing-two-columns"><Field label="价格（micro）"><input inputMode="numeric" value={price} disabled={busy || pending !== null} onChange={(event) => setPrice(event.target.value)} /></Field><Field label="授予额度（micro）"><input inputMode="numeric" value={credit} disabled={busy || pending !== null} onChange={(event) => setCredit(event.target.value)} /></Field></div>
      <label className="toggle-field"><input type="checkbox" checked={enabled} disabled={busy || pending !== null} onChange={(event) => setEnabled(event.target.checked)} /><span><strong>允许购买</strong><small>停用不会改写既有订阅。</small></span></label>
      <FormError error={error} />
      <div className="dialog__actions billing-dialog-actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>取消</Button>{pending ? <Button type="button" disabled={busy} onClick={() => void save(pending)}>用原操作编号重试</Button> : <Button type="submit" disabled={busy}>{busy ? '保存中…' : '保存套餐'}</Button>}</div>
    </form>
  </Dialog>
}

function SubscriptionEditor({ csrf, plans, onClose, onSaved }: { csrf: string; plans: BillingPlan[]; onClose: () => void; onSaved: () => void }) {
  const [owner, setOwner] = useState<BillingOwner>({ kind: 'employee', employee_id: '' })
  const [planID, setPlanID] = useState(plans[0]?.id ?? '')
  const [confirming, setConfirming] = useState<{ operation_id: string; owner: BillingOwner; plan_id: string } | null>(null)
  const [pending, setPending] = useState<typeof confirming>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const plan = plans.find((item) => item.id === planID)
  function prepare(event: FormEvent) { event.preventDefault(); const clean = cleanBillingOwner(owner); if (!ownerValid(clean) || !plan) { setError('请选择套餐并填写完整归属对象。'); return }; setConfirming({ operation_id: crypto.randomUUID(), owner: clean, plan_id: plan.id }) }
  async function send(body: NonNullable<typeof confirming>) { setBusy(true); setError(null); setPending(body); try { await api.createBillingSubscription(body, csrf); setPending(null); onSaved() } catch (caught) { setError(billingError(caught)); if (caught instanceof ApiError) setPending(null) } finally { setBusy(false) } }
  return <>{!confirming ? <Dialog title="购买套餐" description="购买会同时记录钱包扣款和额度入账，不会触发真实支付。" onClose={onClose}><form className="form-grid" onSubmit={prepare}><OwnerFields owner={owner} onChange={setOwner} /><Field label="套餐"><select value={planID} onChange={(event) => setPlanID(event.target.value)}>{plans.map((item) => <option key={item.id} value={item.id}>{item.name} · {formatMicro(item.price_micro, item.currency)} → {formatMicro(item.credit_micro, item.currency)}</option>)}</select></Field><FormError error={error} /><div className="dialog__actions"><Button type="button" variant="secondary" onClick={onClose}>取消</Button><Button type="submit">核对购买</Button></div></form></Dialog> : null}
    {confirming && plan ? <ConfirmWrite title="确认购买套餐" description="需要钱包余额足够；成功后使用套餐当前 revision 的价格与额度快照。" confirmLabel="确认购买" busy={busy} error={error} pendingRetry={pending !== null} onConfirm={() => void send(confirming)} onRetry={() => pending && void send(pending)} onClose={onClose} details={<dl className="billing-confirm-list"><div><dt>归属对象</dt><dd>{ownerLabel(confirming.owner)}</dd></div><div><dt>套餐</dt><dd>{plan.name} · r{plan.revision}</dd></div><div><dt>钱包扣款</dt><dd>−{formatMicro(plan.price_micro, plan.currency)}</dd></div><div><dt>额度入账</dt><dd>+{formatMicro(plan.credit_micro, plan.currency)}</dd></div></dl>} /> : null}</>
}

function CancelSubscription({ csrf, item, onClose, onSaved }: { csrf: string; item: BillingSubscription; onClose: () => void; onSaved: () => void }) {
  const initial = { operation_id: crypto.randomUUID(), expected_revision: item.revision }
  const [pending, setPending] = useState<typeof initial | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  async function send(body: typeof initial) { setBusy(true); setError(null); setPending(body); try { await api.cancelBillingSubscription(item.id, body, csrf); setPending(null); onSaved() } catch (caught) { setError(billingError(caught)); if (caught instanceof ApiError) setPending(null) } finally { setBusy(false) } }
  return <ConfirmWrite title="确认取消订阅" description="取消只更新订阅状态，不自动退款或撤销既有额度。" confirmLabel="确认取消" danger busy={busy} error={error} pendingRetry={pending !== null} onConfirm={() => void send(initial)} onRetry={() => pending && void send(pending)} onReload={onSaved} onClose={onClose} details={<dl className="billing-confirm-list"><div><dt>订阅</dt><dd><code>{item.id}</code></dd></div><div><dt>套餐</dt><dd>{item.plan_id} · r{item.plan_revision}</dd></div><div><dt>expected revision</dt><dd>{item.revision}</dd></div></dl>} />
}

function RenewSubscriptionDialog({ csrf, item, plan, onClose, onSaved }: { csrf: string; item: BillingSubscription; plan?: BillingPlan; onClose: () => void; onSaved: () => void }) {
  const [operation] = useState(() => ({ operation_id: crypto.randomUUID() }))
  const [pending, setPending] = useState<typeof operation | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  async function send(body: typeof operation) { setBusy(true); setError(null); setPending(body); try { await api.renewBillingSubscription(item.id, body, csrf); setPending(null); onSaved() } catch (caught) { setError(billingError(caught)); if (caught instanceof ApiError) setPending(null) } finally { setBusy(false) } }
  return <ConfirmWrite title="确认手工续购一期" description="仅为这条已到期月订阅的原归属对象购买新一期；从当前时间开始，不补齐过期间隔，不会自动续费或开通模型权限。" confirmLabel="确认续购" busy={busy} error={error} pendingRetry={pending !== null} onConfirm={() => void send(operation)} onRetry={() => pending && void send(pending)} onReload={onSaved} onClose={onClose} details={<dl className="billing-confirm-list"><div><dt>前一期</dt><dd><code>{item.id}</code></dd></div><div><dt>已到期</dt><dd>{item.period_end_at ? new Date(item.period_end_at).toLocaleString() : '—'}</dd></div><div><dt>当前套餐</dt><dd>{plan ? `${plan.name} · r${plan.revision}` : item.plan_id}</dd></div><div><dt>本次钱包扣款</dt><dd>{plan ? `−${formatMicro(plan.price_micro, plan.currency)}` : '以服务端当前套餐为准'}</dd></div><div><dt>本次额度入账</dt><dd>{plan ? `+${formatMicro(plan.credit_micro, plan.currency)}` : '以服务端当前套餐为准'}</dd></div></dl>} />
}

const oneShotStateLabel: Record<BillingOneShotRenewal['state'], string> = {
  none: '未预约', armed: '已预约，等待到期尝试', disarmed: '已解除（不可重新预约同一期）',
  succeeded: '续购成功', failed: '未成交（终态）', superseded: '管理员手工续购先完成', cancelled: '前一期已取消',
}

const oneShotReasonLabel: Record<string, string> = {
  disarmed: '管理员已解除', commercial_disabled: '到期执行时商业开关关闭', plan_unavailable: '到期执行时当前套餐不可用',
  insufficient_balance: '到期执行时钱包余额不足', owner_unavailable: '归属对象已不可用',
  period_unrepresentable: '下一期结束时间不可表示', manual_renewal: '管理员手工续购先完成', predecessor_cancelled: '前一期已取消',
}

function OneShotRenewalDialog({ csrf, item, plan, commercialEnabled, onClose, onSaved }: { csrf: string; item: BillingSubscription; plan?: BillingPlan; commercialEnabled: boolean; onClose: () => void; onSaved: () => void }) {
  const [status, setStatus] = useState<BillingOneShotRenewal | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [confirming, setConfirming] = useState<{ kind: 'arm' | 'disarm'; body: { operation_id: string; expected_revision: number } } | null>(null)
  const [pending, setPending] = useState<typeof confirming>(null)
  const [busy, setBusy] = useState(false)

  async function reload() {
    setLoading(true); setError(null)
    try { setStatus(await api.billingOneShotRenewal(item.id)) }
    catch (caught) { setError(billingError(caught)) }
    finally { setLoading(false) }
  }
  useEffect(() => { void reload() }, [item.id])

  async function send(intent: NonNullable<typeof confirming>) {
    setBusy(true); setError(null); setPending(intent)
    try {
      const result = intent.kind === 'arm'
        ? await api.armBillingOneShotRenewal(item.id, intent.body, csrf)
        : await api.disarmBillingOneShotRenewal(item.id, intent.body, csrf)
      setStatus(result.one_shot_renewal); setConfirming(null); setPending(null); onSaved()
    } catch (caught) { setError(billingError(caught)); if (caught instanceof ApiError) setPending(null) }
    finally { setBusy(false) }
  }

  if (confirming) return <ConfirmWrite
    title={confirming.kind === 'arm' ? '确认预约一次到期续购' : '确认解除一次性预约'}
    description={confirming.kind === 'arm' ? '到期后仅尝试一次钱包续购；按执行时当前套餐价格和币种扣款，余额不足或商业开关关闭会成为不会自动重试的未成交终态。后继订阅不会继承预约。' : '解除后此订阅无法再次预约；若尚未成交，可在确认提交前阻止自动扣款。已成交不能撤销。'}
    confirmLabel={confirming.kind === 'arm' ? '确认预约一次' : '确认解除且不再预约'}
    danger={confirming.kind === 'disarm'} busy={busy} error={error} pendingRetry={pending !== null}
    onConfirm={() => void send(confirming)} onRetry={() => pending && void send(pending)}
    onReload={() => { setConfirming(null); setPending(null); void reload(); onSaved() }} onClose={() => { setConfirming(null); setPending(null); setError(null) }}
    details={<dl className="billing-confirm-list"><div><dt>前一期</dt><dd><code>{item.id}</code></dd></div><div><dt>到期时刻</dt><dd>{status?.due_at ? new Date(status.due_at).toLocaleString() : '—'}</dd></div><div><dt>当前套餐仅供参考</dt><dd>{plan ? `${plan.name} · ${formatMicro(plan.price_micro, plan.currency)}` : item.plan_id}</dd></div><div><dt>预约性质</dt><dd>一次性、不会传给后继</dd></div></dl>}
  />

  return <Dialog title="一次性到期续购预约" description="这里只读取单条订阅的预约状态；操作前会再次由服务端核验资格。" onClose={onClose}>
    <div className="form-grid">
      <PageState loading={loading} error={error} onRetry={() => void reload()} />
      {status ? <dl className="billing-confirm-list"><div><dt>订阅</dt><dd><code>{item.id}</code></dd></div><div><dt>预约状态</dt><dd>{oneShotStateLabel[status.state]}</dd></div><div><dt>到期时刻</dt><dd>{status.due_at ? new Date(status.due_at).toLocaleString() : '—'}</dd></div><div><dt>结果</dt><dd>{status.reason ? oneShotReasonLabel[status.reason] ?? status.reason : '—'}</dd></div>{status.successor_id ? <div><dt>新一期</dt><dd><code>{status.successor_id}</code></dd></div> : null}</dl> : null}
      {status?.state === 'disarmed' ? <p className="billing-disabled-inline">解除预约是终态：同一前一期不可再次预约。后续新一期可以独立预约。</p> : null}
      <div className="dialog__actions billing-dialog-actions">
        <Button type="button" variant="secondary" onClick={onClose}>关闭</Button>
        <Button type="button" variant="secondary" disabled={loading} onClick={() => void reload()}>刷新状态</Button>
        {status?.state === 'none' && item.status === 'active' && !item.successor_id && commercialEnabled ? <Button type="button" onClick={() => setConfirming({ kind: 'arm', body: { operation_id: crypto.randomUUID(), expected_revision: item.revision } })}>预约一次</Button> : null}
        {status?.state === 'armed' ? <Button type="button" variant="danger" onClick={() => setConfirming({ kind: 'disarm', body: { operation_id: crypto.randomUUID(), expected_revision: status.revision } })}>解除预约</Button> : null}
      </div>
    </div>
  </Dialog>
}
