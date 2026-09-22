import type { ButtonHTMLAttributes, FormEvent, ReactNode } from 'react'

export function Icon({ name }: { name: 'people' | 'link' | 'route' | 'status' | 'logout' | 'plus' | 'key' | 'copy' | 'close' | 'menu' }) {
  const paths: Record<string, ReactNode> = {
    people: <><circle cx="9" cy="7" r="3"/><path d="M3.5 19a5.5 5.5 0 0 1 11 0M17 9a3 3 0 0 1 0 6m2 4a4 4 0 0 0-3-3.87"/></>,
    link: <><path d="M10 13a5 5 0 0 0 7.54.54l2-2a5 5 0 0 0-7.07-7.07l-1.14 1.14"/><path d="M14 11a5 5 0 0 0-7.54-.54l-2 2a5 5 0 0 0 7.07 7.07l1.14-1.14"/></>,
    route: <><circle cx="6" cy="5" r="2"/><circle cx="18" cy="19" r="2"/><path d="M8 5h5a3 3 0 0 1 3 3v8M6 7v12h10"/></>,
    status: <><path d="M4 19V9m6 10V4m6 15v-7m4 7V7"/></>,
    logout: <><path d="M10 5H5v14h5M14 8l4 4-4 4m4-4H9"/></>,
    plus: <path d="M12 5v14M5 12h14"/>,
    key: <><circle cx="8" cy="15" r="4"/><path d="m11 12 8-8m-3 3 2 2m-5 1 2 2"/></>,
    copy: <><rect x="8" y="8" width="11" height="11" rx="2"/><path d="M16 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h3"/></>,
    close: <path d="m6 6 12 12M18 6 6 18"/>,
    menu: <path d="M4 7h16M4 12h16M4 17h16"/>,
  }
  return <svg className="icon" viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">{paths[name]}</svg>
}

export function Button({ variant = 'primary', children, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'primary' | 'secondary' | 'danger' | 'text' }) {
  return <button className={`button button--${variant}`} {...props}>{children}</button>
}

export function Dialog({ title, description, children, onClose, wide = false }: { title: string; description?: string; children: ReactNode; onClose: () => void; wide?: boolean }) {
  return <div className="dialog-backdrop" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose() }}>
    <section className={`dialog ${wide ? 'dialog--wide' : ''}`} role="dialog" aria-modal="true" aria-labelledby="dialog-title">
      <header className="dialog__header">
        <div><h2 id="dialog-title">{title}</h2>{description ? <p>{description}</p> : null}</div>
        <button className="icon-button" aria-label="关闭" onClick={onClose}><Icon name="close" /></button>
      </header>
      {children}
    </section>
  </div>
}

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return <label className="field"><span>{label}</span>{children}{hint ? <small>{hint}</small> : null}</label>
}

export function FormError({ error }: { error: string | null }) {
  return error ? <div className="inline-error" role="alert">{error}</div> : null
}

export function EmptyState({ title, body, action }: { title: string; body: string; action?: ReactNode }) {
  return <div className="empty"><div className="empty__mark">·</div><h3>{title}</h3><p>{body}</p>{action}</div>
}

export function PageState({ loading, error, onRetry }: { loading: boolean; error: string | null; onRetry: () => void }) {
  if (loading) return <div className="loading" role="status"><span />正在读取…</div>
  if (error) return <div className="error-state" role="alert"><h3>暂时无法读取</h3><p>{error}</p><Button variant="secondary" onClick={onRetry}>重新加载</Button></div>
  return null
}

export function submitHandler(action: (form: FormData) => Promise<void>) {
  return (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    return action(new FormData(event.currentTarget))
  }
}
