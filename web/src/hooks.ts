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

const membershipErrorMessages: Record<string, string> = {
  feature_disabled: 'Codex 文件导入实验未启用。请使用 --experimental-codex-membership 启动服务后重试。',
  invalid_codex_auth: '授权文件无效、缺少账号信息，或已无法调度。请从已登录的 Codex 客户端重新获取。',
  invalid_upstream_type: '这条上游不是 Codex 会员类型，无法重新导入。',
  codex_auth_too_large: '授权文件超过 1 MiB，请选择正确的 auth.json。',
  codex_auth_invalid_json: '授权文件不是有效的 JSON。',
  codex_auth_invalid_type: '授权文件结构无效，请从已登录的 Codex 客户端重新获取。',
  codex_api_key_not_membership: '该文件是 API Key 凭据，不是 Codex 会员授权文件。',
  codex_auth_mode_unsupported: '该授权文件的登录方式暂不支持。',
  codex_auth_missing_access_token: '授权文件缺少访问凭据，请从已登录的 Codex 客户端重新获取。',
  codex_auth_missing_refresh_token: '授权文件缺少刷新凭据，请从已登录的 Codex 客户端重新获取。',
  codex_auth_missing_account_id: '授权文件缺少账号信息，请从已登录的 Codex 客户端重新获取。',
  codex_auth_expiring: '授权文件已过期或即将过期，请先在 Codex 客户端重新登录。',
  revision_conflict: '这条会员记录已被更新。旧凭据保持不变，请刷新后重试。',
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

// Import responses are deliberately mapped to fixed copy so a malformed or
// accidentally over-detailed server error can never echo credential material.
export function membershipMessageFor(error: unknown) {
  if (error instanceof ApiError) {
    if (membershipErrorMessages[error.code]) return membershipErrorMessages[error.code]
    if (error.status === 409) return '导入发生冲突；现有凭据保持不变，请刷新后重试。'
    if (error.status === 413) return '授权文件超过服务允许的大小。'
    if (error.status === 403) return '当前会话无权导入授权文件，请重新登录。'
    return '无法导入授权文件。文件内容未显示，请检查文件后重试。'
  }
  return '网络连接失败。现有凭据保持不变，请检查服务后重试。'
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
