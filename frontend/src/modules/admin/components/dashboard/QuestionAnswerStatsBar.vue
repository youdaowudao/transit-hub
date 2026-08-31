<script setup lang="ts">
import { computed, ref } from 'vue'
import { ChevronDown, ChevronUp } from 'lucide-vue-next'

import type {
  QuestionAnswerModelStats,
  QuestionAnswerStats,
} from '../../types/connectionHealth'
import {
  formatQuestionAnswerAccuracy,
  questionAnswerAccuracy,
} from '../../utils/questionAnswers'
import { t } from '@/locales'

const props = defineProps<{
  reviewStats: QuestionAnswerStats | null
  todayStats: QuestionAnswerStats
  lifetimeStats: QuestionAnswerStats
}>()

const prefix = 'admin.connectionHealth.manualProbeDialog.questionAnswer.stats'
const showModels = ref(false)
const periods = computed(() => [
  ...(props.reviewStats ? [{ key: 'review', label: t(`${prefix}.reviewBatch`), stats: props.reviewStats }] : []),
  { key: 'today', label: t(`${prefix}.todayShanghai`), stats: props.todayStats },
  { key: 'lifetime', label: t(`${prefix}.allTime`), stats: props.lifetimeStats },
])
const hasModelStats = computed(() => periods.value.some(period => period.stats.byModel.length > 0))
const modelAccuracy = (value: QuestionAnswerModelStats): string => formatQuestionAnswerAccuracy(questionAnswerAccuracy({
  requests: value.requests,
  reviews: value.reviews,
  byModel: [],
}))
</script>

<template>
  <section data-testid="question-answer-stats-bar" class="mb-3 overflow-hidden rounded-lg border border-border/50 bg-surface-line/20">
    <div data-testid="question-answer-periods" class="grid grid-cols-1" :class="reviewStats ? 'md:grid-cols-3' : 'md:grid-cols-2'">
      <div
        v-for="(period, index) in periods"
        :key="period.key"
        :data-testid="`question-answer-stats-${period.key}`"
        class="px-3 py-2.5"
        :class="index > 0 ? 'border-t border-border/50 md:border-l md:border-t-0' : ''"
      >
        <p class="text-xs font-semibold text-foreground">{{ period.label }}</p>
        <dl class="mt-2 grid grid-cols-3 gap-2 sm:grid-cols-5">
          <div>
            <dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.answered`) }}</dt>
            <dd class="text-sm font-semibold text-foreground">{{ period.stats.requests.succeeded }}</dd>
          </div>
          <div>
            <dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.failedShort`) }}</dt>
            <dd class="text-sm font-semibold text-red-600 dark:text-red-400">{{ period.stats.requests.failed }}</dd>
          </div>
          <div>
            <dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.correct`) }}</dt>
            <dd class="text-sm font-semibold text-green-600 dark:text-green-400">{{ period.stats.reviews.correct }}</dd>
          </div>
          <div>
            <dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.incorrect`) }}</dt>
            <dd class="text-sm font-semibold text-red-600 dark:text-red-400">{{ period.stats.reviews.incorrect }}</dd>
          </div>
          <div>
            <dt class="text-[11px] text-muted-foreground">{{ t(`${prefix}.accuracy`) }}</dt>
            <dd data-testid="question-answer-accuracy" class="text-2xl font-bold leading-none text-primary">
              {{ formatQuestionAnswerAccuracy(questionAnswerAccuracy(period.stats)) }}
            </dd>
          </div>
        </dl>
      </div>
    </div>

    <div v-if="hasModelStats" class="border-t border-border/40 px-3 py-2">
      <button type="button" class="inline-flex items-center gap-1 text-xs font-medium text-muted-foreground hover:text-foreground" @click="showModels = !showModels">
        {{ showModels ? t(`${prefix}.hideModels`) : t(`${prefix}.showModels`) }}
        <ChevronUp v-if="showModels" class="h-3.5 w-3.5" />
        <ChevronDown v-else class="h-3.5 w-3.5" />
      </button>
      <div v-if="showModels" class="mt-2 grid gap-2 lg:grid-cols-3">
        <div v-for="period in periods" :key="period.key" class="space-y-2">
          <p class="text-[11px] font-medium text-muted-foreground">{{ period.label }}</p>
          <div
            v-for="item in period.stats.byModel"
            :key="item.modelName"
            data-testid="question-answer-model-stats"
            class="rounded-md border border-border/40 p-2"
          >
            <p class="break-all text-xs font-medium text-foreground">{{ item.modelName }}</p>
            <dl class="mt-2 grid grid-cols-3 gap-1.5 sm:grid-cols-5">
              <div><dt class="text-[10px] text-muted-foreground">{{ t(`${prefix}.answered`) }}</dt><dd class="text-xs font-semibold text-foreground">{{ item.requests.succeeded }}</dd></div>
              <div><dt class="text-[10px] text-muted-foreground">{{ t(`${prefix}.failedShort`) }}</dt><dd class="text-xs font-semibold text-red-600 dark:text-red-400">{{ item.requests.failed }}</dd></div>
              <div><dt class="text-[10px] text-muted-foreground">{{ t(`${prefix}.correct`) }}</dt><dd class="text-xs font-semibold text-green-600 dark:text-green-400">{{ item.reviews.correct }}</dd></div>
              <div><dt class="text-[10px] text-muted-foreground">{{ t(`${prefix}.incorrect`) }}</dt><dd class="text-xs font-semibold text-red-600 dark:text-red-400">{{ item.reviews.incorrect }}</dd></div>
              <div><dt class="text-[10px] text-muted-foreground">{{ t(`${prefix}.accuracy`) }}</dt><dd class="text-base font-bold text-primary">{{ modelAccuracy(item) }}</dd></div>
            </dl>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
