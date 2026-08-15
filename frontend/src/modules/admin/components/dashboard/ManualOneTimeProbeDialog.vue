<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import {
  AlertTriangle,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  Loader2,
  ShieldAlert,
  StopCircle,
  X,
  XCircle,
  Zap,
} from 'lucide-vue-next'
import {
  connectionHealthMessageKey,
  connectionHealthRecordColorClass,
  formatConnectionHealthTime,
  useConnectionHealth,
} from '../../composables/useConnectionHealth'
import {
  cancelQuestionAnswerBatch,
  getLatestQuestionAnswerBatch,
  getQuestionAnswerBatch,
  getQuestionAnswerHistory,
  listTestQuestions,
  setQuestionAnswerManualError,
  startQuestionAnswerBatch,
} from '../../api/connectionHealth'
import type {
  ManualProbeModelOption,
  ManualProbeResult,
  ModelHealth,
  QuestionAnswerBatch,
  QuestionAnswerHistory,
  QuestionAnswerRecord,
  TestQuestion,
} from '../../types/connectionHealth'
import {
  isCurrentQuestionAnswerOperation,
  questionAnswerCompletedTodayInShanghai,
  questionAnswerElapsedMilliseconds,
  type QuestionAnswerOperationScope,
} from '../../utils/questionAnswers'
import { t, te } from '@/locales'

export interface ManualProbeTargetSummary {
  targetId: string
  accountName: string
  platform: string
  type: string
  status: string
  groupName: string
  formalModels: ManualProbeModelOption[]
}

const props = defineProps<{
  open: boolean
  target: ManualProbeTargetSummary | null
}>()

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'completed'): void
  (event: 'question-answer-started', targetId: string): void
  (event: 'question-answer-viewed', targetId: string): void
}>()

const prefix = 'admin.connectionHealth.manualProbeDialog'
const { discoverModels, runManualProbeOnce, manualProbeTarget, errorKey: serviceErrorKey } = useConnectionHealth()

type Phase = 'loading' | 'ready' | 'testing' | 'error'
type ProbeMode = 'once' | 'formal' | 'questionAnswer'

const phase = ref<Phase>('loading')
const mode = ref<ProbeMode>('once')
const models = ref<ManualProbeModelOption[]>([])
const onceModels = ref<ManualProbeModelOption[]>([])
const onceLoadState = ref<'loading' | 'ready' | 'error'>('loading')
const selected = ref<Set<string>>(new Set())
const results = ref<ManualProbeResult[]>([])
const loadErrorKey = ref('')
const testErrorKey = ref('')
const formalProgress = ref<'starting' | 'queued' | 'direct' | 'running' | ''>('')

const qaQuestions = ref<TestQuestion[]>([])
const qaSelectedQuestions = ref<Set<string>>(new Set())
const qaLoading = ref(false)
const qaStarting = ref(false)
const qaCancelling = ref(false)
const qaBatch = ref<QuestionAnswerBatch | null>(null)
const qaHistory = ref<QuestionAnswerHistory>({
  records: [],
  page: 1,
  pageSize: 20,
  totalItems: 0,
  totalPages: 0,
  stats: { total: 0, normal: 0, errors: 0 },
  todayStats: { total: 0, normal: 0, errors: 0 },
})
const qaCurrentExpanded = ref<Set<string>>(new Set())
const qaExpanded = ref<Set<string>>(new Set())
const qaMarking = ref<Set<string>>(new Set())
const qaErrorKey = ref('')
const qaCompletedNotice = ref(false)
const qaClockNow = ref(Date.now())

let loadSequence = 0
let activeRequestController: AbortController | null = null
let modelDiscoveryController: AbortController | null = null
let qaDataController: AbortController | null = null
let qaPollController: AbortController | null = null
let qaCancelController: AbortController | null = null
let qaPollTimer: ReturnType<typeof setTimeout> | null = null
let qaClockTimer: ReturnType<typeof setInterval> | null = null
let qaStartSequence = 0
let qaCancelSequence = 0

const cancelActiveRequest = () => {
  activeRequestController?.abort()
  activeRequestController = null
}

const beginRequest = (): AbortController => {
  cancelActiveRequest()
  const controller = new AbortController()
  activeRequestController = controller
  return controller
}

const finishRequest = (controller: AbortController) => {
  if (activeRequestController === controller) activeRequestController = null
}

const cancelModelDiscovery = () => {
  modelDiscoveryController?.abort()
  modelDiscoveryController = null
}

const beginModelDiscovery = (): AbortController => {
  cancelModelDiscovery()
  const controller = new AbortController()
  modelDiscoveryController = controller
  return controller
}

const finishModelDiscovery = (controller: AbortController) => {
  if (modelDiscoveryController === controller) modelDiscoveryController = null
}

const clearQuestionAnswerPolling = () => {
  if (qaPollTimer) clearTimeout(qaPollTimer)
  qaPollTimer = null
  qaPollController?.abort()
  qaPollController = null
}

const clearQuestionAnswerClock = () => {
  if (qaClockTimer) clearInterval(qaClockTimer)
  qaClockTimer = null
}

const startQuestionAnswerClock = () => {
  qaClockNow.value = Date.now()
  if (qaClockTimer) return
  qaClockTimer = setInterval(() => {
    qaClockNow.value = Date.now()
  }, 1000)
}

const cancelQuestionAnswerRequests = () => {
  clearQuestionAnswerPolling()
  clearQuestionAnswerClock()
  qaDataController?.abort()
  qaDataController = null
  qaCancelController?.abort()
  qaCancelController = null
  qaCancelSequence++
  qaCancelling.value = false
}

const cancelQuestionAnswerStart = () => {
  qaStartSequence++
  if (qaStarting.value) cancelActiveRequest()
  qaStarting.value = false
}

const cleanupFrontendWork = () => {
  loadSequence++
  cancelQuestionAnswerStart()
  cancelActiveRequest()
  cancelModelDiscovery()
  cancelQuestionAnswerRequests()
}

onBeforeUnmount(cleanupFrontendWork)

const defaultSelection = (options: ManualProbeModelOption[]): Set<string> =>
  new Set(options.length > 0 ? [options.find((option) => option.id === 'gpt-5.6-sol')?.id ?? options[0].id] : [])

const restoreActiveQuestionAnswerSelection = (batch: QuestionAnswerBatch | null): boolean => {
  if (!batch?.active) return false
  selected.value = new Set(batch.records.map(record => record.modelName))
  qaSelectedQuestions.value = new Set(batch.records.map(record => record.questionId))
  return true
}

const readableMessage = (rawKey: string): string => t(connectionHealthMessageKey(rawKey, te))
const qaReadableError = computed(() => qaErrorKey.value.startsWith('admin.')
  ? t(qaErrorKey.value)
  : t(`${prefix}.questionAnswer.errorTypes.${qaErrorKey.value || 'unknown'}`))

watch(
  () => [props.open, props.target?.targetId],
  async ([isOpen]) => {
    if (!isOpen || !props.target) {
      cleanupFrontendWork()
      return
    }
    cleanupFrontendWork()
    const targetId = props.target.targetId
    const sequence = loadSequence
    const controller = beginModelDiscovery()
    const formalModels = props.target.formalModels ?? []
    mode.value = formalModels.length > 0 ? 'formal' : 'once'
    models.value = formalModels
    onceModels.value = []
    onceLoadState.value = 'loading'
    selected.value = defaultSelection(formalModels)
    results.value = []
    loadErrorKey.value = ''
    testErrorKey.value = ''
    formalProgress.value = ''
    qaQuestions.value = []
    qaSelectedQuestions.value = new Set()
    qaBatch.value = null
    qaHistory.value = {
      records: [], page: 1, pageSize: 20, totalItems: 0, totalPages: 0,
      stats: { total: 0, normal: 0, errors: 0 },
      todayStats: { total: 0, normal: 0, errors: 0 },
    }
    qaCurrentExpanded.value = new Set()
    qaExpanded.value = new Set()
    qaErrorKey.value = ''
    qaCompletedNotice.value = false
    phase.value = formalModels.length > 0 ? 'ready' : 'loading'

    const outcome = await discoverModels(targetId, controller.signal)
    finishModelDiscovery(controller)
    if (sequence !== loadSequence || !props.open || props.target?.targetId !== targetId) return
    if ('errorKey' in outcome) {
      loadErrorKey.value = outcome.errorKey
      onceLoadState.value = 'error'
      if (mode.value !== 'formal') phase.value = 'error'
      return
    }
    onceModels.value = outcome.models
    onceLoadState.value = 'ready'
    if (mode.value === 'formal') return
    models.value = onceModels.value
    if (!restoreActiveQuestionAnswerSelection(qaBatch.value)) selected.value = defaultSelection(outcome.models)
    phase.value = 'ready'
  },
)

watch(mode, (nextMode) => {
  results.value = []
  testErrorKey.value = ''
  formalProgress.value = ''
  qaCompletedNotice.value = false
  if (nextMode !== 'questionAnswer') cancelQuestionAnswerStart()
  cancelQuestionAnswerRequests()
  if (nextMode === 'formal') {
    models.value = props.target?.formalModels ?? []
    selected.value = defaultSelection(models.value)
    phase.value = 'ready'
    return
  }
  models.value = onceModels.value
  selected.value = defaultSelection(models.value)
  phase.value = onceLoadState.value
  if (nextMode === 'questionAnswer' && props.open && props.target) {
    emit('question-answer-viewed', props.target.targetId)
    void loadQuestionAnswerData(props.target.targetId, loadSequence)
  }
})

const hasModels = computed(() => models.value.length > 0)
const qaActive = computed(() => Boolean(qaBatch.value?.active))
const qaSelectionLocked = computed(() => qaStarting.value || qaActive.value)
const canStartTest = computed(() => {
  if (!hasModels.value || selected.value.size === 0 || phase.value === 'testing') return false
  if (mode.value !== 'questionAnswer') return true
  return qaSelectedQuestions.value.size > 0 && !qaSelectionLocked.value && !qaLoading.value
})
const qaRequestCount = computed(() => selected.value.size * qaSelectedQuestions.value.size)
const currentBatchGridClass = computed(() => {
  const count = qaBatch.value?.records.length ?? 0
  if (count <= 1) return 'grid-cols-1'
  if (count === 2) return 'grid-cols-1 md:grid-cols-2'
  return 'grid-cols-1 md:grid-cols-2 xl:grid-cols-3'
})
const qaPageNumbers = computed(() => {
  const total = qaHistory.value.totalPages
  if (total <= 7) return Array.from({ length: total }, (_, index) => index + 1)
  const values = new Set([1, total])
  for (let page = Math.max(1, qaHistory.value.page - 2); page <= Math.min(total, qaHistory.value.page + 2); page++) values.add(page)
  return Array.from(values).sort((a, b) => a - b)
})

const toggle = (modelId: string) => {
  if (phase.value === 'testing' || (mode.value === 'questionAnswer' && qaSelectionLocked.value)) return
  const next = new Set(selected.value)
  if (next.has(modelId)) next.delete(modelId)
  else next.add(modelId)
  selected.value = next
}

const toggleQuestion = (questionId: string) => {
  if (qaSelectionLocked.value) return
  const next = new Set(qaSelectedQuestions.value)
  if (next.has(questionId)) next.delete(questionId)
  else next.add(questionId)
  qaSelectedQuestions.value = next
}

const retryLoad = async () => {
  if (!props.target) return
  const targetId = props.target.targetId
  const sequence = loadSequence
  const controller = beginModelDiscovery()
  phase.value = 'loading'
  onceLoadState.value = 'loading'
  loadErrorKey.value = ''
  const outcome = await discoverModels(targetId, controller.signal)
  finishModelDiscovery(controller)
  if (sequence !== loadSequence || !props.open || props.target?.targetId !== targetId) return
  if ('errorKey' in outcome) {
    loadErrorKey.value = outcome.errorKey
    onceLoadState.value = 'error'
    phase.value = 'error'
    return
  }
  onceModels.value = outcome.models
  onceLoadState.value = 'ready'
  models.value = outcome.models
  selected.value = defaultSelection(outcome.models)
  phase.value = 'ready'
}

const loadQuestionAnswerData = async (targetId: string, sequence: number) => {
  qaDataController?.abort()
  const controller = new AbortController()
  qaDataController = controller
  qaLoading.value = true
  qaErrorKey.value = ''
  try {
    const [questions, history, batch] = await Promise.all([
      listTestQuestions(controller.signal),
      getQuestionAnswerHistory(targetId, 1, controller.signal),
      getLatestQuestionAnswerBatch(targetId, controller.signal),
    ])
    if (sequence !== loadSequence || mode.value !== 'questionAnswer' || props.target?.targetId !== targetId) return
    qaQuestions.value = questions.filter(question => question.enabled)
    const defaultQuestion = qaQuestions.value.find(question => question.isDefault)
    qaSelectedQuestions.value = new Set(defaultQuestion ? [defaultQuestion.id] : [])
    qaHistory.value = history
    qaBatch.value = batch.batchId ? batch : null
    restoreActiveQuestionAnswerSelection(qaBatch.value)
    if (batch.active) scheduleQuestionAnswerPoll()
    else clearQuestionAnswerClock()
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    qaErrorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
  } finally {
    if (qaDataController === controller) qaDataController = null
    if (sequence === loadSequence) qaLoading.value = false
  }
}

const questionAnswerScopeIsCurrent = (scope: QuestionAnswerOperationScope): boolean => (
  isCurrentQuestionAnswerOperation(scope, {
    sequence: loadSequence,
    open: props.open,
    mode: mode.value,
    targetId: props.target?.targetId ?? null,
    batchId: qaBatch.value?.batchId ?? null,
  })
)

const scheduleQuestionAnswerPoll = () => {
  clearQuestionAnswerPolling()
  if (!props.open || mode.value !== 'questionAnswer' || !qaBatch.value?.active) return
  startQuestionAnswerClock()
  qaPollTimer = setTimeout(() => void pollQuestionAnswerBatch(), 2000)
}

const pollQuestionAnswerBatch = async () => {
  if (!props.target || !qaBatch.value?.batchId || !props.open || mode.value !== 'questionAnswer') return
  const targetId = props.target.targetId
  const batchId = qaBatch.value.batchId
  const sequence = loadSequence
  const controller = new AbortController()
  qaPollController = controller
  try {
    let batch = await getQuestionAnswerBatch(targetId, batchId, controller.signal)
    if (sequence !== loadSequence || mode.value !== 'questionAnswer' || props.target?.targetId !== targetId) return
    qaBatch.value = batch
    if (batch.active) {
      scheduleQuestionAnswerPoll()
      return
    }
    batch = await getQuestionAnswerBatch(targetId, batchId, controller.signal)
    if (sequence !== loadSequence || mode.value !== 'questionAnswer') return
    qaBatch.value = batch
    const history = await getQuestionAnswerHistory(targetId, 1, controller.signal)
    if (sequence !== loadSequence || mode.value !== 'questionAnswer' || props.target?.targetId !== targetId) return
    qaHistory.value = history
    qaCompletedNotice.value = true
    clearQuestionAnswerPolling()
    clearQuestionAnswerClock()
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    qaErrorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
    if (qaBatch.value?.active) scheduleQuestionAnswerPoll()
    else clearQuestionAnswerClock()
  } finally {
    if (qaPollController === controller) qaPollController = null
  }
}

const startQuestionAnswers = async () => {
  if (!canStartTest.value || !props.target) return
  qaStarting.value = true
  qaErrorKey.value = ''
  qaCompletedNotice.value = false
  const sequence = loadSequence
  const targetId = props.target.targetId
  const startSequence = ++qaStartSequence
  const scope = { sequence, targetId }
  const controller = beginRequest()
  try {
    const batch = await startQuestionAnswerBatch(
      targetId,
      Array.from(selected.value),
      Array.from(qaSelectedQuestions.value),
      controller.signal,
    )
    if (startSequence !== qaStartSequence || !questionAnswerScopeIsCurrent(scope)) return
    qaBatch.value = batch
    qaCurrentExpanded.value = new Set()
    emit('question-answer-started', targetId)
    qaHistory.value.page = 1
    scheduleQuestionAnswerPoll()
    try {
      const history = await getQuestionAnswerHistory(targetId, 1, controller.signal)
      if (startSequence === qaStartSequence && questionAnswerScopeIsCurrent(scope)) qaHistory.value = history
    } catch (error) {
      if (error instanceof Error && error.name === 'AbortError') return
      if (startSequence === qaStartSequence && questionAnswerScopeIsCurrent(scope)) {
        qaErrorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
      }
    }
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    if (startSequence === qaStartSequence && questionAnswerScopeIsCurrent(scope)) {
      qaErrorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
    }
  } finally {
    finishRequest(controller)
    if (startSequence === qaStartSequence) qaStarting.value = false
  }
}

const stopQuestionAnswers = async () => {
  if (!props.target || !qaBatch.value?.batchId || qaCancelling.value) return
  const targetId = props.target.targetId
  const batchId = qaBatch.value.batchId
  const sequence = loadSequence
  const cancelSequence = ++qaCancelSequence
  const scope = { sequence, targetId, batchId }
  qaCancelController?.abort()
  const controller = new AbortController()
  qaCancelController = controller
  qaCancelling.value = true
  qaErrorKey.value = ''
  clearQuestionAnswerPolling()
  try {
    const batch = await cancelQuestionAnswerBatch(targetId, batchId, controller.signal)
    if (cancelSequence !== qaCancelSequence || !questionAnswerScopeIsCurrent(scope)) return
    qaBatch.value = batch
    clearQuestionAnswerClock()
    const history = await getQuestionAnswerHistory(targetId, 1, controller.signal)
    if (cancelSequence !== qaCancelSequence || !questionAnswerScopeIsCurrent(scope)) return
    qaHistory.value = history
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    if (cancelSequence === qaCancelSequence && questionAnswerScopeIsCurrent(scope)) {
      qaErrorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
      if (qaBatch.value?.active) scheduleQuestionAnswerPoll()
    }
  } finally {
    if (qaCancelController === controller) qaCancelController = null
    if (cancelSequence === qaCancelSequence) qaCancelling.value = false
  }
}

const goQuestionAnswerPage = async (page: number) => {
  if (!props.target || page < 1 || page > qaHistory.value.totalPages || page === qaHistory.value.page) return
  const targetId = props.target.targetId
  const sequence = loadSequence
  qaDataController?.abort()
  const controller = new AbortController()
  qaDataController = controller
  qaErrorKey.value = ''
  try {
    const history = await getQuestionAnswerHistory(targetId, page, controller.signal)
    if (sequence !== loadSequence || mode.value !== 'questionAnswer' || props.target?.targetId !== targetId) return
    qaHistory.value = history
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    qaErrorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
  } finally {
    if (qaDataController === controller) qaDataController = null
  }
}

const toggleQuestionAnswerExpanded = (recordId: string) => {
  const next = new Set(qaExpanded.value)
  if (next.has(recordId)) next.delete(recordId)
  else next.add(recordId)
  qaExpanded.value = next
}

const toggleCurrentQuestionAnswerExpanded = (recordId: string) => {
  const next = new Set(qaCurrentExpanded.value)
  if (next.has(recordId)) next.delete(recordId)
  else next.add(recordId)
  qaCurrentExpanded.value = next
}

const replaceQuestionAnswerRecord = (recordId: string, update: (record: QuestionAnswerRecord) => QuestionAnswerRecord) => {
  qaHistory.value = { ...qaHistory.value, records: qaHistory.value.records.map(record => record.id === recordId ? update(record) : record) }
  if (qaBatch.value) qaBatch.value = { ...qaBatch.value, records: qaBatch.value.records.map(record => record.id === recordId ? update(record) : record) }
}

const updateQuestionAnswerStatsForMark = (record: QuestionAnswerRecord, markAsError: boolean, direction: 1 | -1) => {
  const normalDelta = (markAsError ? -1 : 1) * direction
  const errorDelta = (markAsError ? 1 : -1) * direction
  qaHistory.value = {
    ...qaHistory.value,
    stats: {
      ...qaHistory.value.stats,
      normal: qaHistory.value.stats.normal + normalDelta,
      errors: qaHistory.value.stats.errors + errorDelta,
    },
    todayStats: questionAnswerCompletedTodayInShanghai(record)
      ? {
          ...qaHistory.value.todayStats,
          normal: qaHistory.value.todayStats.normal + normalDelta,
          errors: qaHistory.value.todayStats.errors + errorDelta,
        }
      : qaHistory.value.todayStats,
  }
}

const toggleQuestionAnswerManualError = async (record: QuestionAnswerRecord) => {
  if (!props.target || record.status !== 'succeeded' || qaMarking.value.has(record.id)) return
  const nextValue = !record.manualError
  const nextMarking = new Set(qaMarking.value)
  nextMarking.add(record.id)
  qaMarking.value = nextMarking
  replaceQuestionAnswerRecord(record.id, current => ({ ...current, manualError: nextValue }))
  updateQuestionAnswerStatsForMark(record, nextValue, 1)
  try {
    const saved = await setQuestionAnswerManualError(props.target.targetId, record.id, nextValue)
    replaceQuestionAnswerRecord(record.id, () => saved)
  } catch (error) {
    replaceQuestionAnswerRecord(record.id, current => ({ ...current, manualError: record.manualError }))
    updateQuestionAnswerStatsForMark(record, nextValue, -1)
    qaErrorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
  } finally {
    const marking = new Set(qaMarking.value)
    marking.delete(record.id)
    qaMarking.value = marking
  }
}

const startTest = async () => {
  if (mode.value === 'questionAnswer') {
    await startQuestionAnswers()
    return
  }
  if (!canStartTest.value || !props.target) return
  phase.value = 'testing'
  testErrorKey.value = ''
  formalProgress.value = mode.value === 'formal' ? 'starting' : ''
  const sequence = loadSequence
  const controller = beginRequest()
  try {
    if (mode.value === 'once') {
      const outcome = await runManualProbeOnce(props.target.targetId, Array.from(selected.value), controller.signal)
      if (sequence !== loadSequence || !props.open) return
      if ('errorKey' in outcome) {
        testErrorKey.value = outcome.errorKey
        return
      }
      results.value = outcome.results
    } else {
      const outcome = await manualProbeTarget(
        props.target.targetId,
        Array.from(selected.value),
        controller.signal,
        (nextPhase) => {
          if (nextPhase === 'queued') formalProgress.value = 'queued'
          else formalProgress.value = formalProgress.value === 'queued' ? 'running' : 'direct'
        },
      )
      if (sequence !== loadSequence || !props.open) return
      if (outcome == null) {
        testErrorKey.value = serviceErrorKey.value
        return
      }
      results.value = outcome.map(formalProbeResult)
      emit('completed')
    }
  } finally {
    finishRequest(controller)
    if (sequence === loadSequence && props.open) {
      phase.value = 'ready'
      formalProgress.value = ''
    }
  }
}

const formalProbeResult = (model: ModelHealth): ManualProbeResult => ({
  modelName: model.modelName,
  result: model.probeResult || model.lastErrorKey || model.state,
  healthy: model.probeResult === 'ok' || model.probeResult === 'slow_response',
  latencyMs: model.lastLatencyMs,
  errorKey: model.probeResult === 'ok' || model.probeResult === 'slow_response' ? '' : (model.probeResult || model.lastErrorKey),
  errorDetail: model.lastErrorDetail,
  probedAt: model.updatedAt ?? new Date().toISOString(),
})

const resultLabel = (result: string): string => readableMessage(result)
const resultIsSlow = (result: ManualProbeResult): boolean => result.result === 'slow_response'
const answerSummary = (value: string): string => value.replace(/\s+/g, ' ').trim().slice(0, 160)
const questionAnswerElapsedLabel = (record: QuestionAnswerRecord): string => {
  const elapsedMs = questionAnswerElapsedMilliseconds(record, qaClockNow.value)
  if (elapsedMs === null) return ''
  const totalSeconds = elapsedMs / 1000
  if (totalSeconds < 60) {
    const seconds = totalSeconds < 10 ? totalSeconds.toFixed(1) : Math.round(totalSeconds).toString()
    return t(`${prefix}.questionAnswer.durationSeconds`, { seconds })
  }
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = Math.floor(totalSeconds % 60).toString().padStart(2, '0')
  return t(`${prefix}.questionAnswer.durationMinutesSeconds`, { minutes, seconds })
}

const questionAnswerCurrentAnswer = (record: QuestionAnswerRecord): string => {
  if (record.status === 'pending') return t(`${prefix}.questionAnswer.waitingAnswer`)
  if (record.status === 'running') return t(`${prefix}.questionAnswer.runningAnswer`)
  return record.answerBody || (record.errorType
    ? questionAnswerErrorLabel(record.errorType)
    : t(`${prefix}.questionAnswer.noAnswer`))
}
const questionAnswerStatusLabel = (record: QuestionAnswerRecord): string => t(`${prefix}.questionAnswer.status.${record.status}`)
const questionAnswerErrorLabel = (errorType: string): string => {
  const key = `${prefix}.questionAnswer.errorTypes.${errorType}`
  return errorType && te(key) ? t(key) : t(`${prefix}.questionAnswer.errorTypes.unknown`)
}
const questionAnswerRecordClass = (record: QuestionAnswerRecord): string => {
  if (record.status === 'cancelled' || record.status === 'pending' || record.status === 'running') return 'border-border/50 bg-surface-line/20'
  if (record.status === 'failed' || record.manualError) return 'border-red-500/60 bg-red-500/20 dark:border-red-400/50 dark:bg-red-500/20'
  return 'border-green-500/35 bg-green-500/10 dark:border-green-400/35 dark:bg-green-500/10'
}

const close = () => {
  cleanupFrontendWork()
  emit('close')
}
</script>

<template>
  <Teleport to="body">
    <Transition
      enter-active-class="transition duration-200 ease-out"
      enter-from-class="opacity-0"
      enter-to-class="opacity-100"
      leave-active-class="transition duration-150 ease-in"
      leave-from-class="opacity-100"
      leave-to-class="opacity-0"
    >
      <div v-if="open && target" class="fixed inset-0 z-[150] flex items-center justify-center p-4">
        <div class="absolute inset-0 bg-background/60 backdrop-blur-sm" @click="close" />

        <div role="dialog" aria-modal="true" :aria-label="t(`${prefix}.title`)" class="relative flex h-[min(760px,calc(100dvh-2rem))] w-full max-w-6xl flex-col overflow-hidden rounded-2xl border border-border/60 bg-card shadow-2xl">
          <div class="flex shrink-0 items-center justify-between gap-3 border-b border-border/60 px-5 py-4">
            <div class="flex min-w-0 items-center gap-2.5">
              <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                <Zap class="h-4 w-4" />
              </div>
              <div class="min-w-0">
                <h3 class="truncate text-sm font-semibold text-foreground">{{ t(`${prefix}.title`) }}</h3>
                <p class="truncate text-xs text-muted-foreground">
                  {{ target.accountName }} · {{ target.platform || '-' }} · {{ target.type || '-' }} · {{ target.status || '-' }} · {{ target.groupName }}
                </p>
              </div>
            </div>
            <button type="button" class="shrink-0 rounded-md p-1 text-muted-foreground transition-colors hover:bg-surface-elevated hover:text-foreground" @click="close">
              <X class="h-4 w-4" />
            </button>
          </div>

          <div class="flex-1 overflow-y-auto px-5 py-4">
            <div class="mb-4 inline-flex max-w-full overflow-x-auto rounded-lg border border-border/60 bg-surface-line/30 p-1">
              <button type="button" class="whitespace-nowrap rounded-md px-3 py-1.5 text-xs font-medium transition-colors" :class="mode === 'formal' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'" :disabled="phase === 'testing'" @click="mode = 'formal'">
                {{ t(`${prefix}.modes.formal`) }}
              </button>
              <button type="button" class="whitespace-nowrap rounded-md px-3 py-1.5 text-xs font-medium transition-colors" :class="mode === 'once' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'" :disabled="phase === 'testing'" @click="mode = 'once'">
                {{ t(`${prefix}.modes.once`) }}
              </button>
              <button type="button" class="whitespace-nowrap rounded-md px-3 py-1.5 text-xs font-medium transition-colors" :class="mode === 'questionAnswer' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'" :disabled="phase === 'testing'" @click="mode = 'questionAnswer'">
                {{ t(`${prefix}.modes.questionAnswer`) }}
              </button>
            </div>

            <p v-if="mode !== 'questionAnswer'" class="mb-4 text-xs text-muted-foreground">{{ t(`${prefix}.modeDescriptions.${mode}`) }}</p>
            <p v-if="mode !== 'questionAnswer'" class="mb-4 text-xs text-muted-foreground">{{ t(`${prefix}.contractLimit`) }}</p>

            <div v-if="phase === 'loading'" class="flex flex-col items-center justify-center gap-2 py-16 text-center">
              <Loader2 class="h-6 w-6 animate-spin text-primary/60" />
              <p class="text-sm text-muted-foreground">{{ t(`${prefix}.loadingModels`) }}</p>
            </div>

            <div v-else-if="phase === 'error'" class="flex flex-col items-center justify-center gap-3 py-16 text-center">
              <ShieldAlert class="h-8 w-8 text-red-500/70" />
              <p class="text-sm text-red-600 dark:text-red-400">{{ readableMessage(loadErrorKey) }}</p>
              <button type="button" class="rounded-lg border border-border/60 px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-surface-line" @click="retryLoad">
                {{ t(`${prefix}.retryLoad`) }}
              </button>
            </div>

            <template v-else>
              <div v-if="!hasModels" class="flex flex-col items-center justify-center gap-2 py-16 text-center">
                <ShieldAlert class="h-8 w-8 text-muted-foreground/40" />
                <p class="text-sm text-muted-foreground">{{ t(`${prefix}.empty`) }}</p>
              </div>

              <template v-else>
                <div v-if="mode === 'questionAnswer'" class="mb-4 grid grid-cols-2 overflow-hidden rounded-lg border border-border/50 bg-surface-line/20">
                  <div class="px-4 py-3">
                    <div class="flex items-center justify-between gap-3">
                      <p class="text-xs font-medium text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.allTime`) }}</p>
                      <p class="text-xs text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.total`, { total: qaHistory.stats.total }) }}</p>
                    </div>
                    <dl class="mt-3 grid grid-cols-2 divide-x divide-border/50">
                      <div class="pr-3">
                        <dt class="text-xs text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.normal`) }}</dt>
                        <dd class="mt-0.5 text-2xl font-semibold text-foreground">{{ qaHistory.stats.normal }}</dd>
                      </div>
                      <div class="pl-3">
                        <dt class="text-xs text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.errors`) }}</dt>
                        <dd class="mt-0.5 text-2xl font-semibold text-foreground">{{ qaHistory.stats.errors }}</dd>
                      </div>
                    </dl>
                  </div>
                  <div class="border-l border-border/50 px-4 py-3">
                    <div class="flex items-center justify-between gap-3">
                      <p class="text-xs font-medium text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.todayShanghai`) }}</p>
                      <p class="text-xs text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.total`, { total: qaHistory.todayStats.total }) }}</p>
                    </div>
                    <dl class="mt-3 grid grid-cols-2 divide-x divide-border/50">
                      <div class="pr-3">
                        <dt class="text-xs text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.normal`) }}</dt>
                        <dd class="mt-0.5 text-2xl font-semibold text-foreground">{{ qaHistory.todayStats.normal }}</dd>
                      </div>
                      <div class="pl-3">
                        <dt class="text-xs text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.errors`) }}</dt>
                        <dd class="mt-0.5 text-2xl font-semibold text-foreground">{{ qaHistory.todayStats.errors }}</dd>
                      </div>
                    </dl>
                  </div>
                </div>

                <p class="mb-3 text-xs text-muted-foreground">
                  {{ t(`${prefix}.${mode === 'formal' ? 'formalSelectHint' : 'selectHint'}`) }}
                </p>
                <div class="grid grid-cols-1 gap-2.5 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
                  <label v-for="model in models" :key="model.id" class="flex cursor-pointer items-start gap-2 rounded-lg border border-border/40 px-3 py-2.5 transition-colors" :class="selected.has(model.id) ? 'border-primary/50 bg-primary/5' : 'hover:bg-surface-line/40'">
                    <input type="checkbox" class="mt-0.5 h-4 w-4 shrink-0 rounded border-border/60" :disabled="phase === 'testing' || (mode === 'questionAnswer' && qaSelectionLocked)" :checked="selected.has(model.id)" @change="toggle(model.id)" />
                    <div class="min-w-0 flex-1">
                      <p class="truncate text-sm font-medium text-foreground">{{ model.name }}</p>
                      <p v-if="model.ownedBy" class="truncate text-xs text-muted-foreground">{{ model.ownedBy }}</p>
                    </div>
                  </label>
                </div>

                <template v-if="mode === 'questionAnswer'">
                  <div class="mt-5 border-t border-border/40 pt-4">
                    <div class="mb-3 flex items-center justify-between gap-3">
                      <h4 class="text-xs font-semibold text-foreground">{{ t(`${prefix}.questionAnswer.questionsTitle`) }}</h4>
                      <span class="text-xs text-muted-foreground">{{ t(`${prefix}.questionAnswer.selectedFormula`, { models: selected.size, questions: qaSelectedQuestions.size, total: qaRequestCount }) }}</span>
                    </div>
                    <div v-if="qaLoading" class="flex items-center gap-2 rounded-lg border border-dashed border-border/50 px-3 py-5 text-xs text-muted-foreground">
                      <Loader2 class="h-4 w-4 animate-spin" />
                      {{ t(`${prefix}.questionAnswer.loading`) }}
                    </div>
                    <div v-else-if="qaQuestions.length === 0" class="rounded-lg border border-dashed border-border/50 px-3 py-5 text-center text-xs text-muted-foreground">
                      {{ t(`${prefix}.questionAnswer.noQuestions`) }}
                    </div>
                    <div v-else class="space-y-1.5">
                      <label v-for="question in qaQuestions" :key="question.id" class="flex cursor-pointer items-start gap-2 rounded-md border border-border/40 px-3 py-2 transition-colors" :class="qaSelectedQuestions.has(question.id) ? 'border-primary/40 bg-primary/5' : 'hover:bg-surface-line/30'">
                        <input type="checkbox" class="mt-0.5 h-4 w-4 shrink-0 rounded border-border/60" :disabled="qaSelectionLocked" :checked="qaSelectedQuestions.has(question.id)" @change="toggleQuestion(question.id)" />
                        <div class="min-w-0 flex-1">
                          <div class="flex items-center gap-2">
                            <span class="truncate text-xs font-medium text-foreground">{{ question.name }}</span>
                            <span v-if="question.isDefault" class="shrink-0 rounded-full bg-amber-500/10 px-1.5 py-0.5 text-[11px] text-amber-600 dark:text-amber-400">{{ t(`${prefix}.questionAnswer.defaultQuestion`) }}</span>
                          </div>
                          <p class="mt-0.5 truncate text-xs text-muted-foreground">{{ answerSummary(question.body) }}</p>
                        </div>
                      </label>
                    </div>
                  </div>

                  <p v-if="qaErrorKey" class="mt-4 rounded-lg bg-red-500/10 px-3 py-2 text-xs text-red-600 dark:text-red-400">{{ qaReadableError }}</p>
                  <p v-if="qaCompletedNotice" class="mt-4 rounded-lg bg-green-500/10 px-3 py-2 text-xs text-green-600 dark:text-green-400">{{ t(`${prefix}.questionAnswer.completedNotice`) }}</p>

                  <section class="mt-5 border-t border-border/40 pt-4">
                    <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
                      <div>
                        <h4 class="text-xs font-semibold text-foreground">{{ t(`${prefix}.questionAnswer.currentTitle`) }}</h4>
                        <p v-if="qaBatch" class="mt-1 text-xs text-muted-foreground">
                          {{ t(`${prefix}.questionAnswer.submitted`, { count: qaBatch.submittedCount }) }} · {{ t(`${prefix}.questionAnswer.progress`, { completed: qaBatch.completedCount, total: qaBatch.submittedCount }) }}
                          <span v-if="qaBatch.active"> · {{ t(`${prefix}.questionAnswer.runningNow`, { model: qaBatch.currentModel || '-', question: qaBatch.currentQuestion || '-' }) }}</span>
                        </p>
                      </div>
                      <button v-if="qaBatch?.active" type="button" class="inline-flex items-center gap-1.5 rounded-lg border border-red-500/30 px-3 py-1.5 text-xs font-medium text-red-600 hover:bg-red-500/10 disabled:opacity-50 dark:text-red-400" :disabled="qaCancelling" @click="stopQuestionAnswers">
                        <Loader2 v-if="qaCancelling" class="h-3.5 w-3.5 animate-spin" />
                        <StopCircle v-else class="h-3.5 w-3.5" />
                        {{ t(`${prefix}.questionAnswer.stop`) }}
                      </button>
                    </div>
                    <div v-if="!qaBatch" class="rounded-lg border border-dashed border-border/50 px-3 py-5 text-center text-xs text-muted-foreground">{{ t(`${prefix}.questionAnswer.noBatch`) }}</div>
                    <div v-else>
                      <ul class="grid items-stretch gap-3" :class="currentBatchGridClass">
                        <li v-for="record in qaBatch.records" :key="record.id" class="flex min-h-56 flex-col rounded-lg border p-3" :class="questionAnswerRecordClass(record)">
                          <div class="flex items-start justify-between gap-3">
                            <div class="flex min-w-0 items-start gap-2">
                              <Loader2 v-if="record.status === 'pending' || record.status === 'running'" class="mt-0.5 h-5 w-5 shrink-0 animate-spin text-primary" />
                              <CheckCircle2 v-else-if="record.status === 'succeeded' && !record.manualError" class="mt-0.5 h-5 w-5 shrink-0 text-green-600 dark:text-green-400" />
                              <XCircle v-else-if="record.status === 'failed' || record.manualError" class="mt-0.5 h-5 w-5 shrink-0 text-red-600 dark:text-red-400" />
                              <StopCircle v-else-if="record.status === 'cancelled'" class="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground" />
                              <div class="min-w-0">
                                <p class="truncate text-sm font-semibold text-foreground">{{ record.questionName }}</p>
                                <p class="mt-0.5 truncate text-xs text-muted-foreground">{{ record.modelName }}</p>
                              </div>
                            </div>
                            <div class="shrink-0 text-right text-xs text-muted-foreground">
                              <p>{{ questionAnswerStatusLabel(record) }}</p>
                              <p v-if="questionAnswerElapsedLabel(record)" class="mt-0.5">{{ questionAnswerElapsedLabel(record) }}</p>
                            </div>
                          </div>

                          <div class="mt-3 flex-1 space-y-3 border-t border-border/40 pt-3">
                            <div>
                              <p class="text-[11px] font-medium text-muted-foreground">{{ t(`${prefix}.questionAnswer.questionLabel`) }}</p>
                              <p class="mt-1 whitespace-pre-wrap break-words text-xs leading-5 text-foreground" :class="qaCurrentExpanded.has(record.id) ? '' : 'line-clamp-2'">{{ record.questionBody }}</p>
                            </div>
                            <div>
                              <p class="text-[11px] font-medium text-muted-foreground">{{ t(`${prefix}.questionAnswer.answerLabel`) }}</p>
                              <p class="mt-1 whitespace-pre-wrap break-words text-sm leading-6 text-foreground" :class="qaCurrentExpanded.has(record.id) ? '' : 'line-clamp-4'">{{ questionAnswerCurrentAnswer(record) }}</p>
                            </div>
                          </div>

                          <button type="button" class="mt-3 inline-flex items-center justify-end gap-1 self-end text-xs font-medium text-muted-foreground hover:text-foreground" @click="toggleCurrentQuestionAnswerExpanded(record.id)">
                            {{ qaCurrentExpanded.has(record.id) ? t(`${prefix}.questionAnswer.collapseCurrent`) : t(`${prefix}.questionAnswer.expandCurrent`) }}
                            <ChevronUp v-if="qaCurrentExpanded.has(record.id)" class="h-3.5 w-3.5" />
                            <ChevronDown v-else class="h-3.5 w-3.5" />
                          </button>
                        </li>
                      </ul>
                    </div>
                  </section>

                  <section class="mt-5 border-t border-border/40 pt-4">
                    <h4 class="mb-3 text-xs font-semibold text-foreground">{{ t(`${prefix}.questionAnswer.historyTitle`) }}</h4>
                    <div v-if="qaHistory.records.length === 0" class="rounded-lg border border-dashed border-border/50 px-3 py-5 text-center text-xs text-muted-foreground">{{ t(`${prefix}.questionAnswer.noHistory`) }}</div>
                    <ul v-else class="space-y-2">
                      <li v-for="record in qaHistory.records" :key="record.id" class="rounded-lg border px-3 py-2.5" :class="questionAnswerRecordClass(record)">
                        <div class="flex items-start justify-between gap-3">
                          <button type="button" class="min-w-0 flex-1 text-left" @click="toggleQuestionAnswerExpanded(record.id)">
                            <div class="flex min-w-0 items-center gap-2">
                              <span class="truncate text-sm font-medium text-foreground">{{ record.questionName }}</span>
                            </div>
                            <div class="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                              <span>{{ record.modelName }}</span>
                              <span>{{ formatConnectionHealthTime(record.createdAt) }}</span>
                              <span>{{ questionAnswerStatusLabel(record) }}</span>
                              <span v-if="questionAnswerElapsedLabel(record)">{{ questionAnswerElapsedLabel(record) }}</span>
                            </div>
                            <p v-if="!qaExpanded.has(record.id)" class="mt-2 truncate text-xs text-muted-foreground">{{ answerSummary(record.answerBody || (record.errorType ? questionAnswerErrorLabel(record.errorType) : t(`${prefix}.questionAnswer.noAnswer`))) }}</p>
                          </button>
                          <div class="flex shrink-0 items-center gap-1.5">
                            <button v-if="record.status === 'succeeded'" type="button" class="flex h-9 w-9 items-center justify-center rounded-md border transition-colors disabled:opacity-50" :class="record.manualError ? 'border-red-500/60 bg-red-500/20 text-red-600 hover:bg-red-500/30 dark:text-red-400' : 'border-green-500/40 bg-green-500/10 text-green-600 hover:bg-green-500/20 dark:text-green-400'" :disabled="qaMarking.has(record.id)" :title="record.manualError ? t(`${prefix}.questionAnswer.restoreNormal`) : t(`${prefix}.questionAnswer.markError`)" @click="toggleQuestionAnswerManualError(record)">
                              <Loader2 v-if="qaMarking.has(record.id)" class="h-5 w-5 animate-spin" />
                              <XCircle v-else-if="record.manualError" class="h-5 w-5" />
                              <CheckCircle2 v-else class="h-5 w-5" />
                            </button>
                            <XCircle v-else-if="record.status === 'failed'" class="h-6 w-6 text-red-600 dark:text-red-400" />
                            <StopCircle v-else-if="record.status === 'cancelled'" class="h-6 w-6 text-muted-foreground" />
                            <Loader2 v-else-if="record.status === 'pending' || record.status === 'running'" class="h-6 w-6 animate-spin text-primary" />
                            <button type="button" class="rounded-md p-1.5 text-muted-foreground hover:bg-surface-elevated hover:text-foreground" @click="toggleQuestionAnswerExpanded(record.id)">
                              <ChevronUp v-if="qaExpanded.has(record.id)" class="h-4 w-4" />
                              <ChevronDown v-else class="h-4 w-4" />
                            </button>
                          </div>
                        </div>
                        <div v-if="qaExpanded.has(record.id)" class="mt-3 space-y-3 border-t border-border/40 pt-3">
                          <div><p class="text-xs font-medium text-muted-foreground">{{ t(`${prefix}.questionAnswer.fullQuestion`) }}</p><p class="mt-1 whitespace-pre-wrap break-words text-sm leading-6 text-foreground">{{ record.questionBody }}</p></div>
                          <div><p class="text-xs font-medium text-muted-foreground">{{ t(`${prefix}.questionAnswer.fullAnswer`) }}</p><p class="mt-1 whitespace-pre-wrap break-words text-sm leading-6 text-foreground">{{ record.answerBody || (record.errorType ? questionAnswerErrorLabel(record.errorType) : t(`${prefix}.questionAnswer.noAnswer`)) }}</p></div>
                        </div>
                      </li>
                    </ul>
                    <div v-if="qaHistory.totalPages > 1" class="mt-4 flex flex-wrap items-center justify-center gap-1.5">
                      <template v-for="(page, index) in qaPageNumbers" :key="page">
                        <span v-if="index > 0 && page - qaPageNumbers[index - 1] > 1" class="px-1 text-xs text-muted-foreground">…</span>
                        <button type="button" class="h-8 min-w-8 rounded-md border px-2 text-xs" :class="page === qaHistory.page ? 'border-primary bg-primary text-primary-foreground' : 'border-border/50 text-muted-foreground hover:bg-surface-elevated'" @click="goQuestionAnswerPage(page)">{{ page }}</button>
                      </template>
                    </div>
                  </section>
                </template>

                <template v-else>
                  <p v-if="testErrorKey" class="mt-4 rounded-lg bg-red-500/10 px-3 py-2 text-xs text-red-600 dark:text-red-400">{{ readableMessage(testErrorKey) }}</p>
                  <p v-if="mode === 'formal' && phase === 'testing' && formalProgress" class="mt-4 flex items-center gap-2 rounded-lg bg-primary/5 px-3 py-2 text-xs text-primary">
                    <Loader2 v-if="formalProgress !== 'direct'" class="h-3.5 w-3.5 animate-spin" />
                    <Zap v-else class="h-3.5 w-3.5" />
                    {{ t(`${prefix}.progress.${formalProgress}`) }}
                  </p>
                  <div class="mt-5">
                    <h4 class="mb-2 text-xs font-semibold text-foreground">{{ t(`${prefix}.resultTitle`) }}</h4>
                    <div v-if="results.length === 0" class="rounded-lg border border-dashed border-border/50 px-3 py-6 text-center text-xs text-muted-foreground">{{ t(`${prefix}.resultEmpty`) }}</div>
                    <ul v-else class="space-y-2">
                      <li v-for="result in results" :key="result.modelName" class="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border/40 px-3 py-2.5">
                        <div class="flex min-w-0 items-center gap-2">
                          <AlertTriangle v-if="resultIsSlow(result)" class="h-4 w-4 shrink-0 text-amber-500" />
                          <CheckCircle2 v-else-if="result.healthy" class="h-4 w-4 shrink-0 text-green-500" />
                          <XCircle v-else class="h-4 w-4 shrink-0 text-red-500" />
                          <span class="truncate text-sm font-medium text-foreground">{{ result.modelName }}</span>
                          <span class="inline-flex shrink-0 items-center gap-1 rounded-full bg-surface-elevated px-2 py-0.5 text-xs text-muted-foreground">
                            <span class="h-1.5 w-1.5 rounded-full" :class="connectionHealthRecordColorClass(result.result)" />
                            {{ resultLabel(result.result) }}
                          </span>
                        </div>
                        <div class="flex shrink-0 items-center gap-3 text-xs text-muted-foreground">
                          <span v-if="result.latencyMs !== null">{{ t(`${prefix}.latency`, { ms: result.latencyMs }) }}</span>
                          <span>{{ formatConnectionHealthTime(result.probedAt) }}</span>
                        </div>
                        <p v-if="!result.healthy && result.errorDetail" class="w-full truncate text-xs text-red-500/80">{{ result.errorDetail }}</p>
                      </li>
                    </ul>
                  </div>
                </template>
              </template>
            </template>
          </div>

          <div class="flex shrink-0 items-center justify-between gap-3 border-t border-border/60 px-5 py-4">
            <p v-if="hasModels" class="flex items-center gap-1 text-xs text-muted-foreground">
              <AlertTriangle v-if="selected.size === 0 || (mode === 'questionAnswer' && qaSelectedQuestions.size === 0)" class="h-3.5 w-3.5" />
              {{ mode === 'questionAnswer'
                ? t(`${prefix}.questionAnswer.selectedFormula`, { models: selected.size, questions: qaSelectedQuestions.size, total: qaRequestCount })
                : t(`${prefix}.selectedCount`, { count: selected.size }) }}
            </p>
            <div v-else />
            <div class="flex items-center gap-2">
              <button type="button" class="rounded-lg px-3 py-1.5 text-sm text-muted-foreground hover:bg-surface-line" @click="close">{{ t(`${prefix}.close`) }}</button>
              <button type="button" class="inline-flex items-center gap-1.5 rounded-lg bg-primary px-4 py-1.5 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-50" :disabled="!canStartTest" @click="startTest">
                <Loader2 v-if="phase === 'testing' || qaStarting" class="h-4 w-4 animate-spin" />
                {{ mode === 'questionAnswer'
                  ? (qaStarting ? t(`${prefix}.questionAnswer.submitting`) : t(`${prefix}.questionAnswer.start`))
                  : (phase === 'testing'
                    ? t(`${prefix}.${mode === 'formal' && formalProgress === 'queued' ? 'queueing' : 'testing'}`)
                    : t(`${prefix}.${mode === 'formal' ? 'startFormal' : 'startTest'}`)) }}
              </button>
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>
