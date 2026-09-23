import { useCallback } from 'react'
import { api } from '../api'
import { useResource } from '../hooks'
import { EmptyState, PageState } from '../ui'
import { PageHeader } from './EmployeesPage'
import { AccountRecoveryPanel } from '../AccountRecovery'

export function StatusPage({ csrf }: { csrf: string }) {
  const load = useCallback(() => api.status(), [])
  const { data, loading, error, reload } = useResource(load)
  return <>
    <PageHeader title="系统状态" description="查看当前服务、存储和开发预览限制。" />
    <div className="status-layout"><PageState loading={loading} error={error} onRetry={() => void reload()} />
      {data ? <><section className="status-summary"><div><span className={`readiness ${data.ready ? 'ready' : ''}`}><i />{data.ready ? '服务已就绪' : '服务未就绪'}</span><h2>{data.version || '开发预览'}</h2><p>存储：{data.storage || '未知'}</p></div></section><section className="limitations"><h2>当前限制</h2>{data.limitations.length ? <ul>{data.limitations.map((item) => <li key={item}>{item}</li>)}</ul> : <EmptyState title="服务未报告限制" body="仍请以开发预览文档和实际测试结果为准。" />}</section></> : null}
    </div>
    {data?.features?.account_recovery ? <AccountRecoveryPanel csrf={csrf} /> : null}
  </>
}
