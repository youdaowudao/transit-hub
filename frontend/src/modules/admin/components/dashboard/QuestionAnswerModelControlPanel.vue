<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { getModelControlTarget, verifyModelControl } from '../../api/connectionHealth'
import type { ModelControlItem } from '../../types/connectionHealth'
import { modelControlText as message, modelControlReason, modelControlRuleRevision } from '../../utils/questionAnswerModelControl'
import QuestionAnswerModelControlRow from './QuestionAnswerModelControlRow.vue'
const props = defineProps<{ targetId: string; refreshKey: number }>()
const emit = defineEmits<{ (event: 'settled', targetId: string): void }>()
const items = ref<ModelControlItem[]>([]), candidates = ref<string[]>([]), loading = ref(false), error = ref('')
let disposed = false
let sequence = 0, automaticVerifyDone = false, controller: AbortController | null = null
const load = async () => {
  if (disposed) return
  const revision = ++sequence, targetId = props.targetId, request = new AbortController(); controller?.abort(); controller = request; loading.value = true; error.value = ''
  const current = () => revision === sequence && targetId === props.targetId && !request.signal.aborted
  try {
    const result = await getModelControlTarget(targetId, request.signal)
    if (!current()) return
    items.value = result.items; candidates.value = result.candidates.filter(model => !result.items.some(item => item.modelName === model))
    const shouldVerify = !automaticVerifyDone && result.items.some(item => Object.keys(item.control.closedEntries).length || item.control.pending || item.control.observation.state === 'last_model')
    automaticVerifyDone = true
    if (shouldVerify) {
      const verified = await verifyModelControl([targetId], request.signal)
      if (!current()) return
      for (const value of verified.items) update(value)
      error.value = verified.errors.find(value => value.targetId === targetId)?.reasonKey ?? ''
      emit('settled', targetId)
    }
  } catch (cause) { if (current()) error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request' }
  finally { if (current()) loading.value = false; if (controller === request) controller = null }
}
let loadScheduled = false
const requestLoad = () => { if (loadScheduled) return; loadScheduled = true; queueMicrotask(() => { loadScheduled = false; void load() }) }
const update = (value: ModelControlItem) => { if (value.targetId !== props.targetId) return; items.value = items.value.some(item => item.modelName === value.modelName) ? items.value.map(item => item.modelName === value.modelName ? value : item) : [...items.value, value]; candidates.value = candidates.value.filter(model => model !== value.modelName) }
const rowUpdated = (value: ModelControlItem) => { sequence++; controller?.abort(); loading.value = false; update(value) }
watch(() => props.targetId, () => { sequence++; controller?.abort(); items.value = []; candidates.value = []; automaticVerifyDone = false; void load() }, { immediate: true })
watch(() => props.refreshKey, requestLoad)
watch(modelControlRuleRevision, value => { if (value.workspace === props.targetId.split(':')[1]) requestLoad() })
onBeforeUnmount(() => { disposed = true; sequence++; controller?.abort() })
</script>
<template>
  <section data-testid="model-control-panel" class="mb-3 space-y-3 rounded-lg border border-border/50 p-3"><header class="flex items-center justify-between gap-2"><h3 class="text-xs font-semibold">{{ message('title') }}</h3><button type="button" class="text-xs text-muted-foreground underline" :disabled="loading" @click="load">{{ message('refresh') }}</button></header><p v-if="error" role="alert" class="text-xs text-destructive">{{ modelControlReason(error) }} · <button type="button" class="underline" @click="load">{{ message('retry') }}</button></p><p v-if="loading && !items.length && !candidates.length" class="text-xs text-muted-foreground">{{ message('loading') }}</p><p v-else-if="!items.length && !candidates.length && !error" class="text-xs text-muted-foreground">{{ message('emptyCandidates') }}</p>
    <QuestionAnswerModelControlRow v-for="item in items" :key="item.modelName" :item="item" :target-id="targetId" :model-name="item.modelName" :workspace="targetId.split(':')[1] ?? ''" @updated="rowUpdated" @refresh="requestLoad" @removed="items = items.filter(value => value.modelName !== item.modelName)" @settled="emit('settled', $event)" />
    <QuestionAnswerModelControlRow v-for="model in candidates" :key="model" :target-id="targetId" :model-name="model" :workspace="targetId.split(':')[1] ?? ''" @updated="rowUpdated" @refresh="requestLoad" @settled="emit('settled', $event)" />
  </section>
</template>
