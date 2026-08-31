<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Pencil } from 'lucide-vue-next'
import { setTargetIntelligenceWeight } from '../../api/connectionHealth'
import type { TargetIntelligenceWeightResult } from '../../types/connectionHealth'
import { t } from '@/locales'

const props = withDefaults(defineProps<{
  targetId: string
  modelValue: number | null
  compact?: boolean
}>(), {
  compact: false,
})

const emit = defineEmits<{
  (event: 'saved', result: TargetIntelligenceWeightResult): void
}>()

const prefix = 'admin.connectionHealth.intelligenceWeight'
const editing = ref(false)
const draft = ref('')
const saving = ref(false)
const errorKey = ref('')
const currentValue = ref<number | null>(props.modelValue)

watch(() => props.modelValue, (value) => {
  currentValue.value = value
  if (!editing.value) draft.value = value == null ? '' : String(value)
})

const currentLabel = computed(() => currentValue.value === null
  ? t(`${prefix}.unscored`)
  : String(currentValue.value))

const beginEditing = () => {
  if (saving.value) return
  editing.value = true
  draft.value = currentValue.value == null ? '' : String(currentValue.value)
  errorKey.value = ''
}

const cancelEditing = () => {
  if (saving.value) return
  editing.value = false
  errorKey.value = ''
}

const validResult = (result: unknown): result is TargetIntelligenceWeightResult => {
  if (!result || typeof result !== 'object') return false
  const value = result as Partial<TargetIntelligenceWeightResult>
  return value.targetId === props.targetId
    && (value.intelligenceWeight === null
      || typeof value.intelligenceWeight === 'number'
        && Number.isInteger(value.intelligenceWeight)
        && value.intelligenceWeight >= 0
        && value.intelligenceWeight <= 100)
}

const persist = async (value: number | null) => {
  if (saving.value) return
  saving.value = true
  errorKey.value = ''
  try {
    const result = await setTargetIntelligenceWeight(props.targetId, value)
    if (!validResult(result)) {
      errorKey.value = `${prefix}.contractInvalid`
      return
    }
    currentValue.value = result.intelligenceWeight
    draft.value = result.intelligenceWeight == null ? '' : String(result.intelligenceWeight)
    editing.value = false
    emit('saved', result)
  } catch {
    errorKey.value = `${prefix}.saveFailed`
  } finally {
    saving.value = false
  }
}

const save = async () => {
  const value = draft.value.trim()
  if (!/^\d+$/.test(value)) {
    errorKey.value = `${prefix}.invalid`
    return
  }
  const parsed = Number(value)
  if (!Number.isInteger(parsed) || parsed < 0 || parsed > 100) {
    errorKey.value = `${prefix}.invalid`
    return
  }
  await persist(parsed)
}

const clear = async () => {
  if (currentValue.value === null) return
  await persist(null)
}
</script>

<template>
  <div
    data-testid="account-intelligence-weight-editor"
    class="inline-flex max-w-full flex-col items-end gap-1"
    :class="compact ? 'text-xs' : 'text-sm'"
  >
    <div class="inline-flex items-center justify-end gap-1">
      <button
        type="button"
        class="inline-flex items-center gap-1 rounded px-1 py-0.5 text-muted-foreground transition-colors hover:bg-surface hover:text-primary disabled:opacity-40"
        :aria-label="t(`${prefix}.edit`)"
        :disabled="saving"
        @click="beginEditing"
      >
        <span data-testid="intelligence-weight-current" class="font-medium text-foreground">{{ currentLabel }}</span>
        <Pencil class="h-3.5 w-3.5" />
      </button>
    </div>
    <div v-if="editing" class="flex max-w-full flex-wrap items-center justify-end gap-1">
      <input
        v-model="draft"
        data-testid="intelligence-weight-input"
        inputmode="numeric"
        class="h-7 w-16 rounded-md border border-border bg-background px-2 text-right text-xs text-foreground outline-none focus:border-primary"
        :disabled="saving"
        @keydown.enter.prevent="save"
      />
      <button
        type="button"
        data-testid="intelligence-weight-save"
        class="rounded-md bg-primary px-2 py-1 text-xs font-medium text-primary-foreground disabled:opacity-50"
        :disabled="saving"
        @click="save"
      >{{ t(`${prefix}.save`) }}</button>
      <button
        type="button"
        data-testid="intelligence-weight-clear"
        class="rounded-md border border-border px-2 py-1 text-xs text-muted-foreground disabled:opacity-40"
        :disabled="saving || currentValue === null"
        @click="clear"
      >{{ t(`${prefix}.clear`) }}</button>
      <button
        type="button"
        class="rounded-md px-2 py-1 text-xs text-muted-foreground disabled:opacity-40"
        :disabled="saving"
        @click="cancelEditing"
      >{{ t(`${prefix}.cancel`) }}</button>
    </div>
    <p v-if="errorKey" data-testid="intelligence-weight-error" class="max-w-52 text-right text-[11px] leading-4 text-destructive">
      {{ t(errorKey) }}
    </p>
  </div>
</template>
