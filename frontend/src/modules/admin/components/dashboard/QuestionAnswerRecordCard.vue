<script setup lang="ts">
import { ChevronDown, ChevronUp, Loader2 } from 'lucide-vue-next'
import type { QuestionAnswerRecord } from '../../types/connectionHealth'
import { formatConnectionHealthTime } from '../../composables/useConnectionHealth'
import { questionAnswerElapsedMilliseconds, questionAnswerSourceLabel, questionAnswerRequestProtocolLabel } from '../../utils/questionAnswers'
import { t } from '@/locales'
import QuestionAnswerHighlightedText from './QuestionAnswerHighlightedText.vue'
const props = defineProps<{ record: QuestionAnswerRecord; expanded: boolean; saving: boolean; savingJudgment?: 'unreviewed' | 'correct' | 'incorrect'; readOnly?: boolean }>()
const emit = defineEmits<{ (event: 'judge', value: 'correct' | 'incorrect'): void; (event: 'expand'): void }>()
const answer = () => props.expanded || props.record.answerBody.length <= 220 ? props.record.answerBody : `${props.record.answerBody.slice(0, 220)}…`
</script>
<template>
  <div class="grid gap-3 md:grid-cols-[minmax(0,1fr)_9rem]" :data-testid="`question-answer-record-${record.id}`">
    <div class="min-w-0">
      <div class="flex flex-wrap items-start justify-between gap-2">
        <div class="min-w-0">
          <p class="break-words text-sm font-semibold text-foreground">{{ record.questionName }} · {{ record.repeatIndex === null || record.repeatIndex === undefined ? '历史样本' : `第${record.repeatIndex}次` }}</p>
          <p class="mt-0.5 break-words text-xs text-muted-foreground">{{ record.modelName }} · {{ questionAnswerRequestProtocolLabel(record.requestProtocol, t('admin.connectionHealth.testConfiguration.legacy')) }} · 力度 {{ record.reasoningEffort ?? '未指定' }}</p>
        </div>
        <span class="text-xs text-muted-foreground" data-testid="question-answer-judgment-source">{{ record.answerJudgment === 'correct' || record.answerJudgment === 'incorrect' ? `${questionAnswerSourceLabel(record)}·${record.answerJudgment === 'correct' ? '正确' : '错误'}` : '待人工判断' }}</span>
      </div>
      <p class="mt-1 break-words text-[11px] text-muted-foreground">完成 {{ record.completedAt ? formatConnectionHealthTime(record.completedAt) : '—' }} · 耗时 {{ questionAnswerElapsedMilliseconds(record) === null ? '—' : `${questionAnswerElapsedMilliseconds(record)}ms` }}</p>
      <p class="mt-2 whitespace-pre-wrap break-words text-xs leading-5 text-foreground">{{ record.questionBody }}</p>
      <p class="mt-1 break-words text-[11px] text-muted-foreground">关键词快照：{{ record.questionKeywordSnapshot === null ? '无关键字快照' : record.questionKeywordSnapshot.length ? record.questionKeywordSnapshot.join('、') : '仅人工判断' }}</p>
      <QuestionAnswerHighlightedText v-if="record.answerBody" class="mt-2" :answer="answer()" :snapshot="record.questionKeywordSnapshot" />
      <p v-else class="mt-2 whitespace-pre-wrap break-words text-sm leading-6 text-muted-foreground">没有回答正文。</p>
      <button v-if="record.answerBody.length > 220" type="button" class="mt-2 inline-flex items-center gap-1 text-xs font-medium text-muted-foreground hover:text-foreground" @click="emit('expand')">{{ expanded ? '收起详情' : '展开详情' }}<ChevronUp v-if="expanded" class="h-3.5 w-3.5" /><ChevronDown v-else class="h-3.5 w-3.5" /></button>
    </div>
    <div v-if="!readOnly" class="grid grid-cols-2 gap-2 md:grid-cols-1 md:self-start">
      <button type="button" class="min-h-14 rounded-md border border-green-500/40 px-3 py-2 text-xs font-medium text-green-700 hover:bg-green-500/10 disabled:opacity-50 dark:text-green-400" :class="record.answerJudgment === 'correct' || savingJudgment === 'correct' ? 'bg-green-500/20' : ''" :aria-pressed="record.answerJudgment === 'correct'" :disabled="saving" @click="emit('judge', 'correct')"><Loader2 v-if="saving" class="inline h-3.5 w-3.5 animate-spin" />正确<span v-if="savingJudgment === 'correct'"> · 保存中</span></button>
      <button type="button" class="min-h-14 rounded-md border border-red-500/40 px-3 py-2 text-xs font-medium text-red-700 hover:bg-red-500/10 disabled:opacity-50 dark:text-red-400" :class="record.answerJudgment === 'incorrect' || savingJudgment === 'incorrect' ? 'bg-red-500/20' : ''" :aria-pressed="record.answerJudgment === 'incorrect'" :disabled="saving" @click="emit('judge', 'incorrect')">错误<span v-if="savingJudgment === 'incorrect'"> · 保存中</span></button>
    </div>
  </div>
</template>
