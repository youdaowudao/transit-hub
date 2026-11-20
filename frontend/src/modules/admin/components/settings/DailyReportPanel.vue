<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Loader2 } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { t } from '@/locales'
import { useAdminAccounts } from '../../composables/useAdminAccounts'
import { getDailyReportPreview, testDailyReport } from '../../api/reports'
import { dateKeyInTimeZone, formatLastUsedAt } from '../../utils/userLastUsed'
import type { StrategySettings } from '../../types/settings'
import type { DailyReportBot, DailyReportPatch, DailyReportPreview, DailyReportScope, DailyReportTest } from '../../types/reports'

const props = defineProps<{ strategy: StrategySettings | null; bots: DailyReportBot[]; botsLoaded: boolean; saving: boolean; loading: boolean }>()
const emit = defineEmits<{ reload: [] }>()
const { currentAccount, isSwitching } = useAdminAccounts()
const scope = computed<DailyReportScope | null>(() => currentAccount.value ? { workspaceAdminAccountId: currentAccount.value.id, workspaceCreatedAt: currentAccount.value.createdAt } : null)
const base = ref<{ enabled: unknown; time: unknown; botIds: unknown } | null>(null)
const enabled = ref(false)
const sendTime = ref('08:00')
const botId = ref('')
const edited = ref({ enabled: false, time: false, botIds: false })
const preview = ref<DailyReportPreview | null>(null)
const testResult = ref<DailyReportTest | null>(null)
const testTime = ref('')
const previewLoading = ref(false)
const testing = ref(false)
const previewError = ref('')
const testError = ref('')
let epoch = 0
let previewSequence = 0
let testSequence = 0
let disposed = false
let previewController: AbortController | null = null
let testController: AbortController | null = null
const same = (left: unknown, right: unknown) => JSON.stringify(left) === JSON.stringify(right)
const idsValid = (value: unknown): value is string[] => Array.isArray(value) && value.length <= 1 && value.every(id => typeof id === 'string' && id.length > 0 && [...id].length <= 128)
const timeValid = (value: unknown) => typeof value === 'string' && /^([01]\d|2[0-3]):[0-5]\d$/.test(value)
const time = (value: string | null) => value ? formatLastUsedAt(value) || t('admin.settings.dailyReport.unknown') : t('admin.settings.dailyReport.unknown')
const reason = (cause: unknown) => cause instanceof Error ? cause.message : t('admin.settings.dailyReport.readError')
const aborted = (cause: unknown) => cause instanceof Error && cause.name === 'AbortError'
const current = (generation: number) => !disposed && epoch === generation
const getPatch = (): DailyReportPatch => {
  const result: DailyReportPatch = {}
  if (!base.value) return result
  if (edited.value.enabled && !same(enabled.value, base.value.enabled)) result.dailyReportEnabled = enabled.value
  if (edited.value.time && !same(sendTime.value, base.value.time)) result.dailyReportTime = sendTime.value
  const ids = botId.value ? [botId.value] : []
  if (edited.value.botIds && !same(ids, base.value.botIds)) result.dailyReportBotIds = ids
  return result
}
const dirty = computed(() => Object.keys(getPatch()).length > 0)
const storedTarget = computed(() => {
  if (!props.botsLoaded || !base.value || !idsValid(base.value.botIds) || base.value.botIds.length !== 1) return null
  const id = base.value.botIds[0]
  const matches = props.bots.filter(bot => bot.id === id)
  return matches.length === 1 && matches[0]?.enabled ? matches[0] : null
})
const missingDraftTarget = computed(() => botId.value && !props.bots.some(bot => bot.id === botId.value))
const storedProblem = computed(() => {
  if (!base.value) return ''
  if (typeof base.value.enabled !== 'boolean') return t('admin.settings.dailyReport.badSavedSwitch')
  if (!timeValid(base.value.time)) return t('admin.settings.dailyReport.badSavedTime')
  if (!idsValid(base.value.botIds)) return t('admin.settings.dailyReport.badSavedBots')
  if (base.value.botIds.length > 0 && !props.botsLoaded) return t('admin.settings.dailyReport.targetUnverified')
  if (base.value.botIds.length > 0 && !storedTarget.value) return t('admin.settings.dailyReport.missingTarget')
  if (base.value.enabled && !storedTarget.value) return t('admin.settings.dailyReport.needTarget')
  return ''
})
const invalidatePreview = () => {
  previewSequence++; previewController?.abort(); preview.value = null; previewError.value = ''; previewLoading.value = false
}
const apply = (settings: StrategySettings | null) => {
  invalidatePreview()
  edited.value = { enabled: false, time: false, botIds: false }
  if (!settings || isSwitching.value) { base.value = null; enabled.value = false; sendTime.value = '08:00'; botId.value = ''; return }
  base.value = { enabled: settings.dailyReportEnabled === undefined ? false : settings.dailyReportEnabled, time: settings.dailyReportTime === undefined ? '08:00' : settings.dailyReportTime, botIds: settings.dailyReportBotIds === undefined ? [] : settings.dailyReportBotIds }
  enabled.value = typeof base.value.enabled === 'boolean' ? base.value.enabled : false
  sendTime.value = timeValid(base.value.time) ? base.value.time as string : ''
  botId.value = idsValid(base.value.botIds) && base.value.botIds.length === 1 ? base.value.botIds[0]! : ''
}
const reset = () => {
  epoch++; invalidatePreview(); testSequence++; testController?.abort(); testing.value = false
  testResult.value = null; testTime.value = ''; testError.value = ''; base.value = null; enabled.value = false; sendTime.value = '08:00'; botId.value = ''; edited.value = { enabled: false, time: false, botIds: false }
}
const closeAndClear = () => {
  if (!base.value || props.saving || isSwitching.value) return
  enabled.value = false; botId.value = ''
  edited.value.enabled = true; edited.value.botIds = true
}
const scheduledIsToday = computed(() => preview.value?.scheduled?.sendingDate === (preview.value ? dateKeyInTimeZone(preview.value.observedAt) : null))
const loadPreview = async () => {
  if (!scope.value || isSwitching.value || props.saving || previewLoading.value) return
  const generation = epoch; const request = ++previewSequence; const captured = { ...scope.value }
  previewController?.abort(); const controller = new AbortController(); previewController = controller
  previewLoading.value = true; previewError.value = ''
  try {
    const value = await getDailyReportPreview(captured, controller.signal)
    if (current(generation) && request === previewSequence && previewController === controller && !controller.signal.aborted) preview.value = value
  } catch (cause) {
    if (current(generation) && request === previewSequence && previewController === controller && !controller.signal.aborted && !aborted(cause)) previewError.value = reason(cause)
  } finally {
    if (current(generation) && request === previewSequence && previewController === controller) previewLoading.value = false
  }
}
const sendTest = async () => {
  if (!scope.value || isSwitching.value || props.saving || dirty.value || testing.value || !storedTarget.value) return
  const generation = epoch; const request = ++testSequence; const captured = { ...scope.value }
  testController?.abort(); const controller = new AbortController(); testController = controller
  testing.value = true; testError.value = ''; testResult.value = null
  try {
    const value = await testDailyReport(captured, controller.signal)
    if (current(generation) && request === testSequence && testController === controller && !controller.signal.aborted) {
      testResult.value = value; testTime.value = formatLastUsedAt(new Date())
    }
  } catch (cause) {
    if (current(generation) && request === testSequence && testController === controller && !controller.signal.aborted && !aborted(cause)) testError.value = reason(cause)
  } finally {
    if (current(generation) && request === testSequence && testController === controller) testing.value = false
  }
}
watch(() => props.strategy, apply, { immediate: true })
watch(() => props.saving, saving => { if (saving) invalidatePreview() })
watch([
  () => scope.value?.workspaceAdminAccountId,
  () => scope.value?.workspaceCreatedAt,
  () => isSwitching.value,
], reset, { flush: 'sync' })
onBeforeUnmount(() => { disposed = true; reset() })
defineExpose({ getPatch, invalidatePreview })
</script>

<template>
  <section class="space-y-4 rounded-2xl border border-border/50 bg-card p-5 shadow-sm">
    <h4 class="text-sm font-semibold text-foreground">{{ t('admin.settings.dailyReport.title') }}</h4>
    <p class="text-sm text-muted-foreground">{{ t('admin.settings.dailyReport.enableHelp') }}</p>
    <p class="text-xs text-muted-foreground">{{ t('admin.settings.dailyReport.oneTarget') }}</p>
    <p v-if="!base" class="text-sm text-warning">{{ t('admin.settings.dailyReport.notLoaded') }}</p>
    <Button v-if="!base || !botsLoaded" variant="secondary" :disabled="loading || saving || isSwitching" @click="emit('reload')">{{ t('admin.settings.dailyReport.reloadSettings') }}</Button>
    <p v-if="storedProblem" role="status" class="text-sm text-warning">{{ storedProblem }}</p>
    <p v-if="base && !storedProblem" class="text-xs text-muted-foreground">{{ base.enabled ? t('admin.settings.dailyReport.enabled') : t('admin.settings.dailyReport.off') }}</p>
    <fieldset :disabled="!base || saving || isSwitching" class="space-y-4">
      <label class="flex items-center gap-2 text-sm"><input v-model="enabled" type="checkbox" class="h-4 w-4 rounded border-border" @change="edited.enabled = true" />{{ t('admin.settings.dailyReport.enable') }}</label>
      <div class="grid gap-4 sm:grid-cols-2">
        <label class="text-sm">{{ t('admin.settings.dailyReport.time') }}<Input v-model="sendTime" maxlength="5" placeholder="08:00" class="mt-2" @input="edited.time = true" /></label>
        <label class="text-sm">
          {{ t('admin.settings.dailyReport.bot') }}
          <select v-model="botId" class="mt-2 h-10 w-full rounded-md border border-border bg-background px-3" @change="edited.botIds = true">
            <option value="">{{ t('admin.settings.dailyReport.noBot') }}</option>
            <option v-if="missingDraftTarget" :value="botId" disabled>{{ botsLoaded ? t('admin.settings.dailyReport.oldBot') : t('admin.settings.dailyReport.targetUnverified') }}</option>
            <option v-for="(bot, index) in bots" :key="`${bot.id}-${index}`" :value="bot.id" :disabled="!bot.enabled">{{ bot.name || bot.id }}{{ bot.enabled ? '' : t('admin.settings.dailyReport.disabledBot') }}</option>
          </select>
        </label>
      </div>
      <Button type="button" variant="secondary" @click="closeAndClear">{{ t('admin.settings.dailyReport.closeAndClear') }}</Button>
      <p class="text-xs text-muted-foreground">{{ t('admin.settings.dailyReport.clearHelp') }}</p>
    </fieldset>
    <p class="text-xs text-muted-foreground">{{ t('admin.settings.dailyReport.saveHelp') }}</p>
    <p v-if="dirty" class="text-sm text-warning">{{ t('admin.settings.dailyReport.dirty') }}</p>
    <p class="text-xs text-muted-foreground">{{ t('admin.settings.dailyReport.inFlight') }}</p>
    <div class="border-t border-border/50 pt-4 space-y-3">
      <p class="text-sm text-muted-foreground">{{ t('admin.settings.dailyReport.previewHelp') }}</p>
      <p class="text-xs text-muted-foreground">{{ t('admin.settings.dailyReport.savedPreview') }}</p>
      <Button variant="secondary" :disabled="!scope || isSwitching || saving || previewLoading" @click="loadPreview"><Loader2 v-if="previewLoading" class="mr-2 h-4 w-4 animate-spin" />{{ t('admin.settings.dailyReport.preview') }}</Button>
      <p v-if="previewError" role="alert" class="text-sm text-destructive">{{ previewError }}</p>
      <template v-if="preview">
        <p class="text-xs text-muted-foreground">{{ t('admin.settings.dailyReport.previewAt') }} {{ time(preview.observedAt) }}</p>
        <p v-if="preview.pauseReason" role="status" class="text-sm text-warning">{{ preview.pauseReason }}</p>
        <div v-for="section in preview.sections" :key="section.title" class="space-y-2 rounded-lg border border-border/50 p-4 text-sm">
          <h5 class="font-semibold">{{ section.title }} · {{ section.date }}</h5>
          <p class="text-xs text-muted-foreground">{{ t('admin.settings.dailyReport.observed') }} {{ time(section.observedAt) }} · {{ t('admin.settings.dailyReport.coverage') }} {{ section.coverage.from || t('admin.settings.dailyReport.unknown') }} — {{ section.coverage.to || t('admin.settings.dailyReport.unknown') }}</p>
          <p class="whitespace-pre-wrap break-words">{{ section.text }}</p>
        </div>
        <details class="text-sm">
          <summary class="cursor-pointer">{{ t('admin.settings.dailyReport.fullText') }}</summary>
          <pre class="mt-3 whitespace-pre-wrap break-words rounded-lg border border-border/50 p-4 font-sans">{{ preview.text }}</pre>
        </details>
        <div class="space-y-2 border-t border-border/50 pt-4 text-sm">
          <h5 class="font-semibold">{{ t('admin.settings.dailyReport.lastScheduled') }}</h5>
          <p v-if="preview.scheduledWarning" class="text-warning">{{ preview.scheduledWarning }}</p>
          <template v-else-if="preview.scheduled">
            <p>{{ preview.scheduled.name || t('admin.settings.dailyReport.savedBot') }} · {{ t('admin.settings.dailyReport.reportDates', { report: preview.scheduled.businessDate, sending: preview.scheduled.sendingDate }) }} · {{ time(preview.scheduled.at) }}</p>
            <p v-if="preview.scheduled.status === 'failed'" class="text-destructive">{{ scheduledIsToday ? t('admin.settings.dailyReport.failed', { reason: preview.scheduled.reason }) : t('admin.settings.dailyReport.failedBefore', { day: preview.scheduled.sendingDate, reason: preview.scheduled.reason }) }}</p>
            <p v-else-if="preview.scheduled.status === 'unknown'" class="text-warning">{{ t('admin.settings.dailyReport.unknownDelivery', { name: preview.scheduled.name || t('admin.settings.dailyReport.savedBot') }) }}</p>
            <p v-else>{{ t('admin.settings.dailyReport.sent') }}</p>
            <p class="text-xs text-muted-foreground">{{ preview.scheduled.reason }}</p>
          </template>
          <p v-else class="text-muted-foreground">{{ t('admin.settings.dailyReport.noScheduled') }}</p>
        </div>
      </template>
    </div>
    <div class="space-y-3 border-t border-border/50 pt-4">
      <p class="text-sm text-muted-foreground">{{ t('admin.settings.dailyReport.testHelp') }}</p>
      <p class="text-xs text-muted-foreground">{{ t('admin.settings.dailyReport.testTwice') }}</p>
      <p v-if="!storedTarget" class="text-sm text-warning">{{ t('admin.settings.dailyReport.testNeedTarget') }}</p>
      <Button variant="secondary" :disabled="!scope || isSwitching || saving || dirty || testing || !storedTarget" @click="sendTest"><Loader2 v-if="testing" class="mr-2 h-4 w-4 animate-spin" />{{ t('admin.settings.dailyReport.test') }}</Button>
      <p v-if="testError" role="alert" class="text-sm text-destructive">{{ testError }}</p>
      <div v-if="testResult" role="status" class="space-y-2 rounded-lg border border-border/50 p-4 text-sm">
        <p>{{ t('admin.settings.dailyReport.testAt') }} {{ testTime }}</p>
        <p>{{ t('admin.settings.dailyReport.counts', { attempted: testResult.attempted, sent: testResult.sent, failed: testResult.failed, unknown: testResult.unknown }) }}</p>
        <div v-for="result in testResult.results" :key="result.botId" class="space-y-1">
          <p>{{ result.name || result.botId }} · {{ t(`admin.settings.dailyReport.result.${result.status}`) }}</p>
          <p v-if="result.status === 'unknown'" class="text-warning">{{ t('admin.settings.dailyReport.unknownDelivery', { name: result.name || t('admin.settings.dailyReport.savedBot') }) }}</p>
          <p>{{ result.reason }}</p>
        </div>
      </div>
    </div>
  </section>
</template>
