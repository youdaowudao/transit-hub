<script setup lang="ts">
import { accountLabelForTarget, accountNameText, accountReminderLabel, type AccountLabels } from '../../utils/connectionHealthAccountLabel'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useDocumentVisibility } from '@vueuse/core'
import { X } from 'lucide-vue-next'
import { getModelControlVerifyTargets, listModelControlEvents, listModelControlItems, verifyModelControl } from '../../api/connectionHealth'
import { canOpenManualProbeHistory } from '../../composables/useConnectionHealth'
import type { AdminGroupHealth, ModelControlCounts, ModelControlEvent, ModelControlItem } from '../../types/connectionHealth'
import { createModelControlVerifyState, modelControlText as message, modelControlReason, modelControlTime, modelControlRuleRevision, modelControlEventLines, modelControlExcludedTargets, modelControlSections, modelControlBusyTargets } from '../../utils/questionAnswerModelControl'
import QuestionAnswerModelControlHeader from './QuestionAnswerModelControlHeader.vue'
import QuestionAnswerModelControlRow from './QuestionAnswerModelControlRow.vue'
const props = defineProps<{ accountLabels?: AccountLabels; groups: AdminGroupHealth[]; hiddenGroupIds?: string[]; hiddenTargetIds?: Set<string>; hiddenListPending?: boolean; workspace: string; platform: string; refreshKey?: number }>()
const emit = defineEmits<{ (event: 'question-answer-view', value: { targetId: string }): void; (event: 'batch-view', value: { targetId: string; batchId: string; accountName: string; accountNameNote?: string; platform: string; groupName: string; expandFailed: true }): void; (event: 'settled', targetIds: string[]): void }>()
const autoHandling = ref(false)
const visibility = useDocumentVisibility(), visible = ref(false), view = ref<'attention' | 'all' | 'events'>('attention'), modelName = ref(''), page = ref(1), totalPages = ref(0)
const items = ref<ModelControlItem[]>([]), events = ref<ModelControlEvent[]>([]), counts = ref<ModelControlCounts | null>(null), loading = ref(false), error = ref(''), verifiedCount = ref(0), verifyTotal = ref(0), verifying = ref(false)
const accountNames = new Map<string, string>(), verifyControllers = new Set<AbortController>()
let lifecycle = 0, revision = 0, automaticVerifyDone = false, timer: ReturnType<typeof setTimeout> | null = null, readController: AbortController | null = null, inventoryController: AbortController | null = null
// 不显示账号 = 屏蔽清单 + 只在左侧隐藏分组里的账号；条目、计数、开抽屉核对都按它过滤。
const liveScope = computed(() => modelControlExcludedTargets(props.groups, props.hiddenGroupIds ?? [], props.hiddenTargetIds ?? new Set<string>()))
// 显示范围在打开抽屉时定格；打开期间左侧分组显示或屏蔽清单变化只提示重新打开，不即时重读，避免与在途读取、核对和手动操作交错。
const scope = ref(liveScope.value)
const excludedSet = computed(() => new Set(scope.value.excludeTargetIds))
const scopeKey = computed(() => liveScope.value.excludeTargetIds.join(','))
const scopeChanged = ref(false)
// 计数属于读到它时的显示范围；重新打开时范围变了就先清空，等新范围的列表读取成功再显示。
let countsScopeKey = ''
// 正在执行手动操作的账号暂不隐藏，等操作结束再按范围过滤，避免卡片卸载中止已发出的主站操作。
const shownItems = computed(() => items.value.filter(item => !excludedSet.value.has(item.targetId) || modelControlBusyTargets.value.has(item.targetId)))
// 分段在同一个网格里按带键的扁平列表渲染：卡片换段时组件实例保留。
const flatEntries = computed(() => sections.value.flatMap(section => [
  { kind: 'section' as const, key: section.key, title: section.title, modelCount: section.modelCount },
  ...section.rows.flatMap(row => [
    { kind: 'account' as const, key: `account:${section.key}:${row.targetId}`, targetId: row.targetId, alsoIn: row.alsoIn },
    ...row.items.map(item => ({ kind: 'item' as const, key: `${item.targetId}:${item.modelName}`, item })),
  ]),
]))
const sections = computed(() => modelControlSections(shownItems.value, props.groups, props.hiddenGroupIds ?? []))
const accountPriority = (targetId: string): number | null => props.groups.flatMap(group => group.accounts).find(account => account.targetId === targetId)?.priority ?? null
const verifyState = createModelControlVerifyState({ verify: async ids => { const request = new AbortController(); verifyControllers.add(request); try { return await verifyModelControl(ids, request.signal) } finally { verifyControllers.delete(request) } } })
const failures = computed(() => [...verifyState.issues.value].filter(([targetId]) => !excludedSet.value.has(targetId)).map(([targetId, issue]) => ({ targetId, ...issue })))
const countsStale = computed(() => failures.value.some(issue => !issue.reasonKey.endsWith('Busy') && !issue.reasonKey.endsWith('Processing')))
const clearTimer = () => { if (timer !== null) clearTimeout(timer); timer = null }
const invalidate = () => { autoHandling.value = false; lifecycle++; revision++; readController?.abort(); inventoryController?.abort(); for (const request of verifyControllers) request.abort(); clearTimer(); loading.value = false; verifying.value = false; verifyState.dispose() }
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
      // 每批发送前按当前范围再过滤一次；过滤后为空的批次不发送（核对接口拒绝空名单）。
      const chunk = ids.slice(offset, offset + 50).filter(id => !excludedSet.value.has(id))
      if (chunk.length === 0) { verifiedCount.value += ids.slice(offset, offset + 50).length; continue }
      const result = await verifyState.verifyNow(chunk)
      if (!current()) return
      result.items.forEach(update); verifiedCount.value += chunk.length; emit('settled', chunk)
    }
  } finally { if (current()) verifying.value = false }
}
const verifyOpening = async () => {
  const generation = lifecycle, workspace = props.workspace, request = new AbortController(); inventoryController = request
  try {
    const result = await getModelControlVerifyTargets(request.signal, workspace)
    if (generation !== lifecycle || workspace !== props.workspace || !visible.value || request.signal.aborted) return
    await verifyIds([...new Set(result.targetIds)].filter(id => !excludedSet.value.has(id)).sort(), generation, workspace)
  } catch (cause) {
    if (generation !== lifecycle || workspace !== props.workspace || !visible.value || request.signal.aborted) return
    error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request'
  } finally { if (inventoryController === request) inventoryController = null }
}
const maybeVerifyOpening = () => {
  if (automaticVerifyDone) return
  automaticVerifyDone = true
  void verifyOpening()
}
const headerRefreshKey = ref(0)
const load = async () => {
  if (!visible.value || visibility.value !== 'visible') return
  headerRefreshKey.value++
  clearTimer(); readController?.abort(); const generation = lifecycle, read = ++revision, workspace = props.workspace, request = new AbortController(); readController = request; loading.value = true; error.value = ''
  const current = () => generation === lifecycle && read === revision && workspace === props.workspace && visible.value && !request.signal.aborted
  try {
    if (view.value === 'events') { const result = await listModelControlEvents('', modelName.value, page.value, request.signal); if (current()) { events.value = result.items; totalPages.value = result.totalPages } }
    else { const result = await listModelControlItems(view.value, modelName.value, page.value, request.signal, { workspace, excludeTargetIds: scope.value.excludeTargetIds }); if (current()) { items.value = result.items; remember(result.items); counts.value = result.counts; countsScopeKey = scope.value.excludeTargetIds.join(','); totalPages.value = result.totalPages; maybeVerifyOpening() } }
  } catch (cause) { if (current()) error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request' }
  finally { if (current()) { loading.value = false; poll() }; if (readController === request) readController = null }
}
const open = () => { if (props.platform !== 'sub2api' || props.hiddenListPending || visible.value) return; scope.value = liveScope.value; scopeChanged.value = false; if (countsScopeKey !== scope.value.excludeTargetIds.join(',')) counts.value = null; visible.value = true; view.value = 'attention'; page.value = 1; modelName.value = ''; automaticVerifyDone = false; verifiedCount.value = 0; verifyTotal.value = 0; void load() }
const close = () => { visible.value = false; invalidate() }
const selectView = (next: typeof view.value) => { view.value = next; page.value = 1; error.value = ''; void load() }
let loadScheduled = false
const requestLoad = () => { if (loadScheduled) return; loadScheduled = true; queueMicrotask(() => { loadScheduled = false; void load() }) }
const rowUpdated = (value: ModelControlItem) => { const interrupted = Boolean(readController); revision++; readController?.abort(); loading.value = false; update(value); if (interrupted) requestLoad() }
const projection = (targetId: string) => props.groups.flatMap(group => group.accounts.filter(account => account.targetId === targetId).map(account => ({ group, account })))[0]
const canOpen = (targetId: string) => { const value = projection(targetId); return Boolean(value && canOpenManualProbeHistory(value.account)) }
const accountLabel = (targetId: string) => ({ ...accountLabelForTarget(props.accountLabels, targetId), name: accountNameText(projection(targetId)?.account.name, accountNames.get(targetId), props.accountLabels?.[targetId]?.name) })
const accountName = (targetId: string) => accountReminderLabel(accountLabel(targetId))
const viewBatch = (item: ModelControlItem) => { if (!item.round) return; const value = projection(item.targetId); emit('batch-view', { targetId: item.targetId, batchId: item.round.batchId, accountName: accountLabel(item.targetId).name, accountNameNote: '当时未记录账号名称，以下名称来自当前资料', platform: value?.account.platform ?? 'sub2api', groupName: value?.group.name ?? '', expandFailed: true }) }
watch(() => props.workspace, () => { close(); items.value = []; events.value = []; counts.value = null; accountNames.clear(); verifyTotal.value = 0 })
watch(visibility, state => { revision++; readController?.abort(); clearTimer(); loading.value = false; if (state === 'visible') void load() })
watch(() => props.refreshKey, requestLoad)
// 不显示范围变化：旧卡片立即按新范围过滤（shownItems），旧计数不再显示；作废在途读取与核对后按新范围重读并重新核对。
// 停在「操作记录」页时不重读记录，切回列表页时再读取计数。只调整分组顺序不改变范围，由 sections 本地重排。
watch(scopeKey, key => { if (visible.value && key !== scope.value.excludeTargetIds.join(',')) scopeChanged.value = true })
watch(modelControlRuleRevision, value => { if (value.workspace === props.workspace) requestLoad() })
onBeforeUnmount(invalidate)
</script>
<template>
  <div class="col-span-2 border-b border-border/50 p-4"><button type="button" data-testid="model-control-open" class="inline-flex h-9 w-full items-center justify-center rounded-lg border border-border/60 bg-background px-3 text-sm font-medium transition-colors hover:bg-surface disabled:opacity-50" :disabled="platform !== 'sub2api' || hiddenListPending" :title="platform !== 'sub2api' ? message('sub2apiOnly') : hiddenListPending ? '正在读取屏蔽清单，读到后即可打开' : ''" @click="open">{{ message('title') }}</button></div>
  <div v-if="visible" data-testid="model-control-drawer" role="dialog" aria-modal="true" :aria-label="message('title')" class="fixed inset-0 z-50 flex justify-end bg-black/35"><div class="absolute inset-0" @click="close" /><section class="relative flex h-full w-full max-w-4xl flex-col overflow-hidden border-l border-border/60 bg-card shadow-2xl"><header class="flex items-center justify-between gap-3 border-b border-border/60 p-4"><div><h2 class="text-base font-semibold">{{ message('title') }}</h2><p class="mt-1 text-xs text-muted-foreground">{{ message(autoHandling ? 'autoScope' : 'scope') }}</p></div><button type="button" :aria-label="message('closeDrawer')" class="rounded-md p-1 text-muted-foreground hover:bg-surface" @click="close"><X class="h-4 w-4" /></button></header>
    <div class="min-h-0 flex-1 space-y-3 overflow-y-auto p-4"><p class="text-xs text-muted-foreground">按分组健康左侧的分组顺序显示，组内按 Priority 从小到大；左侧隐藏的分组和已屏蔽的账号这里不显示。</p><p v-if="scope.partial" data-testid="model-control-partial-scope" class="text-xs text-amber-600">有分组的账号没读到，原本属于左侧隐藏分组的账号可能暂时出现在这里。请刷新分组健康页后再打开。</p><p v-if="scopeChanged" data-testid="model-control-scope-changed" class="text-xs text-amber-600">左侧分组显示或屏蔽清单有变化，关闭后重新打开模型管理即可按新范围显示。</p><QuestionAnswerModelControlHeader scope="workspace" :workspace="workspace" :counts="counts" :counts-stale="countsStale" :refresh-key="headerRefreshKey" @auto-enabled="autoHandling = $event" /><div class="flex flex-wrap gap-2"><button v-for="tab in (['attention', 'all', 'events'] as const)" :key="tab" type="button" :data-testid="`model-control-tab-${tab}`" class="rounded-md border border-border/60 px-2.5 py-1.5 text-xs" :class="view === tab ? 'bg-primary/10 text-primary' : 'text-muted-foreground'" :aria-pressed="view === tab" @click="selectView(tab)">{{ message(tab) }}</button></div>
      <label class="flex items-center gap-2 text-xs">{{ message('modelFilter') }}<input v-model="modelName" :aria-label="message('modelFilter')" type="text" class="min-w-0 rounded-md border border-border/60 bg-background px-2 py-1" @change="page = 1; void load()" /></label>
      <p v-if="error" role="alert" class="break-words text-xs text-destructive">{{ modelControlReason(error) }} · <button type="button" class="underline" @click="load">{{ message('retryRead') }}</button></p><p v-if="loading" class="text-xs text-muted-foreground">{{ message('loading') }}</p><p v-if="verifying || verifyTotal" data-testid="model-control-verify-progress" class="text-xs text-muted-foreground">{{ message('progress', { done: verifiedCount, total: verifyTotal }) }}</p><div v-if="failures.length" class="space-y-1 text-xs text-destructive"><p v-for="failure in failures" :key="failure.targetId" class="break-words">{{ accountName(failure.targetId) }} · {{ modelControlReason(failure.reasonKey) }}。请重新读取主站结果；仍失败时，请按这里的账号名称、模型和时间查看后台记录。 <button type="button" class="underline" @click="reread(failure.targetId)">{{ message('reread') }}</button></p></div>
      <template v-if="view === 'events'"><p v-if="!events.length && !loading && !error" class="text-xs text-muted-foreground">{{ message('noEvents') }}</p><article v-for="event in events" :key="event.id" data-testid="model-control-event" class="space-y-1 rounded-lg border border-border/50 p-3 text-xs"><p class="break-words">{{ modelControlTime(event.createdAt) }} · {{ accountName(event.targetId) }} · {{ event.modelName }} · <span v-if="event.actorUserId === ''" data-testid="model-control-event-auto" class="mr-1 rounded-md bg-primary/10 px-1 text-primary">{{ message('autoEvent') }}</span>{{ modelControlEventLines(event).sentence }}</p><p v-for="(line, index) in modelControlEventLines(event).entries" :key="index" class="break-all text-muted-foreground">{{ line }}</p><p v-if="modelControlEventLines(event).more" class="text-muted-foreground">{{ message('moreEntries', { count: modelControlEventLines(event).more }) }}</p></article></template>
      <template v-else><p v-if="!shownItems.length && !loading && !error" class="text-xs text-muted-foreground">{{ view === 'attention' ? message('noAttention') : message('noManaged') }}</p><div class="grid gap-3 [grid-template-columns:repeat(auto-fill,minmax(min(18rem,100%),1fr))]"><template v-for="entry in flatEntries" :key="entry.key"><h3 v-if="entry.kind === 'section'" data-testid="model-control-section" class="col-span-full pt-2 text-sm font-semibold text-foreground">{{ entry.title }} · {{ entry.modelCount }} 个模型</h3><p v-else-if="entry.kind === 'account'" data-testid="model-control-account" :data-target-id="entry.targetId" class="col-span-full text-xs text-muted-foreground">{{ accountName(entry.targetId) }}<template v-if="accountPriority(entry.targetId) != null"> · Priority {{ accountPriority(entry.targetId) }}</template><template v-if="entry.alsoIn"> · 也在：{{ entry.alsoIn }}</template></p><div v-else class="space-y-2"><QuestionAnswerModelControlRow :account-label="accountLabel(entry.item.targetId)" :item="entry.item" :target-id="entry.item.targetId" :model-name="entry.item.modelName" :workspace="workspace" show-account :show-coverage="view === 'all'" :issue="verifyState.issues.value.get(entry.item.targetId)" :execute-unconfirmed="verifyState.isUnconfirmed(entry.item.targetId, entry.item.modelName)" :account-unconfirmed="verifyState.accountUnconfirmed(entry.item.targetId)" @updated="rowUpdated" @refresh="requestLoad" @removed="items = items.filter(value => value.targetId !== entry.item.targetId || value.modelName !== entry.item.modelName)" @settled="emit('settled', [$event])" @verify-now="reread" @execute-unconfirmed="verifyState.markUnconfirmed" @view-round="viewBatch(entry.item)" /><div class="flex flex-wrap gap-2 text-xs"><button type="button" class="underline disabled:opacity-50" :disabled="!canOpen(entry.item.targetId)" :title="!canOpen(entry.item.targetId) ? message('missingProjection') : ''" @click="emit('question-answer-view', { targetId: entry.item.targetId })">{{ message('openAnswers') }}</button><button v-if="entry.item.round" type="button" class="underline" @click="viewBatch(entry.item)">{{ message('viewBatch') }}</button><span v-if="!canOpen(entry.item.targetId)" class="text-muted-foreground">{{ message('missingProjection') }}</span></div></div></template></div></template>
      <div v-if="totalPages > 1" class="flex justify-center gap-3 text-xs"><button type="button" :disabled="page <= 1 || loading" @click="page--; void load()">{{ message('previousPage') }}</button><span>{{ page }} / {{ totalPages }}</span><button type="button" :disabled="page >= totalPages || loading" @click="page++; void load()">{{ message('nextPage') }}</button></div>
    </div></section></div>
</template>
