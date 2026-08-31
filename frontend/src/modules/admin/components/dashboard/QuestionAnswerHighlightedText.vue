<script setup lang="ts">
import { computed } from 'vue'

import { highlightQuestionAnswer } from '../../utils/questionAnswers'

const props = defineProps<{
  answer: string
  snapshot: string[] | null
}>()

const segments = computed(() => highlightQuestionAnswer(props.answer, props.snapshot ?? []))
</script>

<template>
  <p class="whitespace-pre-wrap break-words text-sm leading-6 text-foreground">
    <template v-for="(segment, index) in segments" :key="`${index}:${segment.text.length}`">
      <mark v-if="segment.highlighted" class="rounded-sm bg-amber-200 px-0.5 text-foreground dark:bg-amber-500/30">{{ segment.text }}</mark>
      <template v-else>{{ segment.text }}</template>
    </template>
  </p>
</template>
