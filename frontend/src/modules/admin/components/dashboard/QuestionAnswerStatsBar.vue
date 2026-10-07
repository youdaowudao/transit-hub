<script setup lang="ts">
import { computed, ref } from 'vue'
import type { QuestionAnswerStats, QuestionAnswerModelStats } from '../../types/connectionHealth'
import { formatQuestionAnswerAccuracy, questionAnswerAccuracy } from '../../utils/questionAnswers'
import { t } from '@/locales'
const props = defineProps<{ reviewStats: QuestionAnswerStats | null; todayStats: QuestionAnswerStats; lifetimeStats: QuestionAnswerStats }>()
const prefix = 'admin.connectionHealth.manualProbeDialog.questionAnswer.stats'
const dimension = ref<'account' | 'model' | 'question'>('account')
const periods = computed(() => [
  ...(props.reviewStats ? [{ key: 'review', label: '当前批次', stats: props.reviewStats }] : []),
  { key: 'today', label: t(`${prefix}.todaySingapore`), stats: props.todayStats },
  { key: 'lifetime', label: t(`${prefix}.allTime`), stats: props.lifetimeStats },
])
const counts = (stats: Pick<QuestionAnswerStats, 'requests' | 'reviews'>) => [
  ['提交', stats.requests.submitted], ['进行中', stats.requests.inProgress], ['成功', stats.requests.succeeded],
  ['待人工', stats.reviews.unreviewed], ['正确', stats.reviews.correct], ['错误', stats.reviews.incorrect],
  ['失败', stats.requests.failed], ['取消', stats.requests.cancelled],
]
const accuracy = (stats: Pick<QuestionAnswerStats, 'requests' | 'reviews'>) => formatQuestionAnswerAccuracy(questionAnswerAccuracy(stats as QuestionAnswerStats))
const summary = (stats: Pick<QuestionAnswerModelStats, 'requests' | 'reviews'>) => counts(stats).map(([label, value]) => `${label} ${value}`).join(' · ')
</script>
<template>
  <section data-testid="question-answer-stats-bar" class="mb-3 overflow-hidden rounded-lg border border-border/50 bg-surface-line/20">
    <div class="flex flex-wrap gap-1 border-b border-border/40 px-3 py-2">
      <button v-for="option in ([['account', '账号汇总'], ['model', '按模型'], ['question', '按题目']] as const)" :key="option[0]" type="button" class="rounded-md px-2.5 py-1.5 text-xs font-medium" :class="dimension === option[0] ? 'bg-background text-foreground' : 'text-muted-foreground hover:text-foreground'" :aria-pressed="dimension === option[0]" @click="dimension = option[0]">{{ option[1] }}</button>
    </div>
    <div data-testid="question-answer-periods" class="grid grid-cols-1" :class="reviewStats ? 'md:grid-cols-3' : 'md:grid-cols-2'">
      <div v-for="(period, index) in periods" :key="period.key" :data-testid="`question-answer-stats-${period.key}`" class="min-w-0 px-3 py-2.5" :class="index > 0 ? 'border-t border-border/50 md:border-l md:border-t-0' : ''">
        <p class="text-xs font-semibold text-foreground">{{ period.label }}</p>
        <p v-if="period.key !== 'review'" class="mt-1 text-[11px] text-muted-foreground">混合配置汇总</p>
        <dl class="mt-2 grid grid-cols-3 gap-2">
          <div v-for="([label, count], countIndex) in counts(period.stats)" :key="countIndex"><dt class="text-[11px] text-muted-foreground">{{ label }}</dt><dd class="text-sm font-semibold text-foreground">{{ count }}</dd></div>
        </dl>
        <div class="mt-2 flex flex-wrap items-end gap-2"><span class="text-[11px] text-muted-foreground">正确率</span><strong data-testid="question-answer-accuracy" class="text-2xl leading-none text-primary">{{ accuracy(period.stats) }}</strong><span class="text-[11px] text-muted-foreground">{{ period.stats.reviews.correct }}正确 / {{ period.stats.reviews.correct + period.stats.reviews.incorrect }}已判</span></div>
        <div v-if="dimension === 'model'" class="mt-3 space-y-2">
          <p v-if="period.stats.byModel.length === 0" class="text-xs text-muted-foreground">暂无模型数据</p>
          <div v-for="item in period.stats.byModel" :key="item.modelName" data-testid="question-answer-model-stats" class="rounded-md border border-border/40 p-2"><p class="break-words text-xs font-medium text-foreground">{{ item.modelName }} · {{ accuracy(item) }}</p><p class="mt-1 break-words text-[11px] text-muted-foreground">{{ summary(item) }}</p></div>
        </div>
        <div v-if="dimension === 'question'" class="mt-3 space-y-2">
          <p v-if="!period.stats.byQuestion?.length" class="text-xs text-muted-foreground">暂无题目数据</p>
          <div v-for="item in period.stats.byQuestion ?? []" :key="item.questionSnapshotKey" data-testid="question-answer-question-stats" class="rounded-md border border-border/40 p-2">
            <p class="break-words text-xs font-medium text-foreground">{{ item.displayQuestionName }} · #{{ item.questionSnapshotKey.slice(0, 8) }} · {{ accuracy(item) }}</p><p class="mt-1 break-words text-[11px] text-muted-foreground">{{ summary(item) }}</p>
            <p v-for="model in item.byModel" :key="model.modelName" class="mt-2 break-words border-t border-border/40 pt-1 text-[11px] text-muted-foreground">{{ model.modelName }} · {{ accuracy(model) }} · {{ summary(model) }}</p>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
