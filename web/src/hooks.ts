import { useCallback, useEffect, useState } from 'react'
import { ApiError } from './api'

export function messageFor(error: unknown) {
  if (error instanceof ApiError) {
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
