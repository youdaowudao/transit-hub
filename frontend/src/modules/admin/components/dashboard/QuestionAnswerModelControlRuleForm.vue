<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ConnectionHealthApiError, saveModelControlSettings } from '../../api/connectionHealth'
import type { ModelControlSettings } from '../../types/connectionHealth'
import { modelControlText as message, modelControlReason, modelControlRuleChanged } from '../../utils/questionAnswerModelControl'
const props = defineProps<{ settings: ModelControlSettings; workspace: string }>()
const emit = defineEmits<{ (event: 'saved', settings: ModelControlSettings): void; (event: 'cancel'): void }>()
const draft = ref({ ...props.settings }), busy = ref(false), error = ref(''), notice = ref('')
let sequence = 0, controller: AbortController | null = null
const reset = () => { sequence++; controller?.abort(); busy.value = false; draft.value = { ...props.settings }; error.value = ''; notice.value = '' }
watch(() => [props.settings, props.workspace], reset)
onBeforeUnmount(() => { sequence++; controller?.abort() })
const valid = computed(() => Number.isInteger(draft.value.minAccuracyPercent) && draft.value.minAccuracyPercent >= 1 && draft.value.minAccuracyPercent <= 100 && Number.isInteger(draft.value.minJudgedAnswers) && draft.value.minJudgedAnswers >= 1 && draft.value.minJudgedAnswers <= 50)
const save = async () => {
  if (busy.value || !valid.value) return
  const generation = ++sequence, workspace = props.workspace, request = new AbortController(); controller = request; busy.value = true; error.value = ''; notice.value = ''
  const current = () => generation === sequence && workspace === props.workspace && !request.signal.aborted
  try {
    const { version, ...settings } = draft.value
    const result = await saveModelControlSettings(settings, version, request.signal)
    if (current()) { draft.value = { ...result }; modelControlRuleChanged(workspace); emit('saved', result) }
  } catch (cause) {
    if (!current()) return
    error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request'
    if (cause instanceof ConnectionHealthApiError && cause.status === 409 && cause.current && typeof cause.current === 'object' && 'minAccuracyPercent' in cause.current) { draft.value = { ...cause.current as ModelControlSettings }; error.value = ''; notice.value = message('ruleChanged') }
  } finally { if (current()) busy.value = false; if (controller === request) controller = null }
}
</script>
<template>
  <form data-testid="model-control-rule-form" class="mt-3 space-y-3 rounded-lg border border-border/50 p-3 text-xs" @submit.prevent="save">
    <p class="break-words text-muted-foreground">{{ message('ruleEditHint') }}</p>
    <div class="flex flex-wrap gap-3"><label>{{ message('accuracyInput') }}<input v-model.number="draft.minAccuracyPercent" :aria-label="message('accuracyLabel')" type="number" min="1" max="100" class="ml-2 w-16 rounded-md border border-border/60 bg-background px-2 py-1" :disabled="busy" /></label><label>{{ message('judgedLabel') }}<input v-model.number="draft.minJudgedAnswers" :aria-label="message('judgedLabel')" type="number" min="1" max="50" class="ml-2 w-16 rounded-md border border-border/60 bg-background px-2 py-1" :disabled="busy" /></label></div>
    <p v-if="!valid" class="text-destructive">{{ message('ruleInvalid') }}</p><p v-if="error" role="alert" class="text-destructive">{{ modelControlReason(error) }}</p><p v-if="notice" role="status">{{ notice }}</p>
    <div class="flex gap-2"><button type="submit" class="rounded-md border border-border/60 px-2.5 py-1.5 disabled:opacity-50" :disabled="busy || !valid">{{ message('saveRule') }}</button><button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5" :disabled="busy" @click="emit('cancel')">{{ message('cancel') }}</button></div>
  </form>
</template>
