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
  connectionHealthProbeResultLabelKey,
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
  AccountTier,
  AccountTierResult,
  ManualProbeModelOption,
  ManualProbeResult,
  ModelHealth,
  QuestionAnswerBatch,
  QuestionAnswerFinalization,
  EffectiveTestConfiguration,
  QuestionAnswerJudgment,
  QuestionAnswerReasoningEffort,
  QuestionAnswerHistory,
  QuestionAnswerHistoryScope,
  QuestionAnswerStats,
  QuestionAnswerRecord,
  TestQuestion,
} from '../../types/connectionHealth'
import {
  groupQuestionAnswerResults,
  formatQuestionAnswerAccuracy,
  questionAnswerAccuracy,
  questionAnswerRecordMatchesQuestion,
  questionAnswerRequestProtocolLabel,
  isCurrentQuestionAnswerOperation,
  partitionQuestionAnswerReviewRecords,
  questionAnswerBatchCompletedAt,
  questionAnswerElapsedMilliseconds,
  questionAnswerReviewStatsFromRecords,
  questionAnswerStatsReconcile,
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
import QuestionAnswerRecordCard from './QuestionAnswerRecordCard.vue'
import QuestionAnswerStatsBar from './QuestionAnswerStatsBar.vue'
import QuestionAnswerModelControlPanel from './QuestionAnswerModelControlPanel.vue'
import AccountTierEditor from './AccountTierEditor.vue'
import { t, te } from '@/locales'

export interface ManualProbeTargetSummary {
  targetId: string
  accountTier?: AccountTier
  accountName: string
  platform: string
  type: string
  status: string
  groupName: string
  formalModels: ManualProbeModelOption[]
  testConfiguration?: EffectiveTestConfiguration
}

const props = withDefaults(defineProps<{
  open: boolean
  target: ManualProbeTargetSummary | null
  questionAnswerPreferences?: QuestionAnswerSelectionPreferences
  initialQuestionAnswerBatchId?: string | null
  summaryRefreshFailed?: boolean
}>(), {
  questionAnswerPreferences: () => createDefaultQuestionAnswerPreferences(),
})

const emit = defineEmits<{
  (event: 'tier-saved', result: AccountTierResult): void
  (event: 'close'): void
  (event: 'completed'): void
  (event: 'question-answer-started', targetId: string): void
  (event: 'question-answer-viewed', targetId: string): void
  (event: 'question-answer-stats-dirty', targetId: string): void
  (event: 'question-answer-stats-retry', targetId: string): void
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
const qaFinalization = ref<QuestionAnswerFinalization | null>(null)
const qaFinalizationUnknown = ref(false)
const qaRuntimeBatch = ref<QuestionAnswerBatch | null>(null)
const qaModelControlRefreshKey = ref(0)
watch(() => [qaRuntimeBatch.value?.batchId, qaRuntimeBatch.value?.active] as const, ([batchId, active], previous) => {
  if (batchId && (batchId !== previous?.[0] || active !== previous?.[1])) qaModelControlRefreshKey.value++
})
const onModelControlSettled = (targetId: string) => { qaModelControlRefreshKey.value++; emit('question-answer-stats-dirty', targetId) }
const qaReviewBatch = ref<QuestionAnswerBatch | null>(null)
const qaReviewBatchSyncFailed = ref(false)
const qaReviewLoadingBatchId = ref<string | null>(null)
const emptyQuestionAnswerStats = (): QuestionAnswerStats => ({ requests: { submitted: 0, inProgress: 0, succeeded: 0, failed: 0, cancelled: 0 }, reviews: { unreviewed: 0, correct: 0, incorrect: 0 }, byModel: [], byQuestion: [] })
const emptyQuestionAnswerHistory = (): QuestionAnswerHistory => ({ batches: [], page: 1, pageSize: 20, totalBatches: 0, totalPages: 0, todayStats: emptyQuestionAnswerStats() })
const qaHistory = ref<QuestionAnswerHistory>(emptyQuestionAnswerHistory())
const qaHistoryScope = ref<QuestionAnswerHistoryScope>('today')
const qaResultGroupsOpen = ref<Set<string>>(new Set())
const qaPendingOpen = ref(true)
const qaMarking = ref<Map<string, QuestionAnswerJudgment>>(new Map())
const qaExpanded = ref<Set<string>>(new Set())
const qaProcessedOpen = ref(true)
const qaFailedOpen = ref(false)
const qaConfigOpen = ref(true)
const qaHistoryOpen = ref(false)
const qaHistoryLoaded = ref(false)
const qaErrorKey = ref('')
type QuestionAnswerLocalReadFailure = {
  intent: number
  batchId: string | null
  selectBatch: boolean
  historyPage: number | null
  historyScope: QuestionAnswerHistoryScope
  errorKey: string
}
const qaLocalReadFailure = ref<QuestionAnswerLocalReadFailure | null>(null)
let qaLocalFailureIntent = 0
const qaLocalRetrying = ref(false)
let qaLocalRetryController: AbortController | null = null
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
let qaConfigVisibilityInitialized = false

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
  qaFinalization.value = null
  qaFinalizationUnknown.value = false
  qaRuntimeBatch.value = null
  qaReviewBatch.value = null
  qaReviewBatchSyncFailed.value = false
  qaHistoryIntentPage = 1
  qaHistory.value = emptyQuestionAnswerHistory()
  qaHistoryScope.value = 'today'
  qaResultGroupsOpen.value = new Set()
  qaPendingOpen.value = true
  qaProcessedOpen.value = true
  qaFailedOpen.value = false
  qaExpanded.value = new Set()
  qaConfigOpen.value = true
  qaHistoryOpen.value = false
  qaHistoryLoaded.value = false
  qaLocalReadFailure.value = null
  qaConfigVisibilityInitialized = false
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
  qaReviewBatchSyncFailed.value = false
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
  qaLocalRetryController?.abort()
  qaLocalRetryController = null
  qaLocalRetrying.value = false
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
  () => [props.open, props.target?.targetId, props.initialQuestionAnswerBatchId],
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

    // Configuration blocks new tests, but persisted history and recovery remain readable.
    if (props.target.testConfiguration?.status === 'conflict' || props.target.testConfiguration?.status === 'unavailable') {
      finishModelDiscovery(controller)
      onceLoadState.value = 'ready'
      phase.value = 'ready'
      return
    }

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
    initializeQuestionAnswerConfigurationVisibility()
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

watch(
  () => Boolean(qaReviewBatch.value?.active),
  (active) => {
    if (active) startQuestionAnswerClock()
    else if (!qaRuntimeBatch.value?.active) clearQuestionAnswerClock()
  },
)

const hasModels = computed(() => models.value.length > 0)
const qaActive = computed(() => Boolean(qaRuntimeBatch.value?.active))
const qaSelectionLocked = computed(() => qaStarting.value || qaActive.value || Boolean(qaFinalization.value) || qaFinalizationUnknown.value)
const requestProtocolLabel = (protocol?: string | null) => questionAnswerRequestProtocolLabel(protocol, t('admin.connectionHealth.testConfiguration.legacy'))
const qaSubmission = computed(() => questionAnswerSubmissionSummary(
  selected.value.size,
  qaSelectedQuestions.value.size,
  qaRepeatCount.value,
))
const canStartTest = computed(() => {
  if (props.target?.testConfiguration?.status === 'conflict' || props.target?.testConfiguration?.status === 'unavailable') return false
  if (!hasModels.value || selected.value.size === 0 || phase.value === 'testing') return false
  if (mode.value !== 'questionAnswer') return true
  return qaSelectedQuestions.value.size > 0
    && qaSubmission.value.validRepeatCount
    && qaSubmission.value.withinBatchLimit
    && !qaSelectionLocked.value
    && !qaLoading.value
})
const qaStartBlockedReason = computed(() => {
  if (mode.value !== 'questionAnswer') return ''
  if (qaLoading.value) return t(`${prefix}.questionAnswer.loading`)
  if (!hasModels.value || selected.value.size === 0 || qaSelectedQuestions.value.size === 0) {
    return t(`${prefix}.questionAnswer.selectionRequired`)
  }
  if (!qaSubmission.value.validRepeatCount) return t(`${prefix}.questionAnswer.repeatCountInvalid`)
  if (!qaSubmission.value.withinBatchLimit) {
    return t(`${prefix}.questionAnswer.batchLimit`, { total: qaSubmission.value.total })
  }
  if (qaStarting.value) return t(`${prefix}.questionAnswer.submitting`)
  if (qaFinalizationUnknown.value) return t(`${prefix}.questionAnswer.finalizationUnknown`)
  if (qaFinalization.value) return t(`${prefix}.questionAnswer.finalizationBlocked`)
  if (qaActive.value) return t(`${prefix}.questionAnswer.activeStartBlocked`)
  return ''
})
const qaReviewPartition = computed(() => partitionQuestionAnswerReviewRecords(qaReviewBatch.value?.records ?? []))
const qaPendingReviewRecords = computed(() => qaReviewPartition.value.pendingReview)
const qaReviewedRecords = computed(() => qaReviewPartition.value.reviewed)
const qaFailedRecords = computed(() => qaReviewPartition.value.failed)
const qaReviewedCorrectCount = computed(() => qaReviewedRecords.value.filter(record => record.answerJudgment === 'correct').length)
const qaReviewedIncorrectCount = computed(() => qaReviewedRecords.value.filter(record => record.answerJudgment === 'incorrect').length)
const qaProcessedSectionVisible = computed(() => {
  const batch = qaReviewBatch.value
  if (!batch) return false
  return batch.active
    || qaReviewBatchSyncFailed.value
    || qaReviewedRecords.value.length > 0
    || qaFailedRecords.value.length > 0
    || (!batch.active && batch.records.length > 0)
})
const qaReviewCompletedAt = computed(() => (
  qaReviewBatch.value ? questionAnswerBatchCompletedAt(qaReviewBatch.value) : null
))
const qaReviewBatchStartedAtMilliseconds = computed(() => {
  const batch = qaReviewBatch.value
  if (!batch || batch.records.length === 0) return null
  const timestamps = batch.records
    .flatMap(record => [record.createdAt, record.startedAt])
    .map(value => value ? Date.parse(value) : Number.NaN)
    .filter(Number.isFinite)
  return timestamps.length > 0 ? Math.min(...timestamps) : null
})
const qaReviewBatchElapsedMilliseconds = computed(() => {
  const batch = qaReviewBatch.value
  const startedAt = qaReviewBatchStartedAtMilliseconds.value
  if (!batch || startedAt === null) return null
  const completedAt = batch.active
    ? qaClockNow.value
    : qaReviewCompletedAt.value
      ? Date.parse(qaReviewCompletedAt.value)
      : Number.NaN
  if (!Number.isFinite(completedAt)) return null
  return Math.max(0, completedAt - startedAt)
})
const qaReviewBatchDurationLabel = computed(() => {
  const elapsedMs = qaReviewBatchElapsedMilliseconds.value
  if (elapsedMs === null) return t(`${prefix}.questionAnswer.batchElapsedUnknown`)
  const totalSeconds = Math.floor(elapsedMs / 1000)
  if (totalSeconds < 60) return t(`${prefix}.questionAnswer.durationSeconds`, { seconds: totalSeconds })
  return t(`${prefix}.questionAnswer.durationMinutesSeconds`, {
    minutes: Math.floor(totalSeconds / 60),
    seconds: String(totalSeconds % 60).padStart(2, '0'),
  })
})
const qaReviewBatchRequestStatsReliable = computed(() => {
  const batch = qaReviewBatch.value
  if (!batch) return false
  const counts = batch.records.reduce((result, record) => {
    if (record.status === 'pending' || record.status === 'running') {
      result.inProgress++
      if (record.status === 'running') result.running++
    }
    else if (record.status === 'succeeded') result.succeeded++
    else if (record.status === 'failed') result.failed++
    else if (record.status === 'cancelled') result.cancelled++
    else result.unknown++
    return result
  }, { inProgress: 0, running: 0, succeeded: 0, failed: 0, cancelled: 0, unknown: 0 })
  const requests = batch.stats.requests
  const recordReviews = questionAnswerReviewStatsFromRecords(batch.records)
  const reviews = batch.stats.reviews
  return counts.unknown === 0
    && requests.submitted === batch.submittedCount
    && requests.submitted === batch.records.length
    && requests.inProgress === counts.inProgress
    && requests.succeeded === counts.succeeded
    && requests.failed === counts.failed
    && requests.cancelled === counts.cancelled
    && batch.completedCount === counts.succeeded + counts.failed + counts.cancelled
    && batch.runningCount === counts.running
    && questionAnswerStatsReconcile(batch.stats)
    && reviews.unreviewed === recordReviews.unreviewed
    && reviews.correct === recordReviews.correct
    && reviews.incorrect === recordReviews.incorrect
})
type QuestionAnswerBatchDisplayStatus = 'active' | 'slow' | 'completed' | 'terminated' | 'unknown' | 'syncFailed'
const qaReviewBatchDisplayStatus = computed<QuestionAnswerBatchDisplayStatus>(() => {
  const batch = qaReviewBatch.value
  if (!batch) return 'unknown'
  if (qaReviewBatchSyncFailed.value) return 'syncFailed'
  const hasUnfinishedRecord = batch.records.some(record => record.status === 'pending' || record.status === 'running')
  if (batch.active) {
    return qaReviewBatchElapsedMilliseconds.value !== null
      && qaReviewBatchElapsedMilliseconds.value > 3 * 60 * 1000
      ? 'slow'
      : 'active'
  }
  if (batch.stats.requests.inProgress > 0 || hasUnfinishedRecord) return 'unknown'
  if (batch.records.length === 0 && batch.submittedCount > 0) return 'unknown'
  if (!qaReviewBatchRequestStatsReliable.value) return 'unknown'
  if (batch.stats.requests.cancelled > 0 || batch.records.some(record => record.status === 'cancelled')) return 'terminated'
  return 'completed'
})
const qaReviewBatchStatusLabel = computed(() => {
  const batch = qaReviewBatch.value
  if (!batch) return ''
  const total = batch.submittedCount
  const duration = qaReviewBatchDurationLabel.value
  if (qaReviewBatchDisplayStatus.value === 'syncFailed') {
    return t(`${prefix}.questionAnswer.batchSyncFailedSummary`, { total, duration })
  }
  if (qaReviewBatchDisplayStatus.value === 'slow') {
    return t(`${prefix}.questionAnswer.batchSlowSummary`, {
      total,
      inProgress: batch.stats.requests.inProgress,
      duration,
    })
  }
  if (qaReviewBatchDisplayStatus.value === 'active') {
    return t(`${prefix}.questionAnswer.batchActiveSummary`, {
      total,
      inProgress: batch.stats.requests.inProgress,
      duration,
    })
  }
  if (qaReviewBatchDisplayStatus.value === 'terminated') {
    return t(`${prefix}.questionAnswer.batchTerminatedSummary`, { total, duration })
  }
  if (qaReviewBatchDisplayStatus.value === 'completed') {
    return qaPendingReviewRecords.value.length > 0
      ? t(`${prefix}.questionAnswer.batchCompletedPendingSummary`, {
        total,
        pending: qaPendingReviewRecords.value.length,
        duration,
      })
      : t(`${prefix}.questionAnswer.batchCompletedSummary`, {
        total,
        reviewed: qaReviewedRecords.value.length,
        duration,
      })
  }
  return t(`${prefix}.questionAnswer.batchUnknownSummary`, { total, duration })
})
const qaReviewBatchStatusClass = computed(() => {
  if (qaReviewBatchDisplayStatus.value === 'terminated' || qaReviewBatchDisplayStatus.value === 'syncFailed') {
    return 'text-red-600 dark:text-red-400'
  }
  if (qaReviewBatchDisplayStatus.value === 'slow') return 'text-amber-600 dark:text-amber-400'
  if (qaReviewBatchDisplayStatus.value === 'completed') return 'text-green-600 dark:text-green-400'
  return 'text-primary'
})
const qaHistoryBatchGroups = computed(() => qaHistory.value.batches)
const qaResultGroups = computed(() => groupQuestionAnswerResults(qaReviewedRecords.value, qaReviewBatch.value?.stats.byQuestion ?? []))
const toggleQuestionAnswerResultGroup = (key: string) => {
  const groups = new Set(qaResultGroupsOpen.value)
  if (groups.has(key)) groups.delete(key); else groups.add(key)
  qaResultGroupsOpen.value = groups
}
const qaSavedPreferenceValid = computed(() => {
  const modelIDs = new Set(models.value.map(model => model.id))
  const questionIDs = new Set(qaQuestions.value.map(question => question.id))
  const compatibleModels = Array.from(new Set(props.questionAnswerPreferences.modelIds)).filter(id => modelIDs.has(id))
  const compatibleQuestions = Array.from(new Set(props.questionAnswerPreferences.questionIds)).filter(id => questionIDs.has(id))
  const repeatCount = props.questionAnswerPreferences.repeatCount
  return compatibleModels.length > 0
    && compatibleQuestions.length > 0
    && ['low', 'medium', 'high', 'xhigh'].includes(props.questionAnswerPreferences.reasoningEffort)
    && Number.isInteger(repeatCount)
    && repeatCount >= 1
    && repeatCount <= 10
    && compatibleModels.length * compatibleQuestions.length * repeatCount <= 50
})
const initializeQuestionAnswerConfigurationVisibility = () => {
  if (qaConfigVisibilityInitialized || !qaSelectionDataReady) return
  if (qaRuntimeBatch.value?.active) {
    qaConfigOpen.value = false
    qaConfigVisibilityInitialized = true
    return
  }
  if (onceLoadState.value !== 'ready') return
  qaConfigOpen.value = !qaSavedPreferenceValid.value
  qaConfigVisibilityInitialized = true
}
const qaSummaryModelNames = computed(() => {
  if (qaRuntimeBatch.value?.active) {
    const namesByID = new Map(models.value.map(model => [model.id, model.name]))
    return Array.from(new Set(qaRuntimeBatch.value.records.map(record => namesByID.get(record.modelName) ?? record.modelName)))
  }
  return models.value.filter(model => selected.value.has(model.id)).map(model => model.name)
})
const qaSummaryQuestionCount = computed(() => qaRuntimeBatch.value?.active
  ? new Set(qaRuntimeBatch.value.records.map(record => record.questionId)).size
  : qaSelectedQuestions.value.size)
const qaSummaryReasoningEffort = computed(() => qaRuntimeBatch.value?.active
  ? qaRuntimeBatch.value.reasoningEffort
  : qaReasoningEffort.value)
const qaSummaryRepeatCount = computed(() => qaRuntimeBatch.value?.active
  ? qaRuntimeBatch.value.repeatCount
  : qaRepeatCount.value)
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
  initializeQuestionAnswerConfigurationVisibility()
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
  const judgmentRevision = qaJudgmentRefreshSequence
  const cancelSequence = qaCancelSequence
  qaErrorKey.value = ''
  try {
    const [questions, history, batch, initialBatch] = await Promise.all([
      listTestQuestions(controller.signal),
      getQuestionAnswerHistory(targetId, historyPage, qaHistoryScope.value, controller.signal),
      getLatestQuestionAnswerBatch(targetId, controller.signal),
      props.initialQuestionAnswerBatchId ? getQuestionAnswerBatch(targetId, props.initialQuestionAnswerBatchId, controller.signal) : Promise.resolve(null),
    ])
    if (
      sequence !== loadSequence
      || !questionAnswerHistoryIntentIsCurrent(historySequence)
      || judgmentRevision !== qaJudgmentRefreshSequence
      || cancelSequence !== qaCancelSequence
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
    qaLocalReadFailure.value = null
    qaHistoryLoaded.value = true
    qaHistoryIntentPage = history.page
    clearQuestionAnswerLocalReadFailure(undefined, history.page)
    qaFinalization.value = batch.finalization ?? null
    qaFinalizationUnknown.value = false
    qaRuntimeBatch.value = batch.batchId ? protectQuestionAnswerTerminalBatch(batch) : null
    qaReviewBatch.value = initialBatch ? protectQuestionAnswerTerminalBatch(initialBatch) : qaRuntimeBatch.value
    qaReviewBatchSyncFailed.value = false
    qaProcessedOpen.value = true
    qaFailedOpen.value = false
    qaSelectionDataReady = true
    if (!restoreActiveQuestionAnswerSelection(qaRuntimeBatch.value)) restoreSavedQuestionAnswerSelection()
    initializeQuestionAnswerConfigurationVisibility()
    if (batch.active) scheduleQuestionAnswerPoll()
    else clearQuestionAnswerClock()
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    if (
      sequence === loadSequence
      && questionAnswerHistoryIntentIsCurrent(historySequence)
      && judgmentRevision === qaJudgmentRefreshSequence
      && cancelSequence === qaCancelSequence
      && mode.value === 'questionAnswer'
      && props.target?.targetId === targetId
    ) {
      qaErrorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
      qaReviewBatchSyncFailed.value = true
    }
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
    batchId: qaFinalization.value?.batchId ?? qaRuntimeBatch.value?.batchId ?? null,
  })
)

const questionAnswerPollIsCurrent = (
  pollSequence: number,
  scope: QuestionAnswerOperationScope,
): boolean => pollSequence === qaPollSequence && questionAnswerScopeIsCurrent(scope)

type QuestionAnswerBatchReadState = { batchId: string; judgmentRevision: number; cancelSequence: number | null }
const captureQuestionAnswerBatchRead = (batchId: string): QuestionAnswerBatchReadState => ({
  batchId,
  judgmentRevision: qaJudgmentRefreshSequences.get(batchId) ?? 0,
  cancelSequence: qaRuntimeBatch.value?.batchId === batchId ? qaCancelSequence : null,
})
const questionAnswerBatchReadIsCurrent = (read: QuestionAnswerBatchReadState, allowCachedTerminal = false): boolean => (
  read.judgmentRevision === (qaJudgmentRefreshSequences.get(read.batchId) ?? 0)
  && (read.cancelSequence === null || read.cancelSequence === qaCancelSequence || (
    allowCachedTerminal && qaRuntimeBatch.value?.batchId === read.batchId && !qaRuntimeBatch.value.active
  ))
)
const protectQuestionAnswerTerminalBatch = (batch: QuestionAnswerBatch): QuestionAnswerBatch => {
  const terminal = [qaRuntimeBatch.value, qaReviewBatch.value].find(previous => previous?.batchId === batch.batchId && !previous.active)
  return batch.active && terminal ? terminal : batch
}
const beginQuestionAnswerRuntimeRead = (batchId: string): number | null => (
  qaRuntimeBatch.value?.batchId === batchId
    ? ++qaRuntimeSnapshotSequence
    : null
)
const questionAnswerExactReadIsCurrent = (runtimeReadSequence: number | null, reviewReadSequence: number): boolean => (
  runtimeReadSequence !== null ? runtimeReadSequence === qaRuntimeSnapshotSequence : reviewReadSequence === qaReviewSequence
)
const resolveQuestionAnswerRuntimeSnapshot = (
  received: QuestionAnswerBatch,
  runtimeReadSequence: number | null,
  reviewReadSequence: number,
): QuestionAnswerBatch => {
  const protectedBatch = protectQuestionAnswerTerminalBatch(received)
  const runtimeBatch = qaRuntimeBatch.value
  // Terminal beats active; a superseded read cannot replace a newer active or terminal snapshot.
  return runtimeReadSequence !== null
    && !questionAnswerExactReadIsCurrent(runtimeReadSequence, reviewReadSequence)
    && runtimeBatch?.batchId === protectedBatch.batchId
    && (protectedBatch.active || !runtimeBatch.active)
    ? runtimeBatch
    : protectedBatch
}
const applyQuestionAnswerReadBatch = (
  received: QuestionAnswerBatch,
  refreshSequence: number | null,
  selectBatch: boolean,
  runtimeReadSequence: number | null,
  reviewReadSequence: number,
) => {
  const batch = resolveQuestionAnswerRuntimeSnapshot(received, runtimeReadSequence, reviewReadSequence)
  const reviewReadIsCurrent = questionAnswerExactReadIsCurrent(null, reviewReadSequence)
  if ((selectBatch && reviewReadIsCurrent) || (
    refreshSequence !== null
    && qaReviewLoadingBatchId.value === batch.batchId
    && qaReviewJudgmentRefreshSequence < refreshSequence
  )) {
    if (!selectBatch) cancelQuestionAnswerReview()
    qaReviewBatch.value = batch
    qaReviewBatchSyncFailed.value = false
  } else if (reviewReadIsCurrent && qaReviewBatch.value?.batchId === batch.batchId) {
    qaReviewBatch.value = batch
    qaReviewBatchSyncFailed.value = false
  }
  if (qaRuntimeBatch.value?.batchId === batch.batchId) {
    qaRuntimeBatch.value = batch
    qaFinalization.value = batch.finalization ?? null
    qaFinalizationUnknown.value = false
    if (!batch.active) clearQuestionAnswerClock()
  }
}
const setQuestionAnswerLocalReadFailure = (failure: Omit<QuestionAnswerLocalReadFailure, 'intent'>) => {
  qaLocalRetryController?.abort()
  qaLocalRetryController = null
  qaLocalRetrying.value = false
  qaLocalReadFailure.value = { ...failure, intent: ++qaLocalFailureIntent }
  qaErrorKey.value = failure.errorKey
}
const clearQuestionAnswerLocalReadFailure = (batchId?: string, historyPage?: number, abandonSelection = false) => {
  const failure = qaLocalReadFailure.value
  if (!failure) return
  const clearBatch = failure.batchId !== null && batchId === failure.batchId
    && (abandonSelection || !failure.selectBatch || qaReviewBatch.value?.batchId === batchId)
  const clearHistory = failure.historyPage !== null && historyPage !== undefined
  if (!clearBatch && !clearHistory) return
  const remaining = {
    ...failure,
    batchId: clearBatch ? null : failure.batchId,
    historyPage: clearHistory ? null : failure.historyPage,
  }
  if (remaining.batchId !== null || remaining.historyPage !== null) qaLocalReadFailure.value = remaining
  else {
    qaLocalReadFailure.value = null
    qaLocalRetryController?.abort()
    qaLocalRetryController = null
    qaLocalRetrying.value = false
    if (qaErrorKey.value === failure.errorKey) qaErrorKey.value = ''
  }
}
const abandonQuestionAnswerLocalSelectionFailure = () => {
  const failure = qaLocalReadFailure.value
  if (failure?.selectBatch && failure.batchId) clearQuestionAnswerLocalReadFailure(failure.batchId, undefined, true)
}

const applyRuntimeQuestionAnswerBatch = (received: QuestionAnswerBatch, runtimeReadSequence: number | null) => {
  const batch = resolveQuestionAnswerRuntimeSnapshot(received, runtimeReadSequence, qaReviewSequence)
  const previousStats = qaRuntimeBatch.value?.stats
  if (props.target && JSON.stringify(previousStats) !== JSON.stringify(batch.stats)) emit('question-answer-stats-dirty', props.target.targetId)
  qaFinalization.value = batch.finalization ?? null
  qaFinalizationUnknown.value = false
  qaRuntimeBatch.value = batch.batchId ? batch : null
  clearQuestionAnswerLocalReadFailure(batch.batchId)
  if (!qaReviewBatch.value || qaReviewBatch.value.batchId === batch.batchId) {
    qaReviewBatch.value = batch
    qaReviewBatchSyncFailed.value = false
  }
}

const scheduleQuestionAnswerPoll = () => {
  clearQuestionAnswerPolling()
  if (!props.open || mode.value !== 'questionAnswer' || !qaRuntimeBatch.value?.active) return
  startQuestionAnswerClock()
  qaPollTimer = setTimeout(() => void pollQuestionAnswerBatch(), 2000)
}
const resumeQuestionAnswerReadPolling = (batchId: string, runtimeReadSequence: number | null, reviewReadSequence: number) => {
  if (
    runtimeReadSequence !== null
    && questionAnswerExactReadIsCurrent(runtimeReadSequence, reviewReadSequence)
    && qaRuntimeBatch.value?.batchId === batchId
    && qaRuntimeBatch.value.active
  ) scheduleQuestionAnswerPoll()
}

const pollQuestionAnswerBatch = async () => {
  if (!props.target || !qaRuntimeBatch.value?.batchId || !props.open || mode.value !== 'questionAnswer') return
  const targetId = props.target.targetId
  const batchId = qaRuntimeBatch.value.batchId
  const sequence = loadSequence
  const scope = { sequence, targetId, batchId }
  const pollSequence = qaPollSequence
  const runtimeSnapshotSequence = ++qaRuntimeSnapshotSequence
  const batchRead = captureQuestionAnswerBatchRead(batchId)
  const controller = new AbortController()
  qaPollController = controller
  let historySequence: number | null = null
  try {
    let batch = await getQuestionAnswerBatch(targetId, batchId, controller.signal)
    if (!questionAnswerPollIsCurrent(pollSequence, scope) || !questionAnswerBatchReadIsCurrent(batchRead)) return
    if (batch.active) {
      if (!qaRuntimeBatch.value?.active) {
        clearQuestionAnswerClock()
        return
      }
      if (runtimeSnapshotSequence !== qaRuntimeSnapshotSequence) {
        scheduleQuestionAnswerPoll()
        return
      }
      applyRuntimeQuestionAnswerBatch(batch, runtimeSnapshotSequence)
      scheduleQuestionAnswerPoll()
      return
    }
    batch = await getQuestionAnswerBatch(targetId, batchId, controller.signal)
    if (!questionAnswerPollIsCurrent(pollSequence, scope) || !questionAnswerBatchReadIsCurrent(batchRead)) return
    if (batch.active) {
      if (!qaRuntimeBatch.value?.active) {
        clearQuestionAnswerClock()
        return
      }
      if (runtimeSnapshotSequence === qaRuntimeSnapshotSequence) applyRuntimeQuestionAnswerBatch(batch, runtimeSnapshotSequence)
      if (qaRuntimeBatch.value?.active) scheduleQuestionAnswerPoll()
      else clearQuestionAnswerClock()
      return
    }
    const reviewFollowsRuntime = qaReviewLoadingBatchId.value === null
      && qaReviewBatch.value?.batchId === batch.batchId
    applyRuntimeQuestionAnswerBatch(batch, runtimeSnapshotSequence)
    if (reviewFollowsRuntime) {
      qaProcessedOpen.value = true
      qaFailedOpen.value = false
    }
    const historyPage = reviewFollowsRuntime ? 1 : qaHistoryIntentPage
    historySequence = beginQuestionAnswerHistoryIntent(historyPage)
    const history = await getQuestionAnswerHistory(targetId, historyPage, qaHistoryScope.value, controller.signal)
    if (!questionAnswerPollIsCurrent(pollSequence, scope) || !questionAnswerBatchReadIsCurrent(batchRead)) return
    if (
      questionAnswerHistoryIntentIsCurrent(historySequence)
      && qaHistoryIntentPage === historyPage
    ) {
      qaHistory.value = history
      qaHistoryIntentPage = history.page
      clearQuestionAnswerLocalReadFailure(undefined, history.page)
    }
    qaCompletedNotice.value = true
    clearQuestionAnswerPolling()
    clearQuestionAnswerClock()
  } catch (error) {
    if (
      (error instanceof Error && error.name === 'AbortError')
      || !questionAnswerPollIsCurrent(pollSequence, scope)
      || !questionAnswerBatchReadIsCurrent(batchRead)
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
    if (historySequence === null && runtimeSnapshotSequence !== qaRuntimeSnapshotSequence) {
      scheduleQuestionAnswerPoll()
      return
    }
    setQuestionAnswerLocalReadFailure({ batchId: historySequence === null ? batchId : null, selectBatch: false, historyPage: qaHistoryIntentPage, historyScope: qaHistoryScope.value, errorKey: error instanceof Error ? error.message : 'admin.connectionHealth.errors.request' })
    if (!qaReviewBatch.value || qaReviewBatch.value.batchId === batchId) {
      qaReviewBatchSyncFailed.value = true
    }
    if (qaRuntimeBatch.value?.active) scheduleQuestionAnswerPoll()
    else clearQuestionAnswerClock()
  } finally {
    if (qaPollController === controller) qaPollController = null
  }
}

const reconcileQuestionAnswerFinalization = async (scope: QuestionAnswerOperationScope, signal: AbortSignal) => {
  const snapshotSequence = ++qaRuntimeSnapshotSequence
  const judgmentRevision = qaJudgmentRefreshSequence
  const cancelSequence = qaCancelSequence
  const startSequence = qaStartSequence
  try {
    const batch = await getLatestQuestionAnswerBatch(scope.targetId, signal)
    if (!questionAnswerScopeIsCurrent(scope) || snapshotSequence !== qaRuntimeSnapshotSequence || judgmentRevision !== qaJudgmentRefreshSequence || cancelSequence !== qaCancelSequence || startSequence !== qaStartSequence) return
    applyRuntimeQuestionAnswerBatch(batch, snapshotSequence)
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    if (!questionAnswerScopeIsCurrent(scope) || snapshotSequence !== qaRuntimeSnapshotSequence || judgmentRevision !== qaJudgmentRefreshSequence || cancelSequence !== qaCancelSequence || startSequence !== qaStartSequence) return
    qaFinalizationUnknown.value = true
  }
}

const startQuestionAnswers = async () => {
  if (!canStartTest.value || !props.target) return
  abandonQuestionAnswerLocalSelectionFailure()
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
    qaFinalization.value = batch.finalization ?? null
    qaFinalizationUnknown.value = false
    qaRuntimeBatch.value = batch
    qaReviewBatch.value = batch
    qaReviewBatchSyncFailed.value = false
    qaConfigOpen.value = false
    qaProcessedOpen.value = true
    qaFailedOpen.value = false
    emit('question-answer-started', targetId)
    emit('question-answer-stats-dirty', targetId)
    const historySequence = beginQuestionAnswerHistoryIntent(1)
    qaHistory.value.page = 1
    scheduleQuestionAnswerPoll()
    try {
      const history = await getQuestionAnswerHistory(targetId, 1, qaHistoryScope.value, controller.signal)
      if (
        startSequence === qaStartSequence
        && questionAnswerHistoryIntentIsCurrent(historySequence)
        && questionAnswerScopeIsCurrent(scope)
      ) {
        qaHistory.value = history
        qaHistoryIntentPage = history.page
        clearQuestionAnswerLocalReadFailure(undefined, history.page)
      }
    } catch (error) {
      if (error instanceof Error && error.name === 'AbortError') return
      if (
        startSequence === qaStartSequence
        && questionAnswerHistoryIntentIsCurrent(historySequence)
        && questionAnswerScopeIsCurrent(scope)
      ) {
        setQuestionAnswerLocalReadFailure({ batchId: null, selectBatch: false, historyPage: 1, historyScope: qaHistoryScope.value, errorKey: error instanceof Error ? error.message : 'admin.connectionHealth.errors.request' })
      }
    }
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    if (startSequence === qaStartSequence && questionAnswerScopeIsCurrent(scope)) {
      qaErrorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
      await reconcileQuestionAnswerFinalization(scope, controller.signal)
    }
  } finally {
    finishRequest(controller)
    if (startSequence === qaStartSequence) qaStarting.value = false
  }
}

const stopQuestionAnswers = async () => {
  const batchId = qaFinalization.value?.recovery === 'cancel' ? qaFinalization.value.batchId : qaRuntimeBatch.value?.batchId
  if (!props.target || !batchId || qaCancelling.value || qaFinalizationUnknown.value) return
  if (qaFinalization.value && qaFinalization.value.recovery !== 'cancel') return
  const targetId = props.target.targetId
  if (!qaFinalization.value && !window.confirm(`终止账号 ${props.target.accountName} 的最新运行批次 ${batchId}？`)) return
  const runtimeAtCancel = qaRuntimeBatch.value
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
    if (qaRuntimeBatch.value !== runtimeAtCancel && !qaRuntimeBatch.value?.active && !qaFinalization.value) return
    const reviewFollowsRuntime = qaReviewLoadingBatchId.value === null
      && qaReviewBatch.value?.batchId === batch.batchId
    applyRuntimeQuestionAnswerBatch(batch, null)
    if (reviewFollowsRuntime) {
      qaProcessedOpen.value = true
      qaFailedOpen.value = false
    }
    clearQuestionAnswerClock()
    const historyPage = reviewFollowsRuntime ? 1 : qaHistoryIntentPage
    historySequence = beginQuestionAnswerHistoryIntent(historyPage)
    const history = await getQuestionAnswerHistory(targetId, historyPage, qaHistoryScope.value, controller.signal)
    if (cancelSequence !== qaCancelSequence || !questionAnswerScopeIsCurrent(scope)) return
    if (
      questionAnswerHistoryIntentIsCurrent(historySequence)
      && qaHistoryIntentPage === historyPage
    ) {
      qaHistory.value = history
      qaHistoryIntentPage = history.page
      clearQuestionAnswerLocalReadFailure(undefined, history.page)
    }
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    if (qaRuntimeBatch.value !== runtimeAtCancel && !qaRuntimeBatch.value?.active && !qaFinalization.value) return
    if (
      cancelSequence === qaCancelSequence
      && questionAnswerScopeIsCurrent(scope)
      && (historySequence === null || questionAnswerHistoryIntentIsCurrent(historySequence))
    ) {
      if (historySequence !== null) setQuestionAnswerLocalReadFailure({ batchId: null, selectBatch: false, historyPage: qaHistoryIntentPage, historyScope: qaHistoryScope.value, errorKey: error instanceof Error ? error.message : 'admin.connectionHealth.errors.request' })
      else qaErrorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
      if (historySequence === null) await reconcileQuestionAnswerFinalization(scope, controller.signal)
      if (qaRuntimeBatch.value?.active && !qaFinalization.value && !qaFinalizationUnknown.value) scheduleQuestionAnswerPoll()
    }
  } finally {
    if (qaCancelController === controller) qaCancelController = null
    if (cancelSequence === qaCancelSequence) qaCancelling.value = false
  }
}

const reviewQuestionAnswerBatch = async (batchId: string) => {
  if (!props.target || qaReviewLoadingBatchId.value === batchId) return
  abandonQuestionAnswerLocalSelectionFailure()
  const targetId = props.target.targetId
  const sequence = loadSequence
  const reviewSequence = cancelQuestionAnswerReview()
  const runtimeSelectionSequence = beginQuestionAnswerRuntimeRead(batchId)
  qaReviewJudgmentRefreshSequence = qaJudgmentRefreshSequences.get(batchId) ?? 0
  const batchRead = captureQuestionAnswerBatchRead(batchId)
  const controller = new AbortController()
  qaReviewController = controller
  qaReviewLoadingBatchId.value = batchId
  qaErrorKey.value = ''
  try {
    const batch = await getQuestionAnswerBatch(targetId, batchId, controller.signal)
    if (
      sequence !== loadSequence
      || reviewSequence !== qaReviewSequence
      || !questionAnswerBatchReadIsCurrent(batchRead, true)
      || mode.value !== 'questionAnswer'
      || props.target?.targetId !== targetId
    ) return
    applyQuestionAnswerReadBatch(batch, null, true, runtimeSelectionSequence, reviewSequence)
    clearQuestionAnswerLocalReadFailure(batchId)
    qaProcessedOpen.value = true
    qaFailedOpen.value = false
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    if (sequence === loadSequence && reviewSequence === qaReviewSequence && questionAnswerBatchReadIsCurrent(batchRead)) {
      setQuestionAnswerLocalReadFailure({ batchId, selectBatch: true, historyPage: null, historyScope: qaHistoryScope.value, errorKey: error instanceof Error ? error.message : 'admin.connectionHealth.errors.request' })
      if (qaReviewBatch.value?.batchId === batchId) {
        qaReviewBatchSyncFailed.value = true
      }
    }
  } finally {
    if (qaReviewController === controller) qaReviewController = null
    if (reviewSequence === qaReviewSequence) {
      qaReviewLoadingBatchId.value = null
      qaReviewJudgmentRefreshSequence = 0
    }
    if (
      reviewSequence === qaReviewSequence
      && sequence === loadSequence
      && questionAnswerBatchReadIsCurrent(batchRead, true)
    ) resumeQuestionAnswerReadPolling(batchId, runtimeSelectionSequence, reviewSequence)
  }
}

const reviewLatestQuestionAnswerBatch = () => {
  if (!qaRuntimeBatch.value) return
  abandonQuestionAnswerLocalSelectionFailure()
  cancelQuestionAnswerReview()
  qaReviewBatch.value = qaRuntimeBatch.value
  qaReviewBatchSyncFailed.value = false
  qaProcessedOpen.value = true
  qaFailedOpen.value = false
}

const goQuestionAnswerPage = async (page: number, force = false) => {
  if (!props.target || page < 1 || (!force && (page > qaHistory.value.totalPages || page === qaHistoryIntentPage))) return
  const targetId = props.target.targetId
  const sequence = loadSequence
  const historyScope = qaHistoryScope.value
  const historySequence = beginQuestionAnswerHistoryIntent(page)
  const controller = new AbortController()
  qaDataController = controller
  qaErrorKey.value = ''
  try {
    const history = await getQuestionAnswerHistory(targetId, page, qaHistoryScope.value, controller.signal)
    if (
      sequence !== loadSequence
      || !questionAnswerHistoryIntentIsCurrent(historySequence)
      || mode.value !== 'questionAnswer'
      || props.target?.targetId !== targetId
    ) return
    qaHistory.value = history
    qaHistoryIntentPage = history.page
    clearQuestionAnswerLocalReadFailure(undefined, history.page)
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    if (
      sequence === loadSequence
      && questionAnswerHistoryIntentIsCurrent(historySequence)
      && mode.value === 'questionAnswer'
      && props.target?.targetId === targetId
    ) {
      qaHistoryIntentPage = qaHistory.value.page
      setQuestionAnswerLocalReadFailure({ batchId: null, selectBatch: false, historyPage: page, historyScope, errorKey: error instanceof Error ? error.message : 'admin.connectionHealth.errors.request' })
    }
  } finally {
    if (qaDataController === controller) qaDataController = null
  }
}

const selectQuestionAnswerHistoryScope = (scope: QuestionAnswerHistoryScope) => {
  if (qaHistoryScope.value === scope) return
  qaHistoryScope.value = scope
  void goQuestionAnswerPage(1, true)
}
const retryQuestionAnswerData = () => {
  if (props.target) void loadQuestionAnswerData(props.target.targetId, loadSequence, qaHistoryIntentPage, true)
}
const retryQuestionAnswerStats = () => {
  if (props.target) emit('question-answer-stats-retry', props.target.targetId)
}

const retryQuestionAnswerLocalData = async () => {
  if (!props.target || !qaLocalReadFailure.value || qaLocalRetrying.value) return
  const failure = qaLocalReadFailure.value
  const scope = { sequence: loadSequence, targetId: props.target.targetId }
  const read = failure.batchId ? captureQuestionAnswerBatchRead(failure.batchId) : null
  const reviewSequence = failure.selectBatch ? cancelQuestionAnswerReview() : qaReviewSequence
  const runtimeReadSequence = failure.batchId ? beginQuestionAnswerRuntimeRead(failure.batchId) : null
  const historyPage = failure.historyPage
  const historySequence = historyPage !== null ? beginQuestionAnswerHistoryIntent(historyPage) : null
  const historyScope = qaHistoryScope.value
  const controller = new AbortController()
  qaLocalRetryController = controller
  qaLocalRetrying.value = true
  const currentFailure = () => {
    if (!questionAnswerScopeIsCurrent(scope)) return null
    const current = qaLocalReadFailure.value
    return current?.intent === failure.intent ? current : null
  }
  try {
    const [batchResult, historyResult] = await Promise.allSettled([
      failure.batchId ? getQuestionAnswerBatch(scope.targetId, failure.batchId, controller.signal) : Promise.resolve(null),
      historyPage !== null ? getQuestionAnswerHistory(scope.targetId, historyPage, historyScope, controller.signal) : Promise.resolve(null),
    ])
    let remaining = currentFailure()
    if (!remaining) return
    // A sibling read may already have restored this part. Never apply its older retry result.
    if (read && remaining.batchId === read.batchId
      && questionAnswerBatchReadIsCurrent(read, failure.selectBatch)
      && (!failure.selectBatch || reviewSequence === qaReviewSequence)
      && batchResult.status === 'fulfilled' && batchResult.value) {
      applyQuestionAnswerReadBatch(batchResult.value, null, failure.selectBatch, runtimeReadSequence, reviewSequence)
      clearQuestionAnswerLocalReadFailure(read.batchId)
    }
    remaining = currentFailure()
    // History has its own intent; a recovered or obsolete batch does not invalidate it.
    if (remaining && remaining.historyPage === historyPage && historySequence !== null
      && questionAnswerHistoryIntentIsCurrent(historySequence)
      && historyScope === qaHistoryScope.value && historyScope === remaining.historyScope
      && historyResult.status === 'fulfilled' && historyResult.value) {
      qaHistory.value = historyResult.value
      qaHistoryIntentPage = historyResult.value.page
      clearQuestionAnswerLocalReadFailure(undefined, historyResult.value.page)
    }
    // Failed or superseded responses leave only the CURRENT remaining parts; no captured state is restored.
  } finally {
    if (read && qaLocalFailureIntent === failure.intent && questionAnswerScopeIsCurrent(scope)
      && questionAnswerBatchReadIsCurrent(read, failure.selectBatch)
      && (!failure.selectBatch || reviewSequence === qaReviewSequence)) {
      resumeQuestionAnswerReadPolling(read.batchId, runtimeReadSequence, reviewSequence)
    }
    if (qaLocalRetryController === controller) {
      qaLocalRetryController = null
      qaLocalRetrying.value = false
    }
  }
}

const toggleQuestionAnswerExpanded = (recordId: string) => {
  const next = new Set(qaExpanded.value)
  if (next.has(recordId)) next.delete(recordId)
  else next.add(recordId)
  qaExpanded.value = next
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
      byQuestion: (batch.stats.byQuestion ?? []).map(item => ({ ...item, reviews: questionAnswerReviewStatsFromRecords(records.filter(record => questionAnswerRecordMatchesQuestion(record, item))), byModel: item.byModel.map(model => ({ ...model, reviews: questionAnswerReviewStatsFromRecords(records.filter(record => record.modelName === model.modelName && questionAnswerRecordMatchesQuestion(record, item))) })) })),
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
}

const clearQuestionAnswerMarking = (recordId: string) => {
  const marking = new Map(qaMarking.value)
  marking.delete(recordId)
  qaMarking.value = marking
}

const saveQuestionAnswerJudgment = async (record: QuestionAnswerRecord, judgment: Exclude<QuestionAnswerJudgment, 'unreviewed'>) => {
  if (!props.target || record.status !== 'succeeded' || qaMarking.value.has(record.id)) return
  const targetId = props.target.targetId
  const sequence = loadSequence
  const scope = { sequence, targetId }
  const judgmentSessionSequence = qaJudgmentSessionSequence
  const initialBatchRead = captureQuestionAnswerBatchRead(record.batchId)
  const initialFailureIntent = qaLocalFailureIntent
  const controller = new AbortController()
  qaJudgmentControllers.set(record.id, controller)
  const nextMarking = new Map(qaMarking.value)
  nextMarking.set(record.id, judgment)
  qaMarking.value = nextMarking
  qaErrorKey.value = ''
  let pollingPausedForRuntime = false
  let judgmentRefreshSequence: number | null = null
  let runtimeReadSequence: number | null = null
  let reviewReadSequence = qaReviewSequence
  let batchRead: QuestionAnswerBatchReadState | null = null
  const pauseRuntimePolling = () => {
    if (qaRuntimeBatch.value?.batchId === record.batchId) {
      clearQuestionAnswerPolling()
      pollingPausedForRuntime = true
    }
  }
  const resumeRuntimePolling = () => {
    if (
      pollingPausedForRuntime
      && judgmentRefreshSequence !== null
      && judgmentSessionSequence === qaJudgmentSessionSequence
      && qaJudgmentRefreshSequences.get(record.batchId) === judgmentRefreshSequence
      && questionAnswerScopeIsCurrent(scope)
      && batchRead !== null && questionAnswerBatchReadIsCurrent(batchRead)
    ) {
      pollingPausedForRuntime = false
      resumeQuestionAnswerReadPolling(record.batchId, runtimeReadSequence, reviewReadSequence)
    }
  }
  const refreshJudgmentData = async (conflictErrorKey?: string) => {
    pauseRuntimePolling()
    const historyPage = qaHistoryIntentPage
    const historyScope = qaHistoryScope.value
    const historySequence = beginQuestionAnswerHistoryIntent()
    judgmentRefreshSequence = ++qaJudgmentRefreshSequence
    qaJudgmentRefreshSequences.set(record.batchId, judgmentRefreshSequence)
    batchRead = captureQuestionAnswerBatchRead(record.batchId)
    runtimeReadSequence = beginQuestionAnswerRuntimeRead(record.batchId)
    const failureIntent = qaLocalFailureIntent
    reviewReadSequence = qaReviewSequence
    const refreshIsCurrent = () => (
      judgmentSessionSequence === qaJudgmentSessionSequence
      && questionAnswerScopeIsCurrent(scope)
      && qaJudgmentRefreshSequences.get(record.batchId) === judgmentRefreshSequence
      && batchRead !== null && questionAnswerBatchReadIsCurrent(batchRead)
    )
    const exactReadIsCurrent = () => questionAnswerExactReadIsCurrent(runtimeReadSequence, reviewReadSequence)
    const historyReadIsCurrent = () => questionAnswerHistoryIntentIsCurrent(historySequence) && historyScope === qaHistoryScope.value
    const failureIntentIsCurrent = () => failureIntent === qaLocalFailureIntent
    const batchOutcomePromise = getQuestionAnswerBatch(targetId, record.batchId, controller.signal).then(
      batch => ({ ok: true as const, batch }),
      error => ({ ok: false as const, error }),
    )
    const historyOutcomePromise = getQuestionAnswerHistory(targetId, historyPage, historyScope, controller.signal).then(
      history => ({ ok: true as const, history }),
      error => ({ ok: false as const, error }),
    )
    const batchOutcome = await batchOutcomePromise
    if (!refreshIsCurrent()) return
    if (batchOutcome.ok) {
      applyQuestionAnswerReadBatch(batchOutcome.batch, judgmentRefreshSequence, false, runtimeReadSequence, reviewReadSequence)
      if (failureIntentIsCurrent() && (exactReadIsCurrent() || !batchOutcome.batch.active)) {
        clearQuestionAnswerLocalReadFailure(record.batchId)
      }
      if (conflictErrorKey) emit('question-answer-stats-dirty', targetId)
    }
    // A completed exact read must release its own pause without interrupting a newer read/poll.
    resumeRuntimePolling()
    const historyOutcome = await historyOutcomePromise
    if (!refreshIsCurrent() || !failureIntentIsCurrent()) return
    if (historyReadIsCurrent() && historyOutcome.ok) {
      qaHistory.value = historyOutcome.history
      qaHistoryIntentPage = historyOutcome.history.page
      clearQuestionAnswerLocalReadFailure(undefined, historyOutcome.history.page)
    }
    const batchFailed = !batchOutcome.ok && exactReadIsCurrent()
      && !(batchOutcome.error instanceof Error && batchOutcome.error.name === 'AbortError')
    const historyFailed = historyReadIsCurrent() && !historyOutcome.ok
      && !(historyOutcome.error instanceof Error && historyOutcome.error.name === 'AbortError')
    const errorKey = conflictErrorKey ?? `${prefix}.questionAnswer.judgmentRefreshFailed`
    if (batchFailed || historyFailed) {
      setQuestionAnswerLocalReadFailure({
        batchId: batchFailed ? record.batchId : null,
        selectBatch: false,
        historyPage: historyFailed ? historyPage : null,
        historyScope,
        errorKey,
      })
    } else if (conflictErrorKey) qaErrorKey.value = conflictErrorKey
  }
  try {
    const authoritative = await setQuestionAnswerJudgment(targetId, record.id, judgment, record.updatedAt, controller.signal)
    if (
      judgmentSessionSequence !== qaJudgmentSessionSequence
      || !questionAnswerScopeIsCurrent(scope)
    ) return
    applyAuthoritativeQuestionAnswerRecord(authoritative)
    qaModelControlRefreshKey.value++
    emit('question-answer-stats-dirty', targetId)
    clearQuestionAnswerMarking(record.id)
    await refreshJudgmentData()
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') return
    if (
      judgmentSessionSequence === qaJudgmentSessionSequence
      && questionAnswerScopeIsCurrent(scope)
      && questionAnswerBatchReadIsCurrent(initialBatchRead)
      && initialFailureIntent === qaLocalFailureIntent
    ) {
      if (error instanceof Error && error.message === 'admin.connectionHealth.errors.questionAnswerJudgmentConflict') await refreshJudgmentData(error.message)
      else qaErrorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
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
  requestPhase: model.requestPhase,
  ruleVersion: model.ruleVersion,
  firstTokenMs: model.requestFirstTokenMs,
  firstEventMs: model.requestFirstEventMs,
  protocol: model.requestProtocol,
  probeTimeoutSeconds: model.requestTimeoutSeconds,
  probeDisposition: model.probeDisposition,
  modelName: model.modelName,
  result: model.probeResult || model.lastErrorKey || model.state,
  healthy: model.probeDisposition !== 'stale' && model.probeDisposition !== 'invalid' && (model.probeResult === 'ok' || model.probeResult === 'slow_response'),
  latencyMs: model.requestLatencyMs ?? model.lastLatencyMs,
  errorKey: model.requestErrorKey ?? (model.probeResult === 'ok' || model.probeResult === 'slow_response' ? '' : (model.probeResult || model.lastErrorKey)),
  errorDetail: model.requestErrorDetail ?? (model.probeDisposition === 'invalid' ? (model.lastAttempt?.errorDetail ?? '') : model.probeDisposition === 'stale' ? '' : model.lastErrorDetail),
  probedAt: model.requestAt ?? model.updatedAt ?? new Date().toISOString(),
})

const resultLabel = (result: ManualProbeResult): string => result.result === 'slow_response' ? t(connectionHealthProbeResultLabelKey(result.result, result.ruleVersion)) : readableMessage(result.result)
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
            <div class="flex min-w-0 flex-1 flex-wrap items-center gap-2.5">
              <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                <Zap class="h-4 w-4" />
              </div>
              <div class="min-w-0">
                <h3 class="truncate text-sm font-semibold text-foreground">{{ t(`${prefix}.title`) }}</h3>
                <p class="truncate text-xs text-muted-foreground">
                  {{ target.accountName }} · {{ target.platform || '-' }} · {{ target.type || '-' }} · {{ target.status || '-' }} · {{ target.groupName }}
                </p>
              </div>
              <AccountTierEditor
                v-if="target.targetId.toLowerCase().startsWith('sub2api:')"
                :target-id="target.targetId"
                :account-tier="target.accountTier"
                @saved="emit('tier-saved', $event)"
              />
            </div>
            <button type="button" class="shrink-0 rounded-md p-1 text-muted-foreground transition-colors hover:bg-surface-elevated hover:text-foreground" @click="close">
              <X class="h-4 w-4" />
            </button>
          </div>

          <div data-testid="question-answer-scroll" class="flex-1 overflow-y-auto px-5 py-4">
            <div v-if="mode !== 'questionAnswer'" class="mb-4 inline-flex max-w-full overflow-x-auto rounded-lg border border-border/60 bg-surface-line/30 p-1">
              <button type="button" class="whitespace-nowrap rounded-md px-3 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground" :disabled="phase === 'testing'" @click="mode = 'questionAnswer'">
                {{ t(`${prefix}.modes.questionAnswer`) }}
              </button>
              <button type="button" class="whitespace-nowrap rounded-md px-3 py-1.5 text-xs font-medium transition-colors" :class="mode === 'formal' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'" :disabled="phase === 'testing'" @click="mode = 'formal'">
                {{ t(`${prefix}.modes.formal`) }}
              </button>
              <button type="button" class="whitespace-nowrap rounded-md px-3 py-1.5 text-xs font-medium transition-colors" :class="mode === 'once' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'" :disabled="phase === 'testing'" @click="mode = 'once'">
                {{ t(`${prefix}.modes.once`) }}
              </button>
            </div>

            <p v-if="target?.testConfiguration" class="mb-3 whitespace-pre-wrap break-words text-xs text-muted-foreground">
              {{ t('admin.connectionHealth.testConfiguration.' + target.testConfiguration.status) }}<template v-if="target.testConfiguration.protocol"> · {{ requestProtocolLabel(target.testConfiguration.protocol) }}</template><template v-if="target.testConfiguration.probeTimeoutSeconds"> / {{ target.testConfiguration.probeTimeoutSeconds }}s</template>
              <span v-if="target.testConfiguration.sourceGroups.length"> · {{ target.testConfiguration.sourceGroups.map(source => `${source.adminGroupName || source.adminGroupId}: ${requestProtocolLabel(source.protocol)} / ${source.probeTimeoutSeconds}s`).join('；') }}</span>
            </p>

            <p v-if="mode !== 'questionAnswer'" class="mb-4 text-xs text-muted-foreground">{{ t(`${prefix}.modeDescriptions.${mode}`) }}</p>
            <p v-if="mode !== 'questionAnswer'" class="mb-4 text-xs text-muted-foreground">{{ t(`${prefix}.contractLimit`) }}</p>
            <p v-if="mode !== 'questionAnswer' && target?.testConfiguration?.protocol === 'responses'" class="mb-3 text-xs leading-5 text-muted-foreground">{{ t('admin.connectionHealth.testConfiguration.responsesBudget') }}</p>
            <p v-if="mode === 'questionAnswer'" class="mb-3 text-xs leading-5 text-muted-foreground">{{ t('admin.connectionHealth.testConfiguration.questionAnswerTimeout') }}</p>

            <div v-if="mode === 'questionAnswer' && (qaFinalization || qaFinalizationUnknown)" data-testid="question-answer-finalization" class="mb-3 rounded-lg border border-amber-500/30 bg-amber-500/5 p-3 text-sm">
              <p v-if="qaFinalizationUnknown">{{ t(prefix + '.questionAnswer.finalizationUnknown') }}</p>
              <template v-else-if="qaFinalization">
                <p>{{ t(prefix + '.questionAnswer.' + (qaFinalization.state === 'failed' ? 'finalizationFailed' : 'finalizationPending')) }}</p>
                <p class="mt-1 text-xs text-muted-foreground">{{ t(prefix + '.questionAnswer.finalizationBatch', { id: shortQuestionAnswerBatchId(qaFinalization.batchId) }) }}</p>
                <p v-if="qaFinalization.recovery === 'service_shutdown'" class="mt-1 text-xs">{{ t(prefix + '.questionAnswer.finalizationShutdown') }}</p>
                <button v-if="qaFinalization.recovery === 'cancel'" data-testid="question-answer-finalization-retry" type="button" class="mt-2 rounded-lg border border-border/60 px-3 py-1.5 text-xs font-medium disabled:opacity-50" :disabled="qaCancelling" @click="stopQuestionAnswers">
                  {{ t(prefix + '.questionAnswer.finalizationRetry') }}
                </button>
              </template>
            </div>

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
              <div v-if="!hasModels && mode !== 'questionAnswer'" class="flex flex-col items-center justify-center gap-2 py-16 text-center">
                <ShieldAlert class="h-8 w-8 text-muted-foreground/40" />
                <p class="text-sm text-muted-foreground">{{ t(`${prefix}.empty`) }}</p>
              </div>

              <template v-else>
                <section
                  v-if="mode === 'questionAnswer' && !qaHistoryLoaded"
                  data-question-answer-section="stats"
                  class="mb-3 rounded-lg border border-border/50 bg-surface-line/20 px-3 py-4"
                >
                  <div v-if="qaLoading" data-testid="question-answer-stats-loading" class="flex items-center gap-2 text-xs text-muted-foreground">
                    <Loader2 class="h-4 w-4 animate-spin" />
                    {{ t(prefix + '.questionAnswer.loading') }}
                  </div>
                  <div v-else data-testid="question-answer-stats-error" class="text-xs text-red-600 dark:text-red-400"><p>{{ qaReadableError }}</p><button type="button" class="mt-2 rounded-md border border-border/60 px-3 py-1.5 font-medium" @click="retryQuestionAnswerData">重新加载</button></div>
                </section>
                <QuestionAnswerStatsBar
                  v-else-if="mode === 'questionAnswer'"
                  data-question-answer-section="stats"
                  :review-stats="qaReviewBatch?.stats ?? null"
                  :today-stats="qaHistory.todayStats"
                />
                <QuestionAnswerModelControlPanel v-if="open && mode === 'questionAnswer' && target?.targetId.startsWith('sub2api:')" :target-id="target.targetId" :refresh-key="qaModelControlRefreshKey" @settled="onModelControlSettled" />
                <p v-if="mode === 'questionAnswer'" class="mb-2 text-xs text-muted-foreground">{{ t(prefix + '.questionAnswer.protocolSummary') }}</p>
                <template v-if="mode === 'questionAnswer'">
                  <div v-if="(qaErrorKey && qaHistoryLoaded) || qaLocalReadFailure || summaryRefreshFailed" data-testid="question-answer-statistics-error" class="mb-3 rounded-lg bg-red-500/10 px-3 py-2 text-xs text-red-600 dark:text-red-400">
                    <p v-if="qaLocalReadFailure">{{ readableMessage(qaLocalReadFailure.errorKey) }}</p>
                    <p v-else-if="qaErrorKey">{{ qaReadableError }}</p>
                    <p v-if="summaryRefreshFailed">{{ t(prefix + '.questionAnswer.judgmentRefreshFailed') }}</p>
                    <button v-if="qaLocalReadFailure" type="button" class="mt-2 rounded-md border border-red-500/30 px-2.5 py-1.5 font-medium disabled:opacity-50" :disabled="qaLocalRetrying" @click="retryQuestionAnswerLocalData">重新加载</button>
                    <button v-if="summaryRefreshFailed" type="button" class="mt-2 rounded-md border border-red-500/30 px-2.5 py-1.5 font-medium" @click="retryQuestionAnswerStats">重试统计</button>
                  </div>
                  <section v-if="qaRuntimeBatch?.active && !qaFinalization && !qaFinalizationUnknown" data-testid="question-answer-latest-runtime" class="mb-3 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border/50 bg-card p-3">
                    <p data-testid="question-answer-latest-running-hint" class="break-words text-xs text-primary">最新运行批次 #{{ shortQuestionAnswerBatchId(qaRuntimeBatch.batchId) }} · {{ qaRuntimeBatch.completedCount }}/{{ qaRuntimeBatch.submittedCount }}</p>
                    <div class="flex flex-wrap gap-2">
                      <button v-if="qaReviewBatch?.batchId !== qaRuntimeBatch.batchId" type="button" class="rounded-lg border border-border/60 px-3 py-1.5 text-xs font-medium hover:bg-surface-line" @click="reviewLatestQuestionAnswerBatch">返回最新</button>
                      <button data-testid="question-answer-stop-latest" type="button" class="inline-flex items-center gap-1.5 rounded-lg border border-red-500/30 px-3 py-1.5 text-xs font-medium text-red-600 hover:bg-red-500/10 disabled:opacity-50 dark:text-red-400" :disabled="qaCancelling" @click="stopQuestionAnswers"><Loader2 v-if="qaCancelling" class="h-3.5 w-3.5 animate-spin" /><StopCircle v-else class="h-3.5 w-3.5" />终止 #{{ shortQuestionAnswerBatchId(qaRuntimeBatch.batchId) }}</button>
                    </div>
                  </section>
                  <section data-question-answer-section="pending" data-testid="question-answer-pending" class="rounded-lg border border-border/50 bg-card p-3">
                    <div class="flex flex-wrap items-center justify-between gap-3">
                      <div>
                        <button type="button" class="inline-flex items-center gap-2 text-sm font-semibold text-foreground" :aria-expanded="qaPendingOpen" @click="qaPendingOpen = !qaPendingOpen">待人工判断 {{ qaPendingReviewRecords.length }}<ChevronUp v-if="qaPendingOpen" class="h-4 w-4" /><ChevronDown v-else class="h-4 w-4" /></button>
                        <p v-if="qaReviewBatch" data-testid="question-answer-review-batch" class="mt-1 break-words text-xs text-muted-foreground">正在查看：批次 #{{ shortQuestionAnswerBatchId(qaReviewBatch.batchId) }} · {{ qaReviewBatchStatusLabel }}<span v-if="qaReviewCompletedAt"> · {{ t(prefix + '.questionAnswer.batchCompletedAt', { time: formatConnectionHealthTime(qaReviewCompletedAt) }) }}</span></p>
                      </div>
                      <button v-if="qaRuntimeBatch && !qaRuntimeBatch.active && qaReviewBatch?.batchId !== qaRuntimeBatch.batchId" type="button" class="rounded-lg border border-border/60 px-3 py-1.5 text-xs font-medium hover:bg-surface-line" @click="reviewLatestQuestionAnswerBatch">返回最新</button>
                    </div>
                    <div v-if="!qaReviewBatch" class="mt-3 rounded-lg border border-dashed border-border/50 px-3 py-4 text-center text-xs text-muted-foreground">{{ t(prefix + '.questionAnswer.noBatch') }}</div>
                    <template v-else-if="qaPendingOpen">
                      <div v-if="qaPendingReviewRecords.length === 0" class="mt-3 rounded-lg border border-dashed border-border/50 px-3 py-4 text-center text-xs text-muted-foreground">{{ qaReviewBatch.active ? t(prefix + '.questionAnswer.waitingForReviewableAnswer') : t(prefix + '.questionAnswer.noPendingReview') }}</div>
                      <ul v-else class="mt-3 space-y-3">
                        <li v-for="record in qaPendingReviewRecords" :key="record.id" class="rounded-lg border border-border/60 bg-card p-3"><QuestionAnswerRecordCard :record="record" :expanded="qaExpanded.has(record.id)" :saving="qaMarking.has(record.id)" :saving-judgment="qaMarking.get(record.id)" @expand="toggleQuestionAnswerExpanded(record.id)" @judge="saveQuestionAnswerJudgment(record, $event)" /></li>
                      </ul>
                    </template>
                  </section>

                  <section v-if="qaProcessedSectionVisible" data-question-answer-section="processed" data-testid="question-answer-processed" class="mt-3 rounded-lg border border-border/50 bg-surface-line/10">
                    <button type="button" class="flex w-full items-center justify-between gap-3 px-3 py-2.5 text-left" :aria-expanded="qaProcessedOpen" @click="qaProcessedOpen = !qaProcessedOpen">
                      <span data-testid="question-answer-processed-summary" class="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm font-semibold tabular-nums text-foreground">
                        <span>{{ t(prefix + '.questionAnswer.processedSummary', { total: qaReviewedRecords.length, correct: qaReviewedCorrectCount, incorrect: qaReviewedIncorrectCount }) }}</span>
                        <span :class="qaReviewBatchStatusClass"> · {{ qaReviewBatchStatusLabel }}</span>
                      </span>
                      <ChevronUp v-if="qaProcessedOpen" class="h-4 w-4 text-muted-foreground" />
                      <ChevronDown v-else class="h-4 w-4 text-muted-foreground" />
                    </button>
                    <div v-if="qaProcessedOpen" data-testid="question-answer-processed-content" class="border-t border-border/40 p-3">
                      <div v-if="qaReviewedRecords.length === 0" class="rounded-md border border-dashed border-border/50 px-3 py-3 text-center text-xs text-muted-foreground">{{ t(prefix + '.questionAnswer.noProcessedAnswers') }}</div>
                      <div v-else class="space-y-2">
                        <div v-for="group in qaResultGroups" :key="group.key" data-testid="question-answer-result-group" class="rounded-md border border-border/40">
                          <button type="button" class="flex w-full flex-wrap items-center justify-between gap-2 px-3 py-2.5 text-left text-xs" :aria-expanded="qaResultGroupsOpen.has(group.key)" @click="toggleQuestionAnswerResultGroup(group.key)">
                            <span class="break-words font-medium text-foreground">{{ group.questionName }}<template v-if="group.questionSnapshotKey"> · #{{ group.questionSnapshotKey.slice(0, 8) }}</template> · {{ group.modelName }} · {{ formatQuestionAnswerAccuracy(questionAnswerAccuracy(group)) }} · 重复 {{ group.records.length }} 次</span><span class="text-muted-foreground">{{ qaResultGroupsOpen.has(group.key) ? '收起' : `展开 ${group.records.length} 条` }}</span>
                          </button>
                          <ul v-if="qaResultGroupsOpen.has(group.key)" class="space-y-2 border-t border-border/40 p-3">
                            <li v-for="record in group.records" :key="record.id" class="rounded-md border p-3" :class="questionAnswerRecordClass(record)"><QuestionAnswerRecordCard :record="record" :expanded="qaExpanded.has(record.id)" :saving="qaMarking.has(record.id)" :saving-judgment="qaMarking.get(record.id)" @expand="toggleQuestionAnswerExpanded(record.id)" @judge="saveQuestionAnswerJudgment(record, $event)" /></li>
                          </ul>
                        </div>
                      </div>
                      <div v-if="qaFailedRecords.length > 0" class="mt-3 border-t border-border/40 pt-3">
                        <button type="button" class="inline-flex items-center gap-1 text-xs font-medium text-red-600 dark:text-red-400" @click="qaFailedOpen = !qaFailedOpen">
                          {{ t(prefix + '.questionAnswer.failedSummary', { count: qaFailedRecords.length }) }}
                          <ChevronUp v-if="qaFailedOpen" class="h-3.5 w-3.5" />
                          <ChevronDown v-else class="h-3.5 w-3.5" />
                        </button>
                        <ul v-if="qaFailedOpen" data-testid="question-answer-failed-content" class="mt-2 space-y-2">
                          <li v-for="record in qaFailedRecords" :key="record.id" class="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2">
                            <div class="flex items-center justify-between gap-3">
                              <div class="min-w-0">
                                <p class="truncate text-sm font-medium text-foreground">{{ record.questionName }}</p>
                                <p class="mt-0.5 truncate text-xs text-muted-foreground">{{ record.modelName }} · {{ requestProtocolLabel(record.requestProtocol) }}</p>
                                <p class="mt-1 text-xs text-foreground">{{ record.questionBody }}</p>
                              </div>
                              <span class="shrink-0 text-xs text-red-600 dark:text-red-400">{{ questionAnswerErrorLabel(record.errorType) }}</span>
                            </div>
                          </li>
                        </ul>
                      </div>
                    </div>
                  </section>

                  <section data-question-answer-section="configuration" data-testid="question-answer-configuration" class="mt-3 rounded-lg border border-border/50 bg-card p-3">
                    <div class="inline-flex max-w-full overflow-x-auto rounded-lg border border-border/60 bg-surface-line/30 p-1">
                      <button type="button" class="whitespace-nowrap rounded-md bg-background px-3 py-1.5 text-xs font-medium text-foreground shadow-sm" :disabled="phase === 'testing'">{{ t(prefix + '.modes.questionAnswer') }}</button>
                      <button type="button" class="whitespace-nowrap rounded-md px-3 py-1.5 text-xs font-medium text-muted-foreground hover:text-foreground" :disabled="phase === 'testing'" @click="mode = 'formal'">{{ t(prefix + '.modes.formal') }}</button>
                      <button type="button" class="whitespace-nowrap rounded-md px-3 py-1.5 text-xs font-medium text-muted-foreground hover:text-foreground" :disabled="phase === 'testing'" @click="mode = 'once'">{{ t(prefix + '.modes.once') }}</button>
                    </div>
                    <p v-if="qaErrorKey && qaHistoryLoaded" class="mt-3 rounded-lg bg-red-500/10 px-3 py-2 text-xs text-red-600 dark:text-red-400">{{ qaReadableError }}</p>
                    <p v-if="qaCompletedNotice" class="mt-3 rounded-lg bg-green-500/10 px-3 py-2 text-xs text-green-600 dark:text-green-400">{{ t(prefix + '.questionAnswer.completedNotice') }}</p>

                    <div class="mt-3 flex flex-wrap items-center justify-between gap-3">
                      <button
                        type="button"
                        data-testid="question-answer-configuration-toggle"
                        class="inline-flex items-center gap-1 rounded-md text-left text-sm font-semibold text-foreground hover:text-primary"
                        :aria-expanded="qaConfigOpen"
                        aria-controls="question-answer-configuration-content"
                        @click="qaConfigOpen = !qaConfigOpen"
                      >
                        <span>{{ t(prefix + '.questionAnswer.newTestConfiguration') }}</span>
                        <ChevronUp v-if="qaConfigOpen" class="h-4 w-4 text-muted-foreground" />
                        <ChevronDown v-else class="h-4 w-4 text-muted-foreground" />
                      </button>
                      <button
                        v-if="!qaSelectionLocked && (!qaConfigOpen || qaSavedPreferenceValid)"
                        type="button"
                        class="rounded-md border border-border/60 px-2.5 py-1 text-xs font-medium text-foreground hover:bg-surface-line"
                        @click="qaConfigOpen = !qaConfigOpen"
                      >
                        {{ qaConfigOpen ? t(prefix + '.questionAnswer.collapseConfiguration') : t(prefix + '.questionAnswer.modifyConfiguration') }}
                      </button>
                    </div>
                    <div v-if="!qaConfigOpen" id="question-answer-configuration-content" class="mt-3 rounded-lg bg-surface-line/30 px-3 py-2.5">
                      <p class="break-words text-sm font-medium text-foreground">{{ qaSummaryModelNames.join('、') || '-' }}</p>
                      <p class="mt-1 text-xs text-muted-foreground">
                        {{ t(prefix + '.questionAnswer.configurationSummary', {
                          questions: qaSummaryQuestionCount,
                          effort: questionAnswerReasoningEffortLabel(qaSummaryReasoningEffort),
                          repeat: qaSummaryRepeatCount,
                        }) }}
                      </p>
                      <p class="mt-1 text-xs text-primary">
                        {{ qaRuntimeBatch?.active ? t(prefix + '.questionAnswer.activeConfiguration') : t(prefix + '.questionAnswer.rememberedConfiguration') }}
                      </p>
                    </div>
                    <div v-else id="question-answer-configuration-content" class="mt-3">
                      <div class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_20rem]">
                        <div data-testid="question-answer-models">
                          <p class="mb-2 text-xs text-muted-foreground">{{ t(prefix + '.selectHint') }}</p>
                          <div class="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
                            <label v-for="model in models" :key="model.id" class="flex cursor-pointer items-start gap-2 rounded-lg border border-border/40 px-3 py-2 transition-colors" :class="selected.has(model.id) ? 'border-primary/50 bg-primary/5' : 'hover:bg-surface-line/40'">
                              <input type="checkbox" class="mt-0.5 h-4 w-4 shrink-0 rounded border-border/60" :disabled="qaSelectionLocked" :checked="selected.has(model.id)" @change="toggle(model.id)" />
                              <div class="min-w-0 flex-1">
                                <p class="truncate text-sm font-medium text-foreground">{{ model.name }}</p>
                                <p v-if="model.ownedBy" class="truncate text-xs text-muted-foreground">{{ model.ownedBy }}</p>
                              </div>
                            </label>
                          </div>
                        </div>
                        <fieldset data-testid="question-answer-reasoning" :disabled="qaSelectionLocked">
                          <legend class="mb-2 text-xs font-semibold text-foreground">{{ t(prefix + '.questionAnswer.reasoningEffort.title') }}</legend>
                          <div class="grid grid-cols-4 overflow-hidden rounded-lg border border-border/50 bg-surface-line/20 xl:grid-cols-1">
                            <label v-for="option in qaReasoningEffortOptions" :key="option.value" class="flex cursor-pointer items-center justify-center border-r border-border/40 px-2 py-2 text-xs transition-colors last:border-r-0 xl:border-b xl:border-r-0 xl:last:border-b-0" :class="qaReasoningEffort === option.value ? 'bg-primary/10 text-primary' : 'text-muted-foreground hover:bg-surface-line/40'">
                              <input :checked="qaReasoningEffort === option.value" class="sr-only" type="radio" name="question-answer-reasoning-effort" :value="option.value" @change="selectQuestionAnswerReasoningEffort(option.value)" />
                              {{ t(prefix + '.questionAnswer.reasoningEffort.options.' + option.labelKey) }}
                            </label>
                          </div>
                          <label class="mt-3 block text-xs font-semibold text-foreground" for="question-answer-repeat-count">{{ t(prefix + '.questionAnswer.repeatCount') }}</label>
                          <select id="question-answer-repeat-count" :value="qaRepeatCount" class="mt-2 w-full rounded-lg border border-border/50 bg-background px-3 py-2 text-xs text-foreground" :disabled="qaSelectionLocked" @change="selectQuestionAnswerRepeatCount">
                            <option v-for="option in qaRepeatCountOptions" :key="option" :value="option">{{ option }}</option>
                          </select>
                        </fieldset>
                      </div>
                      <div data-testid="question-answer-questions" class="mt-4 border-t border-border/40 pt-3">
                        <div class="mb-2 flex items-center justify-between gap-3">
                          <h4 class="text-xs font-semibold text-foreground">{{ t(prefix + '.questionAnswer.questionsTitle') }}</h4>
                          <span class="text-xs text-muted-foreground">{{ t(prefix + '.questionAnswer.selectedFormula', { models: qaSubmission.modelCount, questions: qaSubmission.questionCount, repeat: qaSubmission.repeatCount, total: qaSubmission.total }) }}</span>
                        </div>
                        <div v-if="qaLoading" class="flex items-center gap-2 rounded-lg border border-dashed border-border/50 px-3 py-4 text-xs text-muted-foreground">
                          <Loader2 class="h-4 w-4 animate-spin" />
                          {{ t(prefix + '.questionAnswer.loading') }}
                        </div>
                        <div v-else-if="qaQuestions.length === 0" class="rounded-lg border border-dashed border-border/50 px-3 py-4 text-center text-xs text-muted-foreground">{{ t(prefix + '.questionAnswer.noQuestions') }}</div>
                        <div v-else class="space-y-1.5">
                          <label v-for="question in qaQuestions" :key="question.id" class="flex cursor-pointer items-start gap-2 rounded-md border border-border/40 px-3 py-2 transition-colors" :class="qaSelectedQuestions.has(question.id) ? 'border-primary/40 bg-primary/5' : 'hover:bg-surface-line/30'">
                            <input type="checkbox" class="mt-0.5 h-4 w-4 shrink-0 rounded border-border/60" :disabled="qaSelectionLocked" :checked="qaSelectedQuestions.has(question.id)" @change="toggleQuestion(question.id)" />
                            <div class="min-w-0 flex-1">
                              <div class="flex items-center gap-2">
                                <span class="truncate text-xs font-medium text-foreground">{{ question.name }}</span>
                                <span v-if="question.isDefault" class="shrink-0 rounded-full bg-amber-500/10 px-1.5 py-0.5 text-[11px] text-amber-600 dark:text-amber-400">{{ t(prefix + '.questionAnswer.defaultQuestion') }}</span>
                              </div>
                              <p class="mt-0.5 truncate text-xs text-muted-foreground">{{ answerSummary(question.body) }}</p>
                            </div>
                          </label>
                        </div>
                        <p v-if="!qaSubmission.validRepeatCount" class="mt-2 text-xs text-red-600 dark:text-red-400">{{ t(prefix + '.questionAnswer.repeatCountInvalid') }}</p>
                        <p v-else-if="!qaSubmission.withinBatchLimit" class="mt-2 text-xs text-red-600 dark:text-red-400">{{ t(prefix + '.questionAnswer.batchLimit', { total: qaSubmission.total }) }}</p>
                      </div>
                    </div>
                  </section>

                  <section data-question-answer-section="history" data-testid="question-answer-history" class="mt-3 rounded-lg border border-border/50 bg-surface-line/10">
                    <button type="button" class="flex w-full items-center justify-between gap-3 px-3 py-2.5 text-left" @click="qaHistoryOpen = !qaHistoryOpen">
                      <span class="text-xs font-semibold text-foreground">{{ t(prefix + '.questionAnswer.todayHistoryTitle') }}</span>
                      <ChevronUp v-if="qaHistoryOpen" class="h-4 w-4 text-muted-foreground" />
                      <ChevronDown v-else class="h-4 w-4 text-muted-foreground" />
                    </button>
                    <div v-if="qaHistoryOpen" data-testid="question-answer-history-content" class="border-t border-border/40 p-3">
                      <div class="mb-3 flex gap-2"><button v-for="scope in (['today', 'all'] as const)" :key="scope" type="button" class="rounded-md border border-border/60 px-3 py-1.5 text-xs font-medium" :class="qaHistoryScope === scope ? 'bg-primary/10 text-primary' : 'text-muted-foreground'" :aria-pressed="qaHistoryScope === scope" @click="selectQuestionAnswerHistoryScope(scope)">{{ scope === 'today' ? '今日' : '全部' }}</button></div>
                      <p class="mb-2 text-[11px] text-muted-foreground">共 {{ qaHistory.totalBatches }} 个完整批次</p>
                      <div v-if="qaHistoryBatchGroups.length === 0" class="rounded-lg border border-dashed border-border/50 px-3 py-4 text-center text-xs text-muted-foreground">{{ t(prefix + '.questionAnswer.noHistory') }}</div>
                      <div v-else class="space-y-3">
                        <div v-for="group in qaHistoryBatchGroups" :key="group.batchId" data-testid="question-answer-history-batch" class="rounded-lg border border-border/50 bg-card p-3">
                          <div class="flex flex-wrap items-center justify-between gap-3"><p class="break-words text-xs font-medium text-foreground">批次 #{{ shortQuestionAnswerBatchId(group.batchId) }} · {{ group.active ? '进行中' : '已结束' }}</p><button type="button" class="inline-flex items-center gap-1.5 rounded-md border border-border/60 px-2.5 py-1.5 text-xs font-medium text-foreground hover:bg-surface-line disabled:opacity-50" :disabled="qaReviewLoadingBatchId === group.batchId" @click="reviewQuestionAnswerBatch(group.batchId)"><Loader2 v-if="qaReviewLoadingBatchId === group.batchId" class="h-3.5 w-3.5 animate-spin" />查看该批次</button></div>
                          <p class="mt-2 break-words text-[11px] text-muted-foreground">创建 {{ formatConnectionHealthTime(group.createdAt) }} · 完成 {{ group.completedAt ? formatConnectionHealthTime(group.completedAt) : '—' }} · {{ requestProtocolLabel(group.requestProtocol) }} · 力度 {{ group.reasoningEffort ?? '未指定' }} · 重复 {{ group.repeatCount }} 次</p>
                          <p class="mt-1 break-words text-[11px] text-muted-foreground">{{ group.models.join('、') }} · {{ group.questions.map(question => `${question.displayQuestionName} · #${question.questionSnapshotKey.slice(0, 8)}`).join('；') }}</p>
                          <p class="mt-1 break-words text-xs text-foreground">提交 {{ group.stats.requests.submitted }} · 进行中 {{ group.stats.requests.inProgress }} · 成功 {{ group.stats.requests.succeeded }} · 待人工 {{ group.stats.reviews.unreviewed }} · 正确 {{ group.stats.reviews.correct }} · 错误 {{ group.stats.reviews.incorrect }} · 失败 {{ group.stats.requests.failed }} · 取消 {{ group.stats.requests.cancelled }}</p>
                        </div>
                      </div>
                      <div v-if="qaHistory.totalPages > 1" class="mt-4 flex flex-wrap items-center justify-center gap-1.5">
                        <template v-for="(page, index) in qaPageNumbers" :key="page">
                          <span v-if="index > 0 && page - qaPageNumbers[index - 1] > 1" class="px-1 text-xs text-muted-foreground">…</span>
                          <button type="button" class="h-8 min-w-8 rounded-md border px-2 text-xs" :class="page === qaHistory.page ? 'border-primary bg-primary text-primary-foreground' : 'border-border/50 text-muted-foreground hover:bg-surface-elevated'" @click="goQuestionAnswerPage(page)">{{ page }}</button>
                        </template>
                      </div>
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
                          <AlertTriangle v-if="result.probeDisposition === 'stale' || resultIsSlow(result)" class="h-4 w-4 shrink-0 text-amber-500" />
                          <CheckCircle2 v-else-if="result.healthy" class="h-4 w-4 shrink-0 text-green-500" />
                          <XCircle v-else class="h-4 w-4 shrink-0 text-red-500" />
                          <span class="truncate text-sm font-medium text-foreground">{{ result.modelName }}</span>
                          <span class="inline-flex shrink-0 items-center gap-1 rounded-full bg-surface-elevated px-2 py-0.5 text-xs text-muted-foreground">
                            <span class="h-1.5 w-1.5 rounded-full" :class="result.probeDisposition === 'stale' ? 'bg-muted-foreground' : connectionHealthRecordColorClass(result.result)" />
                            {{ result.probeDisposition === 'stale' ? t('admin.connectionHealth.testConfiguration.stale') : resultLabel(result) }}
                          </span>
                        </div>
                        <div class="flex shrink-0 items-center gap-3 text-xs text-muted-foreground">
                          <span>{{ requestProtocolLabel(result.protocol) }}<template v-if="result.probeTimeoutSeconds"> / {{ result.probeTimeoutSeconds }}s</template></span>
                          <span v-if="result.configurationChanged">{{ t('admin.connectionHealth.testConfiguration.changed') }}</span>
                          <span v-if="result.requestPhase === 'waiting_headers' || result.requestPhase === 'reading_body'">{{ t('admin.connectionHealth.testConfiguration.requestPhases.' + result.requestPhase) }}</span>
                          <span v-if="result.firstTokenMs != null">首字：{{ result.firstTokenMs }}ms</span>
                          <span v-if="result.firstEventMs != null">首个事件：{{ result.firstEventMs }}ms</span>
                          <span v-if="result.latencyMs !== null">{{ t(`${prefix}.latency`, { ms: result.latencyMs }) }}</span>
                          <span>{{ formatConnectionHealthTime(result.probedAt) }}</span>
                        </div>
                        <p v-if="!result.healthy && result.errorDetail" class="w-full whitespace-pre-wrap break-words text-xs text-red-500/80">{{ result.errorDetail }}</p>
                      </li>
                    </ul>
                  </div>
                </template>
              </template>
            </template>
          </div>

          <div data-testid="question-answer-footer" class="flex shrink-0 items-center justify-between gap-3 border-t border-border/60 px-5 py-4">
            <p v-if="mode === 'questionAnswer' || hasModels" class="flex min-w-0 flex-1 items-center gap-1 text-xs text-muted-foreground">
              <AlertTriangle v-if="selected.size === 0 || (mode === 'questionAnswer' && qaSelectedQuestions.size === 0)" class="h-3.5 w-3.5" />
              {{ mode === 'questionAnswer'
                ? (qaStartBlockedReason || t(`${prefix}.questionAnswer.selectedFormula`, { models: qaSubmission.modelCount, questions: qaSubmission.questionCount, repeat: qaSubmission.repeatCount, total: qaSubmission.total }))
                : t(`${prefix}.selectedCount`, { count: selected.size }) }}
            </p>
            <div v-else />
            <div class="flex shrink-0 items-center gap-2">
              <button type="button" class="whitespace-nowrap rounded-lg px-3 py-1.5 text-sm text-muted-foreground hover:bg-surface-line" @click="close">{{ mode === 'questionAnswer' ? t(`${prefix}.questionAnswer.leave`) : t(`${prefix}.close`) }}</button>
              <button type="button" class="inline-flex items-center gap-1.5 whitespace-nowrap rounded-lg bg-primary px-4 py-1.5 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-50" :disabled="!canStartTest" @click="startTest">
                <Loader2 v-if="phase === 'testing' || qaStarting" class="h-4 w-4 animate-spin" />
                {{ mode === 'questionAnswer'
                  ? (qaStarting ? t(`${prefix}.questionAnswer.submitting`) : t(`${prefix}.questionAnswer.startAnswer`))
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
