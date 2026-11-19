<script setup lang="ts">
import { accountNameText, currentAccountNames, displayAccountName, type AccountLabels } from '../../utils/connectionHealthAccountLabel'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Loader2, X } from 'lucide-vue-next'
import { t, te } from '@/locales'
import { getAccessToken } from '@/modules/auth/api/auth'
import { isTestConfigurationBlocked, testProtocolName } from '../../composables/useConnectionHealth'
import { ConnectionHealthApiError, discoverTargetModels, listTestQuestions, startQuestionAnswerBatch } from '../../api/connectionHealth'
import QuestionAnswerQueuePanel from './QuestionAnswerQueuePanel.vue'
import { refreshQuestionAnswerQueue } from '../../composables/useQuestionAnswerQueue'
import type {
  AdminGroupHealth,
  QuestionAnswerReasoningEffort,
  TestQuestion,
} from '../../types/connectionHealth'
import type { QuestionAnswerPreferences } from '../../utils/connectionHealthPreferences'
import { formatLastUsedAt } from '../../utils/userLastUsed'
import {
  batchModelCoverage,
  collectQuestionAnswerBatchTargets,
  displayedBatchModelIds,
  displayedBatchQuestionIds,
  mergeBatchModelPreference,
  questionAnswerDuplicateQuestionNames,
  questionAnswerReadableError,
  questionAnswerOperationKey,
  selectedBatchTargets,
  toggleBatchGroup,
  type QuestionAnswerBatchTarget,
} from '../../utils/questionAnswers'

const props = defineProps<{
  accountLabels?: AccountLabels
  groups: AdminGroupHealth[]
  hiddenTargetIds?: Set<string>
  preferenceScope: string
  preferences: QuestionAnswerPreferences
}>()

const emit = defineEmits<{
  (event: 'preferences-changed', value: QuestionAnswerPreferences): void
  (event: 'question-answer-started', targetId: string): void
  (event: 'question-answer-view', value: { targetId: string; batchId?: string }): void
}>()

interface BatchTargetPreview {
  target: QuestionAnswerBatchTarget
  compatibleModelIds: string[]
  incompatibleModelIds: string[]
  requestCount: number
  discoveryErrorKey: string
}

type BatchTargetOutcomeKind = 'started' | 'skipped' | 'failed' | 'unknown'

interface BatchTargetOutcome {
  targetId: string
  accountName: string
  batchId?: string
  kind: BatchTargetOutcomeKind
  compatibleModelIds: string[]
  incompatibleModelIds: string[]
  requestCount: number
  acceptedRequestCount: number | null
  reasonKey: string
  errorKey: string
}

interface BatchRunSnapshot {
  preferenceScope: string
  previews: BatchTargetPreview[]
  questionIds: string[]
  reasoningEffort: QuestionAnswerReasoningEffort
  repeatCount: number
  previewTotal: number
}

/** 模型读取缓存：键为账号，签名变了视为没有缓存。 */
interface DiscoveryEntry {
  signature: string
  token: number
  status: 'loading' | 'ok' | 'empty' | 'failed' | 'blocked'
  models: string[]
  errorKey: string
}

const MAX_CONCURRENT_DISCOVERY = 4
const efforts: QuestionAnswerReasoningEffort[] = ['low', 'medium', 'high', 'xhigh']

const visible = ref(false)
const running = ref(false)
const outcomes = ref<BatchTargetOutcome[]>([])
const runPlan = ref<BatchTargetPreview[]>([])
const submittingTarget = ref('')
const runPreviewTotal = ref(0)
const runTargetTotal = ref(0)
const startedAt = ref<number | null>(null)
const showDetails = ref(false)

const checkedGroupIds = ref<string[]>([])
const uncheckedTargetIds = ref<Set<string>>(new Set())
const questions = ref<TestQuestion[]>([])
const questionsState = ref<'idle' | 'loading' | 'loaded' | 'failed'>('idle')
const questionsError = ref('')
const questionsTouched = ref(false)
const modelsTouched = ref(false)
const discovery = ref<Record<string, DiscoveryEntry>>({})
let discoveryQueue: Array<{ targetId: string; token: number }> = []
let discoveryInFlight = 0
let discoveryToken = 0
let session = 0
let sessionController: AbortController | null = null
let runSequence = 0
let runController: AbortController | null = null
let runSessionIdentity: string | null = null

const hidden = computed(() => props.hiddenTargetIds ?? new Set<string>())
const incompleteGroups = computed(() => props.groups.filter(group => Boolean(group.accountsError)))
const visibleCount = (group: AdminGroupHealth): number => group.accounts.filter(account => account.targetId.trim() && !hidden.value.has(account.targetId)).length
const hiddenCount = (group: AdminGroupHealth): number => group.accounts.filter(account => hidden.value.has(account.targetId)).length
const allTargets = computed(() => new Map(collectQuestionAnswerBatchTargets(props.groups).map(target => [target.targetId, target])))
const selectedTargets = computed(() => selectedBatchTargets(props.groups, checkedGroupIds.value, uncheckedTargetIds.value, hidden.value))
const selectedIdSet = computed(() => new Set(selectedTargets.value.map(target => target.targetId)))
const accountModels = computed(() => {
  const result = new Map<string, string>()
  for (const group of props.groups) for (const account of group.accounts) if (!result.has(account.targetId.trim())) result.set(account.targetId.trim(), account.models ?? '')
  return result
})
// 签名包含账号声明的模型列表与测试配置；任一变化都视为没有缓存，需要重新读取。
const targetSignature = (target: QuestionAnswerBatchTarget): string => JSON.stringify({ testConfiguration: target.testConfiguration ?? null, platform: target.platform, status: target.status, models: accountModels.value.get(target.targetId) ?? '' })
const entryFor = (target: QuestionAnswerBatchTarget): DiscoveryEntry | undefined => {
  const entry = discovery.value[target.targetId]
  return entry && entry.signature === targetSignature(target) ? entry : undefined
}
const groupAccounts = (group: AdminGroupHealth): QuestionAnswerBatchTarget[] => {
  const seen = new Set<string>()
  const result: QuestionAnswerBatchTarget[] = []
  for (const account of group.accounts) {
    const target = allTargets.value.get(account.targetId.trim())
    if (!target || seen.has(target.targetId) || hidden.value.has(target.targetId)) continue
    seen.add(target.targetId)
    result.push(target)
  }
  return result
}
const otherGroupNames = (target: QuestionAnswerBatchTarget, group: AdminGroupHealth): string => target.groupNames.filter(name => name !== group.name).join('、')

// 第 1 步：题目、思考力度、次数
const enabledQuestions = computed(() => questions.value.filter(question => question.enabled))
const questionIds = computed(() => displayedBatchQuestionIds(props.preferences.questionIds, questions.value, questionsTouched.value))
const duplicateQuestionNames = computed(() => questionAnswerDuplicateQuestionNames(enabledQuestions.value.filter(question => questionIds.value.includes(question.id))))

// 第 3 步：模型
const discoveryProgress = computed(() => {
  const total = selectedTargets.value.length
  const done = selectedTargets.value.filter(target => { const entry = entryFor(target); return entry && entry.status !== 'loading' }).length
  return { done, total, loading: done < total }
})
const coverage = computed(() => batchModelCoverage(selectedTargets.value.flatMap(target => { const entry = entryFor(target); return entry?.status === 'ok' ? [entry.models] : [] })))
const coverageIds = computed(() => coverage.value.map(item => item.id))
const readableModelTargets = computed(() => selectedTargets.value.filter(target => entryFor(target)?.status === 'ok').length)
const modelIds = computed(() => displayedBatchModelIds(props.preferences.modelIds, coverageIds.value, modelsTouched.value))
// 记住的选择不可用、页面显示的是默认题或默认模型（未写入偏好）时，沿用旧提示。开始提交后不再显示。
const defaultSelectionShown = computed(() => {
  if (running.value || startedAt.value !== null) return false
  const savedQuestions = new Set(props.preferences.questionIds)
  const questionDefault = !questionsTouched.value && questionsState.value === 'loaded' && questionIds.value.length > 0 && !questionIds.value.some(id => savedQuestions.has(id))
  const savedModels = new Set(props.preferences.modelIds)
  const modelDefault = !modelsTouched.value && modelIds.value.length > 0 && !modelIds.value.some(id => savedModels.has(id))
  return questionDefault || modelDefault
})

// 第 4 步：每个账号能提交多少
const previews = computed<BatchTargetPreview[]>(() => selectedTargets.value.map((target) => {
  const entry = entryFor(target)
  const preview: BatchTargetPreview = { target, compatibleModelIds: [], incompatibleModelIds: [...modelIds.value], requestCount: 0, discoveryErrorKey: '' }
  if (!entry || entry.status === 'loading') return preview
  if (entry.status === 'blocked' || entry.status === 'failed') { preview.discoveryErrorKey = entry.errorKey; return preview }
  const available = new Set(entry.models)
  preview.compatibleModelIds = modelIds.value.filter(id => available.has(id))
  preview.incompatibleModelIds = modelIds.value.filter(id => !available.has(id))
  preview.requestCount = preview.compatibleModelIds.length * questionIds.value.length * props.preferences.repeatCount
  return preview
}))
const previewLine = (preview: BatchTargetPreview): string => {
  const entry = entryFor(preview.target)
  if (!entry || entry.status === 'loading') return '正在读取模型…'
  if (entry.status === 'blocked') return `测试配置不可用：${safeConnectionHealthError(entry.errorKey)}，会跳过；请到该分组的测试配置检查`
  if (entry.status === 'failed') return `模型没读到：${safeConnectionHealthError(entry.errorKey)}，会跳过；可点「重新读取模型」`
  if (entry.status === 'empty') return '这个账号没有读到任何模型，会跳过；请先在主站确认它提供模型，再重新打开本抽屉'
  if (preview.compatibleModelIds.length === 0) return '没有所选模型，会跳过'
  if (preview.requestCount > 50) return '单个账号最多 50 次请求（模型数 × 题目数 × 次数），请少选一些'
  return `${preview.requestCount} 次`
}
const runnablePreviews = computed(() => previews.value.filter(preview => preview.requestCount > 0 && preview.requestCount <= 50 && !preview.discoveryErrorKey))
const previewTotal = computed(() => runnablePreviews.value.reduce((total, preview) => total + preview.requestCount, 0))
const overLimitPreview = computed(() => previews.value.find(preview => preview.requestCount > 50))
const failedDiscoveryCount = computed(() => selectedTargets.value.filter(target => entryFor(target)?.status === 'failed').length)
const blockReason = computed((): string => {
  if (running.value) return '正在提交，请等这一轮结束'
  if (questionsState.value === 'failed') return '测试题没读到，请点「重新读取」'
  if (questionsState.value !== 'loaded') return '正在读取测试题，请稍等'
  if (questionIds.value.length === 0) return '先勾选题目'
  if (selectedTargets.value.length === 0) return '先勾选分组或账号'
  if (discoveryProgress.value.loading) return '正在读取账号模型，请稍等'
  if (coverageIds.value.length === 0) return '所选账号都不能测，请看上面每个账号的原因'
  if (modelIds.value.length === 0) return '先勾选模型'
  if (overLimitPreview.value) return `${displayAccountName(props.accountLabels?.[overLimitPreview.value.target.targetId]?.name, overLimitPreview.value.target.accountName)} 超过单个账号 50 次请求，请少选一些`
  if (runnablePreviews.value.length === 0) return '所选账号都不能测，请看上面每个账号的原因'
  return ''
})
const canStart = computed(() => visible.value && incompleteGroups.value.length === 0 && !blockReason.value)

// 运行结果（沿用现有逐个提交与结果展示）
const waitingTargets = computed(() => runPlan.value.filter(preview => preview.target.targetId !== submittingTarget.value && !outcomes.value.some(outcome => outcome.targetId === preview.target.targetId)))
const submittingAccount = computed(() => runPlan.value.find(preview => preview.target.targetId === submittingTarget.value)?.target.accountName)
const displayedPreviewTotal = computed(() => running.value ? runPreviewTotal.value : previewTotal.value)
const confirmedRequestTotal = computed(() => outcomes.value.reduce((total, outcome) => total + (outcome.acceptedRequestCount ?? 0), 0))
const unknownCount = computed(() => outcomes.value.filter(outcome => outcome.kind === 'unknown').length)
const acceptedRequestTotal = computed(() => unknownCount.value || running.value ? '尚未确认' : confirmedRequestTotal.value)
const queueAccounts = computed(() => ({ ...Object.fromEntries(collectQuestionAnswerBatchTargets(props.groups).map(target => [target.targetId, accountNameText(target.accountName)])), ...currentAccountNames(props.accountLabels) }))
const startedCount = computed(() => outcomes.value.filter(outcome => outcome.kind === 'started').length)
const skippedCount = computed(() => outcomes.value.filter(outcome => outcome.kind === 'skipped').length)
const failedCount = computed(() => outcomes.value.filter(outcome => outcome.kind === 'failed').length)
const startedAtLabel = computed(() => startedAt.value === null ? '' : formatLastUsedAt(new Date(startedAt.value)).slice(11, 16))

const safeConnectionHealthError = (rawKey: string): string => questionAnswerReadableError(rawKey, t, te)

// ---- 模型读取：最多 4 个并发，按账号缓存，发出前再核对选中状态与签名 ----
const pumpDiscovery = () => {
  const current = session
  const controller = sessionController
  while (discoveryInFlight < MAX_CONCURRENT_DISCOVERY && discoveryQueue.length > 0) {
    const queued = discoveryQueue.shift()!
    const targetId = queued.targetId
    const target = allTargets.value.get(targetId)
    const entry = discovery.value[targetId]
    // 只发入队时登记的那一条：签名变化后重新入队的会带新 token，旧项直接丢弃，保证每个 token 只请求一次。
    if (!entry || entry.token !== queued.token) continue
    if (!target || !selectedIdSet.value.has(targetId)) { delete discovery.value[targetId]; continue }
    const signature = targetSignature(target)
    if (entry.signature !== signature || entry.status !== 'loading') continue
    const token = entry.token
    // 只有发出请求时登记的这一条记录还在，响应才能写入缓存；旧请求的迟到响应丢弃。
    const owns = () => current === session && discovery.value[targetId]?.token === token
    discoveryInFlight++
    void discoverTargetModels(targetId, controller?.signal).then((models) => {
      if (!owns()) return
      const ids = models.map(model => model.id)
      discovery.value = { ...discovery.value, [targetId]: { signature, token, status: ids.length ? 'ok' : 'empty', models: ids, errorKey: '' } }
    }).catch((error: unknown) => {
      if (error instanceof Error && error.name === 'AbortError') return
      if (!owns()) return
      discovery.value = { ...discovery.value, [targetId]: { signature, token, status: 'failed', models: [], errorKey: error instanceof Error ? error.message : 'admin.connectionHealth.errors.request' } }
    }).finally(() => {
      if (current !== session) return
      discoveryInFlight--
      pumpDiscovery()
    })
  }
}

const scheduleDiscovery = () => {
  if (!visible.value || running.value) return
  const next = { ...discovery.value }
  let changed = false
  for (const target of selectedTargets.value) {
    const signature = targetSignature(target)
    const entry = next[target.targetId]
    if (entry && entry.signature === signature) continue
    if (isTestConfigurationBlocked(target.testConfiguration)) {
      next[target.targetId] = { signature, token: ++discoveryToken, status: 'blocked', models: [], errorKey: target.testConfiguration?.blockedReason || 'admin.connectionHealth.errors.testConfigurationUnavailable' }
    } else {
      const token = ++discoveryToken
      next[target.targetId] = { signature, token, status: 'loading', models: [], errorKey: '' }
      discoveryQueue.push({ targetId: target.targetId, token })
    }
    changed = true
  }
  // 排队中但已不再选中的账号出队（未发出请求）。
  discoveryQueue = discoveryQueue.filter(({ targetId, token }) => {
    if (next[targetId]?.token !== token) return false
    if (selectedIdSet.value.has(targetId)) return true
    if (next[targetId]?.status === 'loading') { delete next[targetId]; changed = true }
    return false
  })
  if (changed) discovery.value = next
  pumpDiscovery()
}

const retryFailedDiscovery = () => {
  const next = { ...discovery.value }
  for (const target of selectedTargets.value) if (next[target.targetId]?.status === 'failed') delete next[target.targetId]
  discovery.value = next
  scheduleDiscovery()
}

const resetSession = () => {
  session++
  sessionController?.abort()
  sessionController = new AbortController()
  discovery.value = {}
  discoveryQueue = []
  discoveryInFlight = 0
}

const loadQuestions = async () => {
  const current = session
  questionsState.value = 'loading'
  questionsError.value = ''
  try {
    const result = await listTestQuestions(sessionController?.signal)
    if (current !== session) return
    questions.value = result
    questionsState.value = 'loaded'
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    if (current !== session) return
    questionsState.value = 'failed'
    questionsError.value = safeConnectionHealthError(error instanceof Error ? error.message : 'admin.connectionHealth.errors.request')
  }
}

const open = () => {
  visible.value = true
  if (running.value) return
  checkedGroupIds.value = []
  uncheckedTargetIds.value = new Set()
  questionsTouched.value = false
  modelsTouched.value = false
  showDetails.value = false
  questions.value = []
  questionsState.value = 'loading'
  resetSession()
  void loadQuestions()
}

const close = () => {
  visible.value = false
  if (running.value) return
  session++
  sessionController?.abort()
  sessionController = null
  discovery.value = {}
  discoveryQueue = []
  discoveryInFlight = 0
}

const toggleGroup = (groupId: string) => {
  if (running.value) return
  const next = toggleBatchGroup(props.groups, checkedGroupIds.value, uncheckedTargetIds.value, groupId)
  checkedGroupIds.value = next.checkedGroupIds
  uncheckedTargetIds.value = next.uncheckedTargetIds
  scheduleDiscovery()
}

const toggleTarget = (targetId: string) => {
  if (running.value) return
  const next = new Set(uncheckedTargetIds.value)
  if (next.has(targetId)) next.delete(targetId)
  else next.add(targetId)
  uncheckedTargetIds.value = next
  scheduleDiscovery()
}

const toggleQuestion = (questionId: string) => {
  if (running.value) return
  // 先取当前显示的勾选（可能是未写入的默认题），再标记“已改动”。
  const current = questionIds.value
  questionsTouched.value = true
  const next = current.includes(questionId) ? current.filter(id => id !== questionId) : [...current, questionId]
  const saved = props.preferences.questionIds.filter(id => !enabledQuestions.value.some(question => question.id === id))
  emit('preferences-changed', { ...props.preferences, questionIds: [...saved, ...next] })
}

const toggleModel = (modelId: string) => {
  if (running.value) return
  const current = modelIds.value
  const next = current.includes(modelId) ? current.filter(id => id !== modelId) : [...current, modelId]
  modelsTouched.value = true
  emit('preferences-changed', { ...props.preferences, modelIds: mergeBatchModelPreference(props.preferences.modelIds, coverageIds.value, next) })
}

const setEffort = (value: string) => {
  if (running.value || !efforts.includes(value as QuestionAnswerReasoningEffort)) return
  emit('preferences-changed', { ...props.preferences, reasoningEffort: value as QuestionAnswerReasoningEffort })
}
const setRepeat = (value: string) => {
  const count = Number(value)
  if (running.value || !Number.isInteger(count) || count < 1 || count > 10) return
  emit('preferences-changed', { ...props.preferences, repeatCount: count })
}

const runIsCurrent = (sequence: number, scope: string): boolean => {
  if (sequence !== runSequence || scope !== props.preferenceScope) return false
  if (runSessionIdentity !== getAccessToken()) {
    cancelRun(); outcomes.value = []; startedAt.value = null
    return false
  }
  return true
}

const start = async () => {
  if (!canStart.value) return
  const snapshot: BatchRunSnapshot = {
    preferenceScope: props.preferenceScope,
    previews: previews.value.map(preview => ({ ...preview, compatibleModelIds: [...preview.compatibleModelIds], incompatibleModelIds: [...preview.incompatibleModelIds] })),
    questionIds: [...questionIds.value],
    reasoningEffort: props.preferences.reasoningEffort,
    repeatCount: props.preferences.repeatCount,
    previewTotal: previewTotal.value,
  }
  const sequence = ++runSequence
  runSessionIdentity = getAccessToken()
  const controller = new AbortController()
  runController = controller
  running.value = true
  outcomes.value = []
  runPlan.value = snapshot.previews
  submittingTarget.value = ''
  runPreviewTotal.value = snapshot.previewTotal
  runTargetTotal.value = snapshot.previews.length
  startedAt.value = Date.now()

  try {
    for (const preview of snapshot.previews) {
      if (!runIsCurrent(sequence, snapshot.preferenceScope)) return
      if (preview.discoveryErrorKey) {
        outcomes.value = [...outcomes.value, { targetId: preview.target.targetId, accountName: preview.target.accountName, kind: 'failed', compatibleModelIds: [], incompatibleModelIds: [...preview.incompatibleModelIds], requestCount: 0, acceptedRequestCount: 0, reasonKey: 'discoveryFailed', errorKey: preview.discoveryErrorKey }]
        continue
      }
      const modelIdsForTarget = [...preview.compatibleModelIds]
      const requestCount = modelIdsForTarget.length * snapshot.questionIds.length * snapshot.repeatCount
      if (modelIdsForTarget.length === 0 || requestCount > 50) {
        outcomes.value = [...outcomes.value, { targetId: preview.target.targetId, accountName: preview.target.accountName, kind: 'skipped', compatibleModelIds: modelIdsForTarget, incompatibleModelIds: [...preview.incompatibleModelIds], requestCount, acceptedRequestCount: 0, reasonKey: 'noCompatibleModels', errorKey: '' }]
        continue
      }

      try {
        submittingTarget.value = preview.target.targetId
        const batch = await startQuestionAnswerBatch(preview.target.targetId, [...modelIdsForTarget], [...snapshot.questionIds], snapshot.reasoningEffort, snapshot.repeatCount, controller.signal)
        if (!runIsCurrent(sequence, snapshot.preferenceScope)) return
        outcomes.value = [...outcomes.value, { targetId: preview.target.targetId, accountName: preview.target.accountName, kind: 'started', batchId: batch.batchId, compatibleModelIds: [...modelIdsForTarget], incompatibleModelIds: [...preview.incompatibleModelIds], requestCount, acceptedRequestCount: batch.submittedCount, reasonKey: 'started', errorKey: '' }]
        emit('question-answer-started', preview.target.targetId)
      } catch (error) {
        if (error instanceof Error && error.name === 'AbortError') { runIsCurrent(sequence, snapshot.preferenceScope); return }
        if (!runIsCurrent(sequence, snapshot.preferenceScope)) return
        const active = questionAnswerOperationKey(error) === 'admin.connectionHealth.errors.questionAnswerActive'
        const unknown = !active && (!(error instanceof ConnectionHealthApiError) || error.operationResult !== 'not_started')
        outcomes.value = [...outcomes.value, { targetId: preview.target.targetId, accountName: preview.target.accountName, kind: active ? 'skipped' : unknown ? 'unknown' : 'failed', compatibleModelIds: [...modelIdsForTarget], incompatibleModelIds: [...preview.incompatibleModelIds], requestCount, acceptedRequestCount: unknown ? null : 0, reasonKey: active ? 'activeBatch' : unknown ? 'submissionUnknown' : 'startFailed', errorKey: active ? '' : (error instanceof Error ? error.message : 'admin.connectionHealth.errors.request') }]
        refreshQuestionAnswerQueue(snapshot.preferenceScope)
      } finally {
        if (runIsCurrent(sequence, snapshot.preferenceScope)) submittingTarget.value = ''
      }
    }
  } finally {
    if (runController === controller) runController = null
    if (runIsCurrent(sequence, snapshot.preferenceScope)) {
      running.value = false
      if (visible.value) scheduleDiscovery()
    }
  }
}

const cancelRun = () => {
  runSequence++
  runController?.abort()
  runController = null
  runSessionIdentity = null
  running.value = false
  submittingTarget.value = ''
  runPlan.value = []
}

const outcomeReason = (outcome: BatchTargetOutcome): string => {
  switch (outcome.reasonKey) {
    case 'activeBatch': return t('admin.connectionHealth.questionAnswerBatch.outcomes.activeBatch')
    case 'noCompatibleModels': return t('admin.connectionHealth.questionAnswerBatch.outcomes.noCompatibleModels')
    case 'discoveryFailed': return t('admin.connectionHealth.questionAnswerBatch.outcomes.discoveryFailed')
    case 'startFailed': return t('admin.connectionHealth.questionAnswerBatch.outcomes.startFailed')
    case 'submissionUnknown': return '提交未确认：创建请求的返回未确认，这不代表后台没有接收；请查看账号最近批次和时间核对，本次不会自动重发。'
    default: return t('admin.connectionHealth.questionAnswerBatch.outcomes.started')
  }
}

defineExpose({ open })
const candidateSignature = computed(() => JSON.stringify(selectedTargets.value.map(target => [target.targetId, targetSignature(target)])))
watch(candidateSignature, () => { if (visible.value && !running.value) scheduleDiscovery() })
watch(() => props.preferenceScope, (scope, previousScope) => {
  if (scope === previousScope) return
  visible.value = false
  cancelRun()
  resetSession()
  checkedGroupIds.value = []
  uncheckedTargetIds.value = new Set()
  outcomes.value = []
  runPreviewTotal.value = 0
  runTargetTotal.value = 0
  startedAt.value = null
}, { flush: 'sync' })
onBeforeUnmount(() => {
  session++
  sessionController?.abort()
  cancelRun()
})
</script>

<template>
  <div class="border-b border-border/50 p-4">
    <button
      type="button"
      data-testid="question-answer-batch-open"
      class="inline-flex h-9 w-full items-center justify-center rounded-lg border border-border/60 bg-background px-3 text-sm font-medium text-foreground transition-colors hover:bg-surface"
      @click="open"
    >
      {{ t('admin.connectionHealth.questionAnswerBatch.entry') }}
    </button>
    <p v-if="startedAt !== null" data-testid="question-answer-batch-entry-summary" class="mt-2 text-xs leading-5 text-muted-foreground">
      {{ t('admin.connectionHealth.questionAnswerBatch.entrySummary', { time: startedAtLabel, processed: outcomes.length, total: runTargetTotal, started: startedCount, skipped: skippedCount, failed: failedCount, accepted: acceptedRequestTotal }) }}
    </p>
  </div>

  <div v-if="visible" data-testid="question-answer-batch-drawer" class="fixed inset-0 z-50 flex justify-end bg-black/35" role="dialog" aria-modal="true" :aria-label="t('admin.connectionHealth.questionAnswerBatch.title')">
    <section class="flex h-full w-full max-w-xl flex-col overflow-hidden border-l border-border/60 bg-background shadow-xl">
      <header class="flex items-center justify-between border-b border-border/60 px-5 py-4">
        <div>
          <h2 class="text-base font-semibold text-foreground">{{ t('admin.connectionHealth.questionAnswerBatch.title') }}</h2>
          <p class="mt-1 text-xs text-muted-foreground">{{ t('admin.connectionHealth.questionAnswerBatch.workspace', { scope: preferenceScope }) }}</p>
        </div>
        <button type="button" data-testid="question-answer-batch-close" class="rounded-md p-2 text-muted-foreground hover:bg-surface hover:text-foreground" :aria-label="t('admin.connectionHealth.questionAnswerBatch.close')" @click="close">
          <X class="h-4 w-4" />
        </button>
      </header>

      <div data-testid="question-answer-batch-body" class="min-h-0 flex-1 space-y-4 overflow-y-auto p-5">
        <p class="text-xs text-muted-foreground">关闭抽屉后本轮提交和已接收任务继续。刷新页面会丢失尚未提交的计划；已接收任务仍可查看。</p>
        <QuestionAnswerQueuePanel :workspace="preferenceScope" :visible="visible" :active="running" :accounts="queueAccounts" @view="emit('question-answer-view', $event)" />
        <div v-if="incompleteGroups.length" class="rounded-lg border border-amber-500/30 bg-amber-500/10 p-4 text-sm text-amber-700 dark:text-amber-300">
          <p class="font-medium">{{ t('admin.connectionHealth.questionAnswerBatch.incomplete') }}</p>
          <ul class="mt-2 list-disc space-y-1 pl-5">
            <li v-for="group in incompleteGroups" :key="group.id">{{ group.name }}</li>
          </ul>
        </div>

        <template v-else>
          <p v-if="defaultSelectionShown" data-testid="question-answer-default-restored" class="text-xs text-muted-foreground">没有可用的记住选择，已选默认模型/题目。请确认本次配置，再点开始；此时还没有发请求。</p>
          <p v-if="running" data-testid="question-answer-batch-running-lock" class="rounded-lg bg-surface px-3 py-2 text-xs text-foreground">本轮正在提交，结束后可以选择下一轮。下面是本轮开始时确定的账号和进度。</p>

          <section v-if="!running" data-testid="question-answer-batch-step-questions" class="space-y-2">
            <h3 class="text-sm font-semibold text-foreground">1 选题目</h3>
            <p class="text-xs text-muted-foreground">勾选要问的题目。选择会被记住，单个账号问答也用同一份。</p>
            <p v-if="questionsState === 'loading'" class="text-xs text-muted-foreground">正在读取测试题…</p>
            <p v-else-if="questionsState === 'failed'" class="break-words text-xs text-destructive">测试题没读到：{{ questionsError }}。请点「重新读取」；仍失败请检查后端服务。 <button type="button" class="underline" @click="loadQuestions">重新读取</button></p>
            <p v-else-if="questionsState === 'loaded' && enabledQuestions.length === 0" class="text-xs text-destructive">没有启用的测试题，请先到设置页启用至少一题。</p>
            <div class="max-h-52 space-y-2 overflow-y-auto">
              <label v-for="question in enabledQuestions" :key="question.id" class="flex items-start gap-2 rounded-md border border-border/40 p-2 text-xs">
                <input type="checkbox" :data-question-id="question.id" :checked="questionIds.includes(question.id)" :disabled="running" @change="toggleQuestion(question.id)">
                <span class="break-words">{{ question.name }}<span class="mt-1 block text-muted-foreground">{{ question.keywords.length ? question.keywords.join('、') : '仅人工判断' }}</span></span>
              </label>
            </div>
            <p v-if="duplicateQuestionNames.length" data-testid="question-answer-batch-duplicate-questions" class="text-xs text-amber-600">{{ duplicateQuestionNames.join('、') }}的题干相同，但按独立题目分别计分。重复内容不会增加测试范围，建议换成不同内容。</p>
            <div class="grid grid-cols-2 gap-3">
              <label class="text-xs">思考力度<select :value="preferences.reasoningEffort" aria-label="思考力度" data-testid="question-answer-batch-effort" class="mt-2 w-full rounded-lg border border-border/50 bg-background p-2" :disabled="running" @change="setEffort(($event.target as HTMLSelectElement).value)"><option v-for="effort in efforts" :key="effort" :value="effort">{{ effort }}</option></select></label>
              <label class="text-xs">每题重复次数<select :value="preferences.repeatCount" aria-label="每题重复次数" data-testid="question-answer-batch-repeat" class="mt-2 w-full rounded-lg border border-border/50 bg-background p-2" :disabled="running" @change="setRepeat(($event.target as HTMLSelectElement).value)"><option v-for="count in 10" :key="count" :value="count">{{ count }}</option></select></label>
            </div>
          </section>

          <section v-if="!running" data-testid="question-answer-batch-step-groups" class="space-y-2">
            <h3 class="text-sm font-semibold text-foreground">2 选分组和账号</h3>
            <p class="text-xs text-muted-foreground">勾选要测的分组，它的账号会在下方展开并默认全选，把不想测的取消即可。已屏蔽的账号不会出现。</p>
            <div class="space-y-1 rounded-lg border border-border/50 p-3">
              <div v-for="group in groups" :key="`batch-group:${group.id}`">
                <label class="flex items-start gap-2 text-sm">
                  <input type="checkbox" :data-group-id="group.id" :checked="checkedGroupIds.includes(group.id)" :disabled="running || visibleCount(group) === 0" @change="toggleGroup(group.id)">
                  <span class="min-w-0 break-words">
                    <span class="font-medium text-foreground">{{ group.name }}</span>
                    <span class="text-xs text-muted-foreground"> · {{ visibleCount(group) }} 个账号<template v-if="hiddenCount(group)"> · 另有 {{ hiddenCount(group) }} 个已屏蔽</template></span>
                    <span v-if="visibleCount(group) === 0" class="block text-xs text-muted-foreground">没有可测账号</span>
                  </span>
                </label>
                <div v-if="checkedGroupIds.includes(group.id)" class="ml-6 mt-1 space-y-1 border-l border-border/50 pl-3">
                  <label v-for="target in groupAccounts(group)" :key="`${group.id}:${target.targetId}`" class="flex items-start gap-2 text-xs">
                    <input type="checkbox" :data-target-id="target.targetId" :checked="selectedIdSet.has(target.targetId)" :disabled="running" @change="toggleTarget(target.targetId)">
                    <span class="min-w-0 break-words">
                      <span :data-testid="`question-answer-batch-target-name-${target.targetId}`" class="text-foreground">{{ displayAccountName(props.accountLabels?.[target.targetId]?.name, target.accountName) }}</span>
                      <span v-if="otherGroupNames(target, group)" class="block text-muted-foreground">也在：{{ otherGroupNames(target, group) }}</span>
                    </span>
                  </label>
                </div>
              </div>
            </div>
            <p data-testid="question-answer-batch-selection-summary" class="text-xs text-muted-foreground">已选 {{ checkedGroupIds.length }} 个分组、{{ selectedTargets.length }} 个账号</p>
          </section>

          <section v-if="!running && selectedTargets.length" data-testid="question-answer-batch-step-models" class="space-y-2">
            <h3 class="text-sm font-semibold text-foreground">3 选模型</h3>
            <p class="text-xs text-muted-foreground">列出所选账号能用的模型，后面的数字是有这个模型的账号数。账号没有的模型会自动跳过。</p>
            <p v-if="discoveryProgress.loading" class="flex items-center gap-2 text-xs text-muted-foreground"><Loader2 class="h-3.5 w-3.5 animate-spin" />正在读取账号模型 {{ discoveryProgress.done }}/{{ discoveryProgress.total }}…</p>
            <p v-else-if="coverage.length === 0" class="text-xs text-destructive">所选账号都没有可选的模型，暂时不能选模型。请在下方查看每个账号的原因。</p>
            <div class="grid gap-2 sm:grid-cols-2">
              <label v-for="model in coverage" :key="model.id" class="flex min-w-0 gap-2 rounded-md border border-border/40 p-2 text-xs">
                <input type="checkbox" :data-model-id="model.id" :checked="modelIds.includes(model.id)" :disabled="running" @change="toggleModel(model.id)">
                <span class="min-w-0 break-all">{{ model.id }}<span class="block text-muted-foreground">{{ model.count }}/{{ readableModelTargets }} 个账号有</span></span>
              </label>
            </div>
          </section>

          <section v-if="!running && selectedTargets.length" data-testid="question-answer-batch-step-confirm" class="space-y-2">
            <h3 class="text-sm font-semibold text-foreground">4 确认并开始</h3>
            <p data-testid="question-answer-batch-total" class="text-sm text-foreground">共 {{ runnablePreviews.length }} 个账号、{{ previewTotal }} 次请求</p>
            <div class="space-y-1">
              <p v-for="preview in previews" :key="`preview:${preview.target.targetId}`" :data-testid="`question-answer-batch-preview-${preview.target.targetId}`" class="break-words text-xs text-muted-foreground">
                <span class="text-foreground">{{ displayAccountName(props.accountLabels?.[preview.target.targetId]?.name, preview.target.accountName) }}</span> · {{ previewLine(preview) }}
              </p>
            </div>
            <button v-if="failedDiscoveryCount" type="button" class="text-xs text-primary underline" @click="retryFailedDiscovery">重新读取模型</button>
            <button type="button" class="block text-xs text-muted-foreground underline" :aria-expanded="showDetails" @click="showDetails = !showDetails">{{ showDetails ? '收起明细' : '查看每个账号的明细' }}</button>
            <div v-if="showDetails" class="space-y-2">
              <div v-for="preview in previews" :key="`detail:${preview.target.targetId}`" :data-testid="`question-answer-batch-detail-${preview.target.targetId}`" class="rounded-lg border border-border/50 px-3 py-2 text-xs text-muted-foreground">
                <p class="break-all font-medium text-foreground">{{ displayAccountName(props.accountLabels?.[preview.target.targetId]?.name, preview.target.accountName) }} <span v-if="preview.target.testConfiguration" class="text-xs text-muted-foreground">· {{ testProtocolName(preview.target.testConfiguration.protocol) || t('admin.connectionHealth.testConfiguration.' + preview.target.testConfiguration.status) }}<template v-if="preview.target.testConfiguration.probeTimeoutSeconds"> / {{ preview.target.testConfiguration.probeTimeoutSeconds }}s</template></span></p>
                <p v-if="preview.target.testConfiguration?.sourceGroups.length" class="break-words">{{ preview.target.testConfiguration.sourceGroups.map(source => `${source.adminGroupName || source.adminGroupId}: ${testProtocolName(source.protocol)} / ${source.probeTimeoutSeconds}s`).join('；') }}</p>
                <p>{{ t('admin.connectionHealth.testConfiguration.questionAnswerTimeout') }}</p>
                <p>{{ t('admin.connectionHealth.questionAnswerBatch.compatibleModels', { models: preview.compatibleModelIds.join('、') || '-' }) }}</p>
                <p v-if="preview.incompatibleModelIds.length">{{ t('admin.connectionHealth.questionAnswerBatch.incompatibleModels', { models: preview.incompatibleModelIds.join('、') }) }}</p>
              </div>
            </div>
          </section>
        </template>
        <!-- 本轮进度和结果不受分组读取状态影响：读取失败只阻止开始新一轮（canStart），不隐藏已提交的进度与结果。 -->
        <div v-if="running || outcomes.length > 0" data-testid="question-answer-batch-run-summary" class="space-y-3 rounded-lg border border-border/50 p-3 text-sm">
          <div class="flex flex-wrap gap-x-4 gap-y-1 text-muted-foreground">
            <span data-testid="question-answer-batch-run-preview-total">{{ t('admin.connectionHealth.questionAnswerBatch.runPreviewTotal', { total: runPreviewTotal }) }}</span>
            <span>{{ t('admin.connectionHealth.questionAnswerBatch.processed', { processed: outcomes.length, total: runTargetTotal }) }}</span>
            <span>{{ t('admin.connectionHealth.questionAnswerBatch.started', { count: startedCount }) }}</span>
            <span>{{ t('admin.connectionHealth.questionAnswerBatch.skipped', { count: skippedCount }) }}</span>
            <span>{{ t('admin.connectionHealth.questionAnswerBatch.failed', { count: failedCount }) }}</span>
          </div>
          <p v-if="unknownCount" class="font-medium text-foreground">实际接受总量尚未确认；已确认受理 {{ confirmedRequestTotal }} 条，另有 {{ unknownCount }} 个账号提交未确认。</p>
          <p v-else-if="running" class="font-medium text-foreground">已确认受理 {{ confirmedRequestTotal }} 条；实际总量等待确认。</p>
          <p v-else class="font-medium text-foreground">{{ t('admin.connectionHealth.questionAnswerBatch.acceptedTotal', { total: acceptedRequestTotal }) }}</p>
          <p v-if="submittingTarget" data-testid="question-answer-batch-submitting">{{ displayAccountName(props.accountLabels?.[submittingTarget]?.name, submittingAccount) }} · 正在提交：正在准备账号和题目，后台尚未确认接收。</p>
          <p v-for="preview in waitingTargets" :key="`waiting:${preview.target.targetId}`" data-testid="question-answer-batch-waiting">{{ displayAccountName(props.accountLabels?.[preview.target.targetId]?.name, preview.target.accountName) }} · 等待提交：尚未向后台创建这批题。</p>
          <div class="space-y-2">
            <div v-for="outcome in outcomes" :key="outcome.targetId" :data-testid="`question-answer-batch-outcome-${outcome.targetId}`" class="rounded-md bg-surface px-3 py-2">
              <p :data-testid="`question-answer-batch-outcome-name-${outcome.targetId}`" class="break-words font-medium text-foreground">{{ displayAccountName(props.accountLabels?.[outcome.targetId]?.name, outcome.accountName) }}</p>
              <p class="mt-0.5 break-words text-xs text-muted-foreground">{{ outcomeReason(outcome) }}</p>
              <button v-if="outcome.kind === 'started' && outcome.batchId" type="button" class="mt-2 rounded-md border border-border/60 px-2.5 py-1.5 text-xs font-medium hover:bg-surface-line" @click="emit('question-answer-view', { targetId: outcome.targetId, batchId: outcome.batchId })">查看该批次</button>
              <button v-else-if="outcome.reasonKey === 'activeBatch'" type="button" class="mt-2 rounded-md border border-border/60 px-2.5 py-1.5 text-xs font-medium hover:bg-surface-line" @click="emit('question-answer-view', { targetId: outcome.targetId })">打开该账号最近批次</button>
              <button v-else-if="outcome.kind === 'unknown'" type="button" class="mt-2 rounded-md border border-border/60 px-2.5 py-1.5 text-xs font-medium hover:bg-surface-line" @click="emit('question-answer-view', { targetId: outcome.targetId })">查看最近批次</button>
              <p v-if="outcome.errorKey" class="mt-0.5 break-words text-xs text-destructive">{{ safeConnectionHealthError(outcome.errorKey) }}</p>
            </div>
          </div>
        </div>
      </div>

      <footer data-testid="question-answer-batch-footer" class="shrink-0 border-t border-border/60 bg-background px-5 py-4">
        <p v-if="incompleteGroups.length === 0 && blockReason" data-testid="question-answer-batch-block-reason" class="mb-2 text-xs text-muted-foreground">{{ blockReason }}</p>
        <div class="flex items-center gap-2 text-xs text-muted-foreground">
          <span>{{ t('admin.connectionHealth.questionAnswerBatch.previewTotal', { total: displayedPreviewTotal }) }}</span>
        </div>
        <button type="button" data-testid="question-answer-batch-start" :disabled="!canStart" class="mt-2 inline-flex h-9 w-full items-center justify-center rounded-lg bg-primary px-4 text-sm font-medium text-primary-foreground disabled:cursor-not-allowed disabled:opacity-50" @click="start">
          {{ t('admin.connectionHealth.questionAnswerBatch.start') }}
        </button>
      </footer>
    </section>
  </div>
</template>
