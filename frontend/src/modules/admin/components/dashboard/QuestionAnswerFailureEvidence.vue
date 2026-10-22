<script setup lang="ts">
import { computed } from 'vue'
import { t } from '@/locales'
import type { QuestionAnswerRecord } from '../../types/connectionHealth'
import { formatConnectionHealthTime } from '../../composables/useConnectionHealth'
import { questionAnswerElapsedMilliseconds } from '../../utils/questionAnswers'

const props = defineProps<{ record: QuestionAnswerRecord }>()
const prefix = 'admin.connectionHealth.manualProbeDialog.questionAnswer.failureEvidence'
const status = computed(() => props.record.upstreamStatus ?? null)
const excerpt = computed(() => props.record.upstreamExcerpt ?? '')
const startedAt = computed(() => props.record.startedAt ? Date.parse(props.record.startedAt) : NaN)
const completedAt = computed(() => props.record.completedAt ? Date.parse(props.record.completedAt) : NaN)
const startLabel = computed(() => Number.isFinite(startedAt.value)
  ? formatConnectionHealthTime(props.record.startedAt!)
  : t(prefix + '.unrecorded'))
const elapsedLabel = computed(() => {
  if (!Number.isFinite(startedAt.value) || !Number.isFinite(completedAt.value) || completedAt.value < startedAt.value) return t(prefix + '.unrecorded')
  const elapsed = questionAnswerElapsedMilliseconds(props.record)
  return elapsed === null ? t(prefix + '.unrecorded') : t(prefix + '.elapsedSeconds', { seconds: (elapsed / 1000).toFixed(1) })
})
</script>

<template>
  <div v-if="record.status === 'failed'" data-testid="question-answer-failure-evidence" class="mt-2 space-y-2 border-t border-border/40 pt-2">
    <p class="text-xs leading-5 text-muted-foreground">{{ t(prefix + (record.errorType === 'response_incomplete' ? '.noteIncomplete' : '.note')) }}</p>
    <p class="text-xs leading-5 text-muted-foreground">{{ t(prefix + '.timing', { time: startLabel, elapsed: elapsedLabel }) }}</p>
    <p v-if="status !== null" class="flex items-baseline gap-2">
      <span class="text-xs text-muted-foreground">{{ t(prefix + '.status') }}</span>
      <span class="text-sm font-semibold text-foreground">{{ status }}</span>
    </p>
    <div v-if="excerpt">
      <p class="text-xs text-muted-foreground">{{ t(prefix + '.excerpt') }}</p>
      <p class="mt-1 whitespace-pre-wrap break-all text-xs leading-5 text-foreground">{{ excerpt }}</p>
    </div>
    <p v-else class="text-xs text-muted-foreground">{{ t(prefix + '.emptyExcerpt') }}</p>
  </div>
</template>
