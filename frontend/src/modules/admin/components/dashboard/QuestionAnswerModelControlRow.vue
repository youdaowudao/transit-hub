<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Tooltip } from '@/components/ui/tooltip'
import { t } from '@/locales'
import { ConnectionHealthApiError, executeModelControl, previewModelControl, removeModelControlManaged } from '../../api/connectionHealth'
import type { ModelControlItem, ModelControlOperation, ModelControlPreview, ModelControlResult } from '../../types/connectionHealth'
import { formatQuestionAnswerAccuracy } from '../../utils/questionAnswers'
import { modelControlText as message, acquireModelControlBusy, releaseModelControlBusy, modelControlBusyTargets, modelControlCardView, modelControlEntryState, modelControlReason, modelControlRoundText, modelControlTime, addManagedModel, type ModelControlVerifyIssue } from '../../utils/questionAnswerModelControl'
const props = defineProps<{ item?: ModelControlItem | null; targetId: string; modelName: string; workspace: string; showCoverage?: boolean; showAccount?: boolean; availableModels?: string[] | null; issue?: ModelControlVerifyIssue; executeUnconfirmed?: boolean; accountUnconfirmed?: boolean }>()
const emit = defineEmits<{ (event: 'updated', item: ModelControlItem): void; (event: 'refresh'): void; (event: 'settled', targetId: string): void; (event: 'removed'): void; (event: 'verify-now', targetId: string): void; (event: 'execute-unconfirmed', targetId: string, model: string): void; (event: 'view-round', batchId: string): void }>()
const currentItem = ref(props.item ?? null), error = ref(''), notice = ref(''), preview = ref<ModelControlPreview | null>(null)
const operation = ref<ModelControlOperation>('close'), confirmWithoutEvidence = ref(false), removing = ref(false), result = ref<ModelControlResult | null>(null), busy = ref(false)
let sequence = 0, controller: AbortController | null = null, localBusy: { targetId: string; owner: symbol } | null = null
watch(() => props.item, value => { currentItem.value = value ?? null; if (error.value.endsWith('LeaseLost')) error.value = '' })
const invalidate = () => { if (localBusy) releaseModelControlBusy(localBusy.targetId, localBusy.owner); localBusy = null; busy.value = false; sequence++; controller?.abort(); preview.value = null; removing.value = false; error.value = ''; notice.value = ''; result.value = null }
watch(() => [props.workspace, props.targetId, props.modelName], () => { invalidate(); currentItem.value = props.item ?? null })
onBeforeUnmount(invalidate)
const item = computed(() => currentItem.value)
const view = computed(() => item.value ? modelControlCardView(item.value, props) : null)
const locked = computed(() => busy.value || modelControlBusyTargets.value.has(props.targetId) || Boolean(item.value?.control.accountPending || item.value?.control.pending || props.accountUnconfirmed))
const closed = computed(() => Boolean(item.value && Object.keys(item.value.control.closedEntries).length))
const unconfirmed = computed(() => item.value?.control.unconfirmedClose ?? null)
const testing = computed(() => item.value?.decision === 'testing')
const missingAccount = computed(() => item.value?.control.observation.state === 'account_missing')
const needsEvidenceConfirmation = computed(() => operation.value === 'restore' && preview.value?.item.decision !== 'usable')
const actionAllowed = (action: ModelControlOperation) => Boolean(item.value && !locked.value && !testing.value && !missingAccount.value && (!(action === 'restore' || action === 'add') || !item.value.control.conflictReason))
const showReread = computed(() => view.value?.showReread || error.value.endsWith('LeaseLost'))
const toneClass = computed(() => view.value?.tone === 'red' ? 'border-red-500/60 bg-red-500/20 text-red-700 dark:text-red-400' : view.value?.tone === 'green' ? 'border-green-500/35 bg-green-500/10 text-green-700 dark:text-green-400' : 'border-border/50')
const labelClass = computed(() => view.value?.tone === 'red' ? 'bg-red-500/15 text-red-700 dark:text-red-400' : view.value?.tone === 'green' ? 'bg-green-500/15 text-green-700 dark:text-green-400' : 'bg-muted text-muted-foreground')
const apply = (value: ModelControlItem) => { currentItem.value = value; emit('updated', value) }
const run = async (action: (signal: AbortSignal, current: () => boolean) => Promise<void>, executing = false) => {
  if (busy.value || modelControlBusyTargets.value.has(props.targetId)) return
  const generation = ++sequence, workspace = props.workspace, requestTarget = props.targetId, model = props.modelName, request = new AbortController(); controller = request; busy.value = true; error.value = ''; notice.value = ''
  const owner = acquireModelControlBusy(requestTarget)!
  localBusy = { targetId: requestTarget, owner }
  const current = () => generation === sequence && workspace === props.workspace && requestTarget === props.targetId && model === props.modelName && !request.signal.aborted
  try { await action(request.signal, current) }
  catch (cause) {
    if (!current()) return
    error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request'
    if (cause instanceof ConnectionHealthApiError && cause.status === 409) {
      if (cause.message.endsWith('Busy')) error.value = 'admin.connectionHealth.modelControl.text.operationBusy'
      const authoritative = cause.current
      if (authoritative && typeof authoritative === 'object' && 'planFingerprint' in authoritative && 'item' in authoritative) { preview.value = authoritative as ModelControlPreview; confirmWithoutEvidence.value = false; apply(preview.value.item); notice.value = message('planChanged') }
      else { preview.value = null; if (authoritative && typeof authoritative === 'object' && 'control' in authoritative) apply(authoritative as ModelControlItem); emit('refresh') }
    } else if (executing) { preview.value = null; emit('execute-unconfirmed', requestTarget, model); emit('refresh') }
  } finally {
    releaseModelControlBusy(requestTarget, owner); if (localBusy?.owner === owner) localBusy = null
    if (generation === sequence) busy.value = false
    if (controller === request) controller = null
  }
}
const add = () => run(async (signal, current) => { const value = await addManagedModel(props.targetId, props.modelName, signal); if (current()) { apply(value); if (value.verifyErrorKey) error.value = value.verifyErrorKey; emit('refresh'); emit('settled', props.targetId) } })
const prepare = (action: ModelControlOperation) => {
  if (!actionAllowed(action) || !item.value) return
  operation.value = action; confirmWithoutEvidence.value = false; result.value = null
  return run(async (signal, current) => { const value = await previewModelControl(props.targetId, props.modelName, action, item.value!.basis, signal); if (current()) { preview.value = value; apply(value.item) } })
}
const confirm = () => {
  const plan = preview.value
  if (!plan || plan.blockReasonKey || !plan.planFingerprint || !actionAllowed(operation.value) || (needsEvidenceConfirmation.value && !confirmWithoutEvidence.value)) return
  return run(async (signal, current) => {
    const value = await executeModelControl(props.targetId, props.modelName, operation.value, plan.item.basis, plan.planFingerprint!, confirmWithoutEvidence.value, signal)
    if (!current()) return
    preview.value = null; result.value = value; apply(value.item); error.value = value.reasonKey || value.errorKey || ''
    notice.value = value.outcome === 'partial' ? message('partialResult') : value.outcome === 'restored' || value.outcome === 'added' ? message('restoredResult') : value.outcome === 'closed' ? message('closedResult') : ''
    if (value.outcome === 'unknown') emit('execute-unconfirmed', props.targetId, props.modelName)
    emit('refresh'); emit('settled', props.targetId)
  }, true)
}
const remove = (abandonClosed: boolean) => {
  if (!item.value || locked.value) return
  return run(async (signal, current) => { await removeModelControlManaged(props.targetId, props.modelName, item.value!.version, abandonClosed, signal); if (current()) { removing.value = false; emit('removed'); emit('refresh'); emit('settled', props.targetId) } })
}
const startRemove = () => { if (locked.value) return; if (closed.value || unconfirmed.value || missingAccount.value) removing.value = true; else void remove(false) }
const sourceSentence = (source: { groupName: string; key: string; count: number | null }) => source.count === 0 ? message('sourceEmpty', { key: source.key }) : message('sourceAfterClose', { group: source.groupName, key: source.key, count: source.count ?? message('unknown') })
const previewEntry = (entry: { key: string; value: string; state: string }) => {
  if (entry.state === 'blocking' || ['manually_restored', 'manually_changed'].includes(entry.state)) return `${entry.key} → ${entry.value} · ${modelControlEntryState(entry.state)}`
  return operation.value === 'add' ? message('previewAddEntry', { model: entry.key }) : message(operation.value === 'restore' ? 'previewRestoreEntry' : 'previewCloseEntry', entry)
}
const noEvidenceReason = computed(() => item.value?.decisionReason === 'not_today' ? t('admin.connectionHealth.modelControl.reasons.not_today', { time: modelControlTime(item.value.round?.createdAt) }) : modelControlReason(item.value?.decisionReason))
</script>
<template>
  <article data-testid="model-control-row" :data-model-name="modelName" :data-target-id="targetId" class="space-y-2 rounded-lg border p-3 text-xs" :class="toneClass">
    <p v-if="showAccount && item?.accountName" class="break-words text-xs text-muted-foreground">{{ item.accountName }}</p>
    <header class="flex items-start justify-between gap-2"><h4 class="min-w-0 break-all text-sm font-semibold">{{ modelName }}</h4><span v-if="view" class="shrink-0 rounded-full px-2 py-0.5 text-xs font-semibold" :class="labelClass">{{ view.label }}</span></header>
    <template v-if="item && view">
      <p class="text-2xl font-semibold tabular-nums" :class="view.tone === 'neutral' ? 'text-muted-foreground' : ''">{{ item.decision === 'no_evidence' ? '—' : formatQuestionAnswerAccuracy(item.round?.accuracyPercent ?? null) }}</p>
      <p v-if="item.decision === 'no_evidence' && item.round" class="text-muted-foreground">{{ message('lastDate', { time: modelControlTime(item.round.createdAt) }) }}</p>
      <p v-if="item.round" class="break-words text-muted-foreground">{{ message('judged') }} {{ item.round.correct + item.round.incorrect }}/{{ item.rule.minJudgedAnswers }} {{ message('answers') }} · {{ modelControlRoundText(item.round) }}<template v-if="item.round.failed"> · {{ message('failed') }} {{ item.round.failed }} · <Tooltip :text="message('viewBatch')"><button type="button" class="underline" @click="emit('view-round', item.round.batchId)">{{ message('lookFailure') }}</button></Tooltip></template><template v-if="item.round.cancelled"> · {{ message('cancelled') }} {{ item.round.cancelled }}</template></p>
      <p v-if="item.decision === 'no_evidence'" class="break-words text-muted-foreground">{{ message('noEvidence', { reason: noEvidenceReason }) }}</p><p v-if="item.decision === 'testing'" class="text-muted-foreground">{{ t('admin.connectionHealth.modelControl.reasons.running') }}</p>
      <p v-if="item.decision === 'awaiting_review'" class="text-muted-foreground">{{ message('awaitingCount', { count: item.round?.unreviewed ?? 0 }) }}</p><p v-if="item.decision === 'insufficient'" class="text-muted-foreground">{{ message('insufficientCount', { count: (item.round?.correct ?? 0) + (item.round?.incorrect ?? 0), min: item.rule.minJudgedAnswers }) }}<template v-if="item.round?.failed">（{{ item.round.failed }} 条请求失败，点“看原因”查看）</template></p>
      <p data-testid="model-control-state" class="break-words text-xs text-foreground"><span class="font-semibold">{{ view.stateSentence }}</span><Tooltip v-if="showReread" :text="message('rereadTooltip')" wide><button type="button" class="ml-1 underline" @click="emit('verify-now', targetId)">{{ message('reread') }}</button></Tooltip></p>
      <p v-if="item.control.conflictReason" class="break-words text-destructive">{{ modelControlReason(item.control.conflictReason) }}</p>
      <p v-if="availableModels && !availableModels.includes(modelName)" class="break-words text-amber-600">{{ message('missingAvailable') }}</p>
      <div v-if="item.control.lastAttempt" data-testid="model-control-last-attempt" class="space-y-1 text-amber-600"><p class="break-words">{{ message('lastAttempt') }}{{ modelControlReason(item.control.lastAttempt.reasonKey) }}</p><p v-for="entry in item.control.lastAttempt.entries" :key="entry.key" class="break-all">{{ entry.key }} → {{ entry.value }} · {{ modelControlEntryState(entry.state) }}</p><p v-for="source in item.control.lastAttempt.groups" :key="`${source.groupId}:${source.key}`" class="break-words" :class="source.count === 0 ? 'text-destructive' : ''">{{ sourceSentence(source) }}</p></div>
      <div v-if="closed" class="space-y-1 text-muted-foreground"><p>{{ modelControlTime(item.control.closedAt) }} 关闭，当时正确率 {{ formatQuestionAnswerAccuracy(item.control.closedAccuracyPercent) }}</p><p v-for="source in item.control.observation.sources" :key="`${source.groupId}:${source.key}`" class="break-words" :class="source.count === 0 ? 'text-destructive' : ''">{{ sourceSentence(source) }}</p></div>
      <p v-if="item.control.accountPending" data-testid="model-control-account-pending" class="break-words text-amber-600">{{ message('accountPending') }}</p>
      <p v-if="unconfirmed" class="break-words text-amber-600">{{ message('unconfirmedExpiry', { time: modelControlTime(unconfirmed.expiresAt) }) }}</p>
      <p v-if="view.primaryAction === 'close' && item.health.recentlyProbed" class="break-words text-muted-foreground">{{ message('probedHint', { state: item.health.state ? t(`admin.connectionHealth.stateLabels.${item.health.state}`) : message('unknown') }) }}</p>
      <p v-if="showCoverage" class="break-words text-muted-foreground">{{ message('coverage') }}{{ item.coverage.schedules.map(schedule => schedule.name).join('、') || message('noCoverage') }}</p>
      <p v-if="item.control.observation.state === 'last_model'" class="break-words text-muted-foreground">{{ message('lastModelHint') }}<template v-if="item.control.observation.accountSchedulable === false && item.decision === 'usable'">{{ message('accountClosed') }}{{ message('reopenHint') }}</template></p>
      <div class="flex flex-wrap gap-2"><Tooltip v-if="view.primaryAction" :text="message(view.primaryAction === 'close' ? 'closeTooltip' : 'openTooltip')" wide><button type="button" class="rounded-md border border-border/60 bg-background/50 px-2.5 py-1.5 text-foreground disabled:opacity-50" :disabled="locked" @click="prepare(view.primaryAction)">{{ message(view.primaryAction === 'close' ? 'close' : 'restore') }}</button></Tooltip><Tooltip :text="message(locked ? 'lockedTooltip' : 'removeTooltip')" wide><button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 text-foreground disabled:opacity-50" :disabled="locked" @click="startRemove">{{ message('remove') }}</button></Tooltip></div>
    </template>
    <button v-else type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 disabled:opacity-50" :disabled="locked" @click="add">{{ message('add') }}</button>
    <p v-if="error" role="alert" class="break-words text-destructive">{{ modelControlReason(error) }}</p><p v-if="notice" role="status" class="break-words text-primary">{{ notice }}</p>
    <ul v-if="result?.entries?.length" data-testid="model-control-entry-results" class="space-y-1"><li v-for="entry in result.entries" :key="entry.key" class="break-all">{{ entry.key }} → {{ entry.value }} · {{ modelControlEntryState(entry.state) }}</li></ul><p v-for="hint in result?.hintKeys ?? []" :key="hint" class="break-words text-amber-600">{{ modelControlReason(hint) }}</p>
    <div v-if="preview" data-testid="model-control-preview" role="dialog" :aria-label="message('previewTitle')" class="space-y-2 rounded-lg border border-border/60 bg-surface-line/30 p-3 text-foreground"><p class="font-medium">{{ message(operation === 'close' ? 'previewCloseTitle' : 'previewOpenTitle', { model: modelName }) }}</p><ul><li v-for="entry in preview.entries" :key="entry.key" class="break-all">{{ previewEntry(entry) }}</li></ul><p v-for="source in preview.groups" :key="`${source.groupId}:${source.key}`" class="break-words" :class="source.ok === false ? 'text-destructive' : ''">{{ sourceSentence(source) }}</p>
      <p v-if="operation === 'restore' || operation === 'add'">{{ message('requestHealth') }}{{ preview.requestHealth.checked ? (preview.requestHealth.allowed ? message('restoreAllowed') : modelControlReason(preview.requestHealth.reasonKey)) : preview.noRemoteWrite ? message('noHealthCheck') : message('healthNotChecked') }}</p><p v-if="preview.blockReasonKey" role="alert" class="break-words text-destructive">{{ modelControlReason(preview.blockReasonKey) }}</p>
      <label v-if="needsEvidenceConfirmation" class="block"><input v-model="confirmWithoutEvidence" type="checkbox" :disabled="locked" /> {{ message('restoreOverride') }}</label>
      <div class="flex gap-2"><Tooltip v-if="!preview.blockReasonKey && preview.planFingerprint" :text="message(operation === 'close' ? 'closeTooltip' : 'openTooltip')" wide><button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 disabled:opacity-50" :disabled="!actionAllowed(operation) || (needsEvidenceConfirmation && !confirmWithoutEvidence)" @click="confirm">{{ message('confirm') }}</button></Tooltip><Tooltip :text="message('cancelPreview')"><button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5" :disabled="busy" @click="preview = null">{{ message('cancelPreview') }}</button></Tooltip></div>
    </div>
    <div v-if="removing && item" data-testid="model-control-remove-confirm" role="dialog" :aria-label="message(missingAccount ? 'removeMissingTitle' : 'removeClosedTitle')" class="space-y-2 rounded-lg border border-destructive/30 p-3 text-foreground"><p class="font-medium">{{ message(missingAccount ? 'removeMissingTitle' : 'removeClosedTitle') }}</p><p>{{ message(missingAccount ? 'removeMissingHint' : 'removeClosedHint') }}</p><ul v-if="!missingAccount"><li v-for="(value, key) in item.control.closedEntries" :key="`closed:${key}`" class="break-all">{{ key }} → {{ value }} · {{ message('closed') }}</li><li v-for="(value, key) in unconfirmed?.entries ?? {}" :key="`unconfirmed:${key}`" class="break-all">{{ key }} → {{ value }} · {{ message('unconfirmed') }}</li></ul><div class="flex gap-2"><Tooltip :text="message('removeTooltip')" wide><button type="button" class="rounded-md border border-destructive/30 px-2.5 py-1.5 text-destructive disabled:opacity-50" :disabled="locked" @click="remove(true)">{{ message(missingAccount ? 'confirmRemove' : 'confirmRemoveClosed') }}</button></Tooltip><Tooltip :text="message('cancel')"><button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5" :disabled="busy" @click="removing = false">{{ message('cancel') }}</button></Tooltip></div></div>
  </article>
</template>
