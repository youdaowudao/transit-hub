<script lang="ts">
import { reactive } from 'vue'
const savingTargets = reactive(new Set<string>())
</script>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Check, Loader2, Pencil, X } from 'lucide-vue-next'
import { Tooltip } from '@/components/ui/tooltip'
import { t, te } from '@/locales'
import { saveAccountPriority } from '../../api/connectionHealth'
import type { AccountManagementResult, AdminGroupAccount } from '../../types/connectionHealth'

const props = defineProps<{ account: AdminGroupAccount; tied?: boolean }>()
const emit = defineEmits<{ (event: 'saved', result: AccountManagementResult): void }>()
const prefix = 'admin.connectionHealth.accountPriority'
const editing = ref(false)
const draftMode = ref<'auto' | 'manual'>('manual')
const draftPriority = ref(1)
const locallyPending = ref(false)
const errorKey = ref('')
const saving = computed(() => savingTargets.has(props.account.targetId))
const pending = computed(() => locallyPending.value || props.account.priorityActionPending === true)
const editable = computed(() => !pending.value && props.account.priorityUsesMultiplierOnly === false && Number.isInteger(props.account.priority) && Number(props.account.priority) > 0)
const oldBaseline = computed(() => props.account.priorityManaged && Number(props.account.priorityExpected) >= 1 && Number(props.account.priorityExpected) <= 9)
const manual = computed(() => props.account.priorityUsesMultiplierOnly === false && !oldBaseline.value && Number(props.account.priority) >= 1 && Number(props.account.priority) <= 9)
const state = computed(() => pending.value ? 'pending'
  : typeof props.account.priorityUsesMultiplierOnly !== 'boolean' ? 'unknown'
    : props.account.priorityUsesMultiplierOnly ? (props.account.priorityManaged ? 'multiplierManaged' : 'multiplierUnmanaged')
      : oldBaseline.value ? 'oldBaseline' : manual.value ? 'manual' : props.account.priorityManaged ? 'managed' : 'unmanaged')
const expected = computed(() => Number(props.account.priorityExpected) > 0 ? props.account.priorityExpected : undefined)
const errorMessage = computed(() => te(errorKey.value) ? t(errorKey.value) : t('admin.connectionHealth.errors.request'))
let operation = 0
const reset = () => { operation++; editing.value = false; errorKey.value = '' }
watch(() => props.account.targetId, () => { reset(); locallyPending.value = false })
watch(() => props.account, account => {
  if (account.priorityActionPending === false) locallyPending.value = false
  if (!editable.value) editing.value = false
})
onBeforeUnmount(reset)
const edit = () => {
  if (!editable.value || saving.value) return
  draftMode.value = manual.value || oldBaseline.value ? 'manual' : 'auto'
  draftPriority.value = Number(props.account.priority) >= 1 && Number(props.account.priority) <= 9 ? Number(props.account.priority) : 1
  editing.value = true; errorKey.value = ''
}
const save = async () => {
  if (!editing.value || saving.value || !editable.value) return
  const targetId = props.account.targetId
  const currentOperation = ++operation
  savingTargets.add(targetId); errorKey.value = ''
  try {
    const result = await saveAccountPriority(targetId, draftMode.value === 'auto' ? { mode: 'auto' } : { mode: 'manual', priority: Number(draftPriority.value) })
    if (currentOperation !== operation || props.account.targetId !== targetId) return
    if (result.result === 'not_sent') errorKey.value = result.errorKey || 'admin.connectionHealth.errors.accountWriteNotSent'
    else {
      locallyPending.value = result.result === 'pending'
      editing.value = false
    }
    emit('saved', result)
  } catch (error) {
    if (currentOperation === operation && props.account.targetId === targetId) errorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
  } finally { savingTargets.delete(targetId) }
}
</script>

<template>
  <div class="min-w-0 text-xs" data-testid="account-priority-editor">
    <div v-if="!editing" class="flex min-h-7 items-center gap-1">
      <span class="tabular-nums text-foreground">{{ account.priority ?? '-' }}</span>
      <Tooltip v-if="editable" :text="t(`${prefix}.edit`)">
        <button type="button" class="inline-flex h-7 w-7 items-center justify-center rounded text-muted-foreground hover:bg-surface hover:text-foreground disabled:opacity-50" :aria-label="t(`${prefix}.edit`)" :disabled="saving" @click="edit"><Pencil class="h-3.5 w-3.5" /></button>
      </Tooltip>
    </div>
    <form v-else class="flex flex-wrap items-center gap-1" @submit.prevent="save">
      <select v-model="draftMode" class="h-7 rounded-md border border-border/60 bg-background px-1.5 text-xs text-foreground" :aria-label="t(`${prefix}.mode`)" :disabled="saving">
        <option value="auto">{{ t(`${prefix}.auto`) }}</option><option value="manual">{{ t(`${prefix}.manualOption`) }}</option>
      </select>
      <select v-if="draftMode === 'manual'" v-model="draftPriority" class="h-7 rounded-md border border-border/60 bg-background px-1.5 text-xs text-foreground" :aria-label="t(`${prefix}.manualValue`)" :disabled="saving"><option v-for="value in 9" :key="value" :value="value">{{ value }}</option></select>
      <button type="submit" class="inline-flex h-7 w-7 items-center justify-center rounded text-primary hover:bg-primary/10 disabled:opacity-50" :aria-label="t(`${prefix}.save`)" :disabled="saving" @click.prevent="save"><Loader2 v-if="saving" class="h-3.5 w-3.5 animate-spin" /><Check v-else class="h-3.5 w-3.5" /></button>
      <button type="button" class="inline-flex h-7 w-7 items-center justify-center rounded text-muted-foreground hover:bg-surface hover:text-foreground disabled:opacity-50" :aria-label="t(`${prefix}.cancel`)" :disabled="saving" @click="reset"><X class="h-3.5 w-3.5" /></button>
      <p v-if="errorKey" role="alert" class="w-full break-words text-xs text-red-600 dark:text-red-400">{{ errorMessage }}</p>
    </form>
    <p class="mt-0.5 text-[11px]" :class="pending ? 'text-amber-600 dark:text-amber-400' : 'text-muted-foreground'">{{ t(`${prefix}.${state}`) }}</p>
    <p v-if="tied" class="mt-0.5 text-[11px] text-muted-foreground">{{ t(`${prefix}.tied`) }}</p>
    <p v-if="expected !== undefined" class="mt-0.5 text-[11px] text-muted-foreground">{{ t(`${prefix}.${pending ? 'recordValue' : 'lastConfirmed'}`, { priority: expected }) }}</p>
    <p v-else-if="account.priorityManaged && (account.priorityExpected == null || account.priorityExpected === 0)" class="mt-0.5 text-[11px] text-muted-foreground">{{ t(`${prefix}.notWritten`) }}</p>
    <p v-if="account.priorityUsesMultiplierOnly === true" class="mt-0.5 text-[11px] text-muted-foreground">{{ t(`${prefix}.multiplierOnlyHelp`) }}</p>
    <p v-if="!pending && manual && account.accountTier === 1" class="mt-0.5 text-[11px] text-muted-foreground">{{ t(`${prefix}.handoff`) }}</p>
  </div>
</template>
