<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Loader2, X } from 'lucide-vue-next'
import { t, te } from '@/locales'
import { connectionHealthMessageKey } from '../../composables/useConnectionHealth'
import { discoverTargetModels, listTestQuestions, startQuestionAnswerBatch } from '../../api/connectionHealth'
import type {
  AdminGroupHealth,
  ManualProbeModelOption,
  QuestionAnswerReasoningEffort,
  TestQuestion,
} from '../../types/connectionHealth'
import type { QuestionAnswerPreferences } from '../../utils/connectionHealthPreferences'
import {
  collectQuestionAnswerBatchTargets,
  compatibleQuestionAnswerModelIds,
  reconcileQuestionAnswerBatchTargetIds,
  resolveQuestionAnswerSelection,
  selectQuestionAnswerBatchTargets,
  type QuestionAnswerBatchTarget,
} from '../../utils/questionAnswers'

const props = defineProps<{
  groups: AdminGroupHealth[]
  preferenceScope: string
  preferences: QuestionAnswerPreferences
}>()

const emit = defineEmits<{
  (event: 'preferences-changed', value: QuestionAnswerPreferences): void
  (event: 'question-answer-started', targetId: string): void
}>()

interface BatchTargetPreview {
  target: QuestionAnswerBatchTarget
  compatibleModelIds: string[]
  incompatibleModelIds: string[]
  requestCount: number
  discoveryErrorKey: string
}

type BatchTargetOutcomeKind = 'started' | 'skipped' | 'failed'

interface BatchTargetOutcome {
  targetId: string
  accountName: string
  kind: BatchTargetOutcomeKind
  compatibleModelIds: string[]
  incompatibleModelIds: string[]
  requestCount: number
  acceptedRequestCount: number
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

const visible = ref(false)
const preparing = ref(false)
const selectedTargetIds = ref<string[]>([])
const previews = ref<BatchTargetPreview[]>([])
const preparationError = ref('')
const preparationReady = ref(false)
const preparedQuestionIds = ref<string[]>([])
const preparedQuestionNames = ref<string[]>([])
const preparedModelIds = ref<string[]>([])
const running = ref(false)
const outcomes = ref<BatchTargetOutcome[]>([])
const runPreviewTotal = ref(0)
const runTargetTotal = ref(0)
const startedAt = ref<number | null>(null)
const preparedSignature = ref('')
let preparationSequence = 0
let preparationController: AbortController | null = null
let runSequence = 0
let runController: AbortController | null = null

const candidates = computed(() => collectQuestionAnswerBatchTargets(props.groups))
const incompleteGroups = computed(() => props.groups.filter(group => Boolean(group.accountsError)))
const candidateSignature = computed(() => JSON.stringify(props.groups.map(group => ({
  id: group.id,
  name: group.name,
  accountsError: group.accountsError ?? '',
  accounts: group.accounts.map(account => ({
    targetId: account.targetId,
    name: account.name,
    platform: account.platform,
    type: account.type,
    status: account.status,
  })),
}))))
const selectionSignature = computed(() => JSON.stringify({
  modelIds: props.preferences.modelIds,
  questionIds: props.preferences.questionIds,
  reasoningEffort: props.preferences.reasoningEffort,
  repeatCount: props.preferences.repeatCount,
}))
const selectedTargets = computed(() => selectQuestionAnswerBatchTargets(candidates.value, selectedTargetIds.value))
const preparationSignature = computed(() => JSON.stringify({
  scope: props.preferenceScope,
  candidates: candidateSignature.value,
  selectedTargetIds: selectedTargets.value.map(target => target.targetId),
  selection: selectionSignature.value,
}))
const sourceTarget = computed(() => selectedTargets.value[0] ?? null)
const previewTotal = computed(() => previews.value.reduce((total, preview) => total + preview.requestCount, 0))
const displayedPreviewTotal = computed(() => running.value ? runPreviewTotal.value : previewTotal.value)
const acceptedRequestTotal = computed(() => outcomes.value.reduce(
  (total, outcome) => total + outcome.acceptedRequestCount,
  0,
))
const startedCount = computed(() => outcomes.value.filter(outcome => outcome.kind === 'started').length)
const skippedCount = computed(() => outcomes.value.filter(outcome => outcome.kind === 'skipped').length)
const failedCount = computed(() => outcomes.value.filter(outcome => outcome.kind === 'failed').length)
const startedAtLabel = computed(() => {
  if (startedAt.value === null) return ''
  const date = new Date(startedAt.value)
  return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`
})
const hasRunSnapshot = computed(() => startedAt.value !== null)
const canStart = computed(() => (
  incompleteGroups.value.length === 0
  && selectedTargets.value.length > 0
  && preparationReady.value
  && !preparing.value
  && !preparationError.value
  && !running.value
  && preparedSignature.value === preparationSignature.value
))

const sameIds = (first: string[], second: string[]): boolean => (
  first.length === second.length && first.every((value, index) => value === second[index])
)

const cancelPreparation = (invalidate = true) => {
  preparationSequence++
  preparationController?.abort()
  preparationController = null
  preparing.value = false
  if (invalidate) {
    preparationReady.value = false
    preparedSignature.value = ''
  }
}

const prepare = async () => {
  cancelPreparation()
  previews.value = []
  preparationError.value = ''
  preparedQuestionIds.value = []
  preparedQuestionNames.value = []
  preparedModelIds.value = []
  if (!visible.value || incompleteGroups.value.length > 0 || selectedTargets.value.length === 0) return

  const sequence = preparationSequence
  const signature = preparationSignature.value
  const controller = new AbortController()
  preparationController = controller
  preparing.value = true
  try {
    let questions: TestQuestion[]
    try {
      questions = await listTestQuestions(controller.signal)
    } catch (error) {
      if (error instanceof Error && error.name === 'AbortError') return
      if (sequence === preparationSequence && visible.value) {
        preparationError.value = 'questionsLoad'
      }
      return
    }
    if (sequence !== preparationSequence || !visible.value || signature !== preparationSignature.value) return
    if (!questions.some(question => question.enabled)) {
      preparationError.value = 'noEnabledQuestions'
      return
    }

    const stableTargets = [...selectedTargets.value]
    const source = stableTargets[0]
    let sourceModels: ManualProbeModelOption[]
    try {
      sourceModels = await discoverTargetModels(source.targetId, controller.signal)
    } catch (error) {
      if (error instanceof Error && error.name === 'AbortError') return
      if (sequence === preparationSequence && visible.value) {
        preparationError.value = 'sourceDiscovery'
      }
      return
    }
    if (sequence !== preparationSequence || !visible.value || signature !== preparationSignature.value) return
    const resolved = resolveQuestionAnswerSelection(props.preferences, sourceModels, questions)
    if (resolved.modelIds.length === 0) {
      preparationError.value = 'noSourceModel'
      return
    }
    if (resolved.questionIds.length === 0) {
      preparationError.value = 'noEnabledQuestions'
      return
    }
    const perAccountRequestCount = resolved.modelIds.length * resolved.questionIds.length * resolved.repeatCount
    if (perAccountRequestCount > 50) {
      preparationError.value = 'overLimit'
      return
    }

    preparedQuestionIds.value = [...resolved.questionIds]
    preparedQuestionNames.value = resolved.questionIds.flatMap((questionId) => {
      const question = questions.find(item => item.id === questionId)
      return question ? [question.name] : []
    })
    preparedModelIds.value = [...resolved.modelIds]
    const sourceCompatibility = compatibleQuestionAnswerModelIds(resolved.modelIds, sourceModels)
    const nextPreviews: BatchTargetPreview[] = [{
      target: source,
      compatibleModelIds: sourceCompatibility.compatible,
      incompatibleModelIds: sourceCompatibility.incompatible,
      requestCount: sourceCompatibility.compatible.length * resolved.questionIds.length * resolved.repeatCount,
      discoveryErrorKey: '',
    }]
    for (const target of stableTargets.slice(1)) {
      try {
        const models = await discoverTargetModels(target.targetId, controller.signal)
        if (sequence !== preparationSequence || !visible.value || signature !== preparationSignature.value) return
        const compatibility = compatibleQuestionAnswerModelIds(resolved.modelIds, models)
        nextPreviews.push({
          target,
          compatibleModelIds: compatibility.compatible,
          incompatibleModelIds: compatibility.incompatible,
          requestCount: compatibility.compatible.length * resolved.questionIds.length * resolved.repeatCount,
          discoveryErrorKey: '',
        })
      } catch (error) {
        if (error instanceof Error && error.name === 'AbortError') return
        if (sequence !== preparationSequence || !visible.value || signature !== preparationSignature.value) return
        nextPreviews.push({
          target,
          compatibleModelIds: [],
          incompatibleModelIds: resolved.modelIds,
          requestCount: 0,
          discoveryErrorKey: error instanceof Error ? error.message : 'admin.connectionHealth.errors.request',
        })
      }
    }
    if (
      sequence === preparationSequence
      && visible.value
      && signature === preparationSignature.value
    ) {
      previews.value = nextPreviews
      preparationReady.value = true
      preparedSignature.value = signature
    }
  } finally {
    if (preparationController === controller) preparationController = null
    if (sequence === preparationSequence) preparing.value = false
  }
}

const runIsCurrent = (sequence: number, scope: string): boolean => (
  sequence === runSequence && scope === props.preferenceScope
)

const start = async () => {
  if (!canStart.value) return
  const snapshot: BatchRunSnapshot = {
    preferenceScope: props.preferenceScope,
    previews: previews.value.map(preview => ({
      ...preview,
      compatibleModelIds: [...preview.compatibleModelIds],
      incompatibleModelIds: [...preview.incompatibleModelIds],
    })),
    questionIds: [...preparedQuestionIds.value],
    reasoningEffort: props.preferences.reasoningEffort,
    repeatCount: props.preferences.repeatCount,
    previewTotal: previewTotal.value,
  }
  const sequence = ++runSequence
  const controller = new AbortController()
  runController = controller
  running.value = true
  outcomes.value = []
  runPreviewTotal.value = snapshot.previewTotal
  runTargetTotal.value = snapshot.previews.length
  startedAt.value = Date.now()

  try {
    for (const preview of snapshot.previews) {
      if (!runIsCurrent(sequence, snapshot.preferenceScope)) return
      if (preview.discoveryErrorKey) {
        outcomes.value = [...outcomes.value, {
          targetId: preview.target.targetId,
          accountName: preview.target.accountName,
          kind: 'failed',
          compatibleModelIds: [],
          incompatibleModelIds: [...preview.incompatibleModelIds],
          requestCount: 0,
          acceptedRequestCount: 0,
          reasonKey: 'discoveryFailed',
          errorKey: preview.discoveryErrorKey,
        }]
        continue
      }
      if (preview.compatibleModelIds.length === 0) {
        outcomes.value = [...outcomes.value, {
          targetId: preview.target.targetId,
          accountName: preview.target.accountName,
          kind: 'skipped',
          compatibleModelIds: [],
          incompatibleModelIds: [...preview.incompatibleModelIds],
          requestCount: 0,
          acceptedRequestCount: 0,
          reasonKey: 'noCompatibleModels',
          errorKey: '',
        }]
        continue
      }

      try {
        const batch = await startQuestionAnswerBatch(
          preview.target.targetId,
          [...preview.compatibleModelIds],
          [...snapshot.questionIds],
          snapshot.reasoningEffort,
          snapshot.repeatCount,
          controller.signal,
        )
        if (!runIsCurrent(sequence, snapshot.preferenceScope)) return
        outcomes.value = [...outcomes.value, {
          targetId: preview.target.targetId,
          accountName: preview.target.accountName,
          kind: 'started',
          compatibleModelIds: [...preview.compatibleModelIds],
          incompatibleModelIds: [...preview.incompatibleModelIds],
          requestCount: preview.requestCount,
          acceptedRequestCount: batch.submittedCount,
          reasonKey: 'started',
          errorKey: '',
        }]
        emit('question-answer-started', preview.target.targetId)
      } catch (error) {
        if (error instanceof Error && error.name === 'AbortError') return
        if (!runIsCurrent(sequence, snapshot.preferenceScope)) return
        const active = error instanceof Error
          && error.message === 'admin.connectionHealth.errors.questionAnswerActive'
        outcomes.value = [...outcomes.value, {
          targetId: preview.target.targetId,
          accountName: preview.target.accountName,
          kind: active ? 'skipped' : 'failed',
          compatibleModelIds: [...preview.compatibleModelIds],
          incompatibleModelIds: [...preview.incompatibleModelIds],
          requestCount: preview.requestCount,
          acceptedRequestCount: 0,
          reasonKey: active ? 'activeBatch' : 'startFailed',
          errorKey: active ? '' : (error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'),
        }]
      }
    }
  } finally {
    if (runController === controller) runController = null
    if (runIsCurrent(sequence, snapshot.preferenceScope)) {
      running.value = false
      if (visible.value && preparedSignature.value !== preparationSignature.value) {
        refreshPreparation()
      }
    }
  }
}

const cancelRun = () => {
  runSequence++
  runController?.abort()
  runController = null
  running.value = false
}

const outcomeReason = (outcome: BatchTargetOutcome): string => {
  switch (outcome.reasonKey) {
    case 'activeBatch': return t('admin.connectionHealth.questionAnswerBatch.outcomes.activeBatch')
    case 'noCompatibleModels': return t('admin.connectionHealth.questionAnswerBatch.outcomes.noCompatibleModels')
    case 'discoveryFailed': return t('admin.connectionHealth.questionAnswerBatch.outcomes.discoveryFailed')
    case 'startFailed': return t('admin.connectionHealth.questionAnswerBatch.outcomes.startFailed')
    default: return t('admin.connectionHealth.questionAnswerBatch.outcomes.started')
  }
}

const safeConnectionHealthError = (rawKey: string): string => (
  t(connectionHealthMessageKey(rawKey, te))
)

const shortenedTargetId = (targetId: string): string => {
  if (targetId.length <= 36) return targetId
  return `${targetId.slice(0, 20)}…${targetId.slice(-10)}`
}

const refreshPreparation = () => {
  if (!visible.value || running.value) return
  if (incompleteGroups.value.length > 0) {
    cancelPreparation()
    selectedTargetIds.value = [...props.preferences.batchTargetIds]
    previews.value = []
    preparationError.value = ''
    return
  }
  const reconciled = reconcileQuestionAnswerBatchTargetIds(props.preferences.batchTargetIds, candidates.value)
  selectedTargetIds.value = reconciled
  if (!sameIds(reconciled, props.preferences.batchTargetIds)) {
    emit('preferences-changed', { ...props.preferences, batchTargetIds: reconciled })
  }
  void prepare()
}

const open = () => {
  visible.value = true
  if (
    hasRunSnapshot.value
    && preparedSignature.value === preparationSignature.value
    && preparationReady.value
  ) return
  preparationError.value = ''
  previews.value = []
  if (!hasRunSnapshot.value) {
    outcomes.value = []
    runPreviewTotal.value = 0
    runTargetTotal.value = 0
  }
  refreshPreparation()
}

const close = () => {
  visible.value = false
  cancelPreparation(preparing.value)
}

const toggleTarget = (targetId: string) => {
  if (running.value) return
  const selected = new Set(selectedTargetIds.value)
  if (selected.has(targetId)) selected.delete(targetId)
  else selected.add(targetId)
  const nextIds = candidates.value
    .filter(target => selected.has(target.targetId))
    .map(target => target.targetId)
  selectedTargetIds.value = nextIds
  emit('preferences-changed', { ...props.preferences, batchTargetIds: nextIds })
  void prepare()
}

const selectGroupTargets = (group: AdminGroupHealth) => {
  if (running.value) return
  const selected = new Set(selectedTargetIds.value)
  for (const account of group.accounts) {
    const targetId = account.targetId.trim()
    if (targetId) selected.add(targetId)
  }
  const nextIds = candidates.value
    .filter(target => selected.has(target.targetId))
    .map(target => target.targetId)
  selectedTargetIds.value = nextIds
  emit('preferences-changed', { ...props.preferences, batchTargetIds: nextIds })
  void prepare()
}

const clearGroupTargets = (group: AdminGroupHealth) => {
  if (running.value) return
  const removedIds = new Set(group.accounts.map(account => account.targetId.trim()).filter(Boolean))
  const nextIds = candidates.value
    .filter(target => selectedTargetIds.value.includes(target.targetId) && !removedIds.has(target.targetId))
    .map(target => target.targetId)
  selectedTargetIds.value = nextIds
  emit('preferences-changed', { ...props.preferences, batchTargetIds: nextIds })
  void prepare()
}

defineExpose({ open })
watch(candidateSignature, () => {
  if (!visible.value || running.value) return
  refreshPreparation()
})
watch(selectionSignature, () => {
  if (!visible.value || running.value || incompleteGroups.value.length > 0) return
  refreshPreparation()
})
watch(() => props.preferenceScope, (scope, previousScope) => {
  if (scope === previousScope) return
  visible.value = false
  cancelPreparation()
  cancelRun()
  selectedTargetIds.value = []
  previews.value = []
  outcomes.value = []
  runPreviewTotal.value = 0
  runTargetTotal.value = 0
  startedAt.value = null
  preparedSignature.value = ''
  preparationError.value = ''
}, { flush: 'sync' })
onBeforeUnmount(() => {
  cancelPreparation()
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
    <p
      v-if="startedAt !== null"
      data-testid="question-answer-batch-entry-summary"
      class="mt-2 text-xs leading-5 text-muted-foreground"
    >
      {{ t('admin.connectionHealth.questionAnswerBatch.entrySummary', {
        time: startedAtLabel,
        processed: outcomes.length,
        total: runTargetTotal,
        started: startedCount,
        skipped: skippedCount,
        failed: failedCount,
        accepted: acceptedRequestTotal,
      }) }}
    </p>
  </div>

  <div
    v-if="visible"
    data-testid="question-answer-batch-drawer"
    class="fixed inset-0 z-50 flex justify-end bg-black/35"
    role="dialog"
    aria-modal="true"
    :aria-label="t('admin.connectionHealth.questionAnswerBatch.title')"
  >
      <section class="flex h-full w-full max-w-xl flex-col overflow-hidden border-l border-border/60 bg-background shadow-xl">
        <header class="flex items-center justify-between border-b border-border/60 px-5 py-4">
          <div>
            <h2 class="text-base font-semibold text-foreground">{{ t('admin.connectionHealth.questionAnswerBatch.title') }}</h2>
            <p class="mt-1 text-xs text-muted-foreground">{{ t('admin.connectionHealth.questionAnswerBatch.workspace', { scope: preferenceScope }) }}</p>
          </div>
          <button
            type="button"
            data-testid="question-answer-batch-close"
            class="rounded-md p-2 text-muted-foreground hover:bg-surface hover:text-foreground"
            :aria-label="t('admin.connectionHealth.questionAnswerBatch.close')"
            @click="close"
          >
            <X class="h-4 w-4" />
          </button>
        </header>

        <div data-testid="question-answer-batch-body" class="min-h-0 flex-1 space-y-4 overflow-y-auto p-5">
          <div v-if="incompleteGroups.length" class="rounded-lg border border-amber-500/30 bg-amber-500/10 p-4 text-sm text-amber-700 dark:text-amber-300">
            <p class="font-medium">{{ t('admin.connectionHealth.questionAnswerBatch.incomplete') }}</p>
            <ul class="mt-2 list-disc space-y-1 pl-5">
              <li v-for="group in incompleteGroups" :key="group.id">{{ group.name }}</li>
            </ul>
          </div>

          <template v-else>
            <div class="space-y-2 rounded-lg border border-border/50 p-3">
              <div v-for="group in groups" :key="`batch-group:${group.id}`" class="flex items-center justify-between gap-3">
                <span class="min-w-0 truncate text-xs font-medium text-foreground">{{ group.name }}</span>
                <span class="flex shrink-0 gap-2">
                  <button
                    type="button"
                    :data-testid="`question-answer-batch-group-select-${group.id}`"
                    class="text-xs text-primary hover:underline disabled:opacity-50"
                    :disabled="running"
                    @click="selectGroupTargets(group)"
                  >
                    {{ t('admin.connectionHealth.questionAnswerBatch.selectGroup') }}
                  </button>
                  <button
                    type="button"
                    :data-testid="`question-answer-batch-group-clear-${group.id}`"
                    class="text-xs text-muted-foreground hover:underline disabled:opacity-50"
                    :disabled="running"
                    @click="clearGroupTargets(group)"
                  >
                    {{ t('admin.connectionHealth.questionAnswerBatch.clearGroup') }}
                  </button>
                </span>
              </div>
            </div>

            <div class="space-y-2">
              <label
                v-for="target in candidates"
                :key="target.targetId"
                class="flex items-start gap-3 rounded-lg border border-border/50 px-3 py-3"
              >
                <input
                  type="checkbox"
                  :data-target-id="target.targetId"
                  :checked="selectedTargetIds.includes(target.targetId)"
                  :disabled="running"
                  class="mt-0.5 h-4 w-4 rounded border-border accent-primary"
                  @change="toggleTarget(target.targetId)"
                >
                <span class="min-w-0 flex-1">
                  <span
                    :data-testid="`question-answer-batch-target-name-${target.targetId}`"
                    class="block break-words text-sm font-medium text-foreground"
                  >{{ target.accountName }}</span>
                  <span class="mt-0.5 block break-words text-xs text-muted-foreground">{{ target.groupNames.join('、') }}</span>
                </span>
              </label>
            </div>

            <div v-if="sourceTarget" data-testid="question-answer-batch-source" class="rounded-lg bg-surface px-3 py-2 text-sm text-foreground">
              {{ t('admin.connectionHealth.questionAnswerBatch.source', { account: sourceTarget.accountName, targetId: shortenedTargetId(sourceTarget.targetId) }) }}
            </div>
            <div v-if="preparedQuestionIds.length" data-testid="question-answer-batch-config" class="space-y-1 rounded-lg border border-border/50 px-3 py-2 text-xs text-muted-foreground">
              <p>{{ t('admin.connectionHealth.questionAnswerBatch.configModels', { models: preparedModelIds.join('、') }) }}</p>
              <p>{{ t('admin.connectionHealth.questionAnswerBatch.configQuestions', { questions: preparedQuestionNames.join('、') }) }}</p>
              <p>{{ t('admin.connectionHealth.questionAnswerBatch.configEffort', { effort: preferences.reasoningEffort }) }}</p>
              <p>{{ t('admin.connectionHealth.questionAnswerBatch.configRepeat', { repeat: preferences.repeatCount }) }}</p>
            </div>
            <div v-if="previews.length" class="space-y-2">
              <div
                v-for="preview in previews"
                :key="`preview:${preview.target.targetId}`"
                :data-testid="`question-answer-batch-preview-${preview.target.targetId}`"
                class="rounded-lg border border-border/50 px-3 py-2 text-xs text-muted-foreground"
              >
                <p
                  :data-testid="`question-answer-batch-preview-identity-${preview.target.targetId}`"
                  class="break-all font-medium text-foreground"
                >{{ preview.target.accountName }} · {{ preview.target.targetId }}</p>
                <p>{{ t('admin.connectionHealth.questionAnswerBatch.compatibleModels', { models: preview.compatibleModelIds.join('、') || '-' }) }}</p>
                <p v-if="preview.incompatibleModelIds.length">{{ t('admin.connectionHealth.questionAnswerBatch.incompatibleModels', { models: preview.incompatibleModelIds.join('、') }) }}</p>
                <p>{{ t('admin.connectionHealth.questionAnswerBatch.requestCount', { count: preview.requestCount }) }}</p>
                <p v-if="preview.discoveryErrorKey" class="break-words text-destructive">
                  {{ t('admin.connectionHealth.questionAnswerBatch.outcomes.discoveryFailed') }}：{{ safeConnectionHealthError(preview.discoveryErrorKey) }}
                </p>
              </div>
            </div>
            <div class="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 v-if="preparing" class="h-4 w-4 animate-spin" />
              <span>{{ t('admin.connectionHealth.questionAnswerBatch.previewTotal', { total: displayedPreviewTotal }) }}</span>
            </div>
            <p v-if="preparationError" class="break-words text-sm text-destructive">{{ t(`admin.connectionHealth.questionAnswerBatch.errors.${preparationError}`) }}</p>

            <div
              v-if="running || outcomes.length > 0"
              data-testid="question-answer-batch-run-summary"
              class="space-y-3 rounded-lg border border-border/50 p-3 text-sm"
            >
              <div class="flex flex-wrap gap-x-4 gap-y-1 text-muted-foreground">
                <span data-testid="question-answer-batch-run-preview-total">{{ t('admin.connectionHealth.questionAnswerBatch.runPreviewTotal', { total: runPreviewTotal }) }}</span>
                <span>{{ t('admin.connectionHealth.questionAnswerBatch.processed', { processed: outcomes.length, total: runTargetTotal }) }}</span>
                <span>{{ t('admin.connectionHealth.questionAnswerBatch.started', { count: startedCount }) }}</span>
                <span>{{ t('admin.connectionHealth.questionAnswerBatch.skipped', { count: skippedCount }) }}</span>
                <span>{{ t('admin.connectionHealth.questionAnswerBatch.failed', { count: failedCount }) }}</span>
              </div>
              <p class="font-medium text-foreground">{{ t('admin.connectionHealth.questionAnswerBatch.acceptedTotal', { total: acceptedRequestTotal }) }}</p>
              <div class="space-y-2">
                <div
                  v-for="outcome in outcomes"
                  :key="outcome.targetId"
                  :data-testid="`question-answer-batch-outcome-${outcome.targetId}`"
                  class="rounded-md bg-surface px-3 py-2"
                >
                  <p
                    :data-testid="`question-answer-batch-outcome-name-${outcome.targetId}`"
                    class="break-words font-medium text-foreground"
                  >{{ outcome.accountName }}</p>
                  <p class="mt-0.5 break-words text-xs text-muted-foreground">{{ outcomeReason(outcome) }}</p>
                  <p v-if="outcome.errorKey" class="mt-0.5 break-words text-xs text-destructive">{{ safeConnectionHealthError(outcome.errorKey) }}</p>
                </div>
              </div>
            </div>
          </template>
        </div>

        <footer data-testid="question-answer-batch-footer" class="shrink-0 border-t border-border/60 bg-background px-5 py-4">
          <button
            type="button"
            data-testid="question-answer-batch-start"
            :disabled="!canStart"
            class="inline-flex h-9 w-full items-center justify-center rounded-lg bg-primary px-4 text-sm font-medium text-primary-foreground disabled:cursor-not-allowed disabled:opacity-50"
            @click="start"
          >
            {{ t('admin.connectionHealth.questionAnswerBatch.start') }}
          </button>
        </footer>
      </section>
  </div>
</template>
