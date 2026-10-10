<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useDocumentVisibility } from '@vueuse/core'
import { X } from 'lucide-vue-next'
import { getModelControlVerifyTargets, listModelControlEvents, listModelControlItems, verifyModelControl } from '../../api/connectionHealth'
import { canOpenManualProbeHistory } from '../../composables/useConnectionHealth'
import type { AdminGroupHealth, ModelControlCounts, ModelControlEvent, ModelControlItem } from '../../types/connectionHealth'
import { createModelControlVerifyState, modelControlText as message, modelControlReason, modelControlTime, modelControlRuleRevision, modelControlEventLines } from '../../utils/questionAnswerModelControl'
import QuestionAnswerModelControlHeader from './QuestionAnswerModelControlHeader.vue'
import QuestionAnswerModelControlRow from './QuestionAnswerModelControlRow.vue'
const props = defineProps<{ groups: AdminGroupHealth[]; workspace: string; platform: string; refreshKey?: number }>()
const emit = defineEmits<{ (event: 'question-answer-view', value: { targetId: string }): void; (event: 'batch-view', value: { targetId: string; batchId: string; accountName: string; platform: string; groupName: string; expandFailed: true }): void; (event: 'settled', targetIds: string[]): void }>()
const visibility = useDocumentVisibility(), visible = ref(false), view = ref<'attention' | 'all' | 'events'>('attention'), modelName = ref(''), page = ref(1), totalPages = ref(0)
const items = ref<ModelControlItem[]>([]), events = ref<ModelControlEvent[]>([]), counts = ref<ModelControlCounts | null>(null), loading = ref(false), error = ref(''), verifiedCount = ref(0), verifyTotal = ref(0), verifying = ref(false)
const accountNames = new Map<string, string>(), verifyControllers = new Set<AbortController>()
let lifecycle = 0, revision = 0, automaticVerifyDone = false, timer: ReturnType<typeof setTimeout> | null = null, readController: AbortController | null = null, inventoryController: AbortController | null = null
const verifyState = createModelControlVerifyState({ verify: async ids => { const request = new AbortController(); verifyControllers.add(request); try { return await verifyModelControl(ids, request.signal) } finally { verifyControllers.delete(request) } } })
const failures = computed(() => [...verifyState.issues.value].map(([targetId, issue]) => ({ targetId, ...issue })))
const countsStale = computed(() => failures.value.some(issue => !issue.reasonKey.endsWith('Busy') && !issue.reasonKey.endsWith('Processing')))
const clearTimer = () => { if (timer !== null) clearTimeout(timer); timer = null }
const invalidate = () => { lifecycle++; revision++; readController?.abort(); inventoryController?.abort(); for (const request of verifyControllers) request.abort(); clearTimer(); loading.value = false; verifying.value = false; verifyState.dispose() }
const poll = () => { clearTimer(); if (visible.value && visibility.value === 'visible') timer = setTimeout(() => { void load() }, 30000) }
const remember = (values: ModelControlItem[]) => { for (const value of values) accountNames.set(value.targetId, value.accountName) }
const update = (value: ModelControlItem) => { remember([value]); items.value = items.value.map(item => item.targetId === value.targetId && item.modelName === value.modelName ? value : item) }
const reread = async (targetId: string) => {
  const generation = lifecycle, workspace = props.workspace
  const result = await verifyState.verifyNow([targetId])
  if (generation !== lifecycle || workspace !== props.workspace || !visible.value) return
  result.items.forEach(update); emit('settled', [targetId]); void load()
}
const verifyIds = async (ids: string[], generation: number, workspace: string) => {
  verifiedCount.value = 0; verifyTotal.value = ids.length; verifying.value = true
  const current = () => generation === lifecycle && workspace === props.workspace && visible.value
  try {
    for (let offset = 0; offset < ids.length && current(); offset += 50) {
      const chunk = ids.slice(offset, offset + 50), result = await verifyState.verifyNow(chunk)
      if (!current()) return
      result.items.forEach(update); verifiedCount.value += chunk.length; emit('settled', chunk)
    }
  } finally { if (current()) verifying.value = false }
}
const verifyOpening = async () => {
  const generation = lifecycle, workspace = props.workspace, request = new AbortController(); inventoryController = request
  try {
    const result = await getModelControlVerifyTargets(request.signal)
    if (generation !== lifecycle || workspace !== props.workspace || !visible.value || request.signal.aborted) return
    await verifyIds([...new Set(result.targetIds)].sort(), generation, workspace)
  } catch (cause) {
    if (generation !== lifecycle || workspace !== props.workspace || !visible.value || request.signal.aborted) return
    error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request'
  } finally { if (inventoryController === request) inventoryController = null }
}
const load = async () => {
  if (!visible.value || visibility.value !== 'visible') return
  clearTimer(); readController?.abort(); const generation = lifecycle, read = ++revision, workspace = props.workspace, request = new AbortController(); readController = request; loading.value = true; error.value = ''
  const current = () => generation === lifecycle && read === revision && workspace === props.workspace && visible.value && !request.signal.aborted
  try {
    if (view.value === 'events') { const result = await listModelControlEvents('', modelName.value, page.value, request.signal); if (current()) { events.value = result.items; totalPages.value = result.totalPages } }
    else { const result = await listModelControlItems(view.value, modelName.value, page.value, request.signal); if (current()) { items.value = result.items; remember(result.items); counts.value = result.counts; totalPages.value = result.totalPages; if (!automaticVerifyDone) { automaticVerifyDone = true; void verifyOpening() } } }
  } catch (cause) { if (current()) error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request' }
  finally { if (current()) { loading.value = false; poll() }; if (readController === request) readController = null }
}
const open = () => { if (props.platform !== 'sub2api' || visible.value) return; visible.value = true; view.value = 'attention'; page.value = 1; modelName.value = ''; automaticVerifyDone = false; verifiedCount.value = 0; verifyTotal.value = 0; void load() }
const close = () => { visible.value = false; invalidate() }
const selectView = (next: typeof view.value) => { view.value = next; page.value = 1; error.value = ''; void load() }
let loadScheduled = false
const requestLoad = () => { if (loadScheduled) return; loadScheduled = true; queueMicrotask(() => { loadScheduled = false; void load() }) }
const rowUpdated = (value: ModelControlItem) => { const interrupted = Boolean(readController); revision++; readController?.abort(); loading.value = false; update(value); if (interrupted) requestLoad() }
const projection = (targetId: string) => props.groups.flatMap(group => group.accounts.filter(account => account.targetId === targetId).map(account => ({ group, account })))[0]
const canOpen = (targetId: string) => { const value = projection(targetId); return Boolean(value && canOpenManualProbeHistory(value.account)) }
const accountName = (targetId: string) => accountNames.get(targetId) || projection(targetId)?.account.name || targetId.slice(-6)
const viewBatch = (item: ModelControlItem) => { if (!item.round) return; const value = projection(item.targetId); emit('batch-view', { targetId: item.targetId, batchId: item.round.batchId, accountName: item.accountName, platform: value?.account.platform ?? 'sub2api', groupName: value?.group.name ?? '', expandFailed: true }) }
watch(() => props.workspace, () => { close(); items.value = []; events.value = []; counts.value = null; accountNames.clear(); verifyTotal.value = 0 })
watch(visibility, state => { revision++; readController?.abort(); clearTimer(); loading.value = false; if (state === 'visible') void load() })
watch(() => props.refreshKey, requestLoad)
watch(modelControlRuleRevision, value => { if (value.workspace === props.workspace) requestLoad() })
onBeforeUnmount(invalidate)
</script>
<template>
  <div class="col-span-2 border-b border-border/50 p-4"><button type="button" data-testid="model-control-open" class="inline-flex h-9 w-full items-center justify-center rounded-lg border border-border/60 bg-background px-3 text-sm font-medium transition-colors hover:bg-surface disabled:opacity-50" :disabled="platform !== 'sub2api'" :title="platform !== 'sub2api' ? message('sub2apiOnly') : ''" @click="open">{{ message('title') }}</button></div>
  <div v-if="visible" data-testid="model-control-drawer" role="dialog" aria-modal="true" :aria-label="message('title')" class="fixed inset-0 z-50 flex justify-end bg-black/35"><div class="absolute inset-0" @click="close" /><section class="relative flex h-full w-full max-w-4xl flex-col overflow-hidden border-l border-border/60 bg-card shadow-2xl"><header class="flex items-center justify-between gap-3 border-b border-border/60 p-4"><div><h2 class="text-base font-semibold">{{ message('title') }}</h2><p class="mt-1 text-xs text-muted-foreground">{{ message('scope') }}</p></div><button type="button" :aria-label="message('closeDrawer')" class="rounded-md p-1 text-muted-foreground hover:bg-surface" @click="close"><X class="h-4 w-4" /></button></header>
    <div class="min-h-0 flex-1 space-y-3 overflow-y-auto p-4"><QuestionAnswerModelControlHeader scope="workspace" :workspace="workspace" :counts="counts" :counts-stale="countsStale" /><div class="flex flex-wrap gap-2"><button v-for="tab in (['attention', 'all', 'events'] as const)" :key="tab" type="button" :data-testid="`model-control-tab-${tab}`" class="rounded-md border border-border/60 px-2.5 py-1.5 text-xs" :class="view === tab ? 'bg-primary/10 text-primary' : 'text-muted-foreground'" :aria-pressed="view === tab" @click="selectView(tab)">{{ message(tab) }}</button></div>
      <label class="flex items-center gap-2 text-xs">{{ message('modelFilter') }}<input v-model="modelName" :aria-label="message('modelFilter')" type="text" class="min-w-0 rounded-md border border-border/60 bg-background px-2 py-1" @change="page = 1; void load()" /></label>
      <p v-if="error" role="alert" class="break-words text-xs text-destructive">{{ modelControlReason(error) }} · <button type="button" class="underline" @click="load">{{ message('retryRead') }}</button></p><p v-if="loading" class="text-xs text-muted-foreground">{{ message('loading') }}</p><p v-if="verifying || verifyTotal" data-testid="model-control-verify-progress" class="text-xs text-muted-foreground">{{ message('progress', { done: verifiedCount, total: verifyTotal }) }}</p><div v-if="failures.length" class="space-y-1 text-xs text-destructive"><p v-for="failure in failures" :key="failure.targetId" class="break-words">{{ accountName(failure.targetId) }} · {{ modelControlReason(failure.reasonKey) }} · <button type="button" class="underline" @click="reread(failure.targetId)">{{ message('reread') }}</button></p></div>
      <template v-if="view === 'events'"><p v-if="!events.length && !loading && !error" class="text-xs text-muted-foreground">{{ message('noEvents') }}</p><article v-for="event in events" :key="event.id" data-testid="model-control-event" class="space-y-1 rounded-lg border border-border/50 p-3 text-xs"><p class="break-words">{{ modelControlTime(event.createdAt) }} · {{ accountName(event.targetId) }} · {{ event.modelName }} · {{ modelControlEventLines(event).sentence }}</p><p v-for="(line, index) in modelControlEventLines(event).entries" :key="index" class="break-all text-muted-foreground">{{ line }}</p><p v-if="modelControlEventLines(event).more" class="text-muted-foreground">{{ message('moreEntries', { count: modelControlEventLines(event).more }) }}</p></article></template>
      <template v-else><p v-if="!items.length && !loading && !error" class="text-xs text-muted-foreground">{{ view === 'attention' ? message('noAttention') : message('noManaged') }}</p><div class="grid gap-3 [grid-template-columns:repeat(auto-fill,minmax(min(18rem,100%),1fr))]"><div v-for="item in items" :key="`${item.targetId}:${item.modelName}`" class="space-y-2"><QuestionAnswerModelControlRow :item="item" :target-id="item.targetId" :model-name="item.modelName" :workspace="workspace" show-account :show-coverage="view === 'all'" :issue="verifyState.issues.value.get(item.targetId)" :execute-unconfirmed="verifyState.isUnconfirmed(item.targetId, item.modelName)" :account-unconfirmed="verifyState.accountUnconfirmed(item.targetId)" @updated="rowUpdated" @refresh="requestLoad" @removed="items = items.filter(value => value.targetId !== item.targetId || value.modelName !== item.modelName)" @settled="emit('settled', [$event])" @verify-now="reread" @execute-unconfirmed="verifyState.markUnconfirmed" @view-round="viewBatch(item)" /><div class="flex flex-wrap gap-2 text-xs"><button type="button" class="underline disabled:opacity-50" :disabled="!canOpen(item.targetId)" :title="!canOpen(item.targetId) ? message('missingProjection') : ''" @click="emit('question-answer-view', { targetId: item.targetId })">{{ message('openAnswers') }}</button><button v-if="item.round" type="button" class="underline" @click="viewBatch(item)">{{ message('viewBatch') }}</button><span v-if="!canOpen(item.targetId)" class="text-muted-foreground">{{ message('missingProjection') }}</span></div></div></div></template>
      <div v-if="totalPages > 1" class="flex justify-center gap-3 text-xs"><button type="button" :disabled="page <= 1 || loading" @click="page--; void load()">{{ message('previousPage') }}</button><span>{{ page }} / {{ totalPages }}</span><button type="button" :disabled="page >= totalPages || loading" @click="page++; void load()">{{ message('nextPage') }}</button></div>
    </div></section></div>
</template>
