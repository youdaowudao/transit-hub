<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useDocumentVisibility } from '@vueuse/core'
import { t, te } from '@/locales'
import { Loader2 } from 'lucide-vue-next'
import { ConnectionHealthApiError, cancelQuestionAnswerScheduleExecution, getQuestionAnswerScheduleExecution } from '../../api/connectionHealth'
import { connectionHealthMessageKey } from '../../composables/useConnectionHealth'
import { formatQuestionAnswerAccuracy, questionAnswerAccuracy } from '../../utils/questionAnswers'
import { questionAnswerScheduleIsActive, questionAnswerScheduleReason, questionAnswerScheduleStatus, questionAnswerScheduleTime } from '../../utils/questionAnswerSchedules'
import type { QuestionAnswerScheduleExecution, QuestionAnswerScheduleExecutionDetail, QuestionAnswerScheduleExecutionTarget, QuestionAnswerStats } from '../../types/connectionHealth'

const props = defineProps<{ executionId: string; workspace: string; scheduleName: string; statsRevision?: number }>()
const emit = defineEmits<{ (event: 'back'): void; (event: 'question-answer-view', value: { targetId: string; batchId: string; accountName: string; platform: string; groupName: string }): void; (event: 'settled', targetIds: string[]): void }>()
const documentVisibility = useDocumentVisibility()
const execution = ref<QuestionAnswerScheduleExecutionDetail | null>(null), loading = ref(false), cancelling = ref(false), error = ref(''), notice = ref('')
let lifecycle = 0, revision = 0, timer: ReturnType<typeof setTimeout> | null = null, readController: AbortController | null = null, cancelController: AbortController | null = null
const readable = (key: string) => t(connectionHealthMessageKey(key, te))
const accuracy = (stats: Pick<QuestionAnswerStats, 'reviews'>) => formatQuestionAnswerAccuracy(questionAnswerAccuracy(stats))
const counts = (stats: QuestionAnswerStats) => `请求状态：成功 ${stats.requests.succeeded} · 失败 ${stats.requests.failed} · 取消 ${stats.requests.cancelled} · 进行中 ${stats.requests.inProgress}`
const reviews = (stats: QuestionAnswerStats) => `判题状态：正确 ${stats.reviews.correct} · 错误 ${stats.reviews.incorrect} · 待人工 ${stats.reviews.unreviewed}`
const models = computed(() => [...new Set([...(execution.value?.stats.byModel?.map(model => model.modelName) ?? []), ...(execution.value?.targets.flatMap(target => target.requestedModels) ?? [])])])
const modelStats = (name: string) => execution.value?.stats.byModel?.find(model => model.modelName === name)
const modelUnavailable = (name: string) => execution.value?.targets.some(target => target.unavailableModels.some(model => model.modelName === name)) ?? false
const active = computed(() => Boolean(execution.value && questionAnswerScheduleIsActive(execution.value.status)))
const frozen = computed(() => execution.value?.configSnapshot.requested.schedule)
const frozenQuestions = computed(() => {
  const questions = execution.value?.configSnapshot.resolved?.questions
  return Array.isArray(questions) ? questions as Array<{ id: string; name: string; body: string; keywords: string[] }> : []
})
const clearTimer = () => { if (timer) clearTimeout(timer); timer = null }
const apply = (current: QuestionAnswerScheduleExecution | QuestionAnswerScheduleExecutionDetail) => {
  if (current.id !== props.executionId || execution.value && current.version < execution.value.version) return
  const wasActive = execution.value ? questionAnswerScheduleIsActive(execution.value.status) : false
  execution.value = { ...execution.value, ...current } as QuestionAnswerScheduleExecutionDetail
  if (wasActive && !questionAnswerScheduleIsActive(current.status)) emit('settled', execution.value.targets?.map(target => target.targetId) ?? [])
}
const schedulePoll = () => { clearTimer(); if (active.value && documentVisibility.value === 'visible') timer = setTimeout(() => void load(), 2000) }
const load = async () => {
  if (documentVisibility.value !== 'visible' || cancelling.value) return
  readController?.abort(); clearTimer(); const own = ++revision, generation = lifecycle, workspace = props.workspace, id = props.executionId
  const controller = new AbortController(); readController = controller; loading.value = true
  const current = () => documentVisibility.value === 'visible' && own === revision && generation === lifecycle && workspace === props.workspace && id === props.executionId && !controller.signal.aborted
  try { const result = await getQuestionAnswerScheduleExecution(id, controller.signal); if (current()) { apply(result); error.value = '' } }
  catch (cause) { if (current()) error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request' }
  finally { if (current()) { loading.value = false; schedulePoll() }; if (readController === controller) readController = null }
}
const cancel = async () => {
  if (!execution.value || !active.value || cancelling.value || execution.value.terminationCause) return
  const targetNames = execution.value.targets.map(target => target.accountSnapshot.accountName || target.targetId).join('、') || '尚未冻结的目标'
  if (!window.confirm(`终止计划“${props.scheduleName}”在 ${questionAnswerScheduleTime(execution.value.scheduledFor)} 的本次执行？\n目标：${targetNames}\n这不会暂停计划。`)) return
  readController?.abort(); revision++; clearTimer(); const generation = lifecycle, workspace = props.workspace, id = props.executionId
  const controller = new AbortController(); cancelController = controller; cancelling.value = true; notice.value = ''; error.value = ''
  const current = () => generation === lifecycle && workspace === props.workspace && id === props.executionId && !controller.signal.aborted
  try {
    const result = await cancelQuestionAnswerScheduleExecution(id, execution.value.version, controller.signal)
    if (!current()) return
    apply(result); notice.value = result.status === 'cancelled' ? '本次执行已终止。' : '终止请求已接受，正在等这批问答停下来'
  } catch (cause) {
    if (!current()) return
    if (cause instanceof ConnectionHealthApiError && cause.current && typeof cause.current === 'object' && 'id' in cause.current) apply(cause.current as QuestionAnswerScheduleExecution)
    error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request'
  } finally { if (current()) { cancelling.value = false; loading.value = false; schedulePoll() }; if (cancelController === controller) cancelController = null }
}
const viewBatch = (target: QuestionAnswerScheduleExecutionTarget) => { if (target.batchAvailable) emit('question-answer-view', { targetId: target.targetId, batchId: target.batchId, accountName: target.accountSnapshot.accountName || target.targetId, platform: target.accountSnapshot.platform, groupName: target.matchedGroupsSnapshot.map(group => group.name).join('、') }) }
const invalidate = () => { lifecycle++; revision++; clearTimer(); readController?.abort(); cancelController?.abort(); cancelling.value = false }
watch(() => [props.executionId, props.workspace], () => { invalidate(); execution.value = null; notice.value = ''; error.value = ''; void load() }, { immediate: true })
watch(documentVisibility, state => { revision++; clearTimer(); readController?.abort(); loading.value = false; if (state === 'visible' && !cancelling.value) void load() })
watch(() => props.statsRevision, () => { if (!cancelling.value) void load() })
onBeforeUnmount(invalidate)
</script>
<template>
  <section data-testid="schedule-execution-detail" class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-2"><button type="button" class="text-xs text-muted-foreground" @click="emit('back')">回到执行历史</button><button type="button" class="inline-flex items-center gap-1 rounded-md border border-border/60 px-2.5 py-1.5 text-xs disabled:opacity-50" :disabled="cancelling" @click="load"><Loader2 v-if="loading" class="h-3.5 w-3.5 animate-spin" />刷新详情</button></div>
    <div v-if="error" role="alert" class="rounded-lg bg-destructive/10 p-3 text-xs text-destructive"><p class="break-words">{{ readable(error) }}</p><button type="button" class="mt-2 underline" :disabled="cancelling" @click="load">重试读取</button></div>
    <p v-if="loading && !execution" class="text-xs text-muted-foreground">正在读取执行记录…</p>
    <template v-if="execution">
      <div><h3 class="break-words text-sm font-semibold">{{ scheduleName }} · {{ questionAnswerScheduleTime(execution.scheduledFor) }} · {{ t(`admin.connectionHealth.questionAnswerSchedule.sources.${execution.trigger}`) }}</h3><p data-testid="schedule-execution-status" class="mt-2 break-words text-xs">{{ questionAnswerScheduleStatus(execution.status) }}<template v-if="execution.statusReason"> · {{ questionAnswerScheduleReason(execution.statusReason) }}</template></p><p v-if="execution.terminationCause" class="mt-1 text-xs text-amber-600">{{ active ? (execution.terminationCause === 'user_cancel' ? '已接受终止，正在等它停下来' : '执行超时，正在等它停下来') : (execution.terminationCause === 'user_cancel' ? '终止原因：用户终止' : '终止原因：整次执行超时') }}</p></div>
      <section class="space-y-2 rounded-lg border border-border/50 p-4"><p class="text-xs text-muted-foreground">总正确率</p><strong data-testid="schedule-total-accuracy" class="text-3xl text-primary">{{ accuracy(execution.stats) }}</strong><p class="break-words text-xs">{{ counts(execution.stats) }}</p><p class="break-words text-xs">{{ reviews(execution.stats) }}</p><p class="text-[11px] text-muted-foreground">选择目标 {{ execution.plannedTargetCount }} · 已建批 {{ execution.batchCreatedTargetCount }} · 请求记录 {{ execution.requestRecordCount }} · 保留预算 {{ execution.reservedRequestCount }}</p><p v-if="execution.missedCount" class="break-words text-xs">错过 {{ execution.missedCount }} 个网格时刻：{{ questionAnswerScheduleTime(execution.missedFrom) }}–{{ questionAnswerScheduleTime(execution.missedThrough) }}</p></section>
      <section><h4 class="mb-2 text-xs font-semibold">按模型</h4><div class="space-y-2"><div v-for="name in models" :key="name" data-testid="schedule-model-stats" class="rounded-md border border-border/40 p-3 text-xs"><p class="break-all font-medium">{{ name }} · {{ modelStats(name) ? accuracy(modelStats(name)!) : '—' }}<template v-if="modelUnavailable(name)"> · {{ modelStats(name)?.requests.succeeded ? '部分账号本次不可用' : '本次不可用' }}</template></p></div><p v-if="models.length === 0" class="text-xs text-muted-foreground">没有已冻结的模型结果</p></div></section>
      <section><h4 class="mb-2 text-xs font-semibold">账号</h4><div class="space-y-2"><article v-for="target in execution.targets" :key="target.targetId" data-testid="schedule-target" class="space-y-2 rounded-lg border border-border/50 p-3 text-xs"><div class="flex flex-wrap items-center justify-between gap-2"><p class="min-w-0 break-words font-medium">{{ target.accountSnapshot.accountName || target.targetId }} · {{ accuracy(target.stats) }}</p><button v-if="target.batchAvailable" type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 hover:text-primary" @click="viewBatch(target)">查看批次</button></div><p class="break-words text-muted-foreground">{{ target.status === 'batch_created' ? '已创建批次' : target.status === 'failed' ? '失败' : target.status === 'skipped' ? '跳过' : target.status === 'cancelled' ? '取消' : '准备中' }}<template v-if="target.statusReason"> · {{ questionAnswerScheduleReason(target.statusReason) }}</template></p><p class="break-words text-muted-foreground">当时分组：{{ target.matchedGroupsSnapshot.map(group => group.name).join('、') || '无' }} · 协议：{{ target.testConfigurationSnapshot.protocol ?? '—' }}</p><p>{{ counts(target.stats) }}</p><p>{{ reviews(target.stats) }}</p><p v-for="model in target.requestedModels" :key="model" class="break-all text-muted-foreground">{{ model }} · {{ target.stats.byModel?.find(item => item.modelName === model) ? accuracy(target.stats.byModel.find(item => item.modelName === model)!) : '—' }}<template v-if="target.unavailableModels.some(item => item.modelName === model)"> · 本次不可用</template></p></article><p v-if="execution.targets.length === 0" class="text-xs text-muted-foreground">本次尚无冻结账号；跳过或准备失败不会创建残缺批次。</p></div></section>
      <details class="rounded-lg border border-border/50 p-3 text-xs"><summary class="cursor-pointer font-medium">本次不可变配置快照</summary><p class="mt-2 break-words text-muted-foreground">模型：{{ frozen?.models.join('、') || '—' }} · Reasoning effort {{ frozen?.reasoningEffort ?? '—' }} · 每题 {{ frozen?.repeatCount ?? '—' }} 次</p><div v-for="question in frozenQuestions" :key="question.id" class="mt-3 border-t border-border/40 pt-2"><p class="break-words font-medium">{{ question.name }}</p><p class="mt-1 whitespace-pre-wrap break-words text-muted-foreground">{{ question.body }}</p><p class="mt-1 break-words text-muted-foreground">关键词：{{ question.keywords?.join('、') || '仅人工判断' }}</p></div></details>
      <p v-if="notice" role="status" class="text-xs text-primary">{{ notice }}</p><button v-if="active && !execution.terminationCause" type="button" class="rounded-lg border border-destructive/30 px-3 py-2 text-xs text-destructive disabled:opacity-50" :disabled="cancelling" @click="cancel">{{ cancelling ? '接受终止中…' : '终止本次执行' }}</button><p v-if="active" class="text-[11px] text-muted-foreground">本次执行仍活动，每2秒读取本地状态；终止只作用于本次执行，不暂停计划。</p>
    </template>
  </section>
</template>
