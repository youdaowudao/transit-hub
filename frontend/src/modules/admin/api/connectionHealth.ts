import type {
  AdminGroupPolicyConfiguration,
  AdminGroupPolicyConfigurationInput,
  AdminGroupHealth,
  ConnectionHealthEvent,
  ConnectionHealthOverview,
  ConnectionHealthStoredSummary,
  ConnectionHealthPolicy,
  ManualProbeModelOption,
  ManualProbeResult,
  ModelHealth,
  OwnGroupHealth,
  PolicyInput,
	PrioritySyncStatus,
  QuestionAnswerBatch,
  QuestionAnswerHistory,
  QuestionAnswerRecord,
  TestQuestion,
  TestQuestionInput,
  TargetPolicyAssignments,
} from '../types/connectionHealth'
import {
  authUnauthorizedErrorKey,
  getAccessToken,
  handleAuthExpired,
  isUnauthorizedApiResponse,
} from '@/modules/auth/api/auth'

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL ?? '/api'

const endpoint = (path: string): string => `${apiBaseUrl.replace(/\/$/, '')}${path}`

const authHeaders = (): HeadersInit => {
  const token = getAccessToken()
  if (!token) return {}
  return { Authorization: `Bearer ${token}` }
}

type ApiErrorPayload = {
  message?: string
}

const requestJson = async <T>(path: string, options: RequestInit = {}): Promise<T> => {
  let response: Response
  try {
    response = await fetch(endpoint(path), {
      ...options,
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
        ...authHeaders(),
        ...(options.headers ?? {}),
      },
    })
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') throw error
    throw new Error('admin.connectionHealth.errors.network')
  }

  const text = await response.text()
  const payload = text ? (JSON.parse(text) as T & ApiErrorPayload) : ({} as T & ApiErrorPayload)

  if (!response.ok) {
    if (isUnauthorizedApiResponse(response.status, payload)) {
      handleAuthExpired()
      throw new Error(authUnauthorizedErrorKey)
    }
    throw new Error(payload.message ?? 'admin.connectionHealth.errors.request')
  }

  return payload
}

export const getConnectionHealthOverview = async (): Promise<ConnectionHealthOverview> =>
  requestJson<ConnectionHealthOverview>('/connection-health/overview')

export const getConnectionHealthStoredSummary = async (): Promise<ConnectionHealthStoredSummary> =>
  requestJson<ConnectionHealthStoredSummary>('/connection-health/stored-summary')

export const getConnectionHealthGroups = async (): Promise<OwnGroupHealth[]> =>
  requestJson<OwnGroupHealth[]>('/connection-health/groups')

// getConnectionHealthAdminGroups 拉取 admin 全量分组健康主列表（含账号/渠道与探活叠加）。
// 对应后端新增路由，不影响旧的 getConnectionHealthGroups。
export const getConnectionHealthAdminGroups = async (): Promise<AdminGroupHealth[]> =>
  requestJson<AdminGroupHealth[]>('/connection-health/admin-groups')

export const getPrioritySyncStatus = async (): Promise<PrioritySyncStatus> =>
	requestJson<PrioritySyncStatus>('/connection-health/priority-sync-status')

export const getConnectionHealthEvents = async (connectionId?: string, limit = 100): Promise<ConnectionHealthEvent[]> => {
  const params = new URLSearchParams()
  if (connectionId) params.set('connectionId', connectionId)
  params.set('limit', String(limit))
  return requestJson<ConnectionHealthEvent[]>(`/connection-health/events?${params.toString()}`)
}

// probeConnection 触发一次手动探活。models 为空或未传时保持旧行为：探活该连接匹配到的
// 全部启用模型目标（向后兼容旧调用方）。传入 models 时后端只探活这些模型名对应的匹配目标。
export const probeConnection = async (connectionId: string, models?: string[]): Promise<ModelHealth[]> =>
  requestJson<ModelHealth[]>(`/connection-health/connections/${encodeURIComponent(connectionId)}/probe`, {
    method: 'POST',
    body: models && models.length > 0 ? JSON.stringify({ models }) : undefined,
  })

// probeTarget 触发一次独立目标的手动探活：按 targetId + models 走，后端 server-only 解析凭据，
// 请求体绝不携带 base_url/key/credentials。models 为空时后端探活该目标全部候选模型。
export const probeTarget = async (targetId: string, models?: string[], signal?: AbortSignal): Promise<ModelHealth[]> =>
  requestJson<ModelHealth[]>(`/connection-health/targets/${encodeURIComponent(targetId)}/probe`, {
    method: 'POST',
    body: models && models.length > 0 ? JSON.stringify({ models }) : undefined,
    signal,
  })

export type ProbeTargetProgressPhase = 'queued' | 'running'

type ProbeTargetStreamEvent = {
  type: 'phase' | 'result' | 'error'
  phase?: ProbeTargetProgressPhase
  results?: ModelHealth[]
  errorKey?: string
}

// probeTargetWithProgress 是正式手动探活的 SSE 版本。只有阶段通过流式事件推送，
// 结果和错误语义与 probeTarget 保持一致；一次性探活继续使用普通 JSON 接口。
export const probeTargetWithProgress = async (
  targetId: string,
  models: string[] | undefined,
  onPhase: (phase: ProbeTargetProgressPhase) => void,
  signal?: AbortSignal,
): Promise<ModelHealth[]> => {
  let response: Response
  try {
    response = await fetch(endpoint(`/connection-health/targets/${encodeURIComponent(targetId)}/probe-stream`), {
      method: 'POST',
      headers: { Accept: 'text/event-stream', 'Content-Type': 'application/json', ...authHeaders() },
      body: models && models.length > 0 ? JSON.stringify({ models }) : undefined,
      signal,
    })
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') throw error
    throw new Error('admin.connectionHealth.errors.network')
  }

  if (!response.ok) {
    let payload: ApiErrorPayload = {}
    try {
      const text = await response.text()
      payload = text ? JSON.parse(text) as ApiErrorPayload : {}
    } catch {
      // 保持通用请求错误，避免把上游响应原文带到界面。
    }
    if (isUnauthorizedApiResponse(response.status, payload)) {
      handleAuthExpired()
      throw new Error(authUnauthorizedErrorKey)
    }
    throw new Error(payload.message ?? 'admin.connectionHealth.errors.request')
  }

  if (!response.body) throw new Error('admin.connectionHealth.errors.request')
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let results: ModelHealth[] | null = null
  let streamErrorKey = ''

  const consume = (part: string) => {
    const dataLine = part.split('\n').find(line => line.startsWith('data: '))
    if (!dataLine) return
    try {
      const event = JSON.parse(dataLine.slice(6)) as ProbeTargetStreamEvent
      if (event.type === 'phase' && event.phase) {
        onPhase(event.phase)
      } else if (event.type === 'result') {
        results = event.results ?? []
      } else if (event.type === 'error') {
        streamErrorKey = event.errorKey ?? 'admin.connectionHealth.errors.unknown'
      }
    } catch {
      // 忽略单条格式错误事件，最终没有结果时统一返回请求错误。
    }
  }

  // eslint-disable-next-line no-constant-condition
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    const parts = buffer.split('\n\n')
    buffer = parts.pop() ?? ''
    parts.forEach(consume)
  }
  buffer += decoder.decode()
  if (buffer.trim()) consume(buffer)
  if (streamErrorKey) throw new Error(streamErrorKey)
  if (results === null) throw new Error('admin.connectionHealth.errors.request')
  return results
}

// discoverTargetModels 是手动一次性探活弹窗打开时调用的 server-only 模型发现接口：
// 后端用当前 admin session 临时解析该 target 的 base_url + key 请求上游 /v1/models，
// 这里只拿到安全字段（id/name/ownedBy/providerFamily），前端绝不接触凭据本身。
export const discoverTargetModels = async (targetId: string, signal?: AbortSignal): Promise<ManualProbeModelOption[]> =>
  requestJson<ManualProbeModelOption[]>(`/connection-health/targets/${encodeURIComponent(targetId)}/models`, { signal })

// manualProbeOnce 触发一次「一次性」探活：不写策略状态/事件，结果仅用于弹窗内即时展示。
// models 必须非空——手动一次性探活没有候选池概念，必须由用户在弹窗里显式勾选。
export const manualProbeOnce = async (targetId: string, models: string[], signal?: AbortSignal): Promise<ManualProbeResult[]> =>
  requestJson<ManualProbeResult[]>(`/connection-health/targets/${encodeURIComponent(targetId)}/manual-probe`, {
    method: 'POST',
    body: JSON.stringify({ models }),
    signal,
  })

export const listTestQuestions = async (signal?: AbortSignal): Promise<TestQuestion[]> =>
  requestJson<TestQuestion[]>('/connection-health/test-questions', { signal })

export const createTestQuestion = async (input: TestQuestionInput): Promise<TestQuestion> =>
  requestJson<TestQuestion>('/connection-health/test-questions', { method: 'POST', body: JSON.stringify(input) })

export const updateTestQuestion = async (questionId: string, input: TestQuestionInput): Promise<TestQuestion> =>
  requestJson<TestQuestion>(`/connection-health/test-questions/${encodeURIComponent(questionId)}`, {
    method: 'PUT',
    body: JSON.stringify(input),
  })

export const setTestQuestionEnabled = async (questionId: string, enabled: boolean): Promise<TestQuestion> =>
  requestJson<TestQuestion>(`/connection-health/test-questions/${encodeURIComponent(questionId)}/enabled`, {
    method: 'POST',
    body: JSON.stringify({ enabled }),
  })

export const setDefaultTestQuestion = async (questionId: string): Promise<TestQuestion> =>
  requestJson<TestQuestion>(`/connection-health/test-questions/${encodeURIComponent(questionId)}/default`, { method: 'POST' })

export const deleteTestQuestion = async (questionId: string): Promise<void> => {
  await requestJson<{ ok: boolean }>(`/connection-health/test-questions/${encodeURIComponent(questionId)}`, { method: 'DELETE' })
}

export const startQuestionAnswerBatch = async (
  targetId: string,
  models: string[],
  questionIds: string[],
  signal?: AbortSignal,
): Promise<QuestionAnswerBatch> =>
  requestJson<QuestionAnswerBatch>(`/connection-health/targets/${encodeURIComponent(targetId)}/question-answers/batches`, {
    method: 'POST',
    body: JSON.stringify({ models, questionIds }),
    signal,
  })

export const getLatestQuestionAnswerBatch = async (targetId: string, signal?: AbortSignal): Promise<QuestionAnswerBatch> =>
  requestJson<QuestionAnswerBatch>(`/connection-health/targets/${encodeURIComponent(targetId)}/question-answers/batches/latest`, { signal })

export const getQuestionAnswerBatch = async (targetId: string, batchId: string, signal?: AbortSignal): Promise<QuestionAnswerBatch> =>
  requestJson<QuestionAnswerBatch>(
    `/connection-health/targets/${encodeURIComponent(targetId)}/question-answers/batches/${encodeURIComponent(batchId)}`,
    { signal },
  )

export const cancelQuestionAnswerBatch = async (targetId: string, batchId: string, signal?: AbortSignal): Promise<QuestionAnswerBatch> =>
  requestJson<QuestionAnswerBatch>(
    `/connection-health/targets/${encodeURIComponent(targetId)}/question-answers/batches/${encodeURIComponent(batchId)}/cancel`,
    { method: 'POST', signal },
  )

export const getQuestionAnswerHistory = async (targetId: string, page: number, signal?: AbortSignal): Promise<QuestionAnswerHistory> =>
  requestJson<QuestionAnswerHistory>(
    `/connection-health/targets/${encodeURIComponent(targetId)}/question-answers/history?page=${page}`,
    { signal },
  )

export const setQuestionAnswerManualError = async (
  targetId: string,
  recordId: string,
  manualError: boolean,
  signal?: AbortSignal,
): Promise<QuestionAnswerRecord> =>
  requestJson<QuestionAnswerRecord>(
    `/connection-health/targets/${encodeURIComponent(targetId)}/question-answers/records/${encodeURIComponent(recordId)}/manual-error`,
    { method: 'PUT', body: JSON.stringify({ manualError }), signal },
  )

export interface TargetSchedulableActionResult {
  targetId: string
  schedulable: boolean
  actionSource: string
  actionAt: string
}

export const setTargetSchedulable = async (targetId: string, schedulable: boolean): Promise<TargetSchedulableActionResult> =>
  requestJson<TargetSchedulableActionResult>(`/connection-health/targets/${encodeURIComponent(targetId)}/schedulable`, {
    method: 'POST',
    body: JSON.stringify({ schedulable }),
  })

// getTargetPolicyAssignments / setTargetPolicyAssignments 管理「账号/channel 显式分配策略」
// 关系：只有分配了已启用策略的 target，后台调度器才会自动探活。
export const getTargetPolicyAssignments = async (targetId: string): Promise<TargetPolicyAssignments> =>
  requestJson<TargetPolicyAssignments>(`/connection-health/targets/${encodeURIComponent(targetId)}/policy-assignments`)

export const setTargetPolicyAssignments = async (targetId: string, policyIds: string[]): Promise<TargetPolicyAssignments> =>
  requestJson<TargetPolicyAssignments>(`/connection-health/targets/${encodeURIComponent(targetId)}/policy-assignments`, {
    method: 'PUT',
    body: JSON.stringify({ policyIds }),
  })

export const getAdminGroupPolicyConfiguration = async (adminGroupId: string): Promise<AdminGroupPolicyConfiguration> =>
  requestJson<AdminGroupPolicyConfiguration>(
    `/connection-health/admin-groups/${encodeURIComponent(adminGroupId)}/policy-configuration`,
  )

export const setAdminGroupPolicyConfiguration = async (
  adminGroupId: string,
  input: AdminGroupPolicyConfigurationInput,
): Promise<AdminGroupPolicyConfiguration> =>
  requestJson<AdminGroupPolicyConfiguration>(
    `/connection-health/admin-groups/${encodeURIComponent(adminGroupId)}/policy-configuration`,
    { method: 'PUT', body: JSON.stringify(input) },
  )

export const disableConnection = async (connectionId: string): Promise<void> => {
  await requestJson<{ ok: boolean }>(`/connection-health/connections/${encodeURIComponent(connectionId)}/disable`, {
    method: 'POST',
  })
}

export const restoreConnection = async (connectionId: string): Promise<void> => {
  await requestJson<{ ok: boolean }>(`/connection-health/connections/${encodeURIComponent(connectionId)}/restore`, {
    method: 'POST',
  })
}

export const listConnectionHealthPolicies = async (): Promise<ConnectionHealthPolicy[]> =>
  requestJson<ConnectionHealthPolicy[]>('/connection-health/policies')

export const createConnectionHealthPolicy = async (input: PolicyInput): Promise<ConnectionHealthPolicy> =>
  requestJson<ConnectionHealthPolicy>('/connection-health/policies', {
    method: 'POST',
    body: JSON.stringify(input),
  })

export const updateConnectionHealthPolicy = async (id: string, input: PolicyInput): Promise<ConnectionHealthPolicy> =>
  requestJson<ConnectionHealthPolicy>(`/connection-health/policies/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: JSON.stringify(input),
  })

export const deleteConnectionHealthPolicy = async (id: string): Promise<void> => {
  await requestJson<{ ok: boolean }>(`/connection-health/policies/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
}
