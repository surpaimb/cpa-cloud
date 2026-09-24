import { useState, type FormEvent } from 'react'
import { api, ApiError, type BillingAdjustment, type BillingBalance, type BillingEntry, type BillingOwner } from '../api'
import { Button, EmptyState, Field, FormError } from '../ui'
import { ConfirmWrite, OwnerFields, billingError, formatMicro, ownerLabel, ownerValid, validMicro } from './common'

type AdjustmentWrite = { operation_id: string; owner: BillingOwner; currency: string; amount_micro: string }

const kindLabels: Record<string, string> = {
  adjustment_credit: '人工增加', adjustment_debit: '人工扣减', topup: '充值入账', redemption: '兑换入账',
  subscription_charge: '套餐扣款', subscription_credit: '套餐额度', refund: '退款冲正', usage_charge: '用量扣款',
}

export function BillingWallet({ csrf }: { csrf: string }) {
  const [owner, setOwner] = useState<BillingOwner>({ kind: 'employee', employee_id: '' })
  const [currency, setCurrency] = useState('USD')
  const [balance, setBalance] = useState<BillingBalance | null>(null)
  const [entries, setEntries] = useState<BillingEntry[]>([])
  const [nextCursor, setNextCursor] = useState<string | null>(null)
  const [ledgerAvailable, setLedgerAvailable] = useState<boolean | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [amount, setAmount] = useState('')
  const [confirming, setConfirming] = useState<AdjustmentWrite | null>(null)
  const [pending, setPending] = useState<AdjustmentWrite | null>(null)
  const [writeError, setWriteError] = useState<string | null>(null)
  const [writeBusy, setWriteBusy] = useState(false)
  const [result, setResult] = useState<BillingAdjustment | null>(null)

  async function read(nextOwner = owner, nextCurrency = currency, after?: string) {
    setLoading(true); setError(null)
    try {
      const [balanceResult, entriesResult] = await Promise.allSettled([
        api.billingBalance(nextOwner, nextCurrency),
        api.billingEntries(nextOwner, nextCurrency, after),
      ])
      if (balanceResult.status === 'rejected') throw balanceResult.reason
      setBalance(balanceResult.value)
      if (entriesResult.status === 'fulfilled') {
        setEntries((current) => after ? [...current, ...entriesResult.value.items] : entriesResult.value.items)
        setNextCursor(entriesResult.value.next_cursor)
        setLedgerAvailable(true)
      } else if (entriesResult.reason instanceof ApiError && entriesResult.reason.status === 404) {
        if (!after) setEntries([])
        setNextCursor(null); setLedgerAvailable(false)
      } else {
        setLedgerAvailable(null)
      }
    } catch (caught) { setError(billingError(caught)); setBalance(null) }
    finally { setLoading(false) }
  }

  function lookup(event: FormEvent) {
    event.preventDefault()
    const cleanOwner = cleanBillingOwner(owner)
    const cleanCurrency = currency.trim().toUpperCase()
    if (!ownerValid(cleanOwner) || !/^[A-Z]{3}$/.test(cleanCurrency)) { setError('请填写完整归属对象和三个大写字母币种。'); return }
    setOwner(cleanOwner); setCurrency(cleanCurrency); setEntries([]); setNextCursor(null); setResult(null)
    void read(cleanOwner, cleanCurrency)
  }

  function prepareAdjustment(event: FormEvent) {
    event.preventDefault(); setWriteError(null)
    if (!balance) { setWriteError('请先读取余额。'); return }
    if (!validMicro(amount, true)) { setWriteError('调整金额必须是非零 int64 十进制整数；负数表示扣减。'); return }
    setConfirming({ operation_id: crypto.randomUUID(), owner: cleanBillingOwner(balance.owner), currency: balance.currency, amount_micro: amount })
  }

  async function send(body: AdjustmentWrite) {
    setWriteBusy(true); setWriteError(null); setPending(body)
    try {
      const next = await api.createBillingAdjustment(body, csrf)
      setResult(next); setBalance(next); setAmount(''); setPending(null); setConfirming(null)
      await read(body.owner, body.currency)
    } catch (caught) {
      setWriteError(billingError(caught))
      if (caught instanceof ApiError) setPending(null)
    } finally { setWriteBusy(false) }
  }

  return <div className="billing-stack">
    <section className="content-panel billing-panel">
      <div className="section-heading"><div><h2>钱包与不可变流水</h2><p>余额和金额均以 microcurrency 十进制整数传输。未知值不会显示为 0。</p></div></div>
      <form className="billing-toolbar" onSubmit={lookup}>
        <OwnerFields owner={owner} onChange={setOwner} disabled={loading} />
        <Field label="币种"><input value={currency} maxLength={3} disabled={loading} onChange={(event) => setCurrency(event.target.value.toUpperCase())} /></Field>
        <Button type="submit" disabled={loading}>{loading ? '读取中…' : '读取钱包'}</Button>
      </form>
      <FormError error={error} />
      {balance ? <div className="billing-balance"><span>{ownerLabel(balance.owner)} · {balance.account_id ? <code>{balance.account_id}</code> : '尚无账户记录'}</span><strong>{formatMicro(balance.balance_micro, balance.currency)}</strong></div> : null}
      {balance ? <form className="billing-adjustment" onSubmit={prepareAdjustment}>
        <Field label="人工调整（micro）" hint="正数增加钱包余额；负数扣减且余额不得为负。"><input inputMode="numeric" value={amount} onChange={(event) => setAmount(event.target.value)} placeholder="例如 1000000 或 -500000" /></Field>
        <Button type="submit" variant="secondary">核对调整</Button>
      </form> : null}
      {!confirming ? <FormError error={writeError} /> : null}
      {result ? <div className="success-note" role="status">调整已记账：{formatMicro(result.amount_micro, result.currency)}；当前余额 {formatMicro(result.balance_micro, result.currency)}。流水 <code>{result.entry_id}</code></div> : null}
      <div className="billing-ledger-heading"><h3>不可变流水</h3><span>按创建顺序分页，只读</span></div>
      {ledgerAvailable === false ? <div className="billing-unavailable"><strong>流水查询暂不可用</strong><p>当前服务尚未提供只读流水端点；钱包余额仍来自实际账本。页面不会根据余额倒推或伪造流水。</p></div> : null}
      {ledgerAvailable === null && balance ? <div className="billing-unavailable"><strong>暂时无法确认流水</strong><p>余额读取成功，但流水响应不可用。请稍后重试钱包查询。</p></div> : null}
      {ledgerAvailable && entries.length === 0 ? <EmptyState title="还没有流水" body="这个归属对象和币种尚无账本条目。" /> : null}
      {entries.length ? <div className="table-scroll"><table><thead><tr><th>时间 / 类型</th><th>金额</th><th>资源</th><th>流水 ID</th></tr></thead><tbody>{entries.map((entry) => <tr key={entry.id}><td>{new Date(entry.created_at).toLocaleString()}<small>{kindLabels[entry.kind] ?? entry.kind}</small></td><td><strong>{formatMicro(entry.amount_micro, entry.currency)}</strong></td><td>{entry.resource_kind || '—'}<small>{entry.resource_id || '—'}</small></td><td><code>{entry.id}</code>{entry.original_entry_id ? <small>冲正原流水：{entry.original_entry_id}</small> : null}</td></tr>)}</tbody></table></div> : null}
      {nextCursor ? <div className="billing-load-more"><Button variant="secondary" disabled={loading} onClick={() => void read(owner, currency, nextCursor)}>加载更多流水</Button></div> : null}
    </section>
    {confirming ? <ConfirmWrite title="确认钱包调整" description="该操作会新增不可变账本条目；不会改写既有流水。" confirmLabel={BigInt(confirming.amount_micro) < 0n ? '确认扣减' : '确认增加'} danger={BigInt(confirming.amount_micro) < 0n} busy={writeBusy} error={writeError} pendingRetry={pending !== null} onConfirm={() => void send(confirming)} onRetry={() => pending && void send(pending)} onClose={() => { setConfirming(null); setPending(null); setWriteError(null) }} details={<dl className="billing-confirm-list"><div><dt>对象</dt><dd>{ownerLabel(confirming.owner)}</dd></div><div><dt>方向</dt><dd>{BigInt(confirming.amount_micro) < 0n ? '扣减钱包余额' : '增加钱包余额'}</dd></div><div><dt>金额</dt><dd>{formatMicro(confirming.amount_micro, confirming.currency)}</dd></div><div><dt>操作编号</dt><dd><code>{confirming.operation_id}</code></dd></div></dl>} /> : null}
  </div>
}

export function cleanBillingOwner(owner: BillingOwner): BillingOwner {
  if (owner.kind === 'key') return { kind: 'key', employee_id: owner.employee_id.trim(), key_id: owner.key_id?.trim() }
  if (owner.kind === 'resource') return { kind: 'resource', employee_id: owner.employee_id.trim(), resource_kind: owner.resource_kind?.trim(), resource_id: owner.resource_id?.trim() }
  return { kind: 'employee', employee_id: owner.employee_id.trim() }
}
