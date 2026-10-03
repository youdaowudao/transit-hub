<script setup lang="ts">
import { computed } from 'vue'
import { ArrowDownRight, ArrowUpRight, Minus } from 'lucide-vue-next'
import type { DashboardColorToken } from '../../types/dashboard'
import { DELTA_TEXT_CLASSES, METRIC_DOT_CLASSES, formatAmount, type DeltaDirection } from '../../utils/dashboard'
import TrendChart from './TrendChart.vue'

const props = defineProps<{
  title: string
  value: string
  color: DashboardColorToken
  values: number[]
  labels: string[]
  deltaDirection: DeltaDirection
  deltaText: string
  deltaCaption: string
}>()

const dotClass = computed(() => METRIC_DOT_CLASSES[props.color])
const deltaClass = computed(() => DELTA_TEXT_CLASSES[props.deltaDirection])
const deltaIcon = computed(() => {
  if (props.deltaDirection === 'up') return ArrowUpRight
  if (props.deltaDirection === 'down') return ArrowDownRight
  return Minus
})

// X 轴最多取 5 个均匀分布的标签，避免过密。
const axisLabels = computed(() => {
  const labels = props.labels
  const count = labels.length
  if (count <= 6) return labels
  const indexes = [0, Math.round(count * 0.25), Math.round(count * 0.5), Math.round(count * 0.75), count - 1]
  return indexes.map((index) => labels[index])
})
</script>

<template>
  <div class="bg-card border border-border/50 rounded-2xl p-6 shadow-sm flex flex-col">
    <div class="flex items-start justify-between gap-4">
      <div class="flex items-center gap-2 min-w-0">
        <span :class="['h-2.5 w-2.5 rounded-full shrink-0', dotClass]" />
        <h3 class="font-semibold text-foreground truncate">{{ title }}</h3>
      </div>
      <div class="text-right shrink-0">
        <p class="text-lg font-bold text-foreground">{{ value }}</p>
        <p :class="['flex items-center justify-end gap-0.5 text-xs font-semibold', deltaClass]">
          <component :is="deltaIcon" class="w-3 h-3" />
          {{ deltaText }}
          <span class="font-normal text-muted-foreground ml-1">{{ deltaCaption }}</span>
        </p>
      </div>
    </div>

    <div class="mt-4">
      <TrendChart :values="values" :labels="labels" :color="color" :height="180" :format-value="formatAmount" />
      <div class="mt-2 flex justify-between text-[11px] text-muted-foreground">
        <span v-for="(label, index) in axisLabels" :key="`${label}-${index}`">{{ label }}</span>
      </div>
    </div>
  </div>
</template>
