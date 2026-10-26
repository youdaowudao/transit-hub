<script setup lang="ts">
import { accountNameText, currentAccountNames, historicalAccountLabel, type AccountLabels } from '../../utils/connectionHealthAccountLabel'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useDocumentVisibility } from '@vueuse/core'
import { X } from 'lucide-vue-next'
import { t, te } from '@/locales'
import { getQuestionAnswerBatch } from '../../api/connectionHealth'
import { formatQuestionAnswerAccuracy, questionAnswerAccuracy, questionAnswerErrorMessageKey, questionAnswerReadableError } from '../../utils/questionAnswers'
import type { QuestionAnswerBatch } from '../../types/connectionHealth'
import QuestionAnswerRecordCard from './QuestionAnswerRecordCard.vue'
import QuestionAnswerFailureEvidence from './QuestionAnswerFailureEvidence.vue'
import QuestionAnswerExecutionDetails from './QuestionAnswerExecutionDetails.vue'
import QuestionAnswerQueuePanel from './QuestionAnswerQueuePanel.vue'
const props = defineProps<{ accountLabels?: AccountLabels; accountNameNote?: string; targetId: string; batchId: string; accountName: string; workspace: string }>()
const batchAccount = computed(() => {
  const name = accountNameText(props.accountName)
  if (name) return { name, note: props.accountNameNote ?? '当时未记录账号名称，以下名称来自当前资料' }
  const label = historicalAccountLabel(props.targetId, props.batchId, '', props.accountLabels?.[props.targetId]?.name)
  return { name: label.name, note: label.note }
})
const emit = defineEmits<{ (event: 'close'): void; (event: 'question-answer-view', value: { targetId: string; batchId: string }): void }>()
const viewQueueBatch = (item: { targetId: string; batchId: string }) => {
  if (item.targetId === props.targetId && item.batchId === props.batchId) void load()
  else emit('question-answer-view', item)
}
const batch = ref<QuestionAnswerBatch | null>(null), error = ref(''), expanded = ref<string[]>([])
const documentVisibility = useDocumentVisibility()
let timer: ReturnType<typeof setTimeout> | null = null
const clearTimer = () => { if (timer) clearTimeout(timer); timer = null }
let sequence = 0, controller: AbortController | null = null
const load = async () => {
  if (documentVisibility.value !== 'visible') return
  clearTimer(); controller?.abort(); const own = ++sequence, workspace = props.workspace, targetId = props.targetId, batchId = props.batchId
  const request = new AbortController(); controller = request
  try { const result = await getQuestionAnswerBatch(targetId, batchId, request.signal); if (own === sequence && workspace === props.workspace && targetId === props.targetId && batchId === props.batchId && !request.signal.aborted) { batch.value = result; error.value = '' } }
  catch (cause) { if (own === sequence && !request.signal.aborted) error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request' }
  finally { if (own === sequence && !request.signal.aborted && documentVisibility.value === 'visible' && batch.value?.active) timer = setTimeout(() => void load(), 2000) }
}
watch(() => [props.targetId, props.batchId, props.workspace], () => { batch.value = null; expanded.value = []; error.value = ''; void load() }, { immediate: true })
watch(documentVisibility, state => { sequence++; clearTimer(); controller?.abort(); if (state === 'visible') void load() })
onBeforeUnmount(() => { sequence++; clearTimer(); controller?.abort() })
</script>
<template>
  <Teleport to="body"><div class="fixed inset-0 z-[160] flex items-center justify-center bg-background/60 p-2" role="dialog" aria-modal="true" aria-label="问答批次（只读）"><section class="flex max-h-[calc(100dvh-1rem)] w-full max-w-4xl flex-col overflow-hidden rounded-2xl border border-border/60 bg-card shadow-2xl"><header class="flex items-center justify-between gap-3 border-b border-border/60 p-4"><div class="min-w-0"><h3 class="break-words text-sm font-semibold">{{ batchAccount.name || '账号（当时未记录名称）' }} · 批次 #{{ batchId.slice(0, 8) }}</h3><p v-if="batchAccount.note" class="mt-1 break-words text-xs text-muted-foreground">{{ batchAccount.note }}</p><p class="mt-1 text-xs text-muted-foreground">显示这批问答已有的账号资料，只能查看</p></div><button type="button" aria-label="关闭批次" @click="emit('close')"><X class="h-4 w-4" /></button></header><div class="space-y-4 overflow-y-auto p-4"><QuestionAnswerQueuePanel :workspace="workspace" :active="!!batch?.active" :accounts="currentAccountNames(accountLabels)" @view="viewQueueBatch" /><p v-if="error" role="alert" class="text-xs text-destructive">{{ questionAnswerReadableError(error, t, te) }}<button type="button" class="ml-2 underline" @click="load">重试读取</button></p><p v-if="!batch && !error" class="text-xs text-muted-foreground">读取中…</p><template v-if="batch"><div v-if="batch.finalization" data-testid="question-answer-readonly-finalization" class="space-y-1 rounded-lg border border-amber-500/30 p-3 text-xs"><p>批次结束尚未确认：{{ batch.finalization.message || '当前尚无具体原因，请按批次编号和时间核查后台日志' }}</p><p v-if="batch.finalization.heldCount != null">批次尚未结束，{{ batch.finalization.heldCount }}个位置暂时保留；这不是未保存的题数，请查看每题实际结果。</p><p>这里只能读取记录，不会重新发题或修改主站。</p><p v-if="batch.finalization.recovery === 'cancel'">如需重试保存并结束，请回到分组健康查找这个账号并打开问答；找不到账号时，请按批次编号、记录编号和时间核查后台日志。</p><p v-else-if="batch.finalization.recovery === 'service_shutdown'">系统里没有可取消的批次记录，需要维护人员核查并按原服务重启方式处理；重启会中断其他正在进行的问答，未保存的回答无法恢复。请先重新读取并核对历史。</p><p v-else>当前没有可用的保存恢复操作，请重新读取；仍未确认时，请按批次编号、记录编号和时间核查后台日志。</p></div><button type="button" class="text-xs text-primary underline" @click="load">刷新</button><div class="rounded-lg border border-border/50 p-3 text-xs"><p class="font-medium">当前批次总正确率 {{ formatQuestionAnswerAccuracy(questionAnswerAccuracy(batch.stats)) }}</p><p class="mt-2">请求状态：成功 {{ batch.stats.requests.succeeded }} · 失败 {{ batch.stats.requests.failed }} · 取消 {{ batch.stats.requests.cancelled }} · 进行中 {{ batch.stats.requests.inProgress }}</p><p class="mt-1">判题状态：正确 {{ batch.stats.reviews.correct }} · 错误 {{ batch.stats.reviews.incorrect }} · 待人工 {{ batch.stats.reviews.unreviewed }}</p><p v-for="model in batch.stats.byModel" :key="model.modelName" class="mt-2 break-all">{{ model.modelName }} · {{ formatQuestionAnswerAccuracy(questionAnswerAccuracy(model)) }}</p></div><article v-for="record in batch.records" :key="record.id" class="rounded-lg border border-border/50 p-3"><QuestionAnswerRecordCard :account-name="batchAccount.name" :account-name-note="batchAccount.note" v-if="record.status === 'succeeded'" :record="record" :expanded="expanded.includes(record.id)" :saving="false" read-only @expand="expanded = expanded.includes(record.id) ? expanded.filter(id => id !== record.id) : [...expanded, record.id]" /><template v-else><p class="break-words text-xs">{{ record.questionName }} · {{ record.modelName }} · {{ record.status }}<template v-if="record.errorType"> · {{ t(questionAnswerErrorMessageKey(record.errorType, te)) }}</template></p><QuestionAnswerFailureEvidence :account-name="batchAccount.name" :account-name-note="batchAccount.note" :record="record" /><QuestionAnswerExecutionDetails :account-name="batchAccount.name" :account-name-note="batchAccount.note" v-if="record.status !== 'failed'" :record="record" :runtime="batch.runtime?.items.find(item => item.recordId === record.id)" :snapshot-at="batch.runtime?.snapshotAt" :stale="!!error" /></template></article></template></div><footer class="flex justify-end border-t border-border/60 p-4"><button type="button" class="rounded-lg border border-border/60 px-3 py-2 text-xs" @click="emit('close')">关闭</button></footer></section></div></Teleport>
</template>
