<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { getModelControlTarget, verifyModelControl } from '../../api/connectionHealth'
import type { ModelControlItem } from '../../types/connectionHealth'
import { createModelControlVerifyState, modelControlItemCounts, modelControlText as message, modelControlReason, modelControlRuleRevision } from '../../utils/questionAnswerModelControl'
import QuestionAnswerModelControlHeader from './QuestionAnswerModelControlHeader.vue'
import QuestionAnswerModelControlRow from './QuestionAnswerModelControlRow.vue'
const props = defineProps<{ targetId: string; refreshKey: number; availableModels?: string[] | null }>()
const emit = defineEmits<{ (event: 'settled', targetId: string): void; (event: 'managed-change', value: { targetId: string; kind: 'loaded' | 'load_failed' | 'updated'; models: string[]; refreshKey: number }): void; (event: 'view-round', batchId: string): void }>()
const items = ref<ModelControlItem[]>([]), loading = ref(false), error = ref('')
let disposed = false, sequence = 0, scope = 0, automaticVerifyDone = false, controller: AbortController | null = null
const verifyControllers = new Set<AbortController>()
const verifyState = createModelControlVerifyState({ verify: async ids => { const request = new AbortController(); verifyControllers.add(request); try { return await verifyModelControl(ids, request.signal) } finally { verifyControllers.delete(request) } } })
const issue = computed(() => verifyState.issues.value.get(props.targetId))
const stale = computed(() => Boolean(issue.value && !issue.value.reasonKey.endsWith('Busy') && !issue.value.reasonKey.endsWith('Processing')))
const changed = (kind: 'loaded' | 'load_failed' | 'updated', refreshKey = props.refreshKey) => emit('managed-change', { targetId: props.targetId, kind, models: items.value.map(item => item.modelName), refreshKey })
const update = (value: ModelControlItem) => { if (value.targetId !== props.targetId) return; items.value = items.value.some(item => item.modelName === value.modelName) ? items.value.map(item => item.modelName === value.modelName ? value : item) : [...items.value, value] }
const reread = async (targetId = props.targetId) => {
  const generation = scope
  const result = await verifyState.verifyNow([targetId])
  if (disposed || generation !== scope || targetId !== props.targetId) return
  result.items.forEach(update); changed('updated'); emit('settled', targetId)
}
const load = async () => {
  if (disposed) return
  const revision = ++sequence, targetId = props.targetId, refreshKey = props.refreshKey, request = new AbortController(); controller?.abort(); controller = request; loading.value = true; error.value = ''
  const current = () => revision === sequence && targetId === props.targetId && !disposed && !request.signal.aborted
  try {
    const result = await getModelControlTarget(targetId, request.signal)
    if (!current()) return
    items.value = result.items; changed('loaded', refreshKey)
    if (!automaticVerifyDone) { automaticVerifyDone = true; if (result.items.length) void reread(targetId) }
  } catch (cause) { if (current()) { error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request'; changed('load_failed', refreshKey) } }
  finally { if (current()) loading.value = false; if (controller === request) controller = null }
}
let loadScheduled = false
const requestLoad = () => { if (loadScheduled) return; loadScheduled = true; queueMicrotask(() => { loadScheduled = false; if (!disposed) void load() }) }
const rowUpdated = (value: ModelControlItem) => { const interrupted = Boolean(controller); sequence++; controller?.abort(); loading.value = false; update(value); changed('updated'); if (interrupted) requestLoad() }
const removed = (model: string) => { items.value = items.value.filter(item => item.modelName !== model); changed('updated') }
watch(() => props.targetId, () => { scope++; sequence++; controller?.abort(); for (const request of verifyControllers) request.abort(); verifyState.dispose(); items.value = []; automaticVerifyDone = false; void load() }, { immediate: true })
watch(() => props.refreshKey, requestLoad)
watch(modelControlRuleRevision, value => { if (value.workspace === props.targetId.split(':')[1]) requestLoad() })
onBeforeUnmount(() => { disposed = true; scope++; sequence++; controller?.abort(); for (const request of verifyControllers) request.abort(); verifyState.dispose() })
</script>
<template>
  <section data-testid="model-control-panel" class="mb-3 space-y-3 rounded-lg border border-border/50 p-3">
    <header class="flex items-center justify-between gap-2"><h3 class="text-xs font-semibold">{{ message('title') }}</h3><button type="button" class="text-xs text-muted-foreground underline" :disabled="loading" @click="load">{{ message('refresh') }}</button></header>
    <QuestionAnswerModelControlHeader scope="account" :workspace="targetId.split(':')[1] ?? ''" :counts="modelControlItemCounts(items)" :counts-stale="stale" />
    <p v-if="error" role="alert" class="text-xs text-destructive">{{ message('listReadFailed') }} · {{ modelControlReason(error) }} · <button type="button" class="underline" @click="load">{{ message('retry') }}</button></p><p v-if="loading && !items.length" class="text-xs text-muted-foreground">{{ message('loading') }}</p><p v-else-if="!items.length && !error" class="text-xs text-muted-foreground">{{ message('emptyCandidates') }}</p>
    <div class="grid gap-3 [grid-template-columns:repeat(auto-fill,minmax(min(18rem,100%),1fr))]"><QuestionAnswerModelControlRow v-for="item in items" :key="item.modelName" :item="item" :target-id="targetId" :model-name="item.modelName" :workspace="targetId.split(':')[1] ?? ''" :available-models="availableModels" :issue="issue" :execute-unconfirmed="verifyState.isUnconfirmed(targetId, item.modelName)" :account-unconfirmed="verifyState.accountUnconfirmed(targetId)" @updated="rowUpdated" @refresh="requestLoad" @removed="removed(item.modelName)" @settled="emit('settled', $event)" @verify-now="reread" @execute-unconfirmed="verifyState.markUnconfirmed" @view-round="emit('view-round', $event)" /></div>
  </section>
</template>
