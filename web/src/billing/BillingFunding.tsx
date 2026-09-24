import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { api, ApiError, type BillingConnector, type BillingOwner, type BillingRefund, type BillingTopUp } from '../api'
import { Button, Dialog, EmptyState, Field, FormError, PageState } from '../ui'
import { ConfirmWrite, OwnerFields, billingError, formatMicro, ownerLabel, ownerValid, validMicro } from './common'
import { cleanBillingOwner } from './BillingWallet'

export function BillingFunding({ csrf, commercialEnabled }: { csrf: string; commercialEnabled: boolean }) {
  const [connectors, setConnectors] = useState<BillingConnector[]>([])
  const [topups, setTopups] = useState<BillingTopUp[]>([])
  const [refunds, setRefunds] = useState<BillingRefund[]>([])
  const [cursors, setCursors] = useState({ connectors: null as string | null, topups: null as string | null, refunds: null as string | null })
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [connectorEditor, setConnectorEditor] = useState<BillingConnector | 'new' | null>(null)
  const [topupEditor, setTopupEditor] = useState(false)
  const [refunding, setRefunding] = useState<BillingTopUp | null>(null)

  async function load() {
    setLoading(true); setError(null)
    try {
      const [connectorPage, topupPage, refundPage] = await Promise.all([api.billingConnectors(undefined, 50), api.billingTopUps(undefined, 50), api.billingRefunds(undefined, 50)])
      setConnectors(connectorPage.items); setTopups(topupPage.items); setRefunds(refundPage.items)
      setCursors({ connectors: connectorPage.next_cursor, topups: topupPage.next_cursor, refunds: refundPage.next_cursor })
    } catch (caught) { setError(billingError(caught)) }
    finally { setLoading(false) }
  }
  useEffect(() => { void load() }, [])

  async function more(kind: keyof typeof cursors) {
    const cursor = cursors[kind]; if (!cursor) return
    setLoading(true); setError(null)
    try {
      if (kind === 'connectors') { const page = await api.billingConnectors(cursor, 50); setConnectors((items) => [...items, ...page.items]); setCursors((value) => ({ ...value, connectors: page.next_cursor })) }
      if (kind === 'topups') { const page = await api.billingTopUps(cursor, 50); setTopups((items) => [...items, ...page.items]); setCursors((value) => ({ ...value, topups: page.next_cursor })) }
      if (kind === 'refunds') { const page = await api.billingRefunds(cursor, 50); setRefunds((items) => [...items, ...page.items]); setCursors((value) => ({ ...value, refunds: page.next_cursor })) }
    } catch (caught) { setError(billingError(caught)) } finally { setLoading(false) }
  }

  return <div className="billing-stack">
    <div className="billing-synthetic-notice"><strong>仅合成回调测试契约</strong><p>连接器只验证本地 HMAC 回调和账本结算行为；这里没有真实支付服务商、收款页面、退款出款或生产资金动作。</p></div>
    <PageState loading={loading && connectors.length === 0 && topups.length === 0} error={error} onRetry={() => void load()} />
    <section className="content-panel billing-panel">
      <div className="section-heading"><div><h2>测试连接器</h2><p>回调密钥只在提交时发送，不回显、不保存在浏览器持久层。</p></div><Button onClick={() => setConnectorEditor('new')}>添加测试连接器</Button></div>
      {connectors.length === 0 && !loading ? <EmptyState title="还没有测试连接器" body="创建连接器后可生成待支付充值记录；不会发起真实收费。" /> : <div className="table-scroll"><table><thead><tr><th>名称</th><th>状态</th><th>版本</th><th>操作</th></tr></thead><tbody>{connectors.map((item) => <tr key={item.id}><td><strong>{item.name}</strong><small><code>{item.id}</code></small></td><td>{item.enabled ? '测试可用' : '已停用'}</td><td>r{item.revision}</td><td><button className="link-button" onClick={() => setConnectorEditor(item)}>编辑</button></td></tr>)}</tbody></table></div>}
      {cursors.connectors ? <div className="billing-load-more"><Button variant="secondary" disabled={loading} onClick={() => void more('connectors')}>加载更多连接器</Button></div> : null}
    </section>
    <section className="content-panel billing-panel">
      <div className="section-heading"><div><h2>充值记录</h2><p>创建动作只生成 pending 记录；paid 只能由合成测试回调结算。</p></div><Button disabled={!commercialEnabled || !connectors.some((item) => item.enabled)} onClick={() => setTopupEditor(true)}>创建待支付记录</Button></div>
      {!commercialEnabled ? <div className="billing-disabled-inline">商业执行总开关已关闭；不能创建充值或退款冲正。</div> : null}
      {topups.length === 0 && !loading ? <EmptyState title="还没有充值记录" body="本页面不会连接真实支付服务商。" /> : <div className="table-scroll"><table><thead><tr><th>支付记录</th><th>金额</th><th>状态</th><th>已退款 / 可退款</th><th>操作</th></tr></thead><tbody>{topups.map((item) => {
        const remaining = remainingRefund(item)
        const refundable = commercialEnabled && (item.status === 'paid' || item.status === 'partially_refunded') && remaining !== null && BigInt(remaining) > 0n
        return <tr key={item.id}><td><code>{item.payment_id}</code><small>{item.external_reference || '外部引用未知'} · r{item.revision}</small></td><td>{formatMicro(item.amount_micro, item.currency)}</td><td>{topUpStatusLabel(item.status)}<small>{item.paid_at ? new Date(item.paid_at).toLocaleString() : '未支付'}</small></td><td>{formatMicro(item.refunded_micro, item.currency)}<small>{remaining === null ? '剩余额度未知' : `剩余 ${formatMicro(remaining, item.currency)}`}</small></td><td>{refundable ? <button className="link-button" onClick={() => setRefunding(item)}>退款冲正</button> : '—'}</td></tr>
      })}</tbody></table></div>}
      {cursors.topups ? <div className="billing-load-more"><Button variant="secondary" disabled={loading} onClick={() => void more('topups')}>加载更多充值</Button></div> : null}
    </section>
    <section className="content-panel billing-panel">
      <div className="section-heading"><div><h2>退款冲正记录</h2><p>这里记录钱包负向冲正；不会调用外部支付服务商出款。</p></div></div>
      {refunds.length === 0 && !loading ? <EmptyState title="还没有退款冲正" body="部分与全额退款都会保留独立不可变记录。" /> : <div className="table-scroll"><table><thead><tr><th>时间</th><th>支付 ID</th><th>冲正金额</th><th>账本流水</th></tr></thead><tbody>{refunds.map((item) => { const topup = topups.find((value) => value.payment_id === item.payment_id); return <tr key={item.id}><td>{new Date(item.created_at).toLocaleString()}</td><td><code>{item.payment_id}</code></td><td>−{formatMicro(item.amount_micro, topup?.currency ?? null)}</td><td><code>{item.entry_id}</code></td></tr> })}</tbody></table></div>}
      {cursors.refunds ? <div className="billing-load-more"><Button variant="secondary" disabled={loading} onClick={() => void more('refunds')}>加载更多退款</Button></div> : null}
    </section>
    {connectorEditor ? <ConnectorEditor csrf={csrf} current={connectorEditor === 'new' ? undefined : connectorEditor} onClose={() => setConnectorEditor(null)} onSaved={() => { setConnectorEditor(null); void load() }} /> : null}
    {topupEditor ? <TopUpEditor csrf={csrf} connectors={connectors.filter((item) => item.enabled)} onClose={() => setTopupEditor(false)} onSaved={() => { setTopupEditor(false); void load() }} /> : null}
    {refunding ? <RefundEditor csrf={csrf} topup={refunding} onClose={() => setRefunding(null)} onSaved={() => { setRefunding(null); void load() }} /> : null}
  </div>
}

function remainingRefund(item: BillingTopUp) {
  try { const value = BigInt(item.amount_micro) - BigInt(item.refunded_micro); return value >= 0n ? value.toString() : null } catch { return null }
}

function topUpStatusLabel(status: BillingTopUp['status']) {
  if (status === 'pending') return '待合成回调'
  if (status === 'paid') return '合成回调已结算'
  if (status === 'partially_refunded') return '已部分退款冲正'
  return '已全额退款冲正'
}

function ConnectorEditor({ csrf, current, onClose, onSaved }: { csrf: string; current?: BillingConnector; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(current?.name ?? '')
  const [secret, setSecret] = useState('')
  const [enabled, setEnabled] = useState(current?.enabled ?? true)
  const [pending, setPending] = useState<{ operation_id: string; expected_revision?: number; name: string; webhook_secret: string | null; enabled: boolean } | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  function submit(event: FormEvent) { event.preventDefault(); if (!name.trim() || (!current && secret.length < 32) || secret && secret.length < 32) { setError('名称不能为空；新密钥或替换密钥必须至少 32 个字符。'); return }; const body = { operation_id: crypto.randomUUID(), name: name.trim(), webhook_secret: secret || null, enabled, ...(current ? { expected_revision: current.revision } : {}) }; void save(body) }
  async function save(body: NonNullable<typeof pending>) { setBusy(true); setError(null); setPending(body); try { if (current) await api.updateBillingConnector(current.id, body as typeof body & { expected_revision: number }, csrf); else await api.createBillingConnector({ ...body, webhook_secret: body.webhook_secret! }, csrf); setPending(null); setSecret(''); onSaved() } catch (caught) { setError(billingError(caught)); if (caught instanceof ApiError) setPending(null) } finally { setBusy(false) } }
  return <Dialog title={current ? `编辑测试连接器：${current.name}` : '添加测试连接器'} description="仅用于本地合成 HMAC 回调；密钥提交后不回显。" onClose={onClose} closeDisabled={busy}><form className="form-grid" onSubmit={submit}><Field label="名称"><input value={name} disabled={busy || pending !== null} onChange={(event) => setName(event.target.value)} autoFocus /></Field><Field label={current ? '替换回调密钥（可留空）' : '回调密钥'} hint="32–256 字符；不要使用真实支付凭据。"><input type="password" autoComplete="new-password" value={secret} disabled={busy || pending !== null} onChange={(event) => setSecret(event.target.value)} /></Field><label className="toggle-field"><input type="checkbox" checked={enabled} disabled={busy || pending !== null} onChange={(event) => setEnabled(event.target.checked)} /><span><strong>允许合成回调</strong><small>这不代表真实支付渠道已接入。</small></span></label><FormError error={error} /><div className="dialog__actions billing-dialog-actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>取消</Button>{pending ? <Button type="button" disabled={busy} onClick={() => void save(pending)}>用原操作编号重试</Button> : <Button type="submit" disabled={busy}>保存测试连接器</Button>}</div></form></Dialog>
}

function TopUpEditor({ csrf, connectors, onClose, onSaved }: { csrf: string; connectors: BillingConnector[]; onClose: () => void; onSaved: () => void }) {
  const [owner, setOwner] = useState<BillingOwner>({ kind: 'employee', employee_id: '' })
  const [connectorID, setConnectorID] = useState(connectors[0]?.id ?? '')
  const [currency, setCurrency] = useState('USD')
  const [amount, setAmount] = useState('')
  const [confirming, setConfirming] = useState<{ operation_id: string; owner: BillingOwner; connector_id: string; currency: string; amount_micro: string } | null>(null)
  const [pending, setPending] = useState<typeof confirming>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  function prepare(event: FormEvent) { event.preventDefault(); const clean = cleanBillingOwner(owner); const unit = currency.trim().toUpperCase(); if (!ownerValid(clean) || !connectorID || !/^[A-Z]{3}$/.test(unit) || !validMicro(amount)) { setError('请填写完整归属、测试连接器、币种和正整数金额。'); return }; setConfirming({ operation_id: crypto.randomUUID(), owner: clean, connector_id: connectorID, currency: unit, amount_micro: amount }) }
  async function send(body: NonNullable<typeof confirming>) { setBusy(true); setError(null); setPending(body); try { await api.createBillingTopUp(body, csrf); setPending(null); onSaved() } catch (caught) { setError(billingError(caught)); if (caught instanceof ApiError) setPending(null) } finally { setBusy(false) } }
  return <>{!confirming ? <Dialog title="创建待支付充值记录" description="此操作不扣款、不增加余额，也不会联系支付服务商。" onClose={onClose}><form className="form-grid" onSubmit={prepare}><OwnerFields owner={owner} onChange={setOwner} /><Field label="测试连接器"><select value={connectorID} onChange={(event) => setConnectorID(event.target.value)}>{connectors.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field><div className="billing-two-columns"><Field label="币种"><input maxLength={3} value={currency} onChange={(event) => setCurrency(event.target.value.toUpperCase())} /></Field><Field label="金额（micro）"><input inputMode="numeric" value={amount} onChange={(event) => setAmount(event.target.value)} /></Field></div><FormError error={error} /><div className="dialog__actions"><Button type="button" variant="secondary" onClick={onClose}>取消</Button><Button type="submit">核对记录</Button></div></form></Dialog> : null}
    {confirming ? <ConfirmWrite title="确认创建待支付记录" description="结果只是一条 pending 测试记录；不会发生真实收费。" confirmLabel="创建 pending 记录" busy={busy} error={error} pendingRetry={pending !== null} onConfirm={() => void send(confirming)} onRetry={() => pending && void send(pending)} onClose={onClose} details={<dl className="billing-confirm-list"><div><dt>对象</dt><dd>{ownerLabel(confirming.owner)}</dd></div><div><dt>金额</dt><dd>{formatMicro(confirming.amount_micro, confirming.currency)}</dd></div><div><dt>测试连接器</dt><dd><code>{confirming.connector_id}</code></dd></div></dl>} /> : null}</>
}

function RefundEditor({ csrf, topup, onClose, onSaved }: { csrf: string; topup: BillingTopUp; onClose: () => void; onSaved: () => void }) {
  const remaining = useMemo(() => remainingRefund(topup), [topup])
  const [amount, setAmount] = useState(remaining ?? '')
  const [body, setBody] = useState<{ operation_id: string; payment_id: string; amount_micro: string } | null>(null)
  const [pending, setPending] = useState<typeof body>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  function prepare(event: FormEvent) { event.preventDefault(); if (!remaining || !validMicro(amount) || BigInt(amount) > BigInt(remaining)) { setError('退款金额必须是正整数，且不超过剩余可退款额度。'); return }; setBody({ operation_id: crypto.randomUUID(), payment_id: topup.payment_id, amount_micro: amount }) }
  async function send(value: NonNullable<typeof body>) { setBusy(true); setError(null); setPending(value); try { await api.createBillingRefund(value, csrf); setPending(null); onSaved() } catch (caught) { setError(billingError(caught)); if (caught instanceof ApiError) setPending(null) } finally { setBusy(false) } }
  return <>{!body ? <Dialog title="退款冲正" description="这是钱包负向账本冲正，不会向外部支付服务商发起退款。" onClose={onClose}><form className="form-grid" onSubmit={prepare}><div className="billing-confirm"><strong>支付 {topup.payment_id}</strong><p>原充值 {formatMicro(topup.amount_micro, topup.currency)}；已退款 {formatMicro(topup.refunded_micro, topup.currency)}；剩余 {formatMicro(remaining, topup.currency)}。</p></div><Field label="本次退款（micro）"><input inputMode="numeric" value={amount} onChange={(event) => setAmount(event.target.value)} /></Field><FormError error={error} /><div className="dialog__actions"><Button type="button" variant="secondary" onClick={onClose}>取消</Button><Button type="submit" variant="danger">核对冲正</Button></div></form></Dialog> : null}
    {body ? <ConfirmWrite title="确认退款冲正" description="成功后钱包余额减少；不会发生外部资金动作。" confirmLabel="确认负向冲正" danger busy={busy} error={error} pendingRetry={pending !== null} onConfirm={() => void send(body)} onRetry={() => pending && void send(pending)} onClose={onClose} details={<dl className="billing-confirm-list"><div><dt>支付 ID</dt><dd><code>{body.payment_id}</code></dd></div><div><dt>钱包减少</dt><dd>−{formatMicro(body.amount_micro, topup.currency)}</dd></div><div><dt>冲正后剩余</dt><dd>{formatMicro((BigInt(remaining ?? '0') - BigInt(body.amount_micro)).toString(), topup.currency)}</dd></div><div><dt>外部退款</dt><dd>不会调用支付服务商</dd></div></dl>} /> : null}</>
}
