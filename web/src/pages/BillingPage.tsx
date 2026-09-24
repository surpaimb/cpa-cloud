import { useEffect, useState } from 'react'
import { api, ApiError, type BillingSettings } from '../api'
import { BillingCodes } from '../billing/BillingCodes'
import { BillingFunding } from '../billing/BillingFunding'
import { BillingPlans } from '../billing/BillingPlans'
import { BillingWallet } from '../billing/BillingWallet'
import { ConfirmWrite, billingError } from '../billing/common'
import { Button, PageState } from '../ui'
import { PageHeader } from './EmployeesPage'

type Section = 'wallet' | 'plans' | 'funding' | 'codes'

export function BillingPage({ csrf }: { csrf: string }) {
  const [settings, setSettings] = useState<BillingSettings | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [section, setSection] = useState<Section>('wallet')
  const [confirming, setConfirming] = useState<{ operation_id: string; expected_revision: number; enabled: boolean } | null>(null)
  const [pending, setPending] = useState<typeof confirming>(null)
  const [writeBusy, setWriteBusy] = useState(false)
  const [writeError, setWriteError] = useState<string | null>(null)

  async function load() {
    setLoading(true); setError(null)
    try { setSettings(await api.billingSettings()) }
    catch (caught) { setError(billingError(caught)) }
    finally { setLoading(false) }
  }
  useEffect(() => { void load() }, [])

  async function save(body: NonNullable<typeof confirming>) {
    setWriteBusy(true); setWriteError(null); setPending(body)
    try {
      await api.putBillingSettings(body, csrf)
      setPending(null); setConfirming(null); await load()
    } catch (caught) { setWriteError(billingError(caught)); if (caught instanceof ApiError) setPending(null) }
    finally { setWriteBusy(false) }
  }

  return <>
    <PageHeader title="商业管理" description="管理员专用的钱包、套餐与合成支付测试管理；不开放员工自助，也不连接真实支付服务商。" />
    <PageState loading={loading} error={error} onRetry={() => void load()} />
    {settings ? <>
      <section className={`billing-switch ${settings.enabled ? 'billing-switch--enabled' : ''}`}>
        <div><span className="billing-eyebrow">商业执行总开关 · revision {settings.revision}</span><h2>{settings.enabled ? '商业执行已开启' : '商业执行默认关闭'}</h2><p>{settings.enabled ? '充值、订阅购买、兑换和退款冲正可由管理员执行。真实支付仍未接入。' : '可预先配置套餐、测试连接器与兑换码；充值、购买、兑换和退款执行保持关闭。'}</p></div>
        <Button variant={settings.enabled ? 'danger' : 'primary'} onClick={() => { setWriteError(null); setConfirming({ operation_id: crypto.randomUUID(), expected_revision: settings.revision, enabled: !settings.enabled }) }}>{settings.enabled ? '关闭商业执行' : '开启商业执行'}</Button>
      </section>
      <div className="billing-boundary"><strong>当前能力边界</strong><p>支付连接器和回调仅用于本地合成测试。没有真实收款、自动续费、外部退款出款或员工自助入口。所有财务写入使用 CSRF、revision 和幂等 operation ID。</p></div>
      <nav className="billing-tabs" aria-label="商业管理分区">
        <button className={section === 'wallet' ? 'active' : ''} onClick={() => setSection('wallet')}>钱包与流水</button>
        <button className={section === 'plans' ? 'active' : ''} onClick={() => setSection('plans')}>套餐与订阅</button>
        <button className={section === 'funding' ? 'active' : ''} onClick={() => setSection('funding')}>充值与退款</button>
        <button className={section === 'codes' ? 'active' : ''} onClick={() => setSection('codes')}>兑换码</button>
      </nav>
      {section === 'wallet' ? <BillingWallet csrf={csrf} /> : null}
      {section === 'plans' ? <BillingPlans csrf={csrf} commercialEnabled={settings.enabled} /> : null}
      {section === 'funding' ? <BillingFunding csrf={csrf} commercialEnabled={settings.enabled} /> : null}
      {section === 'codes' ? <BillingCodes csrf={csrf} commercialEnabled={settings.enabled} /> : null}
    </> : null}
    {confirming ? <ConfirmWrite title={confirming.enabled ? '确认开启商业执行' : '确认关闭商业执行'} description={`将以 expected revision ${confirming.expected_revision} 更新总开关。`} confirmLabel={confirming.enabled ? '确认开启' : '确认关闭'} danger={!confirming.enabled} busy={writeBusy} error={writeError} pendingRetry={pending !== null} onConfirm={() => void save(confirming)} onRetry={() => pending && void save(pending)} onReload={() => { setConfirming(null); setPending(null); setWriteError(null); void load() }} onClose={() => { setConfirming(null); setPending(null); setWriteError(null) }} details={<dl className="billing-confirm-list"><div><dt>执行状态</dt><dd>{confirming.enabled ? '允许管理员执行商业写入' : '停止充值、购买、兑换与退款写入'}</dd></div><div><dt>不会改变</dt><dd>既有钱包余额、流水、套餐和记录</dd></div><div><dt>真实支付</dt><dd>仍未接入</dd></div><div><dt>操作编号</dt><dd><code>{confirming.operation_id}</code></dd></div></dl>} /> : null}
  </>
}
