<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ConnectionHealthApiError, addModelControlManaged, executeModelControl, previewModelControl, removeModelControlManaged, verifyModelControl } from '../../api/connectionHealth'
import type { ModelControlItem, ModelControlOperation, ModelControlPreview, ModelControlResult, ModelControlRule } from '../../types/connectionHealth'
import { formatQuestionAnswerAccuracy } from '../../utils/questionAnswers'
import { modelControlText as message, acquireModelControlBusy, releaseModelControlBusy, modelControlBusyTargets, modelControlDecision, modelControlEntryState, modelControlPendingState, modelControlReason, modelControlRoundText, modelControlState, modelControlTime } from '../../utils/questionAnswerModelControl'
import QuestionAnswerModelControlRuleForm from './QuestionAnswerModelControlRuleForm.vue'

const props = defineProps<{ item?: ModelControlItem | null; targetId: string; modelName: string; workspace: string; showCoverage?: boolean }>()
const emit = defineEmits<{ (event: 'updated', item: ModelControlItem): void; (event: 'refresh'): void; (event: 'settled', targetId: string): void; (event: 'rule-saved', rule: ModelControlRule): void; (event: 'removed'): void }>()
const currentItem = ref(props.item ?? null), error = ref(''), notice = ref(''), modifyingRule = ref(false), preview = ref<ModelControlPreview | null>(null)
const operation = ref<ModelControlOperation>('close'), confirmWithoutEvidence = ref(false), abandon = ref(false), result = ref<ModelControlResult | null>(null), busy = ref(false)
let sequence = 0, controller: AbortController | null = null, localBusy: { targetId: string; owner: symbol } | null = null
watch(() => props.item, value => { currentItem.value = value ?? null })
const invalidate = () => { if (localBusy) releaseModelControlBusy(localBusy.targetId, localBusy.owner); localBusy = null; busy.value = false; sequence++; controller?.abort(); preview.value = null; abandon.value = false; modifyingRule.value = false; error.value = ''; notice.value = ''; result.value = null }
watch(() => [props.workspace, props.targetId, props.modelName], () => { invalidate(); currentItem.value = props.item ?? null })
onBeforeUnmount(invalidate)
const item = computed(() => currentItem.value)
const pending = computed(() => item.value?.control.accountPending ?? null)
const locked = computed(() => busy.value || modelControlBusyTargets.value.has(props.targetId) || Boolean(pending.value))
const closed = computed(() => Boolean(item.value && Object.keys(item.value.control.closedEntries).length))
const unconfirmed = computed(() => item.value?.control.unconfirmedClose ?? null)
const unknown = computed(() => Boolean(item.value?.control.pending))
const testing = computed(() => item.value?.decision === 'testing')
const canClose = computed(() => item.value && ['serving', 'partially_closed'].includes(item.value.control.observation.state) && (item.value.decision === 'close_recommended' || testing.value))
const lastModel = computed(() => item.value?.control.observation.state === 'last_model')
const canCloseAccount = computed(() => lastModel.value && item.value?.control.observation.accountSchedulable === true && (item.value?.decision === 'close_recommended' || testing.value))
const removalBlocked = computed(() => locked.value || closed.value || Boolean(unconfirmed.value) || item.value?.control.observation.state === 'account_missing')
const needsEvidenceConfirmation = computed(() => operation.value === 'restore' && preview.value?.item.decision !== 'usable')
const apply = (value: ModelControlItem) => { currentItem.value = value; emit('updated', value) }
const run = async (action: (signal: AbortSignal, current: () => boolean) => Promise<void>) => {
  if (busy.value || modelControlBusyTargets.value.has(props.targetId)) return
  const generation = ++sequence, workspace = props.workspace, requestTarget = props.targetId, request = new AbortController(); controller = request; busy.value = true; error.value = ''; notice.value = ''
  const owner = acquireModelControlBusy(requestTarget)!; localBusy = { targetId: requestTarget, owner }
  const current = () => generation === sequence && workspace === props.workspace && !request.signal.aborted
  try { await action(request.signal, current) }
  catch (cause) {
    if (!current()) return
    error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request'
    if (cause instanceof ConnectionHealthApiError && cause.status === 409) {
      const authoritative = cause.current
      if (authoritative && typeof authoritative === 'object' && 'planFingerprint' in authoritative && 'item' in authoritative) { preview.value = authoritative as ModelControlPreview; confirmWithoutEvidence.value = false; apply(preview.value.item); notice.value = message('planChanged') }
      else { preview.value = null; if (authoritative && typeof authoritative === 'object' && 'control' in authoritative) apply(authoritative as ModelControlItem); emit('refresh') }
    }
  } finally {
    releaseModelControlBusy(requestTarget, owner); if (localBusy?.owner === owner) localBusy = null
    if (generation === sequence) busy.value = false
    if (controller === request) controller = null
  }
}
const add = () => run(async (signal, current) => { const value = await addModelControlManaged(props.targetId, props.modelName, signal); if (current()) { apply(value); if (value.verifyErrorKey) error.value = value.verifyErrorKey; emit('refresh'); emit('settled', props.targetId) } })
const prepare = (action: ModelControlOperation) => {
  if (locked.value || testing.value || !item.value) return
  operation.value = action; confirmWithoutEvidence.value = false; result.value = null
  return run(async (signal, current) => { const value = await previewModelControl(props.targetId, props.modelName, action, item.value!.basis, signal); if (current()) { preview.value = value; apply(value.item) } })
}
const confirm = () => {
  const plan = preview.value
  if (!plan || plan.blockReasonKey || !plan.planFingerprint || locked.value || testing.value || (needsEvidenceConfirmation.value && !confirmWithoutEvidence.value)) return
  return run(async (signal, current) => {
    const value = await executeModelControl(props.targetId, props.modelName, operation.value, plan.item.basis, plan.planFingerprint!, confirmWithoutEvidence.value, signal)
    if (!current()) return
    preview.value = null; result.value = value; apply(value.item); error.value = value.reasonKey || value.errorKey || ''; notice.value = value.outcome === 'unknown' ? message('unknownResult') : value.outcome === 'partial' ? message('partialResult') : operation.value === 'close_account' ? message('accountResult') : value.outcome === 'restored' ? message('restoredResult') : value.outcome === 'closed' ? message('closedResult') : ''
    emit('refresh'); emit('settled', props.targetId)
  })
}
const verify = () => run(async (signal, current) => {
  const value = await verifyModelControl([props.targetId], signal)
  if (!current()) return
  const refreshed = value.items.find(candidate => candidate.modelName === props.modelName && candidate.targetId === props.targetId)
  if (refreshed) apply(refreshed)
  error.value = value.errors.find(candidate => candidate.targetId === props.targetId)?.reasonKey ?? ''; emit('refresh'); emit('settled', props.targetId)
})
const remove = (abandonClosed: boolean) => {
  if (!item.value || locked.value || (!abandonClosed && removalBlocked.value)) return
  return run(async (signal, current) => { await removeModelControlManaged(props.targetId, props.modelName, item.value!.version, abandonClosed, signal); if (current()) { abandon.value = false; emit('removed'); emit('refresh'); emit('settled', props.targetId) } })
}
const ruleSaved = (rule: ModelControlRule) => { modifyingRule.value = false; emit('rule-saved', rule); emit('refresh') }
</script>
<template>
  <article data-testid="model-control-row" :data-model-name="modelName" :data-target-id="targetId" class="space-y-2 rounded-lg border border-border/50 p-3 text-xs">
    <header class="flex flex-wrap items-start justify-between gap-2"><h4 class="min-w-0 break-words font-semibold">{{ item?.accountName ? `${item.accountName} · ` : '' }}{{ modelName }}</h4><span v-if="item" class="text-primary">{{ modelControlDecision(item.decision) }}</span></header>
    <template v-if="item">
      <p v-if="item.round" class="break-words">{{ message('lastRound') }} {{ formatQuestionAnswerAccuracy(item.round.accuracyPercent) }} · {{ message('judged') }} {{ item.round.correct + item.round.incorrect }} {{ message('answers') }} · {{ modelControlRoundText(item.round) }} · {{ message('failed') }} {{ item.round.failed }} · {{ message('cancelled') }} {{ item.round.cancelled }}</p>
      <p v-if="item.previousRound" class="break-words text-muted-foreground">{{ message('previousRound') }}{{ formatQuestionAnswerAccuracy(item.previousRound.accuracyPercent) }} · {{ modelControlRoundText(item.previousRound) }}</p>
      <p v-if="item.decision === 'no_evidence'">{{ message('noEvidence') }}{{ item.decisionReason ? ` · ${modelControlReason(item.decisionReason)}` : '' }}</p>
      <p v-if="item.decision === 'awaiting_review'">{{ message('awaitingCount', { count: item.round?.unreviewed ?? 0 }) }}</p><p v-if="item.decision === 'insufficient'">{{ message('insufficientCount', { count: (item.round?.correct ?? 0) + (item.round?.incorrect ?? 0), min: item.rule.minJudgedAnswers }) }}</p>
      <p data-testid="model-control-state" class="break-words">{{ message('site') }}{{ modelControlState(item) }}<span v-if="item.control.observation.checkedAt" class="text-muted-foreground"> · {{ modelControlTime(item.control.observation.checkedAt) }}</span></p>
      <p v-if="item.control.observation.reasonKey" class="break-words text-amber-600">{{ modelControlReason(item.control.observation.reasonKey) }}</p><p v-if="item.control.conflictReason" class="break-words text-destructive">{{ modelControlReason(item.control.conflictReason) }}</p>
      <div v-if="item.control.lastAttempt" data-testid="model-control-last-attempt" class="space-y-1 text-amber-600"><p class="break-words">{{ message('lastAttempt') }}{{ modelControlReason(item.control.lastAttempt.reasonKey) }}</p><p v-for="entry in item.control.lastAttempt.entries" :key="entry.key" class="break-all">{{ entry.key }} → {{ entry.value }} · {{ modelControlEntryState(entry.state) }}</p><p v-for="source in item.control.lastAttempt.groups" :key="`${source.groupId}:${source.key}`" class="break-words">{{ source.groupName }} · {{ source.key }} · {{ message('sources') }} {{ source.count ?? message('unknown') }}</p></div>
      <div v-if="closed" class="space-y-1"><p>{{ message('closedAt') }} {{ modelControlTime(item.control.closedAt) }} · {{ message('closedAccuracy') }} {{ formatQuestionAnswerAccuracy(item.control.closedAccuracyPercent) }}</p><p v-for="source in item.control.observation.sources" :key="`${source.groupId}:${source.key}`" class="break-words" :class="source.count === 0 ? 'text-destructive' : 'text-muted-foreground'">{{ source.groupName }} · {{ source.key }} · {{ message('sources') }} {{ source.count ?? message('unknown') }}</p></div>
      <p v-if="pending" data-testid="model-control-account-pending" class="break-words text-amber-600">{{ message('accountPending', { model: pending.modelName, state: modelControlPendingState(pending.state) }) }}</p>
      <p v-if="unconfirmed" class="break-words text-amber-600">{{ message('unconfirmedExpiry', { time: modelControlTime(unconfirmed.expiresAt) }) }}</p>
      <p v-if="item.health.recentlyProbed" class="break-words text-muted-foreground">{{ message('probedHint', { state: item.health.state ?? message('unknown') }) }}</p>
      <p class="flex flex-wrap items-center gap-2 text-muted-foreground">{{ message('rule') }}{{ item.rule.minAccuracyPercent }}% · {{ message('minRound') }} {{ item.rule.minJudgedAnswers }} {{ message('answers') }} · {{ item.rule.includeManual ? message('manualIncluded') : message('manualExcluded') }} · {{ item.rule.includeScheduled ? message('scheduledIncluded') : message('scheduledExcluded') }}<button type="button" class="underline disabled:opacity-50" :disabled="locked" @click="modifyingRule = !modifyingRule">{{ message('edit') }}</button></p>
      <QuestionAnswerModelControlRuleForm v-if="modifyingRule" :rule="item.rule" :workspace="workspace" @saved="ruleSaved" @cancel="modifyingRule = false" />
      <p v-if="showCoverage" class="break-words text-muted-foreground">{{ message('coverage') }}{{ item.coverage.schedules.map(schedule => schedule.name).join('、') || message('noCoverage') }}</p>
      <p v-if="lastModel && item.control.observation.accountSchedulable === false">{{ message('accountClosed') }}<span v-if="item.decision === 'usable'">{{ message('reopenHint') }}</span></p>
      <div class="flex flex-wrap gap-2">
        <template v-if="!unknown"><button v-if="canClose" type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 disabled:opacity-50" :disabled="locked || testing" @click="prepare('close')">{{ message('close') }}</button><button v-if="closed && item.control.observation.state !== 'account_missing'" type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 disabled:opacity-50" :disabled="locked || testing || Boolean(item.control.conflictReason)" @click="prepare('restore')">{{ message('restore') }}</button><button v-if="canCloseAccount" type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 disabled:opacity-50" :disabled="locked || testing" @click="prepare('close_account')">{{ message('closeAccount') }}</button></template>
        <button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 disabled:opacity-50" :disabled="busy || modelControlBusyTargets.has(targetId)" @click="verify">{{ message('verify') }}</button>
        <template v-if="!unknown"><button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 disabled:opacity-50" :disabled="removalBlocked" :title="closed ? message('closedRemoval') : unconfirmed ? message('unconfirmedExpiry', { time: modelControlTime(unconfirmed.expiresAt) }) : ''" @click="remove(false)">{{ message('remove') }}</button><button type="button" class="rounded-md border border-destructive/30 px-2.5 py-1.5 text-destructive disabled:opacity-50" :disabled="locked" @click="abandon = true">{{ message('abandon') }}</button></template>
      </div><p v-if="closed" class="text-muted-foreground">{{ message('closedRemovalHint') }}</p>
    </template>
    <button v-else type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 disabled:opacity-50" :disabled="locked" @click="add">{{ message('add') }}</button>
    <p v-if="error" role="alert" class="break-words text-destructive">{{ modelControlReason(error) }}</p><p v-if="notice" role="status" class="break-words text-primary">{{ notice }}</p>
    <ul v-if="result?.entries?.length" data-testid="model-control-entry-results" class="space-y-1"><li v-for="entry in result.entries" :key="entry.key" class="break-all">{{ entry.key }} → {{ entry.value }} · {{ modelControlEntryState(entry.state) }}</li></ul><p v-for="hint in result?.hintKeys ?? []" :key="hint" class="break-words text-amber-600">{{ modelControlReason(hint) }}</p>
    <div v-if="preview" data-testid="model-control-preview" role="dialog" :aria-label="message('previewTitle')" class="space-y-2 rounded-lg border border-border/60 bg-surface-line/30 p-3">
      <p class="font-medium">{{ operation === 'restore' ? message('restore') : operation === 'close_account' ? message('closeAccount') : message('close') }} · {{ message('preview') }}</p><ul><li v-for="entry in preview.entries" :key="entry.key" class="break-all">{{ entry.key }} → {{ entry.value }} · {{ modelControlEntryState(entry.state) }}</li></ul><p v-for="source in preview.groups" :key="`${source.groupId}:${source.key}`" class="break-words" :class="source.ok === false ? 'text-destructive' : ''">{{ source.groupName }} · {{ source.key }} · {{ message('sources') }} {{ source.count ?? message('unknown') }}</p>
      <p v-if="operation === 'restore'">{{ message('requestHealth') }}{{ preview.requestHealth.checked ? (preview.requestHealth.allowed ? message('restoreAllowed') : modelControlReason(preview.requestHealth.reasonKey)) : preview.noRemoteWrite ? message('noHealthCheck') : message('healthNotChecked') }}</p><p v-if="preview.blockReasonKey" role="alert" class="break-words text-destructive">{{ modelControlReason(preview.blockReasonKey) }}</p><p v-if="operation === 'close_account'">{{ message('accountActionHint') }}</p>
      <label v-if="needsEvidenceConfirmation" class="block"><input v-model="confirmWithoutEvidence" type="checkbox" /> {{ message('restoreOverride') }}</label>
      <div class="flex gap-2"><button v-if="!preview.blockReasonKey && preview.planFingerprint" type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 disabled:opacity-50" :disabled="locked || testing || (needsEvidenceConfirmation && !confirmWithoutEvidence)" @click="confirm">{{ message('confirm') }}</button><button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5" :disabled="busy" @click="preview = null">{{ message('cancelPreview') }}</button></div>
    </div>
    <div v-if="abandon && item" data-testid="model-control-abandon" role="dialog" :aria-label="message('abandonTitle')" class="space-y-2 rounded-lg border border-destructive/30 p-3"><p>{{ message('abandonHint') }}</p><ul><li v-for="(value, key) in item.control.closedEntries" :key="`closed:${key}`" class="break-all">{{ key }} → {{ value }} · {{ message('closed') }}</li><li v-for="(value, key) in unconfirmed?.entries ?? {}" :key="`unconfirmed:${key}`" class="break-all">{{ key }} → {{ value }} · {{ message('unconfirmed') }}</li></ul><div class="flex gap-2"><button type="button" class="rounded-md border border-destructive/30 px-2.5 py-1.5 text-destructive disabled:opacity-50" :disabled="locked" @click="remove(true)">{{ message('confirmAbandon') }}</button><button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5" :disabled="busy" @click="abandon = false">{{ message('cancel') }}</button></div></div>
  </article>
</template>
