<script setup lang="ts">
import { accountNameText, accountReminderLabel } from '../../utils/connectionHealthAccountLabel'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import type { QuestionAnswerRecord, QuestionAnswerRuntimeItem } from '../../types/connectionHealth'
import { formatConnectionHealthTime } from '../../composables/useConnectionHealth'
import { questionAnswerSavingMessage, questionAnswerSnapshotTime } from '../../utils/questionAnswers'

const props = defineProps<{
  accountName?: string
  accountNameNote?: string
  record: QuestionAnswerRecord
  runtime?: QuestionAnswerRuntimeItem
  snapshotAt?: string
  stale?: boolean
}>()
const receivedAt = ref(Date.now()), clock = ref(Date.now())
let timer: ReturnType<typeof setInterval> | null = null
const clearClock = () => { if (timer !== null) clearInterval(timer); timer = null }
const active = computed(() => props.record.status === 'pending' || props.record.status === 'running')
const diagnostics = computed(() => active.value ? props.runtime?.diagnostics ?? props.record.diagnostics : props.record.diagnostics)
watch(() => props.snapshotAt, () => { receivedAt.value = Date.now(); clock.value = receivedAt.value }, { immediate: true })
watch(() => [active.value, props.stale, diagnostics.value?.requestStartedAt] as const, ([running, stale, started]) => {
  clearClock()
  if (running && !stale && started) timer = setInterval(() => { clock.value = Date.now() }, 1000)
}, { immediate: true })
onBeforeUnmount(clearClock)
const serverNow = computed(() => questionAnswerSnapshotTime(props.snapshotAt, receivedAt.value, clock.value, props.stale))
const duration = (value: number | null | undefined): string => typeof value === 'number' && Number.isFinite(value) ? `${Math.max(0, value / 1000).toFixed(1)}秒` : '当时未记录'
const time = (value: string | null | undefined): string => value && Number.isFinite(Date.parse(value)) ? formatConnectionHealthTime(value) : '当时未记录'
const wait = computed(() => {
  const start = diagnostics.value?.selectedAt ?? props.record.startedAt
  if (!start || !Number.isFinite(Date.parse(start)) || !Number.isFinite(Date.parse(props.record.createdAt))) return '当时未记录'
  return `${diagnostics.value?.selectedAt ? '等待' : '从创建到记录开始'} ${duration(Date.parse(start) - Date.parse(props.record.createdAt))}`
})
const httpElapsed = computed(() => {
  const d = diagnostics.value
  if (typeof d?.requestElapsedMs === 'number') return d.requestElapsedMs
  if (!d?.requestStartedAt) return null
  const started = Date.parse(d.requestStartedAt)
  if (d.requestFinishedAt) return Number.isFinite(started) && Number.isFinite(Date.parse(d.requestFinishedAt)) ? Math.max(0, Date.parse(d.requestFinishedAt) - started) : null
  return Number.isFinite(started) ? Math.max(0, serverNow.value - started) : null
})
const saveWait = computed(() => {
  const finished = diagnostics.value?.requestFinishedAt
  return active.value && (props.runtime?.step === 'saving' || props.runtime?.step === 'confirming') && finished && Number.isFinite(Date.parse(finished)) ? Math.max(0, serverNow.value - Date.parse(finished)) : null
})
const stage = computed(() => {
  const d = diagnostics.value
  if (!active.value) return ''
  if (!props.runtime) return '后台过程信息不可用，无法确认当前进度。'
  if (props.runtime.step === 'saving' || props.runtime.step === 'confirming') return questionAnswerSavingMessage(props.runtime)
  if (d?.responseHeadersAt) return `已收到响应，最近在${time(d.lastReceivedAt)}收到内容；内容可能是心跳，不代表答案已生成。`
  if (d?.requestWrittenAt) return '客户端已写出请求，正在等响应；这不能证明对端已经收到。'
  return '已开始处理，正在记录开始/准备连接；尚无请求写出完成的记录。'
})
const stepName = computed(() => ({ starting: '记录这题开始', build: '构建请求', request: '发送请求或等待响应', receive: '读取响应', parse: '解析回答' })[diagnostics.value?.failureStep ?? 'starting'])
const failure = computed(() => {
  if (props.record.status !== 'failed') return ''
  const d = diagnostics.value
  const cause = d?.localError
  if (props.record.errorType === 'service_restarted') return '服务曾重启，本题未确认完成；未保存的回答无法恢复。请先查看历史，确认后再手动测试。'
  if (props.record.errorType === 'timeout' && d?.timeoutMs != null) return `本地最长等待${duration(d.timeoutMs)}已到，本次请求已结束，未取得可确认的完整回答。请先核查最后内容和对端记录，再决定是否手动测试，可能再次计费。`
  if (props.record.upstreamStatus && (props.record.upstreamStatus < 200 || props.record.upstreamStatus >= 300)) return `答题接口返回了错误（HTTP ${props.record.upstreamStatus}）：${props.record.upstreamExcerpt || '接口未返回具体原因'}。请核查该账号对应上游同一时刻的请求记录；本次不会自动重发。`
  if (cause && d?.failureStep === 'starting') return `记录这题开始时失败：${cause}；这条题目没有调用答题接口，后续发送已停止。请先读取这个批次，核查每题实际结果。`
  if (cause && (d?.failureStep === 'build' || d?.failureStep === 'parse' || props.record.errorType === 'response_too_large' || props.record.errorType === 'response_incomplete')) return `在${stepName.value}时发现：${cause}；未取得可判题的完整回答，本题不判对错。请核查返回格式或已显示的内容、输出上限，处理后再手动测试。`
  if (cause) return `在${stepName.value}时发生：${cause}；未取得可判题的完整回答。目前不能确认是本机、网络还是对端中断。请按下方批次、记录编号和时间核查后台日志；手动重测可能重复计费。`
  if (props.record.upstreamStatus === 200 && props.record.upstreamExcerpt) return `已收到响应，但接口明确报告失败或回答未完整结束：${props.record.upstreamExcerpt}。本题不判对错；处理具体原因后可手动测试。`
  if (props.record.errorType === 'storage_error') return '保存结果尚未确认，请先重新读取这个批次，核查每题实际结果后再处理；不要直接重新发题。'
  if (props.record.errorType === 'service_shutdown') return '服务停止时本题没有确认完成；请先查看历史，确认结果后再决定是否手动测试。'
  if (!d) return ''
  return '当前没有足够记录确认原因，请凭下方批次、记录编号和时间核查后台日志；本题不会自动重发。'
})
</script>

<template>
  <div data-testid="question-answer-execution-details" class="mt-2 space-y-2 text-xs leading-5 text-muted-foreground">
    <p v-if="stage" data-testid="question-answer-process-stage">{{ stage }}</p>
    <p v-if="saveWait !== null" data-testid="question-answer-save-wait">从请求结束到现在已等待保存/结束确认 {{ duration(saveWait) }}；这段时间不计入HTTP等待。</p>
    <p v-if="failure" data-testid="question-answer-direct-failure" class="break-words">{{ failure }}</p>
    <p v-if="active && httpElapsed !== null && httpElapsed >= 180000 && !diagnostics?.requestFinishedAt">HTTP已等待{{ duration(httpElapsed) }}，本题最长等待{{ duration(diagnostics?.timeoutMs) }}，排队和保存不计入；仍在处理。</p>
    <p v-if="stale">当前过程截止于{{ time(snapshotAt) }}；读取尚未更新，等待时间暂停递增。</p>
    <details>
      <summary data-testid="question-answer-execution-toggle" class="cursor-pointer font-medium text-foreground">展开过程与错误详情</summary>
      <p>查看这题的请求过程、失败步骤和已有原因；不会修改结果或主站。</p>
      <p class="break-all">{{ accountNameNote && !accountNameText(accountName) ? '账号（当时未记录名称）' : accountReminderLabel({ name: accountName }) }} · 模型 {{ record.modelName }} · {{ record.questionName }} · {{ record.repeatIndex ? `第${record.repeatIndex}次` : '历史样本' }}</p>
      <p v-if="accountNameNote" class="break-words">{{ accountNameNote }}</p>
      <p class="break-all">批次 #{{ record.batchId }} · 记录 #{{ record.id }} · 创建 {{ time(record.createdAt) }}</p>
      <p v-if="record.diagnosticsUnavailable">过程记录无法读取，答案和原状态仍保留。请按批次、记录编号和时间核查后台日志。</p>
      <p v-else-if="!diagnostics">当时未记录网络过程，无法确认具体卡在哪一步；可凭批次编号{{ record.batchId }}和时间{{ time(record.createdAt) }}核查后台日志。</p>
      <template v-if="diagnostics">
        <p>{{ wait }} · HTTP等待 {{ duration(httpElapsed) }} · HTTP最长等待 {{ duration(diagnostics.timeoutMs) }}；排队和保存另计。</p>
        <p>记录开始 {{ time(diagnostics.selectedAt) }} · HTTP开始 {{ time(diagnostics.requestStartedAt) }} · 请求结束 {{ time(diagnostics.requestFinishedAt) }}</p>
        <p>客户端写出 {{ time(diagnostics.requestWrittenAt) }} · 首次响应字节 {{ time(diagnostics.firstResponseByteAt) }} · 收到响应头 {{ time(diagnostics.responseHeadersAt) }}</p>
        <p>最近收到内容 {{ time(diagnostics.lastReceivedAt) }} · 已读取 {{ diagnostics.receivedBytes }} 字节；可能包含心跳，不代表答案文字。</p>
        <p v-if="diagnostics.failureStep">失败步骤：{{ stepName }}；原分类：{{ record.errorType || '未分类' }}</p>
        <p v-if="diagnostics.localError" class="whitespace-pre-wrap break-all">本地直接原因：{{ diagnostics.localError }}</p>
      </template>
    </details>
  </div>
</template>
