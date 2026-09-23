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
  revision_conflict: '这条会员记录已被其他操作更新；本次重新导入未覆盖该更新。请刷新列表后重试。',
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
    if (error.status === 409) return '导入发生冲突；本次请求未覆盖现有记录，请刷新列表后重试。'
    if (error.status === 413) return '授权文件超过服务允许的大小。'
    if (error.status === 403) return '当前会话无权导入授权文件，请重新登录。'
    return '无法导入授权文件。文件内容未显示，请检查文件后重试。'
  }
  return '未能确认导入结果，请刷新列表核对后重试。'
}

const oauthErrorMessages: Record<string, string> = {
  feature_disabled: 'Codex 会员实验未启用。请检查服务启动参数。',
  codex_oauth_not_configured: 'OAuth 尚未配置。请使用 --codex-oauth-client-id 与 --codex-oauth-redirect-uri 启动服务。',
  codex_oauth_configuration_changed: 'OAuth 配置已变化。请关闭此流程并重新开始授权。',
  already_exists: '授权会话发生冲突，请关闭后重新开始。',
}

export function oauthMessageFor(error: unknown) {
  if (error instanceof ApiError) {
    if (oauthErrorMessages[error.code]) return oauthErrorMessages[error.code]
    if (error.status === 409) return '授权会话状态已变化，请关闭后重新开始。'
    return '无法创建 Codex OAuth 授权会话，请检查服务配置后重试。'
  }
  return '未能确认授权会话是否已创建。可在当前页面重试，系统会复用同一操作编号。'
}

const refreshErrorMessages: Record<string, string> = {
  feature_disabled: 'Codex 会员实验未启用，无法刷新。',
  codex_oauth_not_configured: 'OAuth 尚未配置。请检查服务启动参数后重新载入列表。',
  codex_refresh_not_bound: '此凭据不是由当前 OAuth 客户端建立，不能手动刷新。请重新授权创建新连接，或重新导入 auth.json。',
  codex_reauthorization_required: '提供商要求重新授权。请从上方 OAuth 入口创建新连接，再停用此记录。',
  revision_conflict: '记录已被其他操作更新，本次刷新未覆盖该更新。请重新载入列表。',
  credential_unavailable: '服务无法读取此凭据。请重新授权或重新导入 auth.json。',
  codex_refresh_failed: '服务端未完成刷新。请稍后重新载入列表再决定是否重试。',
}

export function refreshMessageFor(error: unknown) {
  if (error instanceof ApiError) {
    if (refreshErrorMessages[error.code]) return refreshErrorMessages[error.code]
    return '无法刷新 Codex 凭据，请重新载入列表核对状态。'
  }
  return '未能确认刷新结果，请重新载入列表核对后再决定是否重试。'
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
