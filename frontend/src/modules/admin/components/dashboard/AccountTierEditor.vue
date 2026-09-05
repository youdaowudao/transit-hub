<script lang="ts">
import { reactive } from 'vue'

const savingTargets = reactive(new Set<string>())
</script>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Check, Loader2, Pencil, X } from 'lucide-vue-next'
import { Tooltip } from '@/components/ui/tooltip'
import { t, te } from '@/locales'
import { saveAccountTier } from '../../api/connectionHealth'
import type { AccountTier, AccountTierResult } from '../../types/connectionHealth'

const props = defineProps<{
  targetId: string
  accountTier?: AccountTier
}>()
const emit = defineEmits<{ (event: 'saved', result: AccountTierResult): void }>()
const prefix = 'admin.connectionHealth.accountTier'
const currentTier = computed<AccountTier>(() => props.accountTier === 1 ? 1 : 2)
const editing = ref(false)
const draft = ref<AccountTier>(2)
const saving = computed(() => savingTargets.has(props.targetId))
const errorKey = ref('')
let operation = 0
const errorMessage = computed(() => te(errorKey.value) ? t(errorKey.value) : t('admin.connectionHealth.errors.request'))

const reset = () => {
  operation++
  editing.value = false
  errorKey.value = ''
}
watch(() => props.targetId, reset)
onBeforeUnmount(reset)

const edit = () => {
  draft.value = currentTier.value
  errorKey.value = ''
  editing.value = true
}

const save = async () => {
  if (saving.value || !editing.value || (draft.value !== 1 && draft.value !== 2)) return
  const currentOperation = ++operation
  const targetId = props.targetId
  const accountTier = draft.value
  savingTargets.add(targetId)
  errorKey.value = ''
  try {
    const result = await saveAccountTier(targetId, accountTier)
    if (operation !== currentOperation || props.targetId !== targetId) return
    emit('saved', result)
    editing.value = false
  } catch (error) {
    if (operation !== currentOperation || props.targetId !== targetId) return
    errorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
  } finally {
    savingTargets.delete(targetId)
  }
}
</script>

<template>
  <div class="account-tier-editor min-w-0 text-xs" data-testid="account-tier-editor">
    <div v-if="!editing" class="flex min-h-7 items-center gap-1">
      <span class="text-muted-foreground">{{ t(`${prefix}.${currentTier === 1 ? 'first' : 'second'}`) }}</span>
      <Tooltip :text="t(`${prefix}.edit`)">
        <button type="button" class="inline-flex h-7 w-7 items-center justify-center rounded text-muted-foreground hover:bg-surface hover:text-foreground" :aria-label="t(`${prefix}.edit`)" @click="edit">
          <Pencil class="h-3.5 w-3.5" />
        </button>
      </Tooltip>
    </div>
    <form v-else class="flex flex-wrap items-center gap-1" @submit.prevent="save">
      <select v-model="draft" class="h-7 rounded-md border border-border/60 bg-background px-1.5 text-xs text-foreground" :aria-label="t(`${prefix}.label`)" :disabled="saving">
        <option :value="1">{{ t(`${prefix}.first`) }}</option>
        <option :value="2">{{ t(`${prefix}.second`) }}</option>
      </select>
      <Tooltip :text="t(`${prefix}.save`)">
        <button type="submit" class="inline-flex h-7 w-7 items-center justify-center rounded text-primary hover:bg-primary/10 disabled:opacity-50" :aria-label="t(`${prefix}.save`)" :disabled="saving" @click.prevent="save">
          <Loader2 v-if="saving" class="h-3.5 w-3.5 animate-spin" />
          <Check v-else class="h-3.5 w-3.5" />
        </button>
      </Tooltip>
      <Tooltip :text="t(`${prefix}.cancel`)">
        <button type="button" class="inline-flex h-7 w-7 items-center justify-center rounded text-muted-foreground hover:bg-surface hover:text-foreground disabled:opacity-50" :aria-label="t(`${prefix}.cancel`)" :disabled="saving" @click="reset">
          <X class="h-3.5 w-3.5" />
        </button>
      </Tooltip>
      <p v-if="errorKey" role="alert" class="w-full break-words text-xs text-red-600 dark:text-red-400">{{ errorMessage }}</p>
    </form>
  </div>
</template>
