import { useEffect, useState, type FormEvent } from 'react'
import { api, ApiError, type BillingOwner, type BillingPlan, type BillingPlanWrite, type BillingSubscription } from '../api'
import { Button, Dialog, EmptyState, Field, FormError, PageState } from '../ui'
import { ConfirmWrite, OwnerFields, billingError, formatMicro, ownerLabel, ownerValid, validMicro } from './common'
import { cleanBillingOwner } from './BillingWallet'

export function BillingPlans({ csrf, commercialEnabled }: { csrf: string; commercialEnabled: boolean }) {
  const [plans, setPlans] = useState<BillingPlan[]>([])
  const [subscriptions, setSubscriptions] = useState<BillingSubscription[]>([])
  const [planCursor, setPlanCursor] = useState<string | null>(null)
  const [subscriptionCursor, setSubscriptionCursor] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<BillingPlan | 'new' | null>(null)
  const [subscribing, setSubscribing] = useState(false)
  const [cancelling, setCancelling] = useState<BillingSubscription | null>(null)

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
      {plans.length === 0 && !loading && !error ? <EmptyState title="还没有套餐" body="创建套餐后，管理员可为指定归属对象购买。" /> : plans.length ? <div className="table-scroll"><table><thead><tr><th>套餐</th><th>价格</th><th>授予额度</th><th>周期 / 状态</th><th>操作</th></tr></thead><tbody>{plans.map((plan) => <tr key={plan.id}><td><strong>{plan.name}</strong><small><code>{plan.id}</code> · r{plan.revision}</small></td><td>{formatMicro(plan.price_micro, plan.currency)}</td><td>{formatMicro(plan.credit_micro, plan.currency)}</td><td>{plan.interval === 'monthly' ? '每月（当前不自动续费）' : '一次性'}<small>{plan.enabled ? '可购买' : '已停用'}</small></td><td><button className="link-button" onClick={() => setEditing(plan)}>编辑</button></td></tr>)}</tbody></table></div> : null}
      {planCursor ? <div className="billing-load-more"><Button variant="secondary" disabled={loading} onClick={() => void morePlans()}>加载更多套餐</Button></div> : null}
    </section>
    <section className="content-panel billing-panel">
      <div className="section-heading"><div><h2>订阅</h2><p>管理员购买会先从钱包扣套餐价格，再授予套餐额度；本里程碑不运行自动续费任务。</p></div><Button disabled={!commercialEnabled || !plans.some((item) => item.enabled)} onClick={() => setSubscribing(true)}>购买套餐</Button></div>
      {!commercialEnabled ? <div className="billing-disabled-inline">商业执行总开关已关闭；仍可配置套餐，但不能购买。</div> : null}
      {subscriptions.length === 0 && !loading && !error ? <EmptyState title="还没有订阅" body="购买动作需要相同币种的钱包余额足够。" /> : subscriptions.length ? <div className="table-scroll"><table><thead><tr><th>订阅</th><th>价格 / 额度</th><th>周期</th><th>状态</th><th>操作</th></tr></thead><tbody>{subscriptions.map((item) => <tr key={item.id}><td><code>{item.id}</code><small>套餐 {item.plan_id} r{item.plan_revision}</small></td><td>{formatMicro(item.price_micro, item.currency)}<small>额度 {formatMicro(item.credit_micro, item.currency)}</small></td><td>{item.interval === 'monthly' ? '每月（不自动续费）' : '一次性'}</td><td>{item.status === 'active' ? '有效' : '已取消'}<small>r{item.revision}</small></td><td>{item.status === 'active' ? <button className="link-button" onClick={() => setCancelling(item)}>取消</button> : '—'}</td></tr>)}</tbody></table></div> : null}
      {subscriptionCursor ? <div className="billing-load-more"><Button variant="secondary" disabled={loading} onClick={() => void moreSubscriptions()}>加载更多订阅</Button></div> : null}
    </section>
    {editing ? <PlanEditor csrf={csrf} current={editing === 'new' ? undefined : editing} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); void load() }} /> : null}
    {subscribing ? <SubscriptionEditor csrf={csrf} plans={plans.filter((item) => item.enabled)} onClose={() => setSubscribing(false)} onSaved={() => { setSubscribing(false); void load() }} /> : null}
    {cancelling ? <CancelSubscription csrf={csrf} item={cancelling} onClose={() => setCancelling(null)} onSaved={() => { setCancelling(null); void load() }} /> : null}
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
