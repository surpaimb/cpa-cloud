import { useState, type ReactNode } from 'react'
import { ApiError, type BillingOwner } from '../api'
import { Button, Dialog, Field, FormError } from '../ui'

const int64Max = 9223372036854775807n

export function validMicro(value: string, signed = false) {
  if (!/^-?(0|[1-9]\d*)$/.test(value)) return false
  const parsed = BigInt(value)
  return (signed ? parsed !== 0n && parsed >= -int64Max : parsed > 0n) && parsed <= int64Max
}

export function formatMicro(value: string | null | undefined, currency?: string | null) {
  if (value == null || currency == null) return '未知'
  const negative = value.startsWith('-')
  const digits = negative ? value.slice(1) : value
  const padded = digits.padStart(7, '0')
  const whole = padded.slice(0, -6).replace(/\B(?=(\d{3})+(?!\d))/g, ',')
  const fraction = padded.slice(-6).replace(/0+$/, '')
  return `${negative ? '−' : ''}${whole}${fraction ? `.${fraction}` : ''} ${currency}`
}

export function billingError(error: unknown) {
  if (error instanceof ApiError) {
    if (error.code === 'operation_conflict') return '操作编号、对象或 revision 与当前状态冲突；当前输入已保留，请重新读取核对。'
    if (error.code === 'insufficient_balance') return '余额不足，操作没有执行。'
    if (error.status === 409) return '数据已被其他操作更新；当前输入已保留，请重新读取后再提交。'
    if (error.status === 404) return '当前服务未提供此项商业管理能力。'
    return error.message
  }
  return '响应可能在写入后丢失。当前操作编号和完整载荷已保留，可安全重试。'
}

export function ownerLabel(owner: BillingOwner) {
  if (owner.kind === 'key') return `Key ${owner.key_id || '未填写'}（员工 ${owner.employee_id || '未填写'}）`
  if (owner.kind === 'resource') return `${owner.resource_kind || '资源'}/${owner.resource_id || '未填写'}（员工 ${owner.employee_id || '未填写'}）`
  return `员工 ${owner.employee_id || '未填写'}`
}

export function ownerValid(owner: BillingOwner) {
  if (!owner.employee_id.trim()) return false
  if (owner.kind === 'key') return Boolean(owner.key_id?.trim())
  if (owner.kind === 'resource') return Boolean(owner.resource_kind?.trim() && owner.resource_id?.trim())
  return true
}

export function OwnerFields({ owner, onChange, disabled = false }: { owner: BillingOwner; onChange: (owner: BillingOwner) => void; disabled?: boolean }) {
  return <div className="billing-owner-fields">
    <Field label="归属类型"><select value={owner.kind} disabled={disabled} onChange={(event) => onChange({ kind: event.target.value as BillingOwner['kind'], employee_id: owner.employee_id })}><option value="employee">员工</option><option value="key">员工 Key</option><option value="resource">员工资源</option></select></Field>
    <Field label="员工 ID"><input value={owner.employee_id} disabled={disabled} onChange={(event) => onChange({ ...owner, employee_id: event.target.value })} placeholder="emp_…" /></Field>
    {owner.kind === 'key' ? <Field label="Key ID"><input value={owner.key_id ?? ''} disabled={disabled} onChange={(event) => onChange({ ...owner, key_id: event.target.value })} placeholder="key_…" /></Field> : null}
    {owner.kind === 'resource' ? <><Field label="资源类型"><input value={owner.resource_kind ?? ''} disabled={disabled} onChange={(event) => onChange({ ...owner, resource_kind: event.target.value })} placeholder="例如 project" /></Field><Field label="资源 ID"><input value={owner.resource_id ?? ''} disabled={disabled} onChange={(event) => onChange({ ...owner, resource_id: event.target.value })} /></Field></> : null}
  </div>
}

export function ConfirmWrite({ title, description, details, confirmLabel, danger = false, busy, error, pendingRetry, onConfirm, onRetry, onReload, onClose }: {
  title: string; description: string; details: ReactNode; confirmLabel: string; danger?: boolean; busy: boolean; error: string | null; pendingRetry: boolean; onConfirm: () => void; onRetry: () => void; onReload?: () => void; onClose: () => void
}) {
  return <Dialog title={title} description={description} onClose={onClose} closeDisabled={busy}>
    <div className="billing-confirm">{details}</div>
    <FormError error={error} />
    <div className="dialog__actions billing-dialog-actions"><Button variant="secondary" disabled={busy} onClick={onClose}>取消</Button>{onReload && error ? <Button variant="secondary" disabled={busy} onClick={onReload}>重新读取</Button> : null}{pendingRetry ? <Button disabled={busy} onClick={onRetry}>用原操作编号重试</Button> : <Button variant={danger ? 'danger' : 'primary'} disabled={busy || Boolean(error)} onClick={onConfirm}>{busy ? '提交中…' : confirmLabel}</Button>}</div>
  </Dialog>
}

export function useOneTimeCopy() {
  const [copied, setCopied] = useState(false)
  return { copied, copy: async (value: string) => { await navigator.clipboard.writeText(value); setCopied(true) } }
}
