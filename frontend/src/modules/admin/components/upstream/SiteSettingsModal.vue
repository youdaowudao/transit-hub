<script setup lang="ts">
import { costFailureText } from '../../utils/upstream'
import { onBeforeUnmount, ref, watch } from 'vue'
import { Settings2, X, Save, Loader2, CheckCircle2 } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import type { SiteSettings, UpstreamSite } from '../../types/upstream'

const props = defineProps<{
  open: boolean
  site: UpstreamSite | null
  saveSettings: (siteId: string, settings: SiteSettings) => Promise<void>
}>()

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'saved', siteId: string, settings: { balanceThreshold: number | null }): void
}>()

import { t } from '@/locales'
const useCustomThreshold = ref(false)
const balanceThreshold = ref('')
const isSaving = ref(false)
const showSuccess = ref(false)
const errorMsg = ref<string | null>(null)

let displayGeneration = 0
let successTimer: ReturnType<typeof setTimeout> | undefined
watch([() => props.open, () => props.site?.id], ([isOpen]) => {
  displayGeneration++
  if (successTimer) clearTimeout(successTimer)
  if (!isOpen || !props.site) return
  const s = props.site.settings
  useCustomThreshold.value = s.balanceThreshold != null
  balanceThreshold.value = s.balanceThreshold != null ? String(s.balanceThreshold) : ''
  errorMsg.value = null
  showSuccess.value = false
}, { immediate: true })

const save = async () => {
  if (isSaving.value || !props.site) return
  const siteId = props.site.id
  const generation = displayGeneration
  const settings = { balanceThreshold: useCustomThreshold.value ? (Number.parseFloat(balanceThreshold.value) || null) : null }
  isSaving.value = true
  errorMsg.value = null
  try {
    await props.saveSettings(siteId, settings)
    emit('saved', siteId, settings)
    if (props.open && props.site?.id === siteId && generation === displayGeneration) {
      showSuccess.value = true
      successTimer = setTimeout(() => { showSuccess.value = false; successTimer = undefined }, 2000)
    }
  } catch (err) {
    if (props.open && props.site?.id === siteId && generation === displayGeneration) errorMsg.value = err instanceof Error && 'failure' in err ? (err.failure as {errorDetail?:string}|undefined)?.errorDetail ?? err.message : err instanceof Error ? err.message : 'admin.upstream.errors.unknown'
  } finally {
    isSaving.value = false
  }
}
onBeforeUnmount(() => { displayGeneration++; if (successTimer) clearTimeout(successTimer) })
</script>

<template>
  <Teleport defer to="body">
    <div v-if="open && site" class="fixed inset-0 z-[100] flex items-center justify-center p-4">
      <div class="absolute inset-0 bg-background/80 backdrop-blur-sm" @click="emit('close')"></div>

      <div
        role="dialog"
        aria-modal="true"
        class="relative w-full max-w-md overflow-hidden rounded-2xl border border-border/60 bg-card text-card-foreground shadow-2xl animate-in fade-in zoom-in-95 duration-200"
      >
        <div class="absolute left-0 right-0 top-0 h-1 bg-gradient-to-r from-primary via-accent to-primary" />

        <div class="flex items-center justify-between px-6 pt-6 pb-4 border-b border-border/40">
          <div class="flex items-center gap-3">
            <div class="flex h-10 w-10 items-center justify-center rounded-xl bg-primary/10 text-primary">
              <Settings2 class="h-5 w-5" />
            </div>
            <div>
              <h2 class="text-base font-semibold text-foreground">{{ t('admin.upstream.siteSettings.title') }}</h2>
              <p class="text-xs text-muted-foreground">{{ site.name }}</p>
            </div>
          </div>
          <button
            type="button"
            class="rounded-md p-1 text-muted-foreground transition-colors hover:bg-surface-elevated hover:text-foreground"
            @click="emit('close')"
          >
            <X class="h-5 w-5" />
          </button>
        </div>

        <div class="px-6 py-5 space-y-5">
          <!-- Balance Threshold Override -->
          <div class="space-y-3">
            <div class="flex items-center justify-between">
              <label class="text-sm font-medium text-foreground">{{ t('admin.upstream.siteSettings.balanceThreshold') }}</label>
              <label class="relative inline-flex items-center cursor-pointer">
                <input type="checkbox" v-model="useCustomThreshold" class="sr-only peer">
                <div class="w-9 h-5 bg-surface-elevated rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-border after:border after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:bg-primary"></div>
              </label>
            </div>
            <p class="text-xs text-muted-foreground">{{ t('admin.upstream.siteSettings.balanceThresholdHelp') }}</p>
            <div v-if="useCustomThreshold" class="animate-in slide-in-from-top-2 fade-in duration-200">
              <Input
                type="number"
                :model-value="balanceThreshold"
                @update:model-value="balanceThreshold = String($event)"
                min="0"
                step="0.01"
                :placeholder="t('admin.upstream.siteSettings.balanceThresholdPlaceholder')"
                class="max-w-[200px]"
              />
            </div>
          </div>

          <p v-if="errorMsg" class="text-sm text-destructive rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2">
            {{ costFailureText(errorMsg) }}
          </p>
        </div>

        <div class="px-6 pb-6 flex justify-end gap-3">
          <Button variant="ghost" @click="emit('close')">
            {{ t('admin.upstream.siteSettings.cancel') }}
          </Button>
          <Button :disabled="isSaving" @click="save">
            <Loader2 v-if="isSaving" class="h-4 w-4 animate-spin mr-2" />
            <CheckCircle2 v-else-if="showSuccess" class="h-4 w-4 mr-2 text-green-400" />
            <Save v-else class="h-4 w-4 mr-2" />
            {{ showSuccess ? t('admin.upstream.siteSettings.saveSuccess') : (isSaving ? t('admin.upstream.siteSettings.saving') : t('admin.upstream.siteSettings.save')) }}
          </Button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
