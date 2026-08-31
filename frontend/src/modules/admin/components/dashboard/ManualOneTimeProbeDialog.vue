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
  setQuestionAnswerJudgment,
  startQuestionAnswerBatch,
} from '../../api/connectionHealth'
import type {
  ManualProbeModelOption,
  ManualProbeResult,
  ModelHealth,
  QuestionAnswerBatch,
  QuestionAnswerJudgment,
  QuestionAnswerReasoningEffort,
  QuestionAnswerHistory,
  QuestionAnswerRecord,
  TestQuestion,
} from '../../types/connectionHealth'
import {
  filterQuestionAnswerRecords,
  groupQuestionAnswerHistoryByBatch,
  isCurrentQuestionAnswerOperation,
  questionAnswerBatchCompletedAt,
  questionAnswerElapsedMilliseconds,
  questionAnswerReviewStatsFromRecords,
  resolveQuestionAnswerSelection,
  questionAnswerSubmissionSummary,
  replaceQuestionAnswerRecord,
  shortQuestionAnswerBatchId,
  type QuestionAnswerOperationScope,
} from '../../utils/questionAnswers'
import {
  createDefaultQuestionAnswerPreferences,
  type QuestionAnswerSelectionPreferences,
} from '../../utils/connectionHealthPreferences'
import QuestionAnswerHighlightedText from './QuestionAnswerHighlightedText.vue'
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

const props = withDefaults(defineProps<{
  open: boolean
  target: ManualProbeTargetSummary | null
  questionAnswerPreferences?: QuestionAnswerSelectionPreferences
}>(), {
  questionAnswerPreferences: () => createDefaultQuestionAnswerPreferences(),
})

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'completed'): void
  (event: 'question-answer-started', targetId: string): void
  (event: 'question-answer-viewed', targetId: string): void
  (event: 'question-answer-preferences-changed', preferences: QuestionAnswerSelectionPreferences): void
}>()

const prefix = 'admin.connectionHealth.manualProbeDialog'
const { discoverModels, runManualProbeOnce, manualProbeTarget, errorKey: serviceErrorKey } = useConnectionHealth()

type Phase = 'loading' | 'ready' | 'testing' | 'error'
type ProbeMode = 'once' | 'formal' | 'questionAnswer'

const phase = ref<Phase>('loading')
const mode = ref<ProbeMode>('questionAnswer')
const currentProbeMode = (): ProbeMode => mode.value
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
const qaReasoningEffort = ref<QuestionAnswerReasoningEffort>('medium')
const qaReasoningEffortOptions: Array<{ value: QuestionAnswerReasoningEffort; labelKey: string }> = [
  { value: 'low', labelKey: 'low' },
  { value: 'medium', labelKey: 'medium' },
  { value: 'high', labelKey: 'high' },
  { value: 'xhigh', labelKey: 'xhigh' },
]
const qaRepeatCount = ref(1)
const qaRepeatCountOptions = Array.from({ length: 10 }, (_, index) => index + 1)
const createQuestionAnswerPreferenceDraft = (): QuestionAnswerSelectionPreferences => ({
  modelIds: [...props.questionAnswerPreferences.modelIds],
  questionIds: [...props.questionAnswerPreferences.questionIds],
  reasoningEffort: props.questionAnswerPreferences.reasoningEffort,
  repeatCount: props.questionAnswerPreferences.repeatCount,
})
const qaPreferenceDraft = ref<QuestionAnswerSelectionPreferences>(createQuestionAnswerPreferenceDraft())
const qaLoading = ref(false)
const qaStarting = ref(false)
const qaCancelling = ref(false)
const qaRuntimeBatch = ref<QuestionAnswerBatch | null>(null)
const qaReviewBatch = ref<QuestionAnswerBatch | null>(null)
const qaReviewLoadingBatchId = ref<string | null>(null)
const qaHistory = ref<QuestionAnswerHistory>({
  records: [],
  page: 1,
  pageSize: 20,
  totalItems: 0,
  totalPages: 0,
  stats: {
    requests: { submitted: 0, inProgress: 0, succeeded: 0, failed: 0, cancelled: 0 },
    reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
    byModel: [],
  },
  todayStats: {
    requests: { submitted: 0, inProgress: 0, succeeded: 0, failed: 0, cancelled: 0 },
    reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
    byModel: [],
  },
})
const qaCurrentExpanded = ref<Set<string>>(new Set())
const qaExpanded = ref<Set<string>>(new Set())
const qaMarking = ref<Map<string, QuestionAnswerJudgment>>(new Map())
const qaShowAll = ref(false)
const qaErrorKey = ref('')
const qaCompletedNotice = ref(false)
const qaClockNow = ref(Date.now())

let loadSequence = 0
let activeRequestController: AbortController | null = null
let modelDiscoveryController: AbortController | null = null
let qaDataController: AbortController | null = null
let qaPollController: AbortController | null = null
let qaCancelController: AbortController | null = null
const qaJudgmentControllers = new Map<string, AbortController>()
const qaJudgmentRefreshSequences = new Map<string, number>()
let qaPollTimer: ReturnType<typeof setTimeout> | null = null
let qaClockTimer: ReturnType<typeof setInterval> | null = null
let qaStartSequence = 0
let qaCancelSequence = 0
let qaReviewSequence = 0
let qaReviewController: AbortController | null = null
let qaReviewJudgmentRefreshSequence = 0
let qaHistorySequence = 0
let qaHistoryIntentPage = 1
let qaJudgmentRefreshSequence = 0
let qaJudgmentSessionSequence = 0
let qaPollSequence = 0
let qaRuntimeSnapshotSequence = 0
let skipInitializedQuestionAnswerModeLoad = false
let qaSelectionDataReady = false

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
  qaPollSequence++
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

const cancelQuestionAnswerReview = (): number => {
  const sequence = ++qaReviewSequence
  qaReviewController?.abort()
  qaReviewController = null
  qaReviewLoadingBatchId.value = null
  qaReviewJudgmentRefreshSequence = 0
  return sequence
}

const beginQuestionAnswerHistoryIntent = (page = qaHistoryIntentPage): number => {
  qaHistoryIntentPage = page
  const sequence = ++qaHistorySequence
  qaDataController?.abort()
  qaDataController = null
  return sequence
}

const questionAnswerHistoryIntentIsCurrent = (sequence: number): boolean => (
  sequence === qaHistorySequence
)

const resetQuestionAnswerViewState = () => {
  qaRuntimeBatch.value = null
  qaReviewBatch.value = null
  qaHistoryIntentPage = 1
  qaHistory.value = {
    records: [], page: 1, pageSize: 20, totalItems: 0, totalPages: 0,
    stats: {
      requests: { submitted: 0, inProgress: 0, succeeded: 0, failed: 0, cancelled: 0 },
      reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
      byModel: [],
    },
    todayStats: {
      requests: { submitted: 0, inProgress: 0, succeeded: 0, failed: 0, cancelled: 0 },
      reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
      byModel: [],
    },
  }
  qaCurrentExpanded.value = new Set()
  qaExpanded.value = new Set()
  qaShowAll.value = false
}

const resetQuestionAnswerTargetState = () => {
  resetQuestionAnswerViewState()
  qaPreferenceDraft.value = createQuestionAnswerPreferenceDraft()
  qaQuestions.value = []
  qaSelectedQuestions.value = new Set()
  qaReasoningEffort.value = 'medium'
  qaRepeatCount.value = 1
  qaMarking.value = new Map()
  qaErrorKey.value = ''
  qaCompletedNotice.value = false
  qaSelectionDataReady = false
}

const cancelQuestionAnswerJudgments = () => {
  for (const controller of qaJudgmentControllers.values()) controller.abort()
  qaJudgmentControllers.clear()
  qaJudgmentRefreshSequences.clear()
  qaJudgmentSessionSequence++
  qaMarking.value = new Map()
}

const cancelQuestionAnswerRequests = () => {
  clearQuestionAnswerPolling()
  clearQuestionAnswerClock()
  cancelQuestionAnswerReview()
  beginQuestionAnswerHistoryIntent()
  qaLoading.value = false
  qaCancelController?.abort()
  qaCancelController = null
  cancelQuestionAnswerJudgments()
  qaRuntimeSnapshotSequence++
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
  if (batch.reasoningEffort) qaReasoningEffort.value = batch.reasoningEffort
  qaRepeatCount.value = batch.repeatCount
  return true
}

const restoreSavedQuestionAnswerSelection = (): boolean => {
  if (
    mode.value !== 'questionAnswer'
    || qaRuntimeBatch.value?.active
    || onceLoadState.value !== 'ready'
    || !qaSelectionDataReady
  ) return false
  const restored = resolveQuestionAnswerSelection(
    props.questionAnswerPreferences,
    onceModels.value,
    qaQuestions.value,
  )
  selected.value = new Set(restored.modelIds)
  qaSelectedQuestions.value = new Set(restored.questionIds)
  qaReasoningEffort.value = restored.reasoningEffort
  qaRepeatCount.value = restored.repeatCount
  return true
}

const mergeVisibleQuestionAnswerIds = (
  savedIds: string[],
  visibleIds: string[],
  selectedVisibleIds: string[],
): string[] => {
  const visible = new Set(visibleIds)
  const selected = Array.from(new Set(selectedVisibleIds.filter(id => visible.has(id))))
  const merged: string[] = []
  let insertedVisible = false
  for (const id of savedIds) {
    if (visible.has(id)) {
      if (!insertedVisible) {
        merged.push(...selected)
        insertedVisible = true
      }
      continue
    }
    if (!merged.includes(id)) merged.push(id)
  }
  if (!insertedVisible) merged.push(...selected)
  return Array.from(new Set(merged))
}

type QuestionAnswerPreferenceField = 'models' | 'questions' | 'reasoningEffort' | 'repeatCount'

const emitQuestionAnswerPreferences = (changedField: QuestionAnswerPreferenceField) => {
  if (mode.value !== 'questionAnswer') return
  if (changedField === 'models') {
    qaPreferenceDraft.value.modelIds = mergeVisibleQuestionAnswerIds(
      qaPreferenceDraft.value.modelIds,
      models.value.map(model => model.id),
      models.value.filter(model => selected.value.has(model.id)).map(model => model.id),
    )
  } else if (changedField === 'questions') {
    qaPreferenceDraft.value.questionIds = mergeVisibleQuestionAnswerIds(
      qaPreferenceDraft.value.questionIds,
      qaQuestions.value.map(question => question.id),
      qaQuestions.value.filter(question => qaSelectedQuestions.value.has(question.id)).map(question => question.id),
    )
  } else if (changedField === 'reasoningEffort') {
    qaPreferenceDraft.value.reasoningEffort = qaReasoningEffort.value
  } else {
    qaPreferenceDraft.value.repeatCount = qaRepeatCount.value
  }
  emit('question-answer-preferences-changed', {
    modelIds: [...qaPreferenceDraft.value.modelIds],
    questionIds: [...qaPreferenceDraft.value.questionIds],
    reasoningEffort: qaPreferenceDraft.value.reasoningEffort,
    repeatCount: qaPreferenceDraft.value.repeatCount,
  })
}

const readableMessage = (rawKey: string): string => t(connectionHealthMessageKey(rawKey, te))
const qaReadableError = computed(() => qaErrorKey.value.startsWith('admin.')
  ? t(qaErrorKey.value)
  : t(`${prefix}.questionAnswer.errorTypes.${qaErrorKey.value || 'unknown'}`))

watch(
  () => [props.open, props.target?.targetId],
  async ([isOpen]) => {
    resetQuestionAnswerTargetState()
    if (!isOpen || !props.target) {
      cleanupFrontendWork()
      return
    }
    cleanupFrontendWork()
    const targetId = props.target.targetId
    const sequence = loadSequence
    const controller = beginModelDiscovery()
    if (mode.value !== 'questionAnswer') skipInitializedQuestionAnswerModeLoad = true
    mode.value = 'questionAnswer'
    models.value = []
    onceModels.value = []
    onceLoadState.value = 'loading'
    selected.value = new Set()
    results.value = []
    loadErrorKey.value = ''
    testErrorKey.value = ''
    formalProgress.value = ''
    phase.value = 'loading'
    emit('question-answer-viewed', targetId)
    void loadQuestionAnswerData(targetId, sequence)

    const outcome = await discoverModels(targetId, controller.signal)
    finishModelDiscovery(controller)
    if (sequence !== loadSequence || !props.open || props.target?.targetId !== targetId) return
    if ('errorKey' in outcome) {
      loadErrorKey.value = outcome.errorKey
      onceLoadState.value = 'error'
      if (currentProbeMode() !== 'formal') phase.value = 'error'
      return
    }
    onceModels.value = outcome.models
    onceLoadState.value = 'ready'
    if (currentProbeMode() === 'formal') return
    models.value = onceModels.value
    if (
      !restoreActiveQuestionAnswerSelection(qaRuntimeBatch.value)
      && !restoreSavedQuestionAnswerSelection()
    ) selected.value = defaultSelection(outcome.models)
    phase.value = 'ready'
  },
)

watch(mode, (nextMode) => {
  if (nextMode === 'questionAnswer' && skipInitializedQuestionAnswerModeLoad) {
    skipInitializedQuestionAnswerModeLoad = false
    return
  }
  results.value = []
  testErrorKey.value = ''
  formalProgress.value = ''
  qaCompletedNotice.value = false
  if (nextMode !== 'questionAnswer') {
    cancelQuestionAnswerStart()
    resetQuestionAnswerViewState()
  }
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
    qaSelectionDataReady = false
    emit('question-answer-viewed', props.target.targetId)
    void loadQuestionAnswerData(props.target.targetId, loadSequence)
  }
})

const hasModels = computed(() => models.value.length > 0)
const qaActive = computed(() => Boolean(qaRuntimeBatch.value?.active))
const qaSelectionLocked = computed(() => qaStarting.value || qaActive.value)
const qaSubmission = computed(() => questionAnswerSubmissionSummary(
  selected.value.size,
  qaSelectedQuestions.value.size,
  qaRepeatCount.value,
))
const canStartTest = computed(() => {
  if (!hasModels.value || selected.value.size === 0 || phase.value === 'testing') return false
  if (mode.value !== 'questionAnswer') return true
  return qaSelectedQuestions.value.size > 0
    && qaSubmission.value.validRepeatCount
    && qaSubmission.value.withinBatchLimit
    && !qaSelectionLocked.value
    && !qaLoading.value
})
const qaWaitingCount = computed(() => Math.max(
  0,
  (qaRuntimeBatch.value?.stats.requests.inProgress ?? 0) - (qaRuntimeBatch.value?.runningCount ?? 0),
))
const qaCurrentRecords = computed(() => {
  if (!qaReviewBatch.value) return []
  return qaReviewBatch.value.active
    ? qaReviewBatch.value.records
    : filterQuestionAnswerRecords(qaReviewBatch.value.records, qaShowAll.value)
})
const qaReviewCompletedAt = computed(() => (
  qaReviewBatch.value ? questionAnswerBatchCompletedAt(qaReviewBatch.value) : null
))
const qaHistoryBatchGroups = computed(() => {
  const reviewRecordIDs = new Set(qaReviewBatch.value?.records.map(record => record.id) ?? [])
  return groupQuestionAnswerHistoryByBatch(
    qaHistory.value.records.filter(record => !reviewRecordIDs.has(record.id)),
  )
})
const qaHistoryRowsVisible = computed(() => Boolean(qaRuntimeBatch.value?.active || qaShowAll.value))
const currentBatchGridClass = computed(() => {
  if (qaReviewBatch.value && !qaReviewBatch.value.active && !qaShowAll.value) return 'grid-cols-1'
  const count = qaCurrentRecords.value.length
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
  if (mode.value === 'questionAnswer') emitQuestionAnswerPreferences('models')
}

const toggleQuestion = (questionId: string) => {
  if (qaSelectionLocked.value) return
  const next = new Set(qaSelectedQuestions.value)
  if (next.has(questionId)) next.delete(questionId)
  else next.add(questionId)
  qaSelectedQuestions.value = next
  emitQuestionAnswerPreferences('questions')
}

const selectQuestionAnswerReasoningEffort = (value: QuestionAnswerReasoningEffort) => {
  if (qaSelectionLocked.value || qaReasoningEffort.value === value) return
  qaReasoningEffort.value = value
  emitQuestionAnswerPreferences('reasoningEffort')
}

const selectQuestionAnswerRepeatCount = (event: Event) => {
  if (qaSelectionLocked.value) return
  const value = Number((event.target as HTMLSelectElement).value)
  if (!Number.isInteger(value) || value < 1 || value > 10 || qaRepeatCount.value === value) return
  qaRepeatCount.value = value
  emitQuestionAnswerPreferences('repeatCount')
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
  if (
    !restoreActiveQuestionAnswerSelection(qaRuntimeBatch.value)
    && !restoreSavedQuestionAnswerSelection()
  ) selected.value = defaultSelection(outcome.models)
  phase.value = 'ready'
}

const loadQuestionAnswerData = async (
  targetId: string,
  sequence: number,
  historyPage = 1,
  preserveQuestionSelection = false,
) => {
  const historySequence = beginQuestionAnswerHistoryIntent(historyPage)
  const controller = new AbortController()
  qaDataController = controller
  qaLoading.value = true
  qaErrorKey.value = ''
  try {
    const [questions, history, batch] = await Promise.all([
      listTestQuestions(controller.signal),
      getQuestionAnswerHistory(targetId, historyPage, controller.signal),
      getLatestQuestionAnswerBatch(targetId, controller.signal),
    ])
    if (
      sequence !== loadSequence
      || !questionAnswerHistoryIntentIsCurrent(historySequence)
      || mode.value !== 'questionAnswer'
      || props.target?.targetId !== targetId
    ) return
    qaQuestions.value = questions.filter(question => question.enabled)
    const enabledQuestionIDs = new Set(qaQuestions.value.map(question => question.id))
    const preservedQuestions = preserveQuestionSelection
      ? new Set(Array.from(qaSelectedQuestions.value).filter(questionID => enabledQuestionIDs.has(questionID)))
      : new Set<string>()
    const defaultQuestion = qaQuestions.value.find(question => question.isDefault)
    qaSelectedQuestions.value = preservedQuestions.size > 0
      ? preservedQuestions
      : new Set(defaultQuestion ? [defaultQuestion.id] : [])
    qaHistory.value = history
    qaHistoryIntentPage = history.page
    qaRuntimeBatch.value = batch.batchId ? batch : null
    qaReviewBatch.value = qaRuntimeBatch.value
    qaShowAll.value = false
    qaCurrentExpanded.value = new Set()
    qaSelectionDataReady = true
    if (!restoreActiveQuestionAnswerSelection(qaRuntimeBatch.value)) restoreSavedQuestionAnswerSelection()
    if (batch.active) scheduleQuestionAnswerPoll()
    else clearQuestionAnswerClock()
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    if (
      sequence === loadSequence
      && questionAnswerHistoryIntentIsCurrent(historySequence)
      && mode.value === 'questionAnswer'
      && props.target?.targetId === targetId
    ) qaErrorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
  } finally {
    if (qaDataController === controller) qaDataController = null
    if (sequence === loadSequence && questionAnswerHistoryIntentIsCurrent(historySequence)) qaLoading.value = false
  }
}

const questionAnswerScopeIsCurrent = (scope: QuestionAnswerOperationScope): boolean => (
  isCurrentQuestionAnswerOperation(scope, {
    sequence: loadSequence,
    open: props.open,
    mode: mode.value,
    targetId: props.target?.targetId ?? null,
    batchId: qaRuntimeBatch.value?.batchId ?? null,
  })
)

const questionAnswerPollIsCurrent = (
  pollSequence: number,
  scope: QuestionAnswerOperationScope,
): boolean => pollSequence === qaPollSequence && questionAnswerScopeIsCurrent(scope)

const applyRuntimeQuestionAnswerBatch = (batch: QuestionAnswerBatch) => {
  qaRuntimeBatch.value = batch
  if (!qaReviewBatch.value || qaReviewBatch.value.batchId === batch.batchId) {
    qaReviewBatch.value = batch
  }
}

const scheduleQuestionAnswerPoll = () => {
  clearQuestionAnswerPolling()
  if (!props.open || mode.value !== 'questionAnswer' || !qaRuntimeBatch.value?.active) return
  startQuestionAnswerClock()
  qaPollTimer = setTimeout(() => void pollQuestionAnswerBatch(), 2000)
}

const pollQuestionAnswerBatch = async () => {
  if (!props.target || !qaRuntimeBatch.value?.batchId || !props.open || mode.value !== 'questionAnswer') return
  const targetId = props.target.targetId
  const batchId = qaRuntimeBatch.value.batchId
  const sequence = loadSequence
  const scope = { sequence, targetId, batchId }
  const pollSequence = qaPollSequence
  const runtimeSnapshotSequence = ++qaRuntimeSnapshotSequence
  const controller = new AbortController()
  qaPollController = controller
  let historySequence: number | null = null
  try {
    let batch = await getQuestionAnswerBatch(targetId, batchId, controller.signal)
    if (!questionAnswerPollIsCurrent(pollSequence, scope)) return
    if (batch.active) {
      if (!qaRuntimeBatch.value?.active) {
        clearQuestionAnswerClock()
        return
      }
      if (runtimeSnapshotSequence !== qaRuntimeSnapshotSequence) {
        scheduleQuestionAnswerPoll()
        return
      }
      applyRuntimeQuestionAnswerBatch(batch)
      scheduleQuestionAnswerPoll()
      return
    }
    batch = await getQuestionAnswerBatch(targetId, batchId, controller.signal)
    if (!questionAnswerPollIsCurrent(pollSequence, scope)) return
    if (batch.active) {
      if (!qaRuntimeBatch.value?.active) {
        clearQuestionAnswerClock()
        return
      }
      if (runtimeSnapshotSequence === qaRuntimeSnapshotSequence) applyRuntimeQuestionAnswerBatch(batch)
      if (qaRuntimeBatch.value?.active) scheduleQuestionAnswerPoll()
      else clearQuestionAnswerClock()
      return
    }
    const reviewFollowsRuntime = qaReviewLoadingBatchId.value === null
      && qaReviewBatch.value?.batchId === batch.batchId
    applyRuntimeQuestionAnswerBatch(batch)
    if (reviewFollowsRuntime) {
      qaShowAll.value = false
      qaCurrentExpanded.value = new Set()
    }
    const historyPage = reviewFollowsRuntime ? 1 : qaHistoryIntentPage
    historySequence = beginQuestionAnswerHistoryIntent(historyPage)
    const history = await getQuestionAnswerHistory(targetId, historyPage, controller.signal)
    if (!questionAnswerPollIsCurrent(pollSequence, scope)) return
    if (
      questionAnswerHistoryIntentIsCurrent(historySequence)
      && qaHistoryIntentPage === historyPage
    ) {
      qaHistory.value = history
      qaHistoryIntentPage = history.page
    }
    qaCompletedNotice.value = true
    clearQuestionAnswerPolling()
    clearQuestionAnswerClock()
  } catch (error) {
    if (
      (error instanceof Error && error.name === 'AbortError')
      || !questionAnswerPollIsCurrent(pollSequence, scope)
    ) return
    if (historySequence !== null && !questionAnswerHistoryIntentIsCurrent(historySequence)) {
      qaCompletedNotice.value = true
      clearQuestionAnswerPolling()
      clearQuestionAnswerClock()
      return
    }
    if (historySequence === null && !qaRuntimeBatch.value?.active) {
      clearQuestionAnswerClock()
      return
    }
    qaErrorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
    if (qaRuntimeBatch.value?.active) scheduleQuestionAnswerPoll()
    else clearQuestionAnswerClock()
  } finally {
    if (qaPollController === controller) qaPollController = null
  }
}

const startQuestionAnswers = async () => {
  if (!canStartTest.value || !props.target) return
  cancelQuestionAnswerReview()
  cancelQuestionAnswerJudgments()
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
      qaReasoningEffort.value,
      qaRepeatCount.value,
      controller.signal,
    )
    if (startSequence !== qaStartSequence || !questionAnswerScopeIsCurrent(scope)) return
    qaErrorKey.value = ''
    qaRuntimeBatch.value = batch
    qaReviewBatch.value = batch
    qaShowAll.value = false
    qaCurrentExpanded.value = new Set()
    emit('question-answer-started', targetId)
    const historySequence = beginQuestionAnswerHistoryIntent(1)
    qaHistory.value.page = 1
    scheduleQuestionAnswerPoll()
    try {
      const history = await getQuestionAnswerHistory(targetId, 1, controller.signal)
      if (
        startSequence === qaStartSequence
        && questionAnswerHistoryIntentIsCurrent(historySequence)
        && questionAnswerScopeIsCurrent(scope)
      ) {
        qaHistory.value = history
        qaHistoryIntentPage = history.page
      }
    } catch (error) {
      if (error instanceof Error && error.name === 'AbortError') return
      if (
        startSequence === qaStartSequence
        && questionAnswerHistoryIntentIsCurrent(historySequence)
        && questionAnswerScopeIsCurrent(scope)
      ) {
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
  if (!props.target || !qaRuntimeBatch.value?.batchId || qaCancelling.value) return
  const targetId = props.target.targetId
  const batchId = qaRuntimeBatch.value.batchId
  const sequence = loadSequence
  const cancelSequence = ++qaCancelSequence
  const scope = { sequence, targetId, batchId }
  qaCancelController?.abort()
  const controller = new AbortController()
  qaCancelController = controller
  qaCancelling.value = true
  qaErrorKey.value = ''
  clearQuestionAnswerPolling()
  let historySequence: number | null = null
  try {
    const batch = await cancelQuestionAnswerBatch(targetId, batchId, controller.signal)
    if (cancelSequence !== qaCancelSequence || !questionAnswerScopeIsCurrent(scope)) return
    if (!qaRuntimeBatch.value?.active) {
      clearQuestionAnswerClock()
      return
    }
    const reviewFollowsRuntime = qaReviewLoadingBatchId.value === null
      && qaReviewBatch.value?.batchId === batch.batchId
    applyRuntimeQuestionAnswerBatch(batch)
    if (reviewFollowsRuntime) {
      qaShowAll.value = false
      qaCurrentExpanded.value = new Set()
    }
    clearQuestionAnswerClock()
    const historyPage = reviewFollowsRuntime ? 1 : qaHistoryIntentPage
    historySequence = beginQuestionAnswerHistoryIntent(historyPage)
    const history = await getQuestionAnswerHistory(targetId, historyPage, controller.signal)
    if (cancelSequence !== qaCancelSequence || !questionAnswerScopeIsCurrent(scope)) return
    if (
      questionAnswerHistoryIntentIsCurrent(historySequence)
      && qaHistoryIntentPage === historyPage
    ) {
      qaHistory.value = history
      qaHistoryIntentPage = history.page
    }
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    if (historySequence === null && !qaRuntimeBatch.value?.active) {
      clearQuestionAnswerClock()
      return
    }
    if (
      cancelSequence === qaCancelSequence
      && questionAnswerScopeIsCurrent(scope)
      && (historySequence === null || questionAnswerHistoryIntentIsCurrent(historySequence))
    ) {
      qaErrorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
      if (qaRuntimeBatch.value?.active) scheduleQuestionAnswerPoll()
    }
  } finally {
    if (qaCancelController === controller) qaCancelController = null
    if (cancelSequence === qaCancelSequence) qaCancelling.value = false
  }
}

const reviewQuestionAnswerBatch = async (batchId: string) => {
  if (!props.target || qaReviewLoadingBatchId.value === batchId) return
  const targetId = props.target.targetId
  const sequence = loadSequence
  const reviewSequence = cancelQuestionAnswerReview()
  const runtimeSelectionSequence = qaRuntimeBatch.value?.batchId === batchId && qaRuntimeBatch.value.active
    ? ++qaRuntimeSnapshotSequence
    : null
  qaReviewJudgmentRefreshSequence = qaJudgmentRefreshSequences.get(batchId) ?? 0
  const controller = new AbortController()
  qaReviewController = controller
  qaReviewLoadingBatchId.value = batchId
  qaErrorKey.value = ''
  try {
    const batch = await getQuestionAnswerBatch(targetId, batchId, controller.signal)
    if (
      sequence !== loadSequence
      || reviewSequence !== qaReviewSequence
      || mode.value !== 'questionAnswer'
      || props.target?.targetId !== targetId
    ) return
    const runtimeBatch = qaRuntimeBatch.value
    if (runtimeSelectionSequence !== null && runtimeBatch?.batchId === batch.batchId) {
      if (
        runtimeBatch.active
        && (runtimeSelectionSequence === qaRuntimeSnapshotSequence || !batch.active)
      ) qaRuntimeBatch.value = batch
      const currentRuntime = qaRuntimeBatch.value
      qaReviewBatch.value = currentRuntime?.batchId === batch.batchId
        ? currentRuntime
        : batch
      if (!qaRuntimeBatch.value?.active) clearQuestionAnswerClock()
    } else {
      qaReviewBatch.value = batch.active
        && runtimeBatch?.batchId === batch.batchId
        && !runtimeBatch.active
        ? runtimeBatch
        : batch
    }
    qaShowAll.value = false
    qaCurrentExpanded.value = new Set()
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    if (sequence === loadSequence && reviewSequence === qaReviewSequence) {
      qaErrorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
    }
  } finally {
    if (qaReviewController === controller) qaReviewController = null
    if (reviewSequence === qaReviewSequence) {
      qaReviewLoadingBatchId.value = null
      qaReviewJudgmentRefreshSequence = 0
    }
    if (
      runtimeSelectionSequence !== null
      && runtimeSelectionSequence === qaRuntimeSnapshotSequence
      && reviewSequence === qaReviewSequence
      && qaRuntimeBatch.value?.batchId === batchId
      && qaRuntimeBatch.value.active
    ) scheduleQuestionAnswerPoll()
  }
}

const reviewLatestQuestionAnswerBatch = () => {
  if (!qaRuntimeBatch.value) return
  cancelQuestionAnswerReview()
  qaReviewBatch.value = qaRuntimeBatch.value
  qaShowAll.value = false
  qaCurrentExpanded.value = new Set()
}

const goQuestionAnswerPage = async (page: number) => {
  if (!props.target || page < 1 || page > qaHistory.value.totalPages || page === qaHistoryIntentPage) return
  const targetId = props.target.targetId
  const sequence = loadSequence
  const historySequence = beginQuestionAnswerHistoryIntent(page)
  const controller = new AbortController()
  qaDataController = controller
  qaErrorKey.value = ''
  try {
    const history = await getQuestionAnswerHistory(targetId, page, controller.signal)
    if (
      sequence !== loadSequence
      || !questionAnswerHistoryIntentIsCurrent(historySequence)
      || mode.value !== 'questionAnswer'
      || props.target?.targetId !== targetId
    ) return
    qaHistory.value = history
    qaHistoryIntentPage = history.page
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    if (
      sequence === loadSequence
      && questionAnswerHistoryIntentIsCurrent(historySequence)
      && mode.value === 'questionAnswer'
      && props.target?.targetId === targetId
    ) {
      qaHistoryIntentPage = qaHistory.value.page
      qaErrorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
    }
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

const replaceQuestionAnswerBatchRecord = (
  batch: QuestionAnswerBatch | null,
  authoritative: QuestionAnswerRecord,
): QuestionAnswerBatch | null => {
  if (!batch || !batch.records.some(record => record.id === authoritative.id)) return batch
  const records = replaceQuestionAnswerRecord(batch.records, authoritative)
  return {
    ...batch,
    records,
    stats: {
      ...batch.stats,
      reviews: questionAnswerReviewStatsFromRecords(records),
      byModel: batch.stats.byModel.map(item => ({
        ...item,
        requests: { ...item.requests },
        reviews: questionAnswerReviewStatsFromRecords(
          records.filter(record => record.modelName === item.modelName),
        ),
      })),
    },
  }
}

const applyAuthoritativeQuestionAnswerRecord = (authoritative: QuestionAnswerRecord) => {
  qaRuntimeBatch.value = replaceQuestionAnswerBatchRecord(qaRuntimeBatch.value, authoritative)
  qaReviewBatch.value = replaceQuestionAnswerBatchRecord(qaReviewBatch.value, authoritative)
  if (qaHistory.value.records.some(record => record.id === authoritative.id)) {
    qaHistory.value = {
      ...qaHistory.value,
      records: replaceQuestionAnswerRecord(qaHistory.value.records, authoritative),
    }
  }
}

const clearQuestionAnswerMarking = (recordId: string) => {
  const marking = new Map(qaMarking.value)
  marking.delete(recordId)
  qaMarking.value = marking
}

const saveQuestionAnswerJudgment = async (record: QuestionAnswerRecord, judgment: QuestionAnswerJudgment) => {
  if (!props.target || record.status !== 'succeeded' || qaMarking.value.has(record.id)) return
  const targetId = props.target.targetId
  const sequence = loadSequence
  const scope = { sequence, targetId }
  const judgmentSessionSequence = qaJudgmentSessionSequence
  const controller = new AbortController()
  qaJudgmentControllers.set(record.id, controller)
  const nextMarking = new Map(qaMarking.value)
  nextMarking.set(record.id, judgment)
  qaMarking.value = nextMarking
  qaErrorKey.value = ''
  let judgmentSaved = false
  let pollingPausedForRuntime = false
  let judgmentRefreshSequence: number | null = null
  let historySequence: number | null = null
  let judgmentRefreshStage: 'put' | 'batch' | 'history' = 'put'
  const resumeRuntimePolling = () => {
    if (
      pollingPausedForRuntime
      && judgmentRefreshSequence !== null
      && judgmentSessionSequence === qaJudgmentSessionSequence
      && qaJudgmentRefreshSequences.get(record.batchId) === judgmentRefreshSequence
      && questionAnswerScopeIsCurrent(scope)
      && qaRuntimeBatch.value?.batchId === record.batchId
      && qaRuntimeBatch.value.active
    ) {
      pollingPausedForRuntime = false
      scheduleQuestionAnswerPoll()
    }
  }
  try {
    const authoritative = await setQuestionAnswerJudgment(targetId, record.id, judgment, controller.signal)
    if (
      judgmentSessionSequence !== qaJudgmentSessionSequence
      || !questionAnswerScopeIsCurrent(scope)
    ) return
    applyAuthoritativeQuestionAnswerRecord(authoritative)
    judgmentSaved = true
    clearQuestionAnswerMarking(record.id)
    if (qaRuntimeBatch.value?.batchId === record.batchId) {
      clearQuestionAnswerPolling()
      pollingPausedForRuntime = true
    }
    const historyPage = qaHistoryIntentPage
    historySequence = beginQuestionAnswerHistoryIntent()
    judgmentRefreshSequence = ++qaJudgmentRefreshSequence
    qaJudgmentRefreshSequences.set(record.batchId, judgmentRefreshSequence)
    judgmentRefreshStage = 'batch'
    const batchPromise = getQuestionAnswerBatch(targetId, record.batchId, controller.signal)
    const historyOutcomePromise = getQuestionAnswerHistory(targetId, historyPage, controller.signal).then(
      history => ({ ok: true as const, history }),
      error => ({ ok: false as const, error }),
    )
    const batch = await batchPromise
    if (
      judgmentSessionSequence !== qaJudgmentSessionSequence
      || qaJudgmentRefreshSequences.get(record.batchId) !== judgmentRefreshSequence
      || !questionAnswerScopeIsCurrent(scope)
    ) return
    if (
      qaReviewLoadingBatchId.value === batch.batchId
      && qaReviewJudgmentRefreshSequence < judgmentRefreshSequence
    ) {
      cancelQuestionAnswerReview()
      const runtimeBatch = qaRuntimeBatch.value
      qaReviewBatch.value = batch.active
        && runtimeBatch?.batchId === batch.batchId
        && !runtimeBatch.active
        ? runtimeBatch
        : batch
    } else if (qaReviewBatch.value?.batchId === batch.batchId && (qaReviewBatch.value.active || !batch.active)) {
      qaReviewBatch.value = batch
    }
    if (qaRuntimeBatch.value?.batchId === batch.batchId && (qaRuntimeBatch.value.active || !batch.active)) {
      qaRuntimeBatch.value = batch
    }
    resumeRuntimePolling()
    judgmentRefreshStage = 'history'
    const historyOutcome = await historyOutcomePromise
    if (!historyOutcome.ok) throw historyOutcome.error
    const history = historyOutcome.history
    if (
      judgmentSessionSequence !== qaJudgmentSessionSequence
      || qaJudgmentRefreshSequences.get(record.batchId) !== judgmentRefreshSequence
      || !questionAnswerScopeIsCurrent(scope)
    ) return
    if (
      questionAnswerHistoryIntentIsCurrent(historySequence)
      && qaHistoryIntentPage === historyPage
    ) {
      qaHistory.value = history
      qaHistoryIntentPage = history.page
    }
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    if (
      judgmentSessionSequence === qaJudgmentSessionSequence
      && questionAnswerScopeIsCurrent(scope)
      && (
        judgmentRefreshSequence === null
        || qaJudgmentRefreshSequences.get(record.batchId) === judgmentRefreshSequence
      )
      && (
        judgmentRefreshStage !== 'history'
        || historySequence === null
        || questionAnswerHistoryIntentIsCurrent(historySequence)
      )
    ) {
      qaErrorKey.value = judgmentSaved
        ? `${prefix}.questionAnswer.judgmentRefreshFailed`
        : (error instanceof Error ? error.message : 'admin.connectionHealth.errors.request')
    }
  } finally {
    if (qaJudgmentControllers.get(record.id) === controller) {
      qaJudgmentControllers.delete(record.id)
      if (
        judgmentSessionSequence === qaJudgmentSessionSequence
        && questionAnswerScopeIsCurrent(scope)
      ) {
        clearQuestionAnswerMarking(record.id)
      }
    }
    resumeRuntimePolling()
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
const questionAnswerReasoningEffortLabel = (value: QuestionAnswerReasoningEffort | null | undefined): string => {
  if (!value) return t(`${prefix}.questionAnswer.reasoningEffort.unspecified`)
  return t(`${prefix}.questionAnswer.reasoningEffort.options.${value}`)
}
const questionAnswerRecordClass = (record: QuestionAnswerRecord): string => {
  if (record.status === 'cancelled' || record.status === 'pending' || record.status === 'running') return 'border-border/50 bg-surface-line/20'
  if (record.status === 'failed' || record.answerJudgment === 'incorrect') return 'border-red-500/60 bg-red-500/20 dark:border-red-400/50 dark:bg-red-500/20'
  if (record.answerJudgment === 'correct') return 'border-green-500/35 bg-green-500/10 dark:border-green-400/35 dark:bg-green-500/10'
  return 'border-border/60 bg-card'
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
      <div v-if="open && target" class="fixed inset-0 z-[150] flex items-center justify-center p-2">
        <div class="absolute inset-0 bg-background/60 backdrop-blur-sm" @click="close" />

        <div role="dialog" aria-modal="true" :aria-label="t(`${prefix}.title`)" class="relative flex h-[calc(100dvh-1rem)] w-[calc(100vw-1rem)] max-w-none flex-col overflow-hidden rounded-2xl border border-border/60 bg-card shadow-2xl">
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
              <button type="button" class="whitespace-nowrap rounded-md px-3 py-1.5 text-xs font-medium transition-colors" :class="mode === 'questionAnswer' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'" :disabled="phase === 'testing'" @click="mode = 'questionAnswer'">
                {{ t(`${prefix}.modes.questionAnswer`) }}
              </button>
              <button type="button" class="whitespace-nowrap rounded-md px-3 py-1.5 text-xs font-medium transition-colors" :class="mode === 'formal' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'" :disabled="phase === 'testing'" @click="mode = 'formal'">
                {{ t(`${prefix}.modes.formal`) }}
              </button>
              <button type="button" class="whitespace-nowrap rounded-md px-3 py-1.5 text-xs font-medium transition-colors" :class="mode === 'once' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'" :disabled="phase === 'testing'" @click="mode = 'once'">
                {{ t(`${prefix}.modes.once`) }}
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
                <div v-if="mode === 'questionAnswer'" class="mb-4 grid overflow-hidden rounded-lg border border-border/50 bg-surface-line/20" :class="qaReviewBatch ? 'grid-cols-3' : 'grid-cols-2'">
                  <div v-if="qaReviewBatch" data-testid="question-answer-review-stats" class="px-4 py-3">
                    <div class="flex items-center justify-between gap-3">
                      <p class="text-xs font-medium text-muted-foreground">{{ t(`${prefix}.questionAnswer.reviewTitle`) }}</p>
                      <p class="text-xs text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.submitted`, { count: qaReviewBatch.stats.requests.submitted }) }}</p>
                    </div>
                    <dl class="mt-3 grid grid-cols-4 gap-x-3 gap-y-2">
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.inProgress`) }}</dt><dd class="font-semibold text-foreground">{{ qaReviewBatch.stats.requests.inProgress }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.succeeded`) }}</dt><dd class="font-semibold text-foreground">{{ qaReviewBatch.stats.requests.succeeded }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.failed`) }}</dt><dd class="font-semibold text-foreground">{{ qaReviewBatch.stats.requests.failed }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.cancelled`) }}</dt><dd class="font-semibold text-foreground">{{ qaReviewBatch.stats.requests.cancelled }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.unreviewed`) }}</dt><dd class="font-semibold text-foreground">{{ qaReviewBatch.stats.reviews.unreviewed }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.correct`) }}</dt><dd class="font-semibold text-foreground">{{ qaReviewBatch.stats.reviews.correct }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.incorrect`) }}</dt><dd class="font-semibold text-foreground">{{ qaReviewBatch.stats.reviews.incorrect }}</dd></div>
                    </dl>
                    <div v-if="qaReviewBatch.stats.byModel.length" class="mt-3 space-y-2 border-t border-border/40 pt-3">
                      <div v-for="item in qaReviewBatch.stats.byModel" :key="item.modelName" data-testid="question-answer-model-stats" class="rounded-md border border-border/40 p-2">
	                        <p class="break-all text-xs font-medium text-foreground">{{ item.modelName }}</p>
	                        <dl class="mt-2 grid grid-cols-4 gap-x-2 gap-y-1">
	                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.submitted`, { count: item.requests.submitted }) }}</dt><dd class="sr-only">{{ item.requests.submitted }}</dd></div>
	                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.inProgress`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.requests.inProgress }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.succeeded`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.requests.succeeded }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.failed`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.requests.failed }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.cancelled`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.requests.cancelled }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.unreviewed`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.reviews.unreviewed }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.correct`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.reviews.correct }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.incorrect`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.reviews.incorrect }}</dd></div>
                        </dl>
                      </div>
                    </div>
                  </div>
                  <div class="px-4 py-3" :class="qaReviewBatch ? 'border-l border-border/50' : ''">
                    <div class="flex items-center justify-between gap-3">
                      <p class="text-xs font-medium text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.allTime`) }}</p>
                      <p class="text-xs text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.submitted`, { count: qaHistory.stats.requests.submitted }) }}</p>
                    </div>
                    <dl class="mt-3 grid grid-cols-4 gap-x-3 gap-y-2">
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.inProgress`) }}</dt><dd class="font-semibold text-foreground">{{ qaHistory.stats.requests.inProgress }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.succeeded`) }}</dt><dd class="font-semibold text-foreground">{{ qaHistory.stats.requests.succeeded }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.failed`) }}</dt><dd class="font-semibold text-foreground">{{ qaHistory.stats.requests.failed }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.cancelled`) }}</dt><dd class="font-semibold text-foreground">{{ qaHistory.stats.requests.cancelled }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.unreviewed`) }}</dt><dd class="font-semibold text-foreground">{{ qaHistory.stats.reviews.unreviewed }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.correct`) }}</dt><dd class="font-semibold text-foreground">{{ qaHistory.stats.reviews.correct }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.incorrect`) }}</dt><dd class="font-semibold text-foreground">{{ qaHistory.stats.reviews.incorrect }}</dd></div>
                    </dl>
                    <div v-if="qaHistory.stats.byModel.length" class="mt-3 space-y-2 border-t border-border/40 pt-3">
                      <div v-for="item in qaHistory.stats.byModel" :key="item.modelName" data-testid="question-answer-model-stats" class="rounded-md border border-border/40 p-2">
	                        <p class="break-all text-xs font-medium text-foreground">{{ item.modelName }}</p>
	                        <dl class="mt-2 grid grid-cols-4 gap-x-2 gap-y-1">
	                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.submitted`, { count: item.requests.submitted }) }}</dt><dd class="sr-only">{{ item.requests.submitted }}</dd></div>
	                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.inProgress`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.requests.inProgress }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.succeeded`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.requests.succeeded }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.failed`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.requests.failed }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.cancelled`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.requests.cancelled }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.unreviewed`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.reviews.unreviewed }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.correct`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.reviews.correct }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.incorrect`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.reviews.incorrect }}</dd></div>
                        </dl>
                      </div>
                    </div>
                  </div>
                  <div class="border-l border-border/50 px-4 py-3">
                    <div class="flex items-center justify-between gap-3">
                      <p class="text-xs font-medium text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.todayShanghai`) }}</p>
                      <p class="text-xs text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.submitted`, { count: qaHistory.todayStats.requests.submitted }) }}</p>
                    </div>
                    <dl class="mt-3 grid grid-cols-4 gap-x-3 gap-y-2">
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.inProgress`) }}</dt><dd class="font-semibold text-foreground">{{ qaHistory.todayStats.requests.inProgress }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.succeeded`) }}</dt><dd class="font-semibold text-foreground">{{ qaHistory.todayStats.requests.succeeded }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.failed`) }}</dt><dd class="font-semibold text-foreground">{{ qaHistory.todayStats.requests.failed }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.cancelled`) }}</dt><dd class="font-semibold text-foreground">{{ qaHistory.todayStats.requests.cancelled }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.unreviewed`) }}</dt><dd class="font-semibold text-foreground">{{ qaHistory.todayStats.reviews.unreviewed }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.correct`) }}</dt><dd class="font-semibold text-foreground">{{ qaHistory.todayStats.reviews.correct }}</dd></div>
                      <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.incorrect`) }}</dt><dd class="font-semibold text-foreground">{{ qaHistory.todayStats.reviews.incorrect }}</dd></div>
                    </dl>
                    <div v-if="qaHistory.todayStats.byModel.length" class="mt-3 space-y-2 border-t border-border/40 pt-3">
                      <div v-for="item in qaHistory.todayStats.byModel" :key="item.modelName" data-testid="question-answer-model-stats" class="rounded-md border border-border/40 p-2">
	                        <p class="break-all text-xs font-medium text-foreground">{{ item.modelName }}</p>
	                        <dl class="mt-2 grid grid-cols-4 gap-x-2 gap-y-1">
	                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.submitted`, { count: item.requests.submitted }) }}</dt><dd class="sr-only">{{ item.requests.submitted }}</dd></div>
	                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.inProgress`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.requests.inProgress }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.succeeded`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.requests.succeeded }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.failed`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.requests.failed }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.cancelled`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.requests.cancelled }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.unreviewed`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.reviews.unreviewed }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.correct`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.reviews.correct }}</dd></div>
                          <div><dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.stats.incorrect`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.reviews.incorrect }}</dd></div>
                        </dl>
                      </div>
                    </div>
                  </div>
                </div>

                <template v-if="mode === 'questionAnswer'">
                  <div class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_20rem]">
                    <div data-testid="question-answer-models">
                      <p class="mb-3 text-xs text-muted-foreground">{{ t(`${prefix}.selectHint`) }}</p>
                      <div class="grid grid-cols-1 gap-2.5 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
                        <label v-for="model in models" :key="model.id" class="flex cursor-pointer items-start gap-2 rounded-lg border border-border/40 px-3 py-2.5 transition-colors" :class="selected.has(model.id) ? 'border-primary/50 bg-primary/5' : 'hover:bg-surface-line/40'">
                          <input type="checkbox" class="mt-0.5 h-4 w-4 shrink-0 rounded border-border/60" :disabled="phase === 'testing' || qaSelectionLocked" :checked="selected.has(model.id)" @change="toggle(model.id)" />
                          <div class="min-w-0 flex-1">
                            <p class="truncate text-sm font-medium text-foreground">{{ model.name }}</p>
                            <p v-if="model.ownedBy" class="truncate text-xs text-muted-foreground">{{ model.ownedBy }}</p>
                          </div>
                        </label>
                      </div>
                    </div>
                    <fieldset data-testid="question-answer-reasoning" :disabled="qaSelectionLocked">
                      <legend class="mb-2 text-xs font-semibold text-foreground">{{ t(`${prefix}.questionAnswer.reasoningEffort.title`) }}</legend>
                      <div class="grid grid-cols-4 overflow-hidden rounded-lg border border-border/50 bg-surface-line/20 xl:grid-cols-1">
                        <label v-for="option in qaReasoningEffortOptions" :key="option.value" class="flex cursor-pointer items-center justify-center border-r border-border/40 px-2 py-2 text-xs transition-colors last:border-r-0 xl:border-b xl:border-r-0 xl:last:border-b-0" :class="qaReasoningEffort === option.value ? 'bg-primary/10 text-primary' : 'text-muted-foreground hover:bg-surface-line/40'">
                          <input :checked="qaReasoningEffort === option.value" class="sr-only" type="radio" name="question-answer-reasoning-effort" :value="option.value" @change="selectQuestionAnswerReasoningEffort(option.value)" />
                          {{ t(`${prefix}.questionAnswer.reasoningEffort.options.${option.labelKey}`) }}
                        </label>
                      </div>
                      <label class="mt-3 block text-xs font-semibold text-foreground" for="question-answer-repeat-count">{{ t(`${prefix}.questionAnswer.repeatCount`) }}</label>
                      <select id="question-answer-repeat-count" :value="qaRepeatCount" class="mt-2 w-full rounded-lg border border-border/50 bg-background px-3 py-2 text-xs text-foreground" :disabled="qaSelectionLocked" @change="selectQuestionAnswerRepeatCount">
                        <option v-for="option in qaRepeatCountOptions" :key="option" :value="option">{{ option }}</option>
                      </select>
                    </fieldset>
                  </div>

                  <div data-testid="question-answer-questions" class="mt-5 border-t border-border/40 pt-4">
                    <div class="mb-3 flex items-center justify-between gap-3">
                      <h4 class="text-xs font-semibold text-foreground">{{ t(`${prefix}.questionAnswer.questionsTitle`) }}</h4>
                      <span class="text-xs text-muted-foreground">{{ t(`${prefix}.questionAnswer.selectedFormula`, { models: qaSubmission.modelCount, questions: qaSubmission.questionCount, repeat: qaSubmission.repeatCount, total: qaSubmission.total }) }}</span>
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
                    <p v-if="!qaSubmission.validRepeatCount" class="mt-2 text-xs text-red-600 dark:text-red-400">{{ t(`${prefix}.questionAnswer.repeatCountInvalid`) }}</p>
                    <p v-else-if="!qaSubmission.withinBatchLimit" class="mt-2 text-xs text-red-600 dark:text-red-400">{{ t(`${prefix}.questionAnswer.batchLimit`, { total: qaSubmission.total }) }}</p>
                  </div>

                  <p v-if="qaErrorKey" class="mt-4 rounded-lg bg-red-500/10 px-3 py-2 text-xs text-red-600 dark:text-red-400">{{ qaReadableError }}</p>
                  <p v-if="qaCompletedNotice" class="mt-4 rounded-lg bg-green-500/10 px-3 py-2 text-xs text-green-600 dark:text-green-400">{{ t(`${prefix}.questionAnswer.completedNotice`) }}</p>

                  <section class="mt-5 border-t border-border/40 pt-4">
                    <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
                      <div>
                        <h4 class="text-xs font-semibold text-foreground">{{ t(`${prefix}.questionAnswer.currentTitle`) }}</h4>
                        <p v-if="qaReviewBatch" class="mt-1 text-xs text-muted-foreground">
                          {{ t(`${prefix}.questionAnswer.submitted`, { count: qaReviewBatch.stats.requests.submitted }) }} · {{ t(`${prefix}.questionAnswer.progress`, { completed: qaReviewBatch.stats.requests.succeeded + qaReviewBatch.stats.requests.failed + qaReviewBatch.stats.requests.cancelled, total: qaReviewBatch.stats.requests.submitted }) }}
                          · {{ t(`${prefix}.questionAnswer.reasoningEffort.label`) }}: {{ questionAnswerReasoningEffortLabel(qaReviewBatch.reasoningEffort) }}
                          <span v-if="qaReviewBatch.active"> · {{ t(`${prefix}.questionAnswer.runningCount`, { count: qaReviewBatch.runningCount }) }}</span>
                        </p>
                        <p v-if="qaReviewBatch" data-testid="question-answer-review-batch" class="mt-1 text-xs text-muted-foreground">
                          {{ t(`${prefix}.questionAnswer.reviewBatchId`, { id: shortQuestionAnswerBatchId(qaReviewBatch.batchId) }) }}
                          <span v-if="qaReviewBatch.active"> · {{ t(`${prefix}.questionAnswer.batchStillRunning`) }}</span>
                          <span v-else-if="qaReviewCompletedAt"> · {{ t(`${prefix}.questionAnswer.batchCompletedAt`, { time: formatConnectionHealthTime(qaReviewCompletedAt) }) }}</span>
                          <span v-else> · {{ t(`${prefix}.questionAnswer.batchTimeUnknown`) }}</span>
                        </p>
                        <p v-if="qaRuntimeBatch?.active" class="mt-1 text-xs text-muted-foreground">{{ t(`${prefix}.questionAnswer.queueProgress`, { waiting: qaWaitingCount, running: qaRuntimeBatch.runningCount, completed: qaRuntimeBatch.completedCount }) }}</p>
                        <p v-if="qaRuntimeBatch?.active && qaWaitingCount > 0" class="mt-1 text-xs text-muted-foreground">{{ t(`${prefix}.questionAnswer.queueNotice`) }}</p>
                        <p v-if="qaRuntimeBatch?.active && qaReviewBatch?.batchId !== qaRuntimeBatch.batchId" class="mt-2 text-xs text-amber-700 dark:text-amber-300">
                          {{ t(`${prefix}.questionAnswer.latestBatchStillRunning`, {
                            completed: qaRuntimeBatch.stats.requests.succeeded + qaRuntimeBatch.stats.requests.failed + qaRuntimeBatch.stats.requests.cancelled,
                            total: qaRuntimeBatch.stats.requests.submitted,
                            running: qaRuntimeBatch.runningCount,
                          }) }}
                        </p>
                      </div>
                      <div class="flex flex-wrap items-center justify-end gap-2">
                        <button v-if="qaReviewBatch && !qaReviewBatch.active" type="button" class="rounded-lg border border-border/60 px-3 py-1.5 text-xs font-medium text-foreground hover:bg-surface-line" @click="qaShowAll = !qaShowAll">
                          {{ qaShowAll ? t(`${prefix}.questionAnswer.onlyUnreviewed`) : t(`${prefix}.questionAnswer.showAll`) }}
                        </button>
                        <button v-if="qaRuntimeBatch && qaReviewBatch?.batchId !== qaRuntimeBatch.batchId" type="button" class="rounded-lg border border-border/60 px-3 py-1.5 text-xs font-medium text-foreground hover:bg-surface-line" @click="reviewLatestQuestionAnswerBatch">
                          {{ t(`${prefix}.questionAnswer.reviewLatestBatch`) }}
                        </button>
                        <button v-if="qaRuntimeBatch?.active" type="button" class="inline-flex items-center gap-1.5 rounded-lg border border-red-500/30 px-3 py-1.5 text-xs font-medium text-red-600 hover:bg-red-500/10 disabled:opacity-50 dark:text-red-400" :disabled="qaCancelling" @click="stopQuestionAnswers">
                          <Loader2 v-if="qaCancelling" class="h-3.5 w-3.5 animate-spin" />
                          <StopCircle v-else class="h-3.5 w-3.5" />
                          {{ t(`${prefix}.questionAnswer.stop`) }}
                        </button>
                      </div>
                    </div>
                    <div v-if="!qaReviewBatch" class="rounded-lg border border-dashed border-border/50 px-3 py-5 text-center text-xs text-muted-foreground">{{ t(`${prefix}.questionAnswer.noBatch`) }}</div>
                    <div v-else>
                      <ul class="grid items-stretch gap-3" :class="currentBatchGridClass">
                        <li
                          v-for="record in qaCurrentRecords"
                          :key="record.id"
                          class="grid min-h-56 gap-6 rounded-lg border p-3"
                          :class="[
                            questionAnswerRecordClass(record),
                            record.status === 'succeeded'
                              ? 'md:grid-cols-[minmax(0,1fr)_12rem] md:grid-rows-2'
                              : 'md:grid-cols-1',
                          ]"
                        >
                          <button
                            v-if="record.status === 'succeeded'"
                            type="button"
                            class="inline-flex min-h-14 items-center justify-center gap-2 rounded-md border border-green-500/40 px-4 py-3 text-sm font-medium text-green-700 disabled:opacity-50 dark:text-green-400 md:col-start-2 md:row-start-1"
                            :class="qaMarking.has(record.id)
                              ? (qaMarking.get(record.id) === 'correct' ? 'bg-green-500/20' : 'hover:bg-green-500/10')
                              : (record.answerJudgment === 'correct' ? 'bg-green-500/20' : 'hover:bg-green-500/10')"
                            :aria-pressed="record.answerJudgment === 'correct'"
                            :disabled="qaMarking.has(record.id)"
                            @click="saveQuestionAnswerJudgment(record, 'correct')"
                          >
                            <Loader2 v-if="qaMarking.get(record.id) === 'correct'" class="h-4 w-4 animate-spin" />
                            {{ t(`${prefix}.questionAnswer.correct`) }}<span v-if="qaMarking.get(record.id) === 'correct'"> · {{ t(`${prefix}.questionAnswer.saving`) }}</span>
                          </button>

                          <div class="min-w-0" :class="record.status === 'succeeded' ? 'md:col-start-1 md:row-span-2 md:row-start-1' : ''">
                            <div class="flex items-start justify-between gap-3">
                              <div class="flex min-w-0 items-start gap-2">
                                <Loader2 v-if="record.status === 'pending' || record.status === 'running'" class="mt-0.5 h-5 w-5 shrink-0 animate-spin text-primary" />
                                <CheckCircle2 v-else-if="record.answerJudgment === 'correct'" class="mt-0.5 h-5 w-5 shrink-0 text-green-600 dark:text-green-400" />
                                <XCircle v-else-if="record.status === 'failed' || record.answerJudgment === 'incorrect'" class="mt-0.5 h-5 w-5 shrink-0 text-red-600 dark:text-red-400" />
                                <AlertTriangle v-else-if="record.answerJudgment === 'unreviewed'" class="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground" />
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

                            <div class="mt-3 space-y-3 border-t border-border/40 pt-3">
                              <div>
                                <p class="text-[11px] font-medium text-muted-foreground">{{ t(`${prefix}.questionAnswer.questionLabel`) }}</p>
                                <p class="mt-1 whitespace-pre-wrap break-words text-xs leading-5 text-foreground" :class="qaReviewBatch.active || qaShowAll ? (qaCurrentExpanded.has(record.id) ? '' : 'line-clamp-2') : ''">{{ record.questionBody }}</p>
                              </div>
                              <div>
                                <p class="text-[11px] font-medium text-muted-foreground">{{ t(`${prefix}.questionAnswer.answerLabel`) }}</p>
                                <QuestionAnswerHighlightedText
                                  v-if="record.status === 'succeeded' && record.answerJudgment === 'unreviewed' && record.questionKeywordSnapshot !== null && record.questionKeywordSnapshot.length > 0"
                                  class="mt-1"
                                  :class="qaReviewBatch.active || qaShowAll ? (qaCurrentExpanded.has(record.id) ? '' : 'line-clamp-4') : ''"
                                  :answer="questionAnswerCurrentAnswer(record)"
                                  :snapshot="record.questionKeywordSnapshot"
                                />
                                <p v-else class="mt-1 whitespace-pre-wrap break-words text-sm leading-6 text-foreground" :class="qaReviewBatch.active || qaShowAll ? (qaCurrentExpanded.has(record.id) ? '' : 'line-clamp-4') : ''">{{ questionAnswerCurrentAnswer(record) }}</p>
                                <p v-if="record.status === 'succeeded' && record.questionKeywordSnapshot === null" class="mt-1 text-[11px] text-muted-foreground">{{ t(`${prefix}.questionAnswer.noKeywordSnapshot`) }}</p>
                              </div>
                            </div>

                            <button v-if="qaReviewBatch.active || qaShowAll" type="button" class="mt-3 inline-flex items-center justify-end gap-1 text-xs font-medium text-muted-foreground hover:text-foreground" @click="toggleCurrentQuestionAnswerExpanded(record.id)">
                              {{ qaCurrentExpanded.has(record.id) ? t(`${prefix}.questionAnswer.collapseCurrent`) : t(`${prefix}.questionAnswer.expandCurrent`) }}
                              <ChevronUp v-if="qaCurrentExpanded.has(record.id)" class="h-3.5 w-3.5" />
                              <ChevronDown v-else class="h-3.5 w-3.5" />
                            </button>
                          </div>

                          <button
                            v-if="record.status === 'succeeded'"
                            type="button"
                            class="inline-flex min-h-14 items-center justify-center gap-2 rounded-md border border-red-500/40 px-4 py-3 text-sm font-medium text-red-700 disabled:opacity-50 dark:text-red-400 md:col-start-2 md:row-start-2"
                            :class="qaMarking.has(record.id)
                              ? (qaMarking.get(record.id) === 'incorrect' ? 'bg-red-500/20' : 'hover:bg-red-500/10')
                              : (record.answerJudgment === 'incorrect' ? 'bg-red-500/20' : 'hover:bg-red-500/10')"
                            :aria-pressed="record.answerJudgment === 'incorrect'"
                            :disabled="qaMarking.has(record.id)"
                            @click="saveQuestionAnswerJudgment(record, 'incorrect')"
                          >
                            <Loader2 v-if="qaMarking.get(record.id) === 'incorrect'" class="h-4 w-4 animate-spin" />
                            {{ t(`${prefix}.questionAnswer.incorrect`) }}<span v-if="qaMarking.get(record.id) === 'incorrect'"> · {{ t(`${prefix}.questionAnswer.saving`) }}</span>
                          </button>
                        </li>
                      </ul>
                    </div>
                  </section>

                  <section class="mt-5 border-t border-border/40 pt-4">
                    <h4 class="mb-3 text-xs font-semibold text-foreground">{{ t(`${prefix}.questionAnswer.historyTitle`) }}</h4>
                    <div v-if="qaHistoryBatchGroups.length === 0" class="rounded-lg border border-dashed border-border/50 px-3 py-5 text-center text-xs text-muted-foreground">{{ t(`${prefix}.questionAnswer.noHistory`) }}</div>
                    <div v-else class="space-y-3">
                      <div v-for="group in qaHistoryBatchGroups" :key="group.batchId" class="rounded-lg border border-border/50 bg-surface-line/10 p-3">
                        <div class="flex flex-wrap items-center justify-between gap-3">
                          <p class="text-xs font-medium text-foreground">{{ t(`${prefix}.questionAnswer.reviewBatchId`, { id: shortQuestionAnswerBatchId(group.batchId) }) }}</p>
                          <button type="button" class="inline-flex items-center gap-1.5 rounded-md border border-border/60 px-2.5 py-1.5 text-xs font-medium text-foreground hover:bg-surface-line disabled:opacity-50" :disabled="qaReviewLoadingBatchId === group.batchId" @click="reviewQuestionAnswerBatch(group.batchId)">
                            <Loader2 v-if="qaReviewLoadingBatchId === group.batchId" class="h-3.5 w-3.5 animate-spin" />
                            {{ t(`${prefix}.questionAnswer.reviewThisBatch`) }}
                          </button>
                        </div>
                        <ul v-if="qaHistoryRowsVisible" class="mt-3 space-y-2 border-t border-border/40 pt-3">
                          <li v-for="record in group.records" :key="record.id" class="rounded-lg border px-3 py-2.5" :class="questionAnswerRecordClass(record)">
                            <div class="flex items-start justify-between gap-3">
                              <button type="button" class="min-w-0 flex-1 text-left" @click="toggleQuestionAnswerExpanded(record.id)">
                                <div class="flex min-w-0 items-center gap-2">
                                  <span class="truncate text-sm font-medium text-foreground">{{ record.questionName }}</span>
                                </div>
                                <div class="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                                  <span>{{ record.modelName }}</span>
                                  <span>{{ t(`${prefix}.questionAnswer.reasoningEffort.label`) }}: {{ questionAnswerReasoningEffortLabel(record.reasoningEffort) }}</span>
                                  <span>{{ formatConnectionHealthTime(record.createdAt) }}</span>
                                  <span>{{ questionAnswerStatusLabel(record) }}</span>
                                  <span v-if="questionAnswerElapsedLabel(record)">{{ questionAnswerElapsedLabel(record) }}</span>
                                </div>
                                <p v-if="!qaExpanded.has(record.id)" class="mt-2 truncate text-xs text-muted-foreground">{{ answerSummary(record.answerBody || (record.errorType ? questionAnswerErrorLabel(record.errorType) : t(`${prefix}.questionAnswer.noAnswer`))) }}</p>
                              </button>
                              <div class="flex shrink-0 items-center gap-3">
                                <div v-if="record.status === 'succeeded'" class="grid gap-6">
                                  <button
                                    type="button"
                                    class="inline-flex min-h-14 items-center justify-center gap-2 rounded-md border border-green-500/40 px-3 py-2 text-xs text-green-700 disabled:opacity-50 dark:text-green-400"
                                    :class="qaMarking.has(record.id)
                                      ? (qaMarking.get(record.id) === 'correct' ? 'bg-green-500/20' : 'hover:bg-green-500/10')
                                      : (record.answerJudgment === 'correct' ? 'bg-green-500/20' : 'hover:bg-green-500/10')"
                                    :aria-pressed="record.answerJudgment === 'correct'"
                                    :disabled="qaMarking.has(record.id)"
                                    @click="saveQuestionAnswerJudgment(record, 'correct')"
                                  >
                                    <Loader2 v-if="qaMarking.get(record.id) === 'correct'" class="h-4 w-4 animate-spin" />
                                    {{ t(`${prefix}.questionAnswer.correct`) }}<span v-if="qaMarking.get(record.id) === 'correct'"> · {{ t(`${prefix}.questionAnswer.saving`) }}</span>
                                  </button>
                                  <button
                                    type="button"
                                    class="inline-flex min-h-14 items-center justify-center gap-2 rounded-md border border-red-500/40 px-3 py-2 text-xs text-red-700 disabled:opacity-50 dark:text-red-400"
                                    :class="qaMarking.has(record.id)
                                      ? (qaMarking.get(record.id) === 'incorrect' ? 'bg-red-500/20' : 'hover:bg-red-500/10')
                                      : (record.answerJudgment === 'incorrect' ? 'bg-red-500/20' : 'hover:bg-red-500/10')"
                                    :aria-pressed="record.answerJudgment === 'incorrect'"
                                    :disabled="qaMarking.has(record.id)"
                                    @click="saveQuestionAnswerJudgment(record, 'incorrect')"
                                  >
                                    <Loader2 v-if="qaMarking.get(record.id) === 'incorrect'" class="h-4 w-4 animate-spin" />
                                    {{ t(`${prefix}.questionAnswer.incorrect`) }}<span v-if="qaMarking.get(record.id) === 'incorrect'"> · {{ t(`${prefix}.questionAnswer.saving`) }}</span>
                                  </button>
                                </div>
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
                      </div>
                    </div>
                    <div v-if="qaHistory.totalPages > 1" class="mt-4 flex flex-wrap items-center justify-center gap-1.5">
                      <template v-for="(page, index) in qaPageNumbers" :key="page">
                        <span v-if="index > 0 && page - qaPageNumbers[index - 1] > 1" class="px-1 text-xs text-muted-foreground">…</span>
                        <button type="button" class="h-8 min-w-8 rounded-md border px-2 text-xs" :class="page === qaHistory.page ? 'border-primary bg-primary text-primary-foreground' : 'border-border/50 text-muted-foreground hover:bg-surface-elevated'" @click="goQuestionAnswerPage(page)">{{ page }}</button>
                      </template>
                    </div>
                  </section>
                </template>

                <template v-else>
                  <p class="mb-3 text-xs text-muted-foreground">
                    {{ t(`${prefix}.${mode === 'formal' ? 'formalSelectHint' : 'selectHint'}`) }}
                  </p>
                  <div class="grid grid-cols-1 gap-2.5 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
                    <label v-for="model in models" :key="model.id" class="flex cursor-pointer items-start gap-2 rounded-lg border border-border/40 px-3 py-2.5 transition-colors" :class="selected.has(model.id) ? 'border-primary/50 bg-primary/5' : 'hover:bg-surface-line/40'">
                      <input type="checkbox" class="mt-0.5 h-4 w-4 shrink-0 rounded border-border/60" :disabled="phase === 'testing'" :checked="selected.has(model.id)" @change="toggle(model.id)" />
                      <div class="min-w-0 flex-1">
                        <p class="truncate text-sm font-medium text-foreground">{{ model.name }}</p>
                        <p v-if="model.ownedBy" class="truncate text-xs text-muted-foreground">{{ model.ownedBy }}</p>
                      </div>
                    </label>
                  </div>
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
                ? t(`${prefix}.questionAnswer.selectedFormula`, { models: qaSubmission.modelCount, questions: qaSubmission.questionCount, repeat: qaSubmission.repeatCount, total: qaSubmission.total })
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
