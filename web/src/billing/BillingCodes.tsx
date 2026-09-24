import { useEffect, useState, type FormEvent } from 'react'
import { api, ApiError, type BillingOwner, type BillingRedemptionCode } from '../api'
import { Button, Dialog, EmptyState, Field, FormError, Icon, PageState } from '../ui'
import { ConfirmWrite, OwnerFields, billingError, formatMicro, ownerLabel, ownerValid, useOneTimeCopy, validMicro } from './common'
import { cleanBillingOwner } from './BillingWallet'

export function BillingCodes({ csrf, commercialEnabled }: { csrf: string; commercialEnabled: boolean }) {
  const [items, setItems] = useState<BillingRedemptionCode[]>([])
  const [nextCursor, setNextCursor] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [redeeming, setRedeeming] = useState(false)
  async function load(after?: string) { setLoading(true); setError(null); try { const page = await api.billingRedemptionCodes(after, 50); setItems((value) => after ? [...value, ...page.items] : page.items); setNextCursor(page.next_cursor) } catch (caught) { setError(billingError(caught)) } finally { setLoading(false) } }
  useEffect(() => { void load() }, [])
  return <div className="billing-stack">
    <PageState loading={loading && items.length === 0} error={error} onRetry={() => void load()} />
    <section className="content-panel billing-panel">
      <div className="section-heading"><div><h2>兑换码</h2><p>明文只在创建成功响应中显示一次；列表仅包含额度、使用次数与状态。</p></div><div className="page-header-buttons"><Button variant="secondary" disabled={!commercialEnabled} onClick={() => setRedeeming(true)}>管理员兑换</Button><Button onClick={() => setCreating(true)}>创建兑换码</Button></div></div>
      {!commercialEnabled ? <div className="billing-disabled-inline">商业执行总开关已关闭；仍可创建兑换码，但不能兑换入账。</div> : null}
      {items.length === 0 && !loading && !error ? <EmptyState title="还没有兑换码" body="创建后请立即安全保存明文；刷新页面无法找回。" /> : items.length ? <div className="table-scroll"><table><thead><tr><th>兑换码记录</th><th>额度</th><th>使用</th><th>有效期</th><th>状态</th></tr></thead><tbody>{items.map((item) => <tr key={item.id}><td><code>{item.id}</code><small>{new Date(item.created_at).toLocaleString()}</small></td><td>{formatMicro(item.amount_micro, item.currency)}</td><td>{item.uses} / {item.max_uses}</td><td>{item.expires_at ? new Date(item.expires_at).toLocaleString() : '永不过期'}</td><td>{item.enabled && item.uses < item.max_uses ? '可兑换' : '不可兑换'}</td></tr>)}</tbody></table></div> : null}
      {nextCursor ? <div className="billing-load-more"><Button variant="secondary" disabled={loading} onClick={() => void load(nextCursor)}>加载更多兑换码</Button></div> : null}
    </section>
    {creating ? <CodeCreator csrf={csrf} onClose={() => setCreating(false)} onSaved={() => void load()} /> : null}
    {redeeming ? <CodeRedeemer csrf={csrf} onClose={() => setRedeeming(false)} onSaved={() => { setRedeeming(false); void load() }} /> : null}
  </div>
}

function CodeCreator({ csrf, onClose, onSaved }: { csrf: string; onClose: () => void; onSaved: () => void }) {
  const [currency, setCurrency] = useState('USD')
  const [amount, setAmount] = useState('')
  const [maxUses, setMaxUses] = useState('1')
  const [expires, setExpires] = useState('')
  const [pending, setPending] = useState<{ operation_id: string; currency: string; amount_micro: string; max_uses: number; expires_at: string | null } | null>(null)
  const [plaintext, setPlaintext] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const clipboard = useOneTimeCopy()
  function submit(event: FormEvent) { event.preventDefault(); const unit = currency.trim().toUpperCase(); const uses = Number(maxUses); let expiresAt: string | null = null; if (expires) { const parsed = new Date(expires); if (Number.isNaN(parsed.getTime())) { setError('失效时间无效。'); return }; expiresAt = parsed.toISOString() }; if (!/^[A-Z]{3}$/.test(unit) || !validMicro(amount) || !/^[1-9]\d*$/.test(maxUses) || !Number.isSafeInteger(uses)) { setError('请填写币种、正整数额度和至少 1 次的安全整数使用次数。'); return }; void save({ operation_id: crypto.randomUUID(), currency: unit, amount_micro: amount, max_uses: uses, expires_at: expiresAt }) }
  async function save(body: NonNullable<typeof pending>) { setBusy(true); setError(null); setPending(body); try { const result = await api.createBillingRedemptionCode(body, csrf); setPending(null); if (result.code) { setPlaintext(result.code); onSaved() } else { setError('服务确认这是原操作的重放，因此不会再次显示兑换码明文。请使用首次响应中已保存的明文。') } } catch (caught) { setError(billingError(caught)); if (caught instanceof ApiError) setPending(null) } finally { setBusy(false) } }
  if (plaintext) return <Dialog title="兑换码只显示这一次" description="关闭后无法从页面或列表找回。请保存到受控的秘密管理位置。" onClose={() => { setPlaintext(null); onClose() }}><div className="billing-secret" data-testid="redemption-plaintext"><code>{plaintext}</code><Button variant="secondary" onClick={() => void clipboard.copy(plaintext)}><Icon name="copy" />{clipboard.copied ? '已复制' : '复制兑换码'}</Button></div><div className="key-warning">不要把兑换码粘贴到日志、工单或共享聊天。它拥有实际钱包入账能力。</div><div className="dialog__actions"><Button onClick={() => { setPlaintext(null); onClose() }}>我已安全保存，关闭</Button></div></Dialog>
  return <Dialog title="创建兑换码" description="创建后明文只在成功响应中返回一次。" onClose={onClose} closeDisabled={busy}><form className="form-grid" onSubmit={submit}><div className="billing-two-columns"><Field label="币种"><input maxLength={3} value={currency} disabled={busy || pending !== null} onChange={(event) => setCurrency(event.target.value.toUpperCase())} /></Field><Field label="每次额度（micro）"><input inputMode="numeric" value={amount} disabled={busy || pending !== null} onChange={(event) => setAmount(event.target.value)} /></Field></div><div className="billing-two-columns"><Field label="最大使用次数"><input inputMode="numeric" value={maxUses} disabled={busy || pending !== null} onChange={(event) => setMaxUses(event.target.value)} /></Field><Field label="失效时间" hint="留空表示不过期。"><input type="datetime-local" value={expires} disabled={busy || pending !== null} onChange={(event) => setExpires(event.target.value)} /></Field></div><FormError error={error} />{pending && error ? <div className="billing-recovery"><strong>结果未知时不可生成新操作编号</strong><code>{pending.operation_id}</code></div> : null}<div className="dialog__actions billing-dialog-actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>取消</Button>{pending ? <Button type="button" disabled={busy} onClick={() => void save(pending)}>用原操作编号重试</Button> : <Button type="submit" disabled={busy}>创建并显示一次</Button>}</div></form></Dialog>
}

function CodeRedeemer({ csrf, onClose, onSaved }: { csrf: string; onClose: () => void; onSaved: () => void }) {
  const [owner, setOwner] = useState<BillingOwner>({ kind: 'employee', employee_id: '' })
  const [code, setCode] = useState('')
  const [body, setBody] = useState<{ operation_id: string; owner: BillingOwner; code: string } | null>(null)
  const [pending, setPending] = useState<typeof body>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  function prepare(event: FormEvent) { event.preventDefault(); const clean = cleanBillingOwner(owner); if (!ownerValid(clean) || code.length < 16) { setError('请填写完整归属对象和有效兑换码。'); return }; setBody({ operation_id: crypto.randomUUID(), owner: clean, code }) }
  async function send(value: NonNullable<typeof body>) { setBusy(true); setError(null); setPending(value); try { await api.redeemBillingCode(value, csrf); setCode(''); setPending(null); onSaved() } catch (caught) { setError(billingError(caught)); if (caught instanceof ApiError) setPending(null) } finally { setBusy(false) } }
  return <>{!body ? <Dialog title="管理员兑换" description="兑换成功会为指定对象的钱包新增不可变入账流水。" onClose={onClose}><form className="form-grid" onSubmit={prepare}><OwnerFields owner={owner} onChange={setOwner} /><Field label="兑换码"><input type="password" autoComplete="off" value={code} onChange={(event) => setCode(event.target.value)} /></Field><FormError error={error} /><div className="dialog__actions"><Button type="button" variant="secondary" onClick={onClose}>取消</Button><Button type="submit">核对兑换</Button></div></form></Dialog> : null}
    {body ? <ConfirmWrite title="确认兑换入账" description="兑换码不会出现在确认摘要、日志或持久浏览器状态中。" confirmLabel="确认兑换" busy={busy} error={error} pendingRetry={pending !== null} onConfirm={() => void send(body)} onRetry={() => pending && void send(pending)} onClose={onClose} details={<dl className="billing-confirm-list"><div><dt>归属对象</dt><dd>{ownerLabel(body.owner)}</dd></div><div><dt>动作</dt><dd>兑换码额度入账</dd></div><div><dt>兑换码</dt><dd>已隐藏</dd></div></dl>} /> : null}</>
}
