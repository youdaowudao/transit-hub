<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useDocumentVisibility } from '@vueuse/core'
import { Loader2, X } from 'lucide-vue-next'
import { t, te } from '@/locales'
import { ConnectionHealthApiError, deleteQuestionAnswerSchedule, getQuestionAnswerSchedule, getQuestionAnswerScheduleLimits, listQuestionAnswerScheduleExecutions, listQuestionAnswerSchedules, runQuestionAnswerSchedule, setQuestionAnswerScheduleState } from '../../api/connectionHealth'
import { connectionHealthMessageKey } from '../../composables/useConnectionHealth'
import { formatQuestionAnswerAccuracy, questionAnswerAccuracy } from '../../utils/questionAnswers'
import { questionAnswerScheduleReason, questionAnswerScheduleRequestId, questionAnswerScheduleStatus, questionAnswerScheduleTime } from '../../utils/questionAnswerSchedules'
import type { AdminGroupHealth, QuestionAnswerSchedule, QuestionAnswerScheduleExecution, QuestionAnswerScheduleLimits } from '../../types/connectionHealth'
import type { QuestionAnswerSelectionPreferences } from '../../utils/connectionHealthPreferences'
import QuestionAnswerScheduleForm from './QuestionAnswerScheduleForm.vue'
import QuestionAnswerScheduleProtection from './QuestionAnswerScheduleProtection.vue'
import QuestionAnswerScheduleExecutionDetail from './QuestionAnswerScheduleExecutionDetail.vue'

const props = defineProps<{ groups: AdminGroupHealth[]; workspace: string; platform: string; preferences: QuestionAnswerSelectionPreferences; statsRevision?: number }>()
const emit = defineEmits<{ (event: 'question-answer-view', value: { targetId: string; batchId: string; accountName: string; platform: string; groupName: string }): void; (event: 'settled', targetIds: string[]): void }>()
const documentVisibility = useDocumentVisibility()
const visible = ref(false), mode = ref<'list' | 'form' | 'protection' | 'history' | 'detail'>('list')
const status = ref<'active' | 'deleted'>('active'), page = ref(1), historyPage = ref(1)
const items = ref<QuestionAnswerSchedule[]>([]), totalPages = ref(0), history = ref<QuestionAnswerScheduleExecution[]>([]), historyTotalPages = ref(0)
const initial = ref<QuestionAnswerSchedule | null>(null), selected = ref<QuestionAnswerSchedule | null>(null), executionId = ref(''), limits = ref<QuestionAnswerScheduleLimits | null>(null)
const loading = ref(false), mutating = ref(''), dirty = ref(false), error = ref(''), notice = ref('')
let lifecycle = 0, revision = 0, timer: ReturnType<typeof setTimeout> | null = null, readController: AbortController | null = null, mutationController: AbortController | null = null
const runRequestIds = new Map<string, string>()
const auxiliaryControllers = new Set<AbortController>()
const readable = (key: string) => t(connectionHealthMessageKey(key, te))
const scheduleLabel = (schedule: QuestionAnswerSchedule) => schedule.deletedAt ? '已删除' : schedule.blockedReason ? '需处理' : schedule.enabled ? '已启用' : '已暂停'
const clearTimer = () => { if (timer) clearTimeout(timer); timer = null }
const invalidate = () => { lifecycle++; revision++; readController?.abort(); mutationController?.abort(); for (const controller of auxiliaryControllers) controller.abort(); auxiliaryControllers.clear(); clearTimer(); loading.value = false; mutating.value = '' }
const scope = () => `${props.workspace}:${mode.value}:${status.value}:${page.value}:${selected.value?.id}:${historyPage.value}`
const schedulePoll = () => { clearTimer(); if (documentVisibility.value === 'visible' && visible.value && (mode.value === 'list' || mode.value === 'history')) timer = setTimeout(() => void load(), 30000) }
const load = async () => {
  if (documentVisibility.value !== 'visible' || !visible.value || (mode.value !== 'list' && mode.value !== 'history') || mutating.value) return
  readController?.abort(); clearTimer(); const own = ++revision, generation = lifecycle, identity = scope(), controller = new AbortController(); readController = controller; loading.value = true
  const current = () => documentVisibility.value === 'visible' && generation === lifecycle && own === revision && identity === scope() && visible.value && !controller.signal.aborted
  try {
    if (mode.value === 'list') {
      const result = await listQuestionAnswerSchedules(page.value, status.value, controller.signal)
      if (!current()) return
      items.value = result.items; totalPages.value = result.totalPages; error.value = ''
    } else if (selected.value) {
      const result = await listQuestionAnswerScheduleExecutions(selected.value.id, historyPage.value, controller.signal)
      if (!current()) return
      history.value = result.items; historyTotalPages.value = result.totalPages; error.value = ''
    }
  } catch (cause) { if (current()) error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request' }
  finally { if (current()) { loading.value = false; schedulePoll() }; if (readController === controller) readController = null }
}
const loadLimits = async () => {
  const generation = lifecycle, workspace = props.workspace, controller = new AbortController()
  auxiliaryControllers.add(controller)
  try { const result = await getQuestionAnswerScheduleLimits(controller.signal); if (generation === lifecycle && workspace === props.workspace && visible.value) limits.value = result }
  catch { if (generation === lifecycle && visible.value) limits.value = null }
  finally { auxiliaryControllers.delete(controller) }
}
const open = () => { invalidate(); visible.value = true; mode.value = 'list'; status.value = 'active'; page.value = 1; items.value = []; error.value = ''; notice.value = ''; void loadLimits(); void load() }
const close = () => { invalidate(); visible.value = false; initial.value = null; selected.value = null; dirty.value = false }
const changeTab = (tab: 'active' | 'deleted') => { readController?.abort(); revision++; status.value = tab; page.value = 1; items.value = []; error.value = ''; notice.value = ''; mode.value = 'list'; selected.value = null; void load() }
const returnList = () => { revision++; readController?.abort(); mode.value = 'list'; initial.value = null; dirty.value = false; notice.value = ''; void load() }
const edit = async (schedule: QuestionAnswerSchedule | null) => {
  if (mutating.value) return
  readController?.abort(); revision++; clearTimer(); const generation = lifecycle, workspace = props.workspace
  if (!schedule) { initial.value = null; dirty.value = false; mode.value = 'form'; return }
  const controller = new AbortController(); mutationController = controller; mutating.value = schedule.id
  try { const result = await getQuestionAnswerSchedule(schedule.id, controller.signal); if (generation !== lifecycle || workspace !== props.workspace || controller.signal.aborted) return; initial.value = result; dirty.value = false; mode.value = 'form'; error.value = '' }
  catch (cause) { if (generation === lifecycle && !controller.signal.aborted) error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request' }
  finally { if (generation === lifecycle) { mutating.value = ''; schedulePoll() }; if (mutationController === controller) mutationController = null }
}
const applySchedule = (current: QuestionAnswerSchedule) => {
  const index = items.value.findIndex(item => item.id === current.id)
  if (index >= 0 && current.version >= items.value[index]!.version) items.value = items.value.map(item => item.id === current.id ? current : item)
  if (selected.value?.id === current.id && current.version >= selected.value.version) selected.value = current
}
const mutate = async (schedule: QuestionAnswerSchedule, operation: 'enable' | 'disable' | 'revalidate' | 'delete' | 'run') => {
  if (mutating.value || (operation === 'run' && dirty.value)) return
  if (operation === 'delete' && !window.confirm(`删除计划“${schedule.name}”？已接受的执行继续，历史保留。`)) return
  revision++; readController?.abort(); clearTimer(); const generation = lifecycle, workspace = props.workspace, controller = new AbortController(); mutationController = controller; mutating.value = schedule.id; error.value = ''; notice.value = ''
  let accepted = false
  const current = () => generation === lifecycle && workspace === props.workspace && visible.value && !controller.signal.aborted
  try {
    if (operation === 'run') {
      const requestId = runRequestIds.get(schedule.id) ?? questionAnswerScheduleRequestId(); runRequestIds.set(schedule.id, requestId)
      const result = await runQuestionAnswerSchedule(schedule.id, requestId, controller.signal)
      if (!current()) return
      runRequestIds.delete(schedule.id); selected.value = schedule; executionId.value = result.id; mode.value = 'detail'; notice.value = '本次执行已接受，计划时刻和启用状态保持。'
    } else {
      const result = operation === 'delete' ? await deleteQuestionAnswerSchedule(schedule.id, schedule.version, controller.signal) : await setQuestionAnswerScheduleState(schedule.id, operation, schedule.version, controller.signal)
      if (!current()) return
      accepted = true; applySchedule(result); if (operation === 'delete') items.value = items.value.filter(item => item.id !== result.id)
      notice.value = operation === 'disable' ? '计划已暂停；已接受的执行继续。' : operation === 'delete' ? '计划已删除，可从已删除计划继续查看历史。' : '计划状态已保存。'
    }
  } catch (cause) {
    if (!current()) return
    error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request'
    if (cause instanceof ConnectionHealthApiError) {
      if (operation === 'run' && cause.activeExecutionId) { runRequestIds.delete(schedule.id); selected.value = schedule; executionId.value = cause.activeExecutionId; mode.value = 'detail' }
      else if (cause.current && typeof cause.current === 'object' && 'targetMode' in cause.current) applySchedule(cause.current as QuestionAnswerSchedule)
      if (operation === 'run' && cause.status < 500) runRequestIds.delete(schedule.id)
    }
  } finally { if (current()) { mutating.value = ''; if (mode.value === 'list') { if (accepted) void load(); else schedulePoll() } }; if (mutationController === controller) mutationController = null }
}
const openProtection = () => { revision++; readController?.abort(); clearTimer(); loading.value = false; mode.value = 'protection' }
const viewHistory = (schedule: QuestionAnswerSchedule) => { revision++; readController?.abort(); selected.value = schedule; history.value = []; historyPage.value = 1; mode.value = 'history'; error.value = ''; notice.value = ''; void load() }
const viewExecution = (execution: QuestionAnswerScheduleExecution) => { revision++; readController?.abort(); clearTimer(); executionId.value = execution.id; mode.value = 'detail'; error.value = ''; notice.value = '' }
const saved = (schedule: QuestionAnswerSchedule) => { applySchedule(schedule); mode.value = 'list'; initial.value = null; dirty.value = false; notice.value = '计划已保存；不会立即执行。'; void load() }
const pageTo = (next: number) => { if (mode.value === 'list') page.value = next; else historyPage.value = next; void load() }
watch(() => props.workspace, () => { close(); items.value = []; history.value = []; limits.value = null; runRequestIds.clear() })
watch(documentVisibility, state => { revision++; readController?.abort(); clearTimer(); loading.value = false; if (state === 'visible') void load() })
onBeforeUnmount(invalidate)
</script>
<template>
  <div class="border-b border-border/50 p-4"><button type="button" data-testid="question-answer-schedule-open" class="inline-flex h-9 w-full items-center justify-center rounded-lg border border-border/60 bg-background px-3 text-sm font-medium transition-colors hover:bg-surface disabled:opacity-50" :disabled="platform !== 'sub2api'" :title="platform !== 'sub2api' ? '定时测试仅支持 Sub2API 主站' : ''" @click="open">定时测试</button></div>
  <div v-if="visible" data-testid="question-answer-schedule-drawer" class="fixed inset-0 z-50 flex justify-end bg-black/35" role="dialog" aria-modal="true" aria-label="定时测试"><div class="absolute inset-0" @click="close" /><section class="relative flex h-full w-full max-w-2xl flex-col overflow-hidden border-l border-border/60 bg-card shadow-2xl"><header class="flex items-center justify-between gap-3 border-b border-border/60 p-4"><div><h2 class="text-base font-semibold">定时测试</h2><p class="mt-1 text-xs text-muted-foreground">新加坡时间（UTC+8） · 当前工作区</p></div><button type="button" aria-label="关闭定时测试" class="rounded-md p-1 text-muted-foreground hover:bg-surface" @click="close"><X class="h-4 w-4" /></button></header>
    <div class="min-h-0 flex-1 overflow-y-auto p-4"><div v-if="error" role="alert" class="mb-4 rounded-lg bg-destructive/10 p-3 text-xs text-destructive"><p class="break-words">{{ readable(error) }}</p><button v-if="mode === 'list' || mode === 'history'" type="button" class="mt-2 underline" @click="load">重试读取</button></div><p v-if="notice" role="status" class="mb-3 break-words text-xs text-primary">{{ notice }}</p>
      <QuestionAnswerScheduleForm v-if="mode === 'form'" :initial="initial" :groups="groups" :workspace="workspace" :preferences="preferences" :limits="limits" @dirty="dirty = $event" @cancel="returnList" @saved="saved" @run="mutate($event, 'run')" />
      <QuestionAnswerScheduleProtection v-else-if="mode === 'protection'" :workspace="workspace" @back="returnList" @saved="loadLimits" />
      <QuestionAnswerScheduleExecutionDetail v-else-if="mode === 'detail' && selected" :execution-id="executionId" :workspace="workspace" :schedule-name="selected.name" :stats-revision="statsRevision" @back="mode = 'history'; void load()" @question-answer-view="emit('question-answer-view', $event)" @settled="emit('settled', $event)" />
      <template v-else-if="mode === 'history' && selected"><div class="mb-4 flex flex-wrap items-center justify-between gap-2"><h3 class="min-w-0 break-words text-sm font-semibold">{{ selected.name }} · 执行历史</h3><button type="button" class="text-xs text-muted-foreground" @click="returnList">返回计划</button></div><button type="button" class="mb-3 rounded-md border border-border/60 px-2.5 py-1.5 text-xs" @click="load">刷新历史</button><p v-if="loading && history.length === 0" class="text-xs text-muted-foreground">读取中…</p><p v-else-if="history.length === 0 && !error" class="rounded-lg border border-dashed border-border/50 p-6 text-center text-xs text-muted-foreground">尚无执行记录。</p><div class="space-y-3"><button v-for="execution in history" :key="execution.id" type="button" data-testid="schedule-history-row" class="w-full space-y-2 rounded-lg border border-border/50 p-3 text-left text-xs hover:bg-surface-line/30" @click="viewExecution(execution)"><p class="break-words font-medium">{{ questionAnswerScheduleTime(execution.scheduledFor) }} · {{ t(`admin.connectionHealth.questionAnswerSchedule.sources.${execution.trigger}`) }} · {{ questionAnswerScheduleStatus(execution.status) }}</p><p v-if="execution.statusReason" class="break-words text-muted-foreground">{{ questionAnswerScheduleReason(execution.statusReason) }}</p><p>总正确率 {{ formatQuestionAnswerAccuracy(questionAnswerAccuracy(execution.stats)) }} · 选择 {{ execution.plannedTargetCount }} 个账号 · 已建批 {{ execution.batchCreatedTargetCount }}</p><p class="text-muted-foreground">查看执行详情和准确批次</p></button></div><div v-if="historyTotalPages > 1" class="mt-4 flex justify-center gap-3"><button type="button" :disabled="historyPage <= 1 || loading" @click="pageTo(historyPage - 1)">上一页</button><span class="text-xs">{{ historyPage }} / {{ historyTotalPages }}</span><button type="button" :disabled="historyPage >= historyTotalPages || loading" @click="pageTo(historyPage + 1)">下一页</button></div></template>
      <template v-else><div class="mb-4 flex flex-wrap justify-between gap-2"><div class="flex gap-2"><button v-for="tab in (['active', 'deleted'] as const)" :key="tab" type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 text-xs" :class="status === tab ? 'bg-primary/10 text-primary' : 'text-muted-foreground'" :aria-pressed="status === tab" @click="changeTab(tab)">{{ tab === 'active' ? '当前计划' : '已删除计划' }}</button></div><div class="flex gap-2"><button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 text-xs" :disabled="Boolean(mutating)" @click="edit(null)">新建计划</button><button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 text-xs" @click="openProtection">运行保护</button><button type="button" aria-label="刷新计划" class="rounded-md border border-border/60 p-1.5" @click="load"><Loader2 v-if="loading" class="h-4 w-4 animate-spin" /><span v-else class="text-xs">刷新</span></button></div></div><p v-if="loading && items.length === 0" class="text-xs text-muted-foreground">正在读取计划…</p><p v-else-if="items.length === 0 && !error" class="rounded-lg border border-dashed border-border/50 p-6 text-center text-xs text-muted-foreground">{{ status === 'deleted' ? '没有已删除计划。' : '尚无定时测试计划，请先新建计划。' }}</p><div class="space-y-3"><article v-for="schedule in items" :key="schedule.id" data-testid="schedule-row" class="space-y-2 rounded-lg border border-border/50 p-4 text-xs"><div class="flex flex-wrap items-start justify-between gap-2"><h3 class="min-w-0 break-words text-sm font-semibold" :title="schedule.name">{{ schedule.name }}</h3><span>{{ scheduleLabel(schedule) }}</span></div><p v-if="schedule.blockedReason" class="break-words text-amber-600">{{ questionAnswerScheduleReason(schedule.blockedReason) }}</p><p class="break-words text-muted-foreground">{{ schedule.targetMode === 'groups' ? '按分组' : '指定账号' }} · 保存时预计 {{ schedule.estimatedTargetCount }} 个账号（{{ questionAnswerScheduleTime(schedule.previewedAt) }}） · 上次实际执行 {{ schedule.lastActualTargetCount ?? '—' }} 个账号</p><p class="break-all text-muted-foreground" :title="schedule.models.join('、')">{{ schedule.models.join('、') }} · {{ schedule.questionIds.length }} 题 · 每题 {{ schedule.repeatCount }} 次</p><p class="break-words text-muted-foreground">高峰 {{ schedule.peakStart.slice(0, 5) }}–{{ schedule.peakEnd.slice(0, 5) }} 每{{ schedule.peakIntervalMinutes }}分钟 · 低峰每{{ schedule.offPeakIntervalMinutes }}分钟</p><p>下次：{{ questionAnswerScheduleTime(schedule.nextRunAt) }} · 最近：{{ schedule.lastExecution ? `${questionAnswerScheduleTime(schedule.lastExecution.scheduledFor)} ${questionAnswerScheduleStatus(schedule.lastExecution.status)}` : '尚无执行' }}</p><div class="flex flex-wrap gap-2"><template v-if="!schedule.deletedAt"><button v-if="schedule.blockedReason" type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 disabled:opacity-50" :disabled="Boolean(mutating)" @click="mutate(schedule, 'revalidate')">重新检查</button><template v-else><button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 disabled:opacity-50" :disabled="Boolean(mutating)" @click="mutate(schedule, schedule.enabled ? 'disable' : 'enable')">{{ schedule.enabled ? '暂停' : '恢复' }}</button><button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 disabled:opacity-50" :disabled="Boolean(mutating) || dirty" :title="dirty ? '请先保存修改' : '使用已保存配置，不改变计划时刻'" @click="mutate(schedule, 'run')">立即执行</button></template></template><button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5" @click="viewHistory(schedule)">历史</button><template v-if="!schedule.deletedAt"><button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 disabled:opacity-50" :disabled="Boolean(mutating)" @click="edit(schedule)">编辑</button><button type="button" class="rounded-md border border-destructive/30 px-2.5 py-1.5 text-destructive disabled:opacity-50" :disabled="Boolean(mutating)" @click="mutate(schedule, 'delete')">删除</button></template></div></article></div><div v-if="totalPages > 1" class="mt-4 flex justify-center gap-3"><button type="button" :disabled="page <= 1 || loading" @click="pageTo(page - 1)">上一页</button><span class="text-xs">{{ page }} / {{ totalPages }}</span><button type="button" :disabled="page >= totalPages || loading" @click="pageTo(page + 1)">下一页</button></div><p class="mt-4 text-[11px] leading-5 text-muted-foreground">计划及历史每30秒只读取本地数据库。列表显示保存时预计与上次实际；进入编辑或显式刷新当前预计才读取主站库存。</p></template>
    </div>
  </section></div>
</template>
