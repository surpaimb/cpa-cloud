import { useEffect, useRef, useState } from 'react'
import { ApiError, api, type CodexOAuthSession, type CodexOAuthSessionStatus, type Upstream } from './api'
import { oauthMessageFor } from './hooks'
import { Button, Dialog, Field, FormError } from './ui'

const POLL_INTERVAL_MS = 1_500
const MAX_POLL_ATTEMPTS = 120

export function isSafeCodexAuthorizationURL(value: string) {
  try {
    const url = new URL(value)
    return url.protocol === 'https:'
      && url.hostname === 'auth.openai.com'
      && url.port === ''
      && url.username === ''
      && url.password === ''
      && url.pathname === '/oauth/authorize'
      && url.hash === ''
  } catch {
    return false
  }
}

function terminalMessage(status: CodexOAuthSessionStatus) {
  if (status.status === 'cancelled') return '授权已取消。可重新开始授权。'
  if (status.status === 'expired') return '授权会话已过期。请重新开始授权。'
  if (status.error_code === 'codex_oauth_configuration_changed') return 'OAuth 配置已变化。请关闭此流程并重新开始授权。'
  if (status.error_code === 'codex_oauth_source_mismatch') return '授权来源与当前服务不匹配。请确认当前 CPA Cloud 的 OAuth 配置后重新开始。'
  return '授权未完成。请检查 OAuth 服务配置后重新开始。'
}

export function CodexOAuthAuthorization({ csrf, onClose, onUpstreamsChanged, onSync }: {
  csrf: string
  onClose: () => void
  onUpstreamsChanged: (upstreamID?: string) => Promise<Upstream | undefined>
  onSync: (upstream: Upstream) => void
}) {
  const [name, setName] = useState('Codex 会员')
  const [operationID, setOperationID] = useState(() => crypto.randomUUID())
  const [session, setSession] = useState<CodexOAuthSession | null>(null)
  const [status, setStatus] = useState<CodexOAuthSessionStatus['status'] | null>(null)
  const [createdUpstream, setCreatedUpstream] = useState<Upstream | null>(null)
  const [pollPaused, setPollPaused] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const generation = useRef(0)

  useEffect(() => {
    if (!session) return
    const current = ++generation.current
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    let attempts = 0

    const poll = async () => {
      if (generation.current !== current) return
      if (Date.now() >= Date.parse(session.expires_at)) {
        setStatus('expired')
        setError('授权会话已过期。请重新开始授权。')
        return
      }
      if (attempts >= MAX_POLL_ATTEMPTS) {
        setPollPaused(true)
        setError('已暂停自动查询，但授权会话可能仍然有效。完成授权后可手动继续查询；不会创建重复连接。')
        return
      }
      attempts += 1
      try {
        const next = await api.codexOAuthSession(session.session_id, controller.signal)
        if (generation.current !== current) return
        setStatus(next.status)
        setError(null)
        if (next.status === 'succeeded') {
          try {
            const upstream = await onUpstreamsChanged(next.upstream_id)
            if (generation.current === current && upstream) setCreatedUpstream(upstream)
          } catch {
            if (generation.current === current) setError('授权已保存，但列表重新载入失败。请关闭窗口并重新载入页面。')
          }
          return
        }
        if (next.status === 'failed' || next.status === 'cancelled' || next.status === 'expired') {
          setError(terminalMessage(next))
          return
        }
        timer = setTimeout(() => void poll(), POLL_INTERVAL_MS)
      } catch (caught) {
        if (generation.current !== current || controller.signal.aborted) return
        if (caught instanceof ApiError && (caught.status === 403 || caught.status === 404)) {
          setStatus('failed')
          setError(caught.status === 404 ? '授权会话不存在或已失效。请重新开始授权。' : '当前管理员会话无法读取此授权状态，请重新登录。')
          return
        }
        setError('暂时无法确认授权状态；将在当前会话内继续查询，不会创建重复连接。')
        timer = setTimeout(() => void poll(), POLL_INTERVAL_MS)
      }
    }
    void poll()
    return () => {
      generation.current += 1
      controller.abort()
      if (timer) clearTimeout(timer)
    }
  }, [onUpstreamsChanged, session])

  function restart() {
    generation.current += 1
    setSession(null)
    setStatus(null)
    setCreatedUpstream(null)
    setPollPaused(false)
    setError(null)
    setOperationID(crypto.randomUUID())
  }

  return <Dialog title="通过 OpenAI 授权 Codex" description="授权会在 OpenAI 官方页面完成；CPA Cloud 只轮询当前会话的结果。" onClose={onClose}>
    {!session ? <form onSubmit={async (event) => {
      event.preventDefault()
      setBusy(true)
      setError(null)
      try {
        const created = await api.createCodexOAuthSession({ name: name.trim(), operation_id: operationID }, csrf)
        if (!isSafeCodexAuthorizationURL(created.authorization_url)) {
          setError('服务返回了不安全的授权地址。请检查 OAuth 服务配置。')
          return
        }
        setSession(created)
        setStatus('pending')
        setPollPaused(false)
      } catch (caught) {
        setError(oauthMessageFor(caught))
      } finally {
        setBusy(false)
      }
    }}>
      <div className="form-grid">
        <Field label="显示名称" hint="仅用于后台识别此会员上游。"><input name="name" value={name} onChange={(event) => setName(event.target.value)} required autoFocus maxLength={120} /></Field>
        <div className="membership-limitations"><strong>授权边界</strong><ul>
          <li>只打开域名为 <code>auth.openai.com</code> 的 HTTPS 官方授权页。</li>
          <li>完成授权只表示凭据已保存，首次真实请求成功前仍是“未验证”。</li>
          <li>授权成功后不会自动创建或启用模型路由。</li>
        </ul></div>
      </div>
      <FormError error={error} />
      <div className="dialog__actions"><Button type="button" variant="secondary" onClick={onClose}>取消</Button><Button type="submit" disabled={busy}>{busy ? '正在创建会话…' : '创建授权会话'}</Button></div>
    </form> : <div className="oauth-flow">
      <div className="oauth-callout">
        <strong>{status === 'succeeded' ? '授权凭据已保存' : '等待 OpenAI 授权'}</strong>
        {status === 'succeeded'
          ? <p>此连接尚未验证。首次真实请求成功后，凭据状态才会变为“已验证”。接下来可同步模型并手动选择要创建的路由。</p>
          : <p>请点击下方按钮打开官方授权页。若浏览器阻止新窗口，请再次点击此链接。</p>}
      </div>
      {status !== 'succeeded' ? <a className="button button--primary oauth-open-link" href={session.authorization_url} target="_blank" rel="noopener noreferrer">打开 OpenAI 官方授权页</a> : null}
      {!pollPaused && (status === 'pending' || status === 'exchanging') ? <div className="oauth-progress" role="status"><span className="spinner" />{status === 'exchanging' ? '正在交换并保存授权凭据…' : '正在等待授权结果…'}</div> : null}
      <FormError error={error} />
      <div className="dialog__actions">
        {pollPaused ? <Button type="button" variant="secondary" onClick={() => { setPollPaused(false); setError(null); setSession({ ...session }) }}>继续查询</Button> : null}
        {status === 'failed' || status === 'cancelled' || status === 'expired' ? <Button type="button" variant="secondary" onClick={restart}>重新开始</Button> : null}
        <Button type="button" variant="secondary" onClick={onClose}>关闭</Button>
        {status === 'succeeded' && createdUpstream ? <Button type="button" onClick={() => onSync(createdUpstream)}>同步模型</Button> : null}
      </div>
    </div>}
  </Dialog>
}
