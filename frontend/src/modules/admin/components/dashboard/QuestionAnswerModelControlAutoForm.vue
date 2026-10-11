<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ConnectionHealthApiError, isModelControlAutoSettings, saveModelControlAutoSettings } from '../../api/connectionHealth'
import type { ModelControlAutoSettings } from '../../types/connectionHealth'
import { modelControlText as message, modelControlReason, modelControlRuleChanged } from '../../utils/questionAnswerModelControl'
const props = defineProps<{ settings: ModelControlAutoSettings; workspace: string }>()
const emit = defineEmits<{ (event: 'saved', settings: ModelControlAutoSettings): void; (event: 'cancel'): void }>()
const draft = ref({ ...props.settings }), busy = ref(false), error = ref(''), notice = ref('')
let sequence = 0, controller: AbortController | null = null
const reset = () => { sequence++; controller?.abort(); busy.value = false; draft.value = { ...props.settings }; error.value = ''; notice.value = '' }
watch(() => [props.settings, props.workspace], reset)
onBeforeUnmount(() => { sequence++; controller?.abort() })
const enabled = computed(() => draft.value.autoCloseEnabled || draft.value.autoRestoreEnabled || draft.value.healthCoordinationEnabled)
const valid = computed(() => Number.isInteger(draft.value.verifyIntervalMinutes) && draft.value.verifyIntervalMinutes >= 5 && draft.value.verifyIntervalMinutes <= 60 && Number.isInteger(draft.value.retryIntervalMinutes) && draft.value.retryIntervalMinutes >= 1 && draft.value.retryIntervalMinutes <= 60)
const save = async () => {
  if (busy.value || !valid.value) return
  const generation = ++sequence, workspace = props.workspace, request = new AbortController(); controller = request; busy.value = true; error.value = ''; notice.value = ''
  const current = () => generation === sequence && workspace === props.workspace && !request.signal.aborted
  try {
    const result = await saveModelControlAutoSettings(draft.value, draft.value.version, request.signal)
    if (current()) { draft.value = { ...result }; modelControlRuleChanged(workspace); emit('saved', result) }
  } catch (cause) {
    if (!current()) return
    error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request'
    if (cause instanceof ConnectionHealthApiError && cause.status === 409 && isModelControlAutoSettings(cause.current)) { draft.value = { ...cause.current }; error.value = ''; notice.value = message('autoSettingsChanged') }
  } finally { if (current()) busy.value = false; if (controller === request) controller = null }
}
</script>
<template>
  <form data-testid="model-control-auto-form" class="mt-3 space-y-3 rounded-lg border border-border/50 p-3 text-xs" @submit.prevent="save">
    <p>{{ message(enabled ? 'autoFormEnabled' : 'autoFormOff') }}</p>
    <div v-for="option in (['autoClose', 'autoRestore', 'healthCoordination'] as const)" :key="option" class="space-y-1">
      <label class="flex items-center gap-2"><input v-model="draft[`${option}Enabled`]" type="checkbox" :aria-label="message(option)" :disabled="busy" />{{ message(option) }}</label>
      <p class="break-words text-muted-foreground">{{ message(`${option}Help`) }}</p>
    </div>
    <div class="space-y-1"><label>{{ message('autoVerifyInterval') }}<input v-model.number="draft.verifyIntervalMinutes" :aria-label="message('autoVerifyInterval')" type="number" min="5" max="60" step="1" class="mx-2 w-16 rounded-md border border-border/60 bg-background px-2 py-1" :disabled="busy" />{{ message('autoMinutes') }}</label><p class="break-words text-muted-foreground">{{ message('autoVerifyHelp') }}</p></div>
    <div class="space-y-1"><label>{{ message('autoRetryInterval') }}<input v-model.number="draft.retryIntervalMinutes" :aria-label="message('autoRetryInterval')" type="number" min="1" max="60" step="1" class="mx-2 w-16 rounded-md border border-border/60 bg-background px-2 py-1" :disabled="busy" />{{ message('autoMinutes') }}</label><p class="break-words text-muted-foreground">{{ message('autoRetryHelp') }}</p></div>
    <p v-if="draft.autoCloseEnabled || draft.autoRestoreEnabled" class="break-words text-amber-600 dark:text-amber-400">{{ message('autoWriteWarning') }}</p>
    <p v-if="draft.autoCloseEnabled && draft.autoRestoreEnabled" class="break-words text-muted-foreground">{{ message('autoBothWarning') }}</p>
    <p v-if="!valid" role="alert" class="text-destructive">{{ message('autoInvalid') }}</p><p v-if="error" role="alert" class="text-destructive">{{ modelControlReason(error) }}</p><p v-if="notice" role="status">{{ notice }}</p>
    <div class="flex gap-2"><button type="submit" class="rounded-md border border-border/60 px-2.5 py-1.5 disabled:opacity-50" :disabled="busy || !valid">{{ message('autoSave') }}</button><button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5" :disabled="busy" @click="emit('cancel')">{{ message('cancel') }}</button></div>
  </form>
</template>
