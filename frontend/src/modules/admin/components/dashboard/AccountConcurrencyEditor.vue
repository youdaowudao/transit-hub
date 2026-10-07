<script lang="ts">
import { reactive } from 'vue'
const savingTargets = reactive(new Set<string>())
</script>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Check, Loader2, Pencil, X } from 'lucide-vue-next'
import { Tooltip } from '@/components/ui/tooltip'
import { t, te } from '@/locales'
import { saveAccountConcurrency } from '../../api/connectionHealth'
import type { AccountManagementResult, AdminGroupAccount } from '../../types/connectionHealth'
const props = defineProps<{ account: AdminGroupAccount }>()
const emit = defineEmits<{ (event: 'saved', result: AccountManagementResult): void }>()
const prefix = 'admin.connectionHealth.accountConcurrency'
const editing = ref(false)
const draft = ref<number | string>(1)
const locallyPending = ref(false)
const errorKey = ref('')
const saving = computed(() => savingTargets.has(props.account.targetId))
const pending = computed(() => locallyPending.value || props.account.concurrencyResultUnconfirmed === true)
const editable = computed(() => !pending.value && Number.isInteger(props.account.concurrency) && Number(props.account.concurrency) > 0)
const valid = computed(() => Number.isInteger(Number(draft.value)) && Number(draft.value) >= 1 && Number(draft.value) <= 1000)
const errorMessage = computed(() => te(errorKey.value) ? t(errorKey.value) : t('admin.connectionHealth.errors.request'))
let operation = 0
const reset = () => { operation++; editing.value = false; errorKey.value = '' }
watch(() => props.account.targetId, () => { reset(); locallyPending.value = false })
watch(() => props.account, account => { if (account.concurrencyResultUnconfirmed === false) locallyPending.value = false })
onBeforeUnmount(reset)
const edit = () => { if (!editable.value || saving.value) return; draft.value = props.account.concurrency ?? 1; editing.value = true; errorKey.value = '' }
const save = async () => {
  if (!editing.value || saving.value || !editable.value || !valid.value) return
  const targetId = props.account.targetId
  const currentOperation = ++operation
  const syncLoadFactor = Number(props.account.loadFactor) > 0
  const requested = Number(draft.value)
  savingTargets.add(targetId); errorKey.value = ''
  try {
    let result = await saveAccountConcurrency(targetId, requested)
    if (currentOperation !== operation || props.account.targetId !== targetId) return
    if (result.result === 'success' && syncLoadFactor && result.loadFactor !== requested) result = { targetId, result: 'pending', errorKey: 'admin.connectionHealth.errors.concurrencyUnconfirmed' }
    if (result.result === 'not_sent') errorKey.value = result.errorKey || 'admin.connectionHealth.errors.accountWriteNotSent'
    else { locallyPending.value = result.result === 'pending'; editing.value = false }
    emit('saved', result)
  } catch (error) {
    if (currentOperation === operation && props.account.targetId === targetId) errorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
  } finally { savingTargets.delete(targetId) }
}
</script>

<template>
  <div class="min-w-0 text-xs" data-testid="account-concurrency-editor">
    <div v-if="!editing" class="flex min-h-7 items-center gap-1">
      <span class="text-muted-foreground">{{ t(`${prefix}.current`, { concurrency: account.concurrency ?? '-' }) }}</span>
      <Tooltip v-if="editable" :text="t(`${prefix}.edit`)"><button type="button" class="inline-flex h-7 w-7 items-center justify-center rounded text-muted-foreground hover:bg-surface hover:text-foreground disabled:opacity-50" :aria-label="t(`${prefix}.edit`)" :disabled="saving" @click="edit"><Pencil class="h-3.5 w-3.5" /></button></Tooltip>
    </div>
    <form v-else class="flex flex-wrap items-center gap-1" @submit.prevent="save">
      <input v-model="draft" type="number" min="1" max="1000" step="1" class="h-7 w-20 rounded-md border border-border/60 bg-background px-1.5 text-xs text-foreground" :aria-label="t(`${prefix}.value`)" :disabled="saving">
      <button type="submit" class="inline-flex h-7 w-7 items-center justify-center rounded text-primary hover:bg-primary/10 disabled:opacity-50" :aria-label="t(`${prefix}.save`)" :disabled="saving || !valid" @click.prevent="save"><Loader2 v-if="saving" class="h-3.5 w-3.5 animate-spin" /><Check v-else class="h-3.5 w-3.5" /></button>
      <button type="button" class="inline-flex h-7 w-7 items-center justify-center rounded text-muted-foreground hover:bg-surface hover:text-foreground disabled:opacity-50" :aria-label="t(`${prefix}.cancel`)" :disabled="saving" @click="reset"><X class="h-3.5 w-3.5" /></button>
      <p v-if="errorKey" role="alert" class="w-full break-words text-xs text-red-600 dark:text-red-400">{{ errorMessage }}</p>
    </form>
    <p v-if="pending" class="mt-0.5 text-[11px] text-amber-600 dark:text-amber-400">{{ t('admin.connectionHealth.errors.concurrencyUnconfirmed') }}</p>
  </div>
</template>
