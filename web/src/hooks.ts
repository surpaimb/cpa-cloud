import { useCallback, useEffect, useState } from 'react'
import { ApiError } from './api'

const errorMessages: Record<string, string> = {
  invalid_credentials: '用户名或密码不正确。',
  session_expired: '管理员会话已过期，请重新登录。',
  invalid_request: '提交内容无效，请检查后重试。',
  invalid_endpoint: '该上游地址不被允许。',
  not_found: '请求的资源不存在或已被删除。',
}

export function messageFor(error: unknown) {
  if (error instanceof ApiError) {
    if (error.status === 409) return '数据已被其他操作更新，请刷新后重试。'
    if (error.status === 403) return '当前会话无权执行该操作，请重新登录。'
    if (errorMessages[error.code]) return errorMessages[error.code]
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
