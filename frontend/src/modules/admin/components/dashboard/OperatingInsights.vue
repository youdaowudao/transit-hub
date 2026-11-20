<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ChevronDown, Loader2, RefreshCw } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { t } from '@/locales'
import { useAdminAccounts } from '../../composables/useAdminAccounts'
import { getOperatingInsights } from '../../api/operatingInsights'
import { formatLastUsedAt } from '../../utils/userLastUsed'
import type { OperatingInsightsResponse, OperatingInsightsScope } from '../../types/operatingInsights'

const { currentAccount, isSwitching } = useAdminAccounts()
const scope = computed<OperatingInsightsScope | null>(() => currentAccount.value ? { workspaceAdminAccountId: currentAccount.value.id, workspaceCreatedAt: currentAccount.value.createdAt } : null)
const open = ref(false)
const loading = ref(false)
const error = ref('')
const result = ref<OperatingInsightsResponse | null>(null)
let sequence = 0
let controller: AbortController | null = null
let disposed = false
const number = (value: number | null | undefined) => value == null ? t('admin.dashboard.operating.unknown') : value.toLocaleString('zh-CN', { maximumFractionDigits: 6 })
const percentage = (value: number | null) => value == null || !Number.isFinite(value * 100) ? t('admin.dashboard.operating.unknown') : `${(value * 100).toFixed(1)}%`
const time = (value: string | null) => value ? formatLastUsedAt(value) || t('admin.dashboard.operating.unknown') : t('admin.dashboard.operating.unknown')
const siteStatus = (value: string) => t(`admin.dashboard.operating.status.${['connected', 'connecting', 'syncing', 'error'].includes(value) ? value : 'unknown'}`)
const load = async () => {
  if (!open.value || !scope.value || isSwitching.value || disposed) return
  const request = ++sequence; const captured = { ...scope.value }
  controller?.abort(); const active = new AbortController(); controller = active
  loading.value = true; error.value = ''; result.value = null
  try {
    const value = await getOperatingInsights(captured, active.signal)
    if (!disposed && open.value && request === sequence && controller === active && !active.signal.aborted) result.value = value
  } catch (cause) {
    if (!disposed && open.value && request === sequence && controller === active && !active.signal.aborted && !(cause instanceof Error && cause.name === 'AbortError')) error.value = cause instanceof Error ? cause.message : t('admin.dashboard.operating.readError')
  } finally {
    if (!disposed && request === sequence && controller === active) loading.value = false
  }
}
const close = () => {
  sequence++; controller?.abort(); open.value = false; loading.value = false; error.value = ''; result.value = null
}
const toggle = () => {
  if (open.value) close()
  else { open.value = true; void load() }
}
watch(() => [scope.value?.workspaceAdminAccountId, scope.value?.workspaceCreatedAt, isSwitching.value], close, { flush: 'sync' })
onBeforeUnmount(() => { disposed = true; close() })
</script>

<template>
  <section class="rounded-lg border border-border/60 bg-card p-4 sm:p-5">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 class="text-base font-semibold text-foreground">{{ t('admin.dashboard.operating.title') }}</h2>
        <p class="mt-2 max-w-3xl text-sm text-muted-foreground">{{ t('admin.dashboard.operating.intro') }}</p>
        <p class="mt-1 text-xs text-muted-foreground">{{ t('admin.dashboard.operating.onDemand') }}</p>
      </div>
      <Button variant="secondary" :disabled="!scope || isSwitching" :aria-expanded="open" aria-controls="operating-insights-content" @click="toggle">
        <ChevronDown class="mr-2 h-4 w-4 transition-transform" :class="{ 'rotate-180': open }" />
        {{ open ? t('admin.dashboard.operating.collapse') : t('admin.dashboard.operating.expand') }}
      </Button>
    </div>
    <p v-if="!scope" class="mt-3 text-sm text-muted-foreground">{{ t('admin.dashboard.operating.noWorkspace') }}</p>
    <div v-if="open" id="operating-insights-content" class="mt-5 space-y-5">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <p class="text-xs text-muted-foreground">{{ t('admin.dashboard.operating.localOnly') }}</p>
        <Button variant="secondary" size="sm" :disabled="loading || !scope || isSwitching" @click="load">
          <Loader2 v-if="loading" class="mr-2 h-4 w-4 animate-spin" />
          <RefreshCw v-else class="mr-2 h-4 w-4" />
          {{ t('admin.dashboard.operating.retry') }}
        </Button>
      </div>
      <p v-if="loading" role="status" class="text-sm text-muted-foreground">{{ t('admin.dashboard.operating.loading') }}</p>
      <p v-if="error" role="alert" class="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">{{ error }}</p>
      <template v-if="result">
        <p class="text-sm">{{ result.range.from }} — {{ result.range.to }} · {{ t('admin.dashboard.operating.sevenDays') }}</p>
        <p class="text-xs text-muted-foreground">{{ t('admin.dashboard.operating.readAt') }} {{ time(result.observedAt) }} · Asia/Singapore</p>
        <div class="space-y-3 border-t border-border/50 pt-4">
          <h3 class="text-sm font-semibold">{{ t('admin.dashboard.operating.totals') }}</h3>
          <p class="text-xs text-muted-foreground">{{ t('admin.dashboard.operating.totalsHelp') }}</p>
          <div v-if="result.snapshotTotals" class="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
            <div><p class="text-xs text-muted-foreground">{{ t('admin.dashboard.operating.revenue') }}</p><p class="mt-1 font-semibold tabular-nums">{{ number(result.snapshotTotals.todayProfit) }}</p></div>
            <div><p class="text-xs text-muted-foreground">{{ t('admin.dashboard.operating.purchase') }}</p><p class="mt-1 font-semibold tabular-nums">{{ number(result.snapshotTotals.todayPurchase) }}</p></div>
            <div><p class="text-xs text-muted-foreground">{{ t('admin.dashboard.operating.operatingCost') }}</p><p class="mt-1 font-semibold tabular-nums">{{ number(result.snapshotTotals.operatingCost) }}</p></div>
            <div><p class="text-xs text-muted-foreground">{{ t('admin.dashboard.operating.netProfit') }}</p><p class="mt-1 font-semibold tabular-nums">{{ number(result.snapshotTotals.adjustedNetProfit) }}</p></div>
            <div><p class="text-xs text-muted-foreground">{{ t('admin.dashboard.operating.margin') }}</p><p class="mt-1 font-semibold tabular-nums">{{ percentage(result.snapshotTotals.profitMargin) }}</p></div>
          </div>
          <p v-else class="text-sm text-amber-700 dark:text-amber-300">{{ t('admin.dashboard.operating.totalsUnavailable') }}</p>
          <p v-if="result.missingDates.length" class="text-sm text-amber-700 dark:text-amber-300">{{ t('admin.dashboard.operating.missingDates') }} {{ result.missingDates.join('、') }}</p>
          <ul v-if="result.warnings.length" class="space-y-2 text-sm text-muted-foreground"><li v-for="warning in result.warnings" :key="warning">{{ warning }}</li></ul>
        </div>
        <div class="space-y-3 border-t border-border/50 pt-4">
          <h3 class="text-sm font-semibold">{{ t('admin.dashboard.operating.sites') }}</h3>
          <p class="text-xs text-muted-foreground">{{ t('admin.dashboard.operating.siteHelp') }}</p>
          <p class="text-xs text-muted-foreground">{{ t('admin.dashboard.operating.observationHelp') }}</p>
          <div class="overflow-x-auto">
            <table class="w-full min-w-[850px] text-left text-sm">
              <thead class="border-y border-border/50 bg-muted/30 text-muted-foreground"><tr>
                <th class="px-3 py-3">{{ t('admin.dashboard.operating.site') }}</th>
                <th class="px-3 py-3">{{ t('admin.dashboard.operating.balance') }}</th>
                <th class="px-3 py-3">{{ t('admin.dashboard.operating.average') }}</th>
                <th class="px-3 py-3">{{ t('admin.dashboard.operating.days') }}</th>
                <th class="px-3 py-3">{{ t('admin.dashboard.operating.advice') }}</th>
              </tr></thead>
              <tbody class="divide-y divide-border/50">
                <tr v-for="site in result.siteEstimates" :key="site.siteId">
                  <td class="px-3 py-3"><p class="font-medium">{{ site.name || site.siteId }}</p><p class="mt-1 text-xs text-muted-foreground">{{ site.enabled ? siteStatus(site.status) : t('admin.dashboard.operating.disabled') }}</p></td>
                  <td class="px-3 py-3"><p class="tabular-nums">{{ number(site.rawBalance) }}</p><p class="mt-1 max-w-[220px] text-xs text-muted-foreground">{{ t('admin.dashboard.operating.originalObserved') }} {{ time(site.lastSyncedAt) }}</p></td>
                  <td class="px-3 py-3 tabular-nums">{{ number(site.averageRawCost) }}</td>
                  <td class="px-3 py-3 tabular-nums">{{ site.daysLeft == null ? t('admin.dashboard.operating.unknown') : t('admin.dashboard.operating.daysValue', { days: number(site.daysLeft) }) }}</td>
                  <td class="max-w-md px-3 py-3">
                    <p v-if="site.suggestedTopUp != null" class="font-medium text-amber-700 dark:text-amber-300">{{ t('admin.dashboard.operating.topUp', { amount: number(site.suggestedTopUp) }) }}</p>
                    <p v-else-if="site.enabled && site.daysLeft != null && site.daysLeft > 2">{{ t('admin.dashboard.operating.sufficient') }}</p>
                    <ul v-if="site.reasons.length" class="mt-2 space-y-1 text-xs text-muted-foreground"><li v-for="reason in site.reasons" :key="reason">{{ reason }}</li></ul>
                  </td>
                </tr>
                <tr v-if="!result.siteEstimates.length"><td colspan="5" class="px-3 py-6 text-center text-muted-foreground">{{ t('admin.dashboard.operating.noSites') }}</td></tr>
              </tbody>
            </table>
          </div>
          <p class="text-xs text-muted-foreground">{{ t('admin.dashboard.operating.adviceHelp') }}</p>
        </div>
      </template>
    </div>
  </section>
</template>
