<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useDocumentVisibility } from '@vueuse/core'
import { X } from 'lucide-vue-next'
import { deleteModelControlRule, getModelControlVerifyTargets, listModelControlEvents, listModelControlItems, listModelControlRules, verifyModelControl } from '../../api/connectionHealth'
import { canOpenManualProbeHistory } from '../../composables/useConnectionHealth'
import type { AdminGroupHealth, ModelControlEvent, ModelControlItem, ModelControlRule } from '../../types/connectionHealth'
import { acquireModelControlBusy, releaseModelControlBusy, modelControlText as message, modelControlReason, modelControlTime, modelControlRuleRevision, modelControlEventLabel } from '../../utils/questionAnswerModelControl'
import QuestionAnswerModelControlRow from './QuestionAnswerModelControlRow.vue'
import QuestionAnswerModelControlRuleForm from './QuestionAnswerModelControlRuleForm.vue'
const props = defineProps<{ groups: AdminGroupHealth[]; workspace: string; platform: string; refreshKey?: number }>()
const emit = defineEmits<{ (event: 'question-answer-view', value: { targetId: string }): void; (event: 'batch-view', value: { targetId: string; batchId: string; accountName: string; platform: string; groupName: string }): void; (event: 'settled', targetIds: string[]): void }>()
const visibility = useDocumentVisibility(), visible = ref(false), view = ref<'attention' | 'all' | 'rules' | 'events'>('attention'), modelName = ref(''), page = ref(1), totalPages = ref(0)
const items = ref<ModelControlItem[]>([]), rules = ref<ModelControlRule[]>([]), events = ref<ModelControlEvent[]>([]), editing = ref(''), loading = ref(false), mutating = ref(false), error = ref(''), verifiedCount = ref(0), verifyTotal = ref(0), verifyErrors = ref<Array<{ targetId: string; reasonKey: string }>>([])
let lifecycle = 0, revision = 0, timer: ReturnType<typeof setTimeout> | null = null, readController: AbortController | null = null, mutationController: AbortController | null = null
const clearTimer = () => { if (timer !== null) clearTimeout(timer); timer = null }
const invalidate = () => { lifecycle++; revision++; readController?.abort(); mutationController?.abort(); clearTimer(); loading.value = false; mutating.value = false }
const poll = () => { clearTimer(); if (visible.value && visibility.value === 'visible' && !editing.value && !mutating.value) timer = setTimeout(() => { void load() }, 30000) }
const load = async () => {
  if (!visible.value || visibility.value !== 'visible' || editing.value || mutating.value) return
  clearTimer(); readController?.abort(); const generation = lifecycle, read = ++revision, workspace = props.workspace, request = new AbortController(); readController = request; loading.value = true; error.value = ''
  const current = () => generation === lifecycle && read === revision && workspace === props.workspace && visible.value && !request.signal.aborted
  try {
    if (view.value === 'rules') { const result = await listModelControlRules(request.signal); if (current()) { rules.value = result.items; totalPages.value = 0 } }
    else if (view.value === 'events') { const result = await listModelControlEvents('', modelName.value, page.value, request.signal); if (current()) { events.value = result.items; totalPages.value = result.totalPages } }
    else { const result = await listModelControlItems(view.value, modelName.value, page.value, request.signal); if (current()) { items.value = result.items; totalPages.value = result.totalPages } }
  } catch (cause) { if (current()) error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request' }
  finally { if (current()) { loading.value = false; poll() }; if (readController === request) readController = null }
}
const open = () => { if (props.platform !== 'sub2api') return; visible.value = true; view.value = 'attention'; page.value = 1; modelName.value = ''; void load() }
const close = () => { visible.value = false; invalidate(); editing.value = '' }
const selectView = (next: typeof view.value) => { readController?.abort(); revision++; view.value = next; page.value = 1; editing.value = ''; error.value = ''; void load() }
const update = (value: ModelControlItem) => { revision++; readController?.abort(); loading.value = false; items.value = items.value.map(item => item.targetId === value.targetId && item.modelName === value.modelName ? value : item) }
const projection = (targetId: string) => props.groups.flatMap(group => group.accounts.filter(account => account.targetId === targetId).map(account => ({ group, account })))[0]
const canOpen = (targetId: string) => { const value = projection(targetId); return Boolean(value && canOpenManualProbeHistory(value.account)) }
const viewBatch = (item: ModelControlItem) => { if (!item.round) return; const value = projection(item.targetId); emit('batch-view', { targetId: item.targetId, batchId: item.round.batchId, accountName: item.accountName, platform: value?.account.platform ?? 'sub2api', groupName: value?.group.name ?? '' }) }
const runMutation = async (action: (signal: AbortSignal, current: () => boolean) => Promise<void>) => {
  if (mutating.value) return
  revision++; readController?.abort(); clearTimer(); const generation = lifecycle, workspace = props.workspace, request = new AbortController(); mutationController = request; mutating.value = true; loading.value = false; error.value = ''
  const current = () => generation === lifecycle && workspace === props.workspace && visible.value && !request.signal.aborted
  try { await action(request.signal, current) } catch (cause) { if (current()) error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request' }
  finally { if (current()) { mutating.value = false; if (error.value) poll(); else void load() }; if (mutationController === request) mutationController = null }
}
const verifyIds = async (ids: string[], signal: AbortSignal, current: () => boolean) => {
  verifyErrors.value = []; verifiedCount.value = 0; verifyTotal.value = ids.length
  for (let offset = 0; offset < ids.length && current(); offset += 50) {
    const chunk = ids.slice(offset, offset + 50)
    const owners = chunk.map(targetId => ({ targetId, owner: acquireModelControlBusy(targetId) }))
    try { const result = await verifyModelControl(chunk, signal); if (!current()) return; result.items.forEach(update); verifyErrors.value.push(...result.errors) }
    catch (cause) { if (!current()) return; verifyErrors.value.push(...chunk.map(targetId => ({ targetId, reasonKey: cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request' }))) }
    finally { for (const { targetId, owner } of owners) if (owner) releaseModelControlBusy(targetId, owner) }
    if (!current()) return
    verifiedCount.value += chunk.length; emit('settled', chunk)
  }
}
const verifyAll = () => runMutation(async (signal, current) => { verifyTotal.value = 0; verifiedCount.value = 0; verifyErrors.value = []; const result = await getModelControlVerifyTargets(signal); if (current()) await verifyIds([...new Set(result.targetIds)].sort(), signal, current) })
const retryVerify = () => runMutation(async (signal, current) => { await verifyIds([...new Set(verifyErrors.value.map(item => item.targetId))].sort(), signal, current) })
const deleteRule = (rule: ModelControlRule) => { if (!window.confirm(message('deleteRuleConfirm', { model: rule.modelName }))) return; return runMutation(async (signal) => { await deleteModelControlRule(rule.modelName, rule.version, signal) }) }
const savedRule = () => { editing.value = ''; void load() }
watch(() => props.workspace, () => { close(); items.value = []; rules.value = []; events.value = []; verifyErrors.value = []; verifyTotal.value = 0 })
watch(visibility, state => { revision++; readController?.abort(); clearTimer(); loading.value = false; if (state === 'visible') void load() })
watch(() => props.refreshKey, () => { void load() })
watch(modelControlRuleRevision, value => { if (value.workspace === props.workspace) void load() })
onBeforeUnmount(invalidate)
</script>
<template>
  <div class="col-span-2 border-b border-border/50 p-4"><button type="button" data-testid="model-control-open" class="inline-flex h-9 w-full items-center justify-center rounded-lg border border-border/60 bg-background px-3 text-sm font-medium transition-colors hover:bg-surface disabled:opacity-50" :disabled="platform !== 'sub2api'" :title="platform !== 'sub2api' ? message('sub2apiOnly') : ''" @click="open">{{ message('title') }}</button></div>
  <div v-if="visible" data-testid="model-control-drawer" role="dialog" aria-modal="true" :aria-label="message('title')" class="fixed inset-0 z-50 flex justify-end bg-black/35"><div class="absolute inset-0" @click="close" /><section class="relative flex h-full w-full max-w-2xl flex-col overflow-hidden border-l border-border/60 bg-card shadow-2xl"><header class="flex items-center justify-between gap-3 border-b border-border/60 p-4"><div><h2 class="text-base font-semibold">{{ message('title') }}</h2><p class="mt-1 text-xs text-muted-foreground">{{ message('scope') }}</p></div><button type="button" :aria-label="message('closeDrawer')" class="rounded-md p-1 text-muted-foreground hover:bg-surface" @click="close"><X class="h-4 w-4" /></button></header>
    <div class="min-h-0 flex-1 space-y-3 overflow-y-auto p-4"><div class="flex flex-wrap justify-between gap-2"><div class="flex flex-wrap gap-2"><button v-for="tab in (['attention', 'all', 'rules', 'events'] as const)" :key="tab" type="button" :data-testid="`model-control-tab-${tab}`" class="rounded-md border border-border/60 px-2.5 py-1.5 text-xs" :class="view === tab ? 'bg-primary/10 text-primary' : 'text-muted-foreground'" :aria-pressed="view === tab" :disabled="mutating" @click="selectView(tab)">{{ message(tab) }}</button></div><button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 text-xs disabled:opacity-50" :disabled="mutating" @click="verifyAll">{{ message('verifyAll') }}</button></div>
      <label v-if="view !== 'rules'" class="flex items-center gap-2 text-xs">{{ message('modelFilter') }}<input v-model="modelName" :aria-label="message('modelFilter')" type="text" class="min-w-0 rounded-md border border-border/60 bg-background px-2 py-1" :disabled="mutating" @change="page = 1; void load()" /></label>
      <p v-if="error" role="alert" class="break-words text-xs text-destructive">{{ modelControlReason(error) }} · <button type="button" class="underline" @click="load">{{ message('retryRead') }}</button></p><p v-if="loading" class="text-xs text-muted-foreground">{{ message('loading') }}</p><p v-if="verifyTotal || mutating" data-testid="model-control-verify-progress" class="text-xs">{{ message('progress', { done: verifiedCount, total: verifyTotal }) }}</p><div v-if="verifyErrors.length" class="space-y-1 text-xs text-destructive"><p v-for="failure in verifyErrors" :key="failure.targetId" class="break-words">{{ failure.targetId }} · {{ modelControlReason(failure.reasonKey) }}</p><button type="button" class="underline" :disabled="mutating" @click="retryVerify">{{ message('retryTargets') }}</button></div>
      <template v-if="view === 'rules'"><p v-if="!rules.length && !loading && !error" class="text-xs text-muted-foreground">{{ message('noRules') }}</p><article v-for="rule in rules" :key="rule.modelName" class="rounded-lg border border-border/50 p-3 text-xs"><p class="break-words font-medium">{{ rule.modelName }} · {{ rule.minAccuracyPercent }}% · {{ message('minRound') }} {{ rule.minJudgedAnswers }} {{ message('answers') }} · {{ rule.includeManual ? message('manualIncluded') : message('manualExcluded') }} · {{ rule.includeScheduled ? message('scheduledIncluded') : message('scheduledExcluded') }}</p><QuestionAnswerModelControlRuleForm v-if="editing === rule.modelName" :rule="rule" :workspace="workspace" @saved="savedRule" @cancel="editing = ''; poll()" /><div v-else class="mt-2 flex gap-2"><button type="button" class="underline" :disabled="mutating" @click="editing = rule.modelName; clearTimer()">{{ message('edit') }}</button><button type="button" class="text-destructive underline" :disabled="mutating" @click="deleteRule(rule)">{{ message('deleteRule') }}</button></div></article></template>
      <template v-else-if="view === 'events'"><p v-if="!events.length && !loading && !error" class="text-xs text-muted-foreground">{{ message('noEvents') }}</p><article v-for="event in events" :key="event.id" data-testid="model-control-event" class="space-y-2 rounded-lg border border-border/50 p-3 text-xs"><p class="break-words">{{ modelControlTime(event.createdAt) }} · {{ event.modelName }} · {{ modelControlEventLabel(event.eventType) }}</p><details><summary class="cursor-pointer text-muted-foreground">{{ message('eventDetails') }}</summary><pre class="mt-2 whitespace-pre-wrap break-all text-[11px]">{{ JSON.stringify(event.detail, null, 2) }}</pre></details></article></template>
      <template v-else><p v-if="!items.length && !loading && !error" class="text-xs text-muted-foreground">{{ view === 'attention' ? message('noAttention') : message('noManaged') }}</p><div v-for="item in items" :key="`${item.targetId}:${item.modelName}`" class="space-y-2"><QuestionAnswerModelControlRow :item="item" :target-id="item.targetId" :model-name="item.modelName" :workspace="workspace" :show-coverage="view === 'all'" @updated="update" @refresh="load" @rule-saved="load" @removed="items = items.filter(value => value.targetId !== item.targetId || value.modelName !== item.modelName)" @settled="emit('settled', [$event])" /><div class="flex flex-wrap gap-2 text-xs"><button type="button" class="underline disabled:opacity-50" :disabled="!canOpen(item.targetId)" :title="!canOpen(item.targetId) ? message('missingProjection') : ''" @click="emit('question-answer-view', { targetId: item.targetId })">{{ message('openAnswers') }}</button><button v-if="item.round" type="button" class="underline" @click="viewBatch(item)">{{ message('viewBatch') }}</button><span v-if="!canOpen(item.targetId)" class="text-muted-foreground">{{ message('missingProjection') }}</span></div></div></template>
      <div v-if="totalPages > 1" class="flex justify-center gap-3 text-xs"><button type="button" :disabled="page <= 1 || loading || mutating" @click="page--; void load()">{{ message('previousPage') }}</button><span>{{ page }} / {{ totalPages }}</span><button type="button" :disabled="page >= totalPages || loading || mutating" @click="page++; void load()">{{ message('nextPage') }}</button></div><p class="text-[11px] text-muted-foreground">{{ message('localPolling') }}</p>
    </div></section></div>
</template>
