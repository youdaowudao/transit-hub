import type { QuestionAnswerReasoningEffort } from '../types/connectionHealth'

export interface QuestionAnswerSelectionPreferences {
  modelIds: string[]
  questionIds: string[]
  reasoningEffort: QuestionAnswerReasoningEffort
  repeatCount: number
}

export interface QuestionAnswerPreferences extends QuestionAnswerSelectionPreferences {
  batchTargetIds: string[]
}

export interface ConnectionHealthPreferences {
  version: 1
  groupOrder: string[]
  hiddenGroupIds: string[]
  hideUnmonitoredAccounts: boolean
  questionAnswerUnreadTargetIds: string[]
  questionAnswer: QuestionAnswerPreferences
}

export const createDefaultQuestionAnswerPreferences = (): QuestionAnswerPreferences => ({
  modelIds: [],
  questionIds: [],
  reasoningEffort: 'medium',
  repeatCount: 1,
  batchTargetIds: [],
})

export const createDefaultConnectionHealthPreferences = (): ConnectionHealthPreferences => ({
  version: 1,
  groupOrder: [],
  hiddenGroupIds: [],
  hideUnmonitoredAccounts: false,
  questionAnswerUnreadTargetIds: [],
  questionAnswer: createDefaultQuestionAnswerPreferences(),
})

export const connectionHealthPreferencesStorageKey = (scope: string): string =>
  `transithub.connection-health.preferences.v1:${scope || 'anonymous'}`

const normalizeIds = (value: unknown): string[] => {
  if (!Array.isArray(value)) return []
  return Array.from(new Set(
    value
      .filter((item): item is string => typeof item === 'string')
      .map(item => item.trim())
      .filter(Boolean),
  ))
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null

const normalizeQuestionAnswerPreferences = (value: unknown): QuestionAnswerPreferences => {
  if (!isRecord(value)) return createDefaultQuestionAnswerPreferences()
  const reasoningEfforts: QuestionAnswerReasoningEffort[] = ['low', 'medium', 'high', 'xhigh']
  const reasoningEffort = typeof value.reasoningEffort === 'string'
    && reasoningEfforts.includes(value.reasoningEffort as QuestionAnswerReasoningEffort)
    ? value.reasoningEffort as QuestionAnswerReasoningEffort
    : 'medium'
  const repeatCount = typeof value.repeatCount === 'number'
    && Number.isInteger(value.repeatCount)
    && value.repeatCount >= 1
    && value.repeatCount <= 10
    ? value.repeatCount
    : 1
  return {
    modelIds: normalizeIds(value.modelIds),
    questionIds: normalizeIds(value.questionIds),
    reasoningEffort,
    repeatCount,
    batchTargetIds: normalizeIds(value.batchTargetIds),
  }
}

const normalizePreferences = (value: unknown): ConnectionHealthPreferences => {
  if (!isRecord(value) || value.version !== 1) return createDefaultConnectionHealthPreferences()
  return {
    version: 1,
    groupOrder: normalizeIds(value.groupOrder),
    hiddenGroupIds: normalizeIds(value.hiddenGroupIds),
    hideUnmonitoredAccounts: value.hideUnmonitoredAccounts === true,
    questionAnswerUnreadTargetIds: normalizeIds(value.questionAnswerUnreadTargetIds),
    questionAnswer: normalizeQuestionAnswerPreferences(value.questionAnswer),
  }
}

export const markQuestionAnswerUnread = (
  preferences: ConnectionHealthPreferences,
  targetId: string,
): ConnectionHealthPreferences => {
  const normalizedTargetId = targetId.trim()
  if (!normalizedTargetId || preferences.questionAnswerUnreadTargetIds.includes(normalizedTargetId)) return preferences
  return {
    ...preferences,
    questionAnswerUnreadTargetIds: [...preferences.questionAnswerUnreadTargetIds, normalizedTargetId],
  }
}

export const clearQuestionAnswerUnread = (
  preferences: ConnectionHealthPreferences,
  targetId: string,
): ConnectionHealthPreferences => {
  const normalizedTargetId = targetId.trim()
  if (!normalizedTargetId || !preferences.questionAnswerUnreadTargetIds.includes(normalizedTargetId)) return preferences
  return {
    ...preferences,
    questionAnswerUnreadTargetIds: preferences.questionAnswerUnreadTargetIds.filter(id => id !== normalizedTargetId),
  }
}

export const readConnectionHealthPreferences = (scope: string): ConnectionHealthPreferences => {
  try {
    if (typeof window === 'undefined') return createDefaultConnectionHealthPreferences()
    const raw = window.localStorage.getItem(connectionHealthPreferencesStorageKey(scope))
    if (!raw) return createDefaultConnectionHealthPreferences()
    return normalizePreferences(JSON.parse(raw) as unknown)
  } catch {
    return createDefaultConnectionHealthPreferences()
  }
}

export const writeConnectionHealthPreferences = (
  scope: string,
  preferences: ConnectionHealthPreferences,
): void => {
  try {
    if (typeof window === 'undefined') return
    window.localStorage.setItem(
      connectionHealthPreferencesStorageKey(scope),
      JSON.stringify(normalizePreferences(preferences)),
    )
  } catch {
    // Local display preferences are optional and must not block the health page.
  }
}

export const mergeConnectionHealthGroupOrder = (
  storedOrder: string[],
  currentIds: string[],
): string[] => {
  const currentSet = new Set(currentIds)
  const result: string[] = []
  const seen = new Set<string>()

  for (const id of storedOrder) {
    if (currentSet.has(id) && !seen.has(id)) {
      result.push(id)
      seen.add(id)
    }
  }

  for (const id of currentIds) {
    if (!seen.has(id)) {
      result.push(id)
      seen.add(id)
    }
  }

  return result
}
