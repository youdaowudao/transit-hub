<script setup lang="ts">
import { computed } from 'vue'
import { t } from '@/locales'
import type { QuestionAnswerRecord } from '../../types/connectionHealth'

const props = defineProps<{ record: QuestionAnswerRecord }>()
const prefix = 'admin.connectionHealth.manualProbeDialog.questionAnswer.failureEvidence'
const status = computed(() => props.record.upstreamStatus ?? null)
const excerpt = computed(() => props.record.upstreamExcerpt ?? '')
</script>

<template>
  <div v-if="record.status === 'failed' && (status !== null || excerpt)" data-testid="question-answer-failure-evidence" class="mt-2 space-y-2 border-t border-border/40 pt-2">
    <p class="text-xs leading-5 text-muted-foreground">{{ t(prefix + '.note') }}</p>
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
