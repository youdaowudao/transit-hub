<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { t, te } from '@/locales'
import { useQuestionAnswerQueue } from '../../composables/useQuestionAnswerQueue'
import { formatConnectionHealthTime } from '../../composables/useConnectionHealth'
import { questionAnswerReadableError, questionAnswerSnapshotTime } from '../../utils/questionAnswers'
import type { QuestionAnswerQueueItem, QuestionAnswerQueueTarget } from '../../types/connectionHealth'

const props = withDefaults(defineProps<{ workspace: string; visible?: boolean; active?: boolean; accounts?: Record<string, string> }>(), { visible: true, active: false })
const emit = defineEmits<{ (event: 'view', value: { targetId: string; batchId: string }): void }>()
const expanded = ref(false), limit = ref(50), receivedAt = ref(Date.now()), clock = ref(Date.now())
const { snapshot, error, stale, loading, refresh } = useQuestionAnswerQueue({ workspace: () => props.workspace, visible: () => props.visible, active: () => props.active })
let timer: ReturnType<typeof setInterval> | null = null
const clearClock = () => { if (timer !== null) clearInterval(timer); timer = null }
watch(() => snapshot.value?.snapshotAt, () => { receivedAt.value = Date.now(); clock.value = receivedAt.value })
watch(() => [expanded.value, props.visible, stale.value, snapshot.value?.processing.length] as const, ([open, shown, old, count]) => {
  clearClock()
  if (open && shown && !old && count) timer = setInterval(() => { clock.value = Date.now() }, 1000)
}, { immediate: true })
watch(() => props.workspace, () => { expanded.value = false; limit.value = 50 })
onBeforeUnmount(clearClock)
const time = (value?: string | null) => value && Number.isFinite(Date.parse(value)) ? formatConnectionHealthTime(value) : '当时未记录'
const account = (targetId: string) => props.accounts?.[targetId] || `账号编号 ${targetId.split(':').at(-1) || targetId}`
const more = computed(() => snapshot.value && Math.max(snapshot.value.queued.length, snapshot.value.processing.length, snapshot.value.stopping.length, snapshot.value.preparingTargets.length, snapshot.value.finalizingBatches.length) > limit.value)
const serverNow = computed(() => questionAnswerSnapshotTime(snapshot.value?.snapshotAt, receivedAt.value, clock.value, stale.value))
const duration = (milliseconds: number) => `${Math.max(0, milliseconds / 1000).toFixed(1)}秒`
const progress = (item: QuestionAnswerQueueItem) => {
  const runtime = item.runtime, d = runtime?.diagnostics
  if (!runtime) return '后台过程信息不可用，无法确认当前进度。'
  if (runtime.step === 'saving' || runtime.step === 'confirming') return `${runtime.pendingResultStatus === 'succeeded' ? '已取得完整回答' : '请求已结束'}，${runtime.storageError ? `保存尚未确认：${runtime.storageError}。暂时保留执行位置，请查看该批次的实际结果。` : '正在保存结果。'}`
  if (d?.responseHeadersAt) return `已收到响应，最近在${time(d.lastReceivedAt)}收到内容；内容可能是心跳，不代表答案已生成。`
  if (d?.requestWrittenAt) return '客户端已写出请求，正在等响应；这不能证明对端已经收到。'
  return '已开始处理，正在记录开始/准备连接；尚无请求写出完成的记录。'
}
const longWait = (item: QuestionAnswerQueueItem) => {
  const d = item.runtime?.diagnostics
  if (!d?.requestStartedAt || d.requestFinishedAt || !Number.isFinite(Date.parse(d.requestStartedAt))) return ''
  const elapsed = Math.max(0, serverNow.value - Date.parse(d.requestStartedAt))
  if (elapsed < 180000) return ''
  return `HTTP已等待${duration(elapsed)}，本题最长等待${d.timeoutMs == null ? '当时未记录' : duration(d.timeoutMs)}，排队和保存不计入；仍在处理。`
}
const view = (item: QuestionAnswerQueueTarget) => emit('view', { targetId: item.targetId, batchId: item.batchId })
</script>

<template>
  <section data-testid="question-answer-queue-panel" class="space-y-2 rounded-lg border border-border/50 p-3 text-xs">
    <button data-testid="question-answer-queue-toggle" type="button" class="w-full text-left font-medium" :aria-expanded="expanded" @click="expanded = !expanded">排队与处理详情（{{ snapshot?.counts.queued ?? '—' }}等待，{{ snapshot?.counts.processing ?? '—' }}处理中）</button>
    <p class="text-muted-foreground">查看本工作区已接收的题目、预计开始顺序和当前进度；展开不会发题，也不会修改主站。</p>
    <p v-if="snapshot?.counts.finalizingBatches" class="text-amber-600">有{{ snapshot.counts.finalizingBatches }}个批次结束尚未确认，请展开查看具体原因和每题实际结果。</p>
      <p v-if="snapshot">全局最多同时处理{{ snapshot.capacity }}题，目前还有{{ snapshot.availableSlots }}个位置；多个账号共用。<template v-if="snapshot.heldCount">本工作区有{{ snapshot.heldCount }}个位置因批次结束尚未确认而暂时保留，原因见下方批次详情。</template></p>
      <p v-if="error" role="alert" data-testid="question-answer-queue-error" class="break-words text-destructive">排队信息尚未更新，上次更新{{ time(snapshot?.snapshotAt) }}。读取失败：{{ questionAnswerReadableError(error, t, te) }}；点击重新读取只查询状态，不会重新发题。</p>
      <p v-if="stale && snapshot" class="text-muted-foreground">当前过程截止于{{ time(snapshot.snapshotAt) }}；读取尚未更新，等待时间暂停递增。</p>
      <button data-testid="question-answer-queue-refresh" type="button" class="underline disabled:opacity-50" :disabled="loading" @click="refresh">重新读取</button>
      <p class="text-muted-foreground">重新读取只查询状态，不会重新发题，也不会修改主站。</p>
    <p v-if="snapshot && Object.values(snapshot.counts).every(count => count === 0)" class="text-muted-foreground">当前没有已知任务；其他账号后来加入的任务请点击重新读取查看。未在内存中的批次不会推测排位，请查看原批次记录。</p>
    <div v-if="expanded" class="space-y-3">
      <p v-if="!snapshot" class="text-muted-foreground">{{ loading ? '正在读取本工作区排队信息…' : '后台过程信息不可用，无法确认当前进度；原批次结果和人工判断仍可查看。' }}</p>
      <template v-if="snapshot">
        <p class="text-muted-foreground">这是当前工作区预计开始顺序，会随任务加入或终止变化；同时处理时，实际写出先后可能不同。</p>
        <article v-for="item in snapshot.preparingTargets.slice(0, limit)" :key="`preparing:${item.targetId}:${item.batchId}`" data-testid="question-answer-queue-preparing-row" class="space-y-1 border-t border-border/40 pt-2"><p>{{ account(item.targetId) }} · 准备批次 #{{ item.batchId }}</p><p>正在提交：正在准备账号和题目，后台尚未确认接收；题目数量尚未确认。</p><button type="button" class="underline" @click="view(item)">查看该批次</button></article>
        <article v-for="item in snapshot.queued.slice(0, limit)" :key="item.recordId" data-testid="question-answer-queue-queued-row" class="space-y-1 border-t border-border/40 pt-2"><p class="break-words">预计第{{ item.position }}位 · {{ account(item.targetId) }} · {{ item.modelName }} · {{ item.questionName }} · 第{{ item.repeatIndex ?? '—' }}次</p><p>本题仍在排队，尚未开始HTTP等待。创建 {{ time(item.createdAt) }} · 批次 #{{ item.batchId }} · 记录 #{{ item.recordId }}</p><button type="button" class="underline" @click="view(item)">查看该批次</button></article>
        <article v-for="item in snapshot.processing.slice(0, limit)" :key="item.recordId" data-testid="question-answer-queue-processing-row" class="space-y-1 border-t border-border/40 pt-2"><p class="break-words">{{ account(item.targetId) }} · {{ item.modelName }} · {{ item.questionName }} · 第{{ item.repeatIndex ?? '—' }}次</p><p>{{ progress(item) }}</p><p v-if="longWait(item)">{{ longWait(item) }}</p><p v-if="item.runtime?.diagnostics?.localError" class="break-words">已有具体原因：{{ item.runtime.diagnostics.localError }}</p><p class="break-all">批次 #{{ item.batchId }} · 记录 #{{ item.recordId }}</p><button type="button" class="underline" @click="view(item)">查看该批次</button></article>
        <article v-for="item in snapshot.stopping.slice(0, limit)" :key="item.recordId" data-testid="question-answer-queue-stopping-row" class="space-y-1 border-t border-border/40 pt-2"><p class="break-words">{{ account(item.targetId) }} · {{ item.modelName }} · {{ item.questionName }}</p><p>已停止发送，结束结果尚在确认；这里不判断是否已保存，请查看批次中的实际结果。</p><button type="button" class="underline" @click="view(item)">查看该批次</button></article>
        <article v-for="item in snapshot.finalizingBatches.slice(0, limit)" :key="`${item.targetId}:${item.batchId}`" data-testid="question-answer-queue-finalizing-row" class="space-y-1 border-t border-border/40 pt-2"><p class="break-words">{{ account(item.targetId) }} · 批次#{{ item.batchId }}结束尚未确认：{{ item.message }}；该批次暂时保留{{ item.heldCount }}个位置，请查看每题实际结果。</p><button type="button" class="underline" @click="view(item)">查看该批次</button></article>
        <p class="text-muted-foreground">“查看该批次”读取这个批次的记录和结束情况，不会发题或修改主站。</p>
        <template v-if="more"><button data-testid="question-answer-queue-more" type="button" class="underline" @click="limit += 50">显示更多50条</button><p class="text-muted-foreground">继续显示已接收的排队记录，不会提交新任务。</p></template>
      </template>
    </div>
  </section>
</template>
