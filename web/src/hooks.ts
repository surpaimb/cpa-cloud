import { useCallback, useEffect, useState } from 'react'
import { ApiError } from './api'

const errorMessages: Record<string, string> = {
  invalid_credentials: '用户名或密码不正确。',
  session_expired: '管理员会话已过期，请重新登录。',
  invalid_request: '提交内容无效，请检查后重试。',
  invalid_endpoint: '该上游地址不被允许。',
  not_found: '请求的资源不存在或已被删除。',
  authentication_required: '管理员会话已过期，请重新登录。',
  origin_rejected: '请求来源校验失败，请从当前 CPA Cloud 后台重试。',
  csrf_rejected: '安全令牌已失效，请重新登录后重试。',
  upstream_disabled: '该上游已停用，请先启用后再同步模型。',
  upstream_authentication_failed: '上游拒绝了当前 API Key，请更新凭据后重试。',
  model_discovery_unsupported: '该上游不支持自动读取模型列表，请在模型路由中手动输入。',
  upstream_rate_limited: '上游请求过于频繁，请稍后重试。',
  model_discovery_timeout: '读取模型列表超时，请检查上游后重试。',
  invalid_model_response: '上游返回的模型列表格式无效。',
  model_discovery_failed: '无法从上游读取模型列表，请稍后重试。',
}

export function messageFor(error: unknown) {
  if (error instanceof ApiError) {
    if (errorMessages[error.code]) return errorMessages[error.code]
    if (error.status === 409) return '数据已被其他操作更新，请刷新后重试。'
    if (error.status === 403) return '当前会话无权执行该操作，请重新登录。'
    return error.message
  }
  return '网络连接失败，请检查服务是否正在运行。'
}

export function useResource<T>(loader: () => Promise<T>) {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const reload = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setData(await loader())
    } catch (caught) {
      setError(messageFor(caught))
    } finally {
      setLoading(false)
    }
  }, [loader])
  useEffect(() => { void reload() }, [reload])
  return { data, setData, loading, error, reload }
}
