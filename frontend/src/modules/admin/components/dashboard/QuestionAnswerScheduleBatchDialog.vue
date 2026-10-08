<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useDocumentVisibility } from '@vueuse/core'
import { X } from 'lucide-vue-next'
import { t, te } from '@/locales'
import { getQuestionAnswerBatch } from '../../api/connectionHealth'
import { connectionHealthMessageKey } from '../../composables/useConnectionHealth'
import { formatQuestionAnswerAccuracy, questionAnswerAccuracy } from '../../utils/questionAnswers'
import type { QuestionAnswerBatch } from '../../types/connectionHealth'
import QuestionAnswerRecordCard from './QuestionAnswerRecordCard.vue'
const props = defineProps<{ targetId: string; batchId: string; accountName: string; workspace: string }>()
const emit = defineEmits<{ (event: 'close'): void }>()
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
  <Teleport to="body"><div class="fixed inset-0 z-[160] flex items-center justify-center bg-background/60 p-2" role="dialog" aria-modal="true" aria-label="准确批次只读查看"><section class="flex max-h-[calc(100dvh-1rem)] w-full max-w-4xl flex-col overflow-hidden rounded-2xl border border-border/60 bg-card shadow-2xl"><header class="flex items-center justify-between gap-3 border-b border-border/60 p-4"><div class="min-w-0"><h3 class="break-words text-sm font-semibold">{{ accountName }} · 批次 #{{ batchId.slice(0, 8) }}</h3><p class="mt-1 text-xs text-muted-foreground">使用账号快照，只读查看该准确批次。</p></div><button type="button" aria-label="关闭批次" @click="emit('close')"><X class="h-4 w-4" /></button></header><div class="space-y-4 overflow-y-auto p-4"><p v-if="error" role="alert" class="text-xs text-destructive">{{ t(connectionHealthMessageKey(error, te)) }}<button type="button" class="ml-2 underline" @click="load">重试读取</button></p><p v-if="!batch && !error" class="text-xs text-muted-foreground">读取中…</p><template v-if="batch"><button type="button" class="text-xs text-primary underline" @click="load">刷新准确批次</button><div class="rounded-lg border border-border/50 p-3 text-xs"><p class="font-medium">当前批次总正确率 {{ formatQuestionAnswerAccuracy(questionAnswerAccuracy(batch.stats)) }}</p><p class="mt-2">请求状态：成功 {{ batch.stats.requests.succeeded }} · 失败 {{ batch.stats.requests.failed }} · 取消 {{ batch.stats.requests.cancelled }} · 进行中 {{ batch.stats.requests.inProgress }}</p><p class="mt-1">判题状态：正确 {{ batch.stats.reviews.correct }} · 错误 {{ batch.stats.reviews.incorrect }} · 待人工 {{ batch.stats.reviews.unreviewed }}</p><p v-for="model in batch.stats.byModel" :key="model.modelName" class="mt-2 break-all">{{ model.modelName }} · {{ formatQuestionAnswerAccuracy(questionAnswerAccuracy(model)) }}</p></div><article v-for="record in batch.records" :key="record.id" class="rounded-lg border border-border/50 p-3"><QuestionAnswerRecordCard v-if="record.status === 'succeeded'" :record="record" :expanded="expanded.includes(record.id)" :saving="false" read-only @expand="expanded = expanded.includes(record.id) ? expanded.filter(id => id !== record.id) : [...expanded, record.id]" /><p v-else class="break-words text-xs">{{ record.questionName }} · {{ record.modelName }} · {{ record.status }} · {{ record.errorType }}</p></article></template></div><footer class="flex justify-end border-t border-border/60 p-4"><button type="button" class="rounded-lg border border-border/60 px-3 py-2 text-xs" @click="emit('close')">关闭</button></footer></section></div></Teleport>
</template>
