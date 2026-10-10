<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { Tooltip } from '@/components/ui/tooltip'
import { getModelControlSettings } from '../../api/connectionHealth'
import type { ModelControlCounts, ModelControlSettings } from '../../types/connectionHealth'
import { modelControlText as message, modelControlRuleRevision } from '../../utils/questionAnswerModelControl'
import QuestionAnswerModelControlRuleForm from './QuestionAnswerModelControlRuleForm.vue'
const props = defineProps<{ scope: 'account' | 'workspace'; counts: ModelControlCounts | null; workspace: string; countsStale?: boolean }>()
const settings = ref<ModelControlSettings | null>(null), editing = ref(false), failed = ref(false)
let sequence = 0, controller: AbortController | null = null
const load = async () => {
  const revision = ++sequence, workspace = props.workspace, request = new AbortController(); controller?.abort(); controller = request; failed.value = false
  try { const value = await getModelControlSettings(request.signal); if (revision === sequence && workspace === props.workspace && !request.signal.aborted) settings.value = value }
  catch { if (revision === sequence && workspace === props.workspace && !request.signal.aborted) failed.value = true }
  finally { if (controller === request) controller = null }
}
watch(() => props.workspace, () => { settings.value = null; editing.value = false; void load() }, { immediate: true })
watch(modelControlRuleRevision, value => { if (value.workspace === props.workspace && !editing.value) void load() })
onBeforeUnmount(() => { sequence++; controller?.abort() })
</script>
<template>
  <div data-testid="model-control-header" class="space-y-2">
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2 text-xs"><span>{{ message('handling') }}</span><Tooltip :text="message('handlingHint')" wide><button type="button" aria-pressed="true" class="rounded-md border border-primary/30 bg-primary/10 px-2.5 py-1 text-primary">{{ message('manualHandling') }}</button></Tooltip><p v-if="failed" role="alert">{{ message('ruleReadFailed') }} · <button type="button" class="underline" @click="load">{{ message('retry') }}</button></p><template v-else-if="settings"><p class="min-w-0 break-words text-muted-foreground">{{ message('rule') }}{{ message('ruleSummary', { accuracy: settings.minAccuracyPercent, min: settings.minJudgedAnswers }) }}</p><button type="button" class="underline" @click="editing = !editing">{{ message('edit') }}</button></template></div>
    <QuestionAnswerModelControlRuleForm v-if="editing && settings" :settings="settings" :workspace="workspace" @saved="settings = $event; editing = false" @cancel="editing = false" />
    <div v-if="counts" class="flex flex-wrap gap-2"><div v-for="key in (['total', 'open', 'closed', 'attention', 'untested'] as const)" :key="key" :data-testid="`model-control-count-${key}`" class="min-w-14 rounded-md border border-border/50 px-2.5 py-1.5" :class="key === 'closed' && counts[key] > 0 ? 'text-red-600 dark:text-red-400' : key === 'attention' && counts[key] > 0 ? 'text-amber-600 dark:text-amber-400' : ''"><span class="block text-[11px] text-muted-foreground">{{ message(`count${key[0]!.toUpperCase()}${key.slice(1)}`) }}</span><span class="text-base font-semibold tabular-nums">{{ counts[key] }}</span></div></div>
    <p v-if="countsStale" class="text-xs text-amber-600 dark:text-amber-400">{{ message('countsStale') }}</p>
  </div>
</template>
