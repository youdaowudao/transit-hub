<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { t, te } from '@/locales'
import { ConnectionHealthApiError, getQuestionAnswerRuntimeSettings, getQuestionAnswerScheduleLimits, saveQuestionAnswerRuntimeSettings, saveQuestionAnswerScheduleLimits } from '../../api/connectionHealth'
import { connectionHealthMessageKey } from '../../composables/useConnectionHealth'
import type { QuestionAnswerRuntimeSettings, QuestionAnswerScheduleLimits } from '../../types/connectionHealth'
const props = defineProps<{ workspace: string }>()
const emit = defineEmits<{ (event: 'back'): void; (event: 'saved'): void }>()
const runtime = ref<QuestionAnswerRuntimeSettings | null>(null), limits = ref<QuestionAnswerScheduleLimits | null>(null)
const concurrency = ref<number | string>(15), limitDraft = ref<Record<string, number | string>>({})
const runtimeError = ref(''), limitsError = ref(''), notice = ref(''), saving = ref<'runtime' | 'limits' | ''>('')
let generation = 0
const controllers = new Set<AbortController>()
const fields = [
  ['maxEnabledQuestionAnswerSchedules', '最大启用计划数', 1, 100], ['maxScheduleTargets', '单计划最大实际目标数', 1, 200],
  ['maxScheduleRequestsPerExecution', '单次执行最大请求数', 1, 10000], ['dailyScheduledRequestLimit', '新加坡自然日定时请求预算', 1, 1000000],
  ['maxActiveScheduleExecutions', '同时活动的定时执行数', 1, 20], ['maxQueuedScheduledRequests', '等待中的定时请求数', 0, 100000],
  ['scheduleExecutionTimeoutMinutes', '整次执行超时（分钟）', 30, 1440], ['scheduleLateGraceMinutes', '到期启动宽限（分钟）', 0, 30],
] as const
const readable = (key: string) => t(connectionHealthMessageKey(key, te))
const valid = (value: number | string, min: number, max: number) => typeof value === 'number' && Number.isInteger(value) && value >= min && value <= max
const runtimeValid = () => valid(concurrency.value, 1, 50)
const limitsValid = () => fields.every(([key, , min, max]) => valid(limitDraft.value[key] ?? '', min, max))
const applyRuntime = (current: QuestionAnswerRuntimeSettings) => { runtime.value = current; concurrency.value = current.questionAnswerConcurrency }
const applyLimits = (current: QuestionAnswerScheduleLimits) => { limits.value = current; limitDraft.value = Object.fromEntries(fields.map(([key]) => [key, current[key]])) }
const invalidate = () => { generation++; for (const controller of controllers) controller.abort(); controllers.clear(); saving.value = '' }
const load = async () => {
  invalidate(); const own = generation, workspace = props.workspace
  const controller = new AbortController(); controllers.add(controller)
  const current = () => own === generation && workspace === props.workspace && !controller.signal.aborted
  await Promise.all([
    getQuestionAnswerRuntimeSettings(controller.signal).then(value => { if (current()) { applyRuntime(value); runtimeError.value = '' } }).catch(cause => { if (current()) runtimeError.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request' }),
    getQuestionAnswerScheduleLimits(controller.signal).then(value => { if (current()) { applyLimits(value); limitsError.value = '' } }).catch(cause => { if (current()) limitsError.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request' }),
  ])
  controllers.delete(controller)
}
const save = async (kind: 'runtime' | 'limits') => {
  if (saving.value || (kind === 'runtime' ? !runtime.value || !runtimeValid() : !limits.value || !limitsValid())) return
  const own = generation, workspace = props.workspace, controller = new AbortController(); controllers.add(controller)
  const current = () => own === generation && workspace === props.workspace && !controller.signal.aborted
  saving.value = kind; notice.value = ''; if (kind === 'runtime') runtimeError.value = ''; else limitsError.value = ''
  try {
    if (kind === 'runtime') {
      const result = await saveQuestionAnswerRuntimeSettings(Number(concurrency.value), runtime.value!.version, controller.signal)
      if (!current()) return
      applyRuntime(result); notice.value = '共享请求槽已保存并立即生效；降低容量不取消在途请求。'
    } else {
      const result = await saveQuestionAnswerScheduleLimits(Object.fromEntries(fields.map(([key]) => [key, Number(limitDraft.value[key])])) as Omit<QuestionAnswerScheduleLimits, 'todayReservedRequests' | 'version' | 'updatedAt'>, limits.value!.version, controller.signal)
      if (!current()) return
      applyLimits(result); notice.value = '运行保护已保存，计划限制原因已重新校验；已有执行继续使用原快照。'
    }
    emit('saved')
  } catch (cause) {
    if (!current()) return
    const message = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request'
    if (kind === 'runtime') runtimeError.value = message; else limitsError.value = message
    if (cause instanceof ConnectionHealthApiError && cause.current && typeof cause.current === 'object') {
      if (kind === 'runtime' && 'questionAnswerConcurrency' in cause.current) applyRuntime(cause.current as QuestionAnswerRuntimeSettings)
      if (kind === 'limits' && 'maxScheduleTargets' in cause.current) applyLimits(cause.current as QuestionAnswerScheduleLimits)
    }
  } finally { controllers.delete(controller); if (current()) saving.value = '' }
}
watch(() => props.workspace, () => { runtime.value = null; limits.value = null; runtimeError.value = ''; limitsError.value = ''; notice.value = ''; void load() }, { immediate: true })
onBeforeUnmount(invalidate)
</script>
<template>
  <section data-testid="schedule-protection" class="space-y-5"><div class="flex justify-between gap-3"><h3 class="text-sm font-semibold">运行保护</h3><button type="button" class="text-xs text-muted-foreground" @click="emit('back')">返回计划</button></div>
    <div class="space-y-3 rounded-lg border border-border/50 p-4"><h4 class="text-sm font-medium">实例共享请求槽</h4><p class="text-xs leading-5 text-muted-foreground">全部工作区的手动和定时问答共用唯一容量，默认15。保存立即生效；降低容量时已有请求继续，后续等待新容量。</p><label class="block text-xs">问答请求并发（1–50）<input v-model.number="concurrency" aria-label="问答请求并发" type="number" min="1" max="50" step="1" class="mt-2 w-full rounded-lg border border-border/50 bg-background px-3 py-2" :disabled="Boolean(saving)"></label><p v-if="!runtimeValid()" class="text-xs text-destructive">请输入1–50的整数。</p><p v-if="runtimeError" role="alert" class="break-words text-xs text-destructive">{{ readable(runtimeError) }}</p><button v-if="!runtime" type="button" class="text-xs text-primary underline" @click="load">重试读取</button><button type="button" class="rounded-lg bg-primary px-3 py-2 text-xs text-primary-foreground disabled:opacity-50" :disabled="!runtime || !runtimeValid() || Boolean(saving)" @click="save('runtime')">{{ saving === 'runtime' ? '保存中…' : '保存共享请求槽' }}</button></div>
    <div class="space-y-3 rounded-lg border border-border/50 p-4"><h4 class="text-sm font-medium">当前工作区计划限制</h4><p class="text-xs leading-5 text-muted-foreground">只影响新接受的执行。降低最大启用数不随机暂停已有计划；取消或完成不退回当天准入预算。</p><p class="text-xs">今日已保留请求 {{ limits?.todayReservedRequests ?? '—' }}</p><div class="grid gap-3 sm:grid-cols-2"><label v-for="[key, label, min, max] in fields" :key="key" class="text-xs">{{ label }}<input v-model.number="limitDraft[key]" :aria-label="label" type="number" :min="min" :max="max" step="1" class="mt-2 w-full rounded-lg border border-border/50 bg-background px-3 py-2" :disabled="!limits || Boolean(saving)"><span class="mt-1 block text-[11px] text-muted-foreground">{{ min }}–{{ max }} 整数</span></label></div><p v-if="limits && !limitsValid()" class="text-xs text-destructive">请填写范围内的整数。</p><p v-if="limitsError" role="alert" class="break-words text-xs text-destructive">{{ readable(limitsError) }}</p><button v-if="!limits" type="button" class="text-xs text-primary underline" @click="load">重试读取</button><button type="button" class="rounded-lg bg-primary px-3 py-2 text-xs text-primary-foreground disabled:opacity-50" :disabled="!limits || !limitsValid() || Boolean(saving)" @click="save('limits')">{{ saving === 'limits' ? '保存中…' : '保存工作区运行保护' }}</button></div>
    <p v-if="notice" role="status" class="break-words text-xs text-primary">{{ notice }}</p>
  </section>
</template>
