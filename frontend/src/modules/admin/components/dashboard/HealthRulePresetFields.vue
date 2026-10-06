<script setup lang="ts">
import type { HealthRulePresetInput, HealthRuleVersion } from '../../types/connectionHealth'
import { healthRulePresetFields } from '../../utils/healthRulePresets'
const props = defineProps<{ modelValue: HealthRulePresetInput; ruleVersion: HealthRuleVersion; readonly?: boolean }>()
const emit = defineEmits<{ (event: 'update:modelValue', value: HealthRulePresetInput): void }>()
const updateNumber = (key: typeof healthRulePresetFields[number]['key'], scale: number, event: Event) => {
  emit('update:modelValue', { ...props.modelValue, [key]: Number((event.target as HTMLInputElement).value) * scale })
}
const protocols = [{ key: 'responses', label: 'Responses', fallback: 10000 }, { key: 'chat_completions', label: 'Chat', fallback: 5000 }]
const updateDelay = (key: string, event: Event) => {
  const value = (event.target as HTMLInputElement).value, delayLineMs = { ...props.modelValue.delayLineMs }
  if (!value) delete delayLineMs[key]
  else delayLineMs[key] = Number(value) * 1000
  emit('update:modelValue', { ...props.modelValue, delayLineMs })
}
</script>
<template>
  <div class="space-y-3">
    <div v-for="field in healthRulePresetFields" :key="field.key" class="space-y-1">
      <label class="flex flex-wrap items-center justify-between gap-2 text-xs font-medium text-foreground">
        <span>{{ field.label }} <span v-if="field.only && field.only !== ruleVersion" class="font-normal text-muted-foreground">（当前规则下不适用）</span></span>
        <span v-if="readonly">{{ modelValue[field.key] / field.scale }} {{ field.unit }}</span>
        <span v-else class="flex items-center gap-1">
          <input :value="modelValue[field.key] / field.scale" :data-testid="'preset-' + field.key" type="number" :min="field.min" :max="field.max" step="1" class="h-8 w-24 rounded-md border border-border/60 bg-background px-2 text-xs" @input="updateNumber(field.key, field.scale, $event)">
          {{ field.unit }}
        </span>
      </label>
      <p class="text-xs leading-5 text-muted-foreground">{{ field.help }}</p>
    </div>
    <div class="space-y-1">
      <p class="text-xs font-medium text-foreground">延迟线（按协议）</p>
      <p class="text-xs leading-5 text-muted-foreground">新规则下首字超过此线时标为延迟，仍算成功；旧规则下整段耗时超过此线算慢。留空时使用该协议默认值。</p>
      <label v-for="protocol in protocols" :key="protocol.key" class="flex items-center justify-between gap-2 text-xs">
        <span>{{ protocol.label }}</span>
        <span v-if="readonly">{{ (modelValue.delayLineMs[protocol.key] ?? protocol.fallback) / 1000 }} 秒 <span v-if="modelValue.delayLineMs[protocol.key] == null">（协议默认值）</span></span>
        <span v-else class="flex items-center gap-1"><input :value="modelValue.delayLineMs[protocol.key] == null ? '' : modelValue.delayLineMs[protocol.key]! / 1000" :aria-label="protocol.label + ' 延迟线'" :placeholder="String(protocol.fallback / 1000)" type="number" min="1" max="120" step="1" class="h-8 w-24 rounded-md border border-border/60 bg-background px-2 text-xs" @input="updateDelay(protocol.key, $event)">秒</span>
      </label>
      <p v-for="(value, key) in Object.fromEntries(Object.entries(modelValue.delayLineMs).filter(([key]) => !protocols.some(protocol => protocol.key === key)))" :key="key" class="text-xs text-muted-foreground">{{ key }}：{{ value / 1000 }} 秒</p>
    </div>
  </div>
</template>
