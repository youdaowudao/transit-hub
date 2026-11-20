<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Loader2, RefreshCw } from 'lucide-vue-next'
import type { EChartsCoreOption } from 'echarts/core'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { t } from '@/locales'
import DashboardEChart from '../components/dashboard/DashboardEChart.vue'
import { useAdminAccounts } from '../composables/useAdminAccounts'
import { defaultSelectedDates, formatLastUsedAt } from '../utils/userLastUsed'
import { excludeSalesEntries, getSalesEntries, getSalesSummary, restoreSalesEntries, saveSalesSettings } from '../api/sales'
import type { SalesChannel, SalesEntries, SalesEntry, SalesEntryKey, SalesEntryQuery, SalesRangeQuery, SalesScope, SalesSummary } from '../types/sales'

const channels: SalesChannel[] = ['payment', 'redeem', 'admin_add', 'admin_deduct', 'affiliate']
const { currentAccount, isSwitching, loadCurrentAccount } = useAdminAccounts()
const scope = computed<SalesScope | null>(() => currentAccount.value ? { workspaceAdminAccountId: currentAccount.value.id, workspaceCreatedAt: currentAccount.value.createdAt } : null)
const summary = ref<SalesSummary | null>(null)
const entries = ref<SalesEntries | null>(null)
const loading = ref(false)
const writing = ref(false)
const error = ref('')
const notice = ref('')
const needsRefresh = ref(false)
const period = ref('30')
const from = ref('')
const to = ref('')
const yesterday = defaultSelectedDates()[1] ?? ''
const query = ref<SalesEntryQuery>({ page: 1, pageSize: 20, channel: '', q: '', excluded: '0' })
const draftChannels = ref<SalesChannel[]>([])
const selected = ref<SalesEntryKey[]>([])
let epoch = 0
let disposed = false
let readController: AbortController | null = null
let writeController: AbortController | null = null
let loadedQuery: SalesEntryQuery = { ...query.value }
const current = (generation: number) => !disposed && generation === epoch
const aborted = (cause: unknown) => cause instanceof Error && cause.name === 'AbortError'
const reason = (cause: unknown) => cause instanceof Error ? cause.message : t('admin.sales.readError')
const number = (value: number | null | undefined) => value == null ? t('admin.sales.unknown') : value.toLocaleString('zh-CN', { maximumFractionDigits: 6 })
const percentage = (value: number | null | undefined) => value == null || !Number.isFinite(value * 100) ? t('admin.sales.unknown') : `${(value * 100).toFixed(1)}%`
const keyOf = (key: SalesEntryKey) => JSON.stringify([key.siteUserId, key.day, key.channel])
const keyOnly = (item: SalesEntry): SalesEntryKey => ({ siteUserId: item.siteUserId, day: item.day, channel: item.channel })
const selectedSet = computed(() => new Set(selected.value.map(keyOf)))
const selectableRows = computed(() => entries.value?.items.filter(item => item.rawAmount > 0 || item.hasExclusion) ?? [])
const allSelected = computed(() => selectableRows.value.length > 0 && selectableRows.value.every(item => selectedSet.value.has(keyOf(item))))
const canRestore = computed(() => selected.value.length > 0 && selected.value.length <= 100 && !loading.value && !writing.value && !needsRefresh.value)
const canExclude = computed(() => canRestore.value && (entries.value?.items.filter(item => selectedSet.value.has(keyOf(item))).every(item => item.rawAmount > 0) ?? false))
const settingsDirty = computed(() => summary.value && JSON.stringify([...draftChannels.value].sort()) !== JSON.stringify([...summary.value.settings.excludedChannels].sort()))
const includedChannels = computed(() => channels.filter(channel => !summary.value?.settings.excludedChannels.includes(channel)).map(channel => t(`admin.sales.channel.${channel}`)).join('、') || t('admin.sales.none'))
const channelDraftHelp = computed(() => draftChannels.value.map(channel => t(`admin.sales.channel.${channel}`)).join('、') || t('admin.sales.none'))
const delta = computed(() => {
  const previous = summary.value?.previous?.credits
  const now = summary.value?.totals.credits
  return previous != null && previous > 0 && now != null ? percentage(now / previous - 1) : t('admin.sales.unknown')
})
const chart = computed<EChartsCoreOption>(() => ({
  tooltip: { trigger: 'axis' }, grid: { left: 65, right: 20, top: 30, bottom: 55 }, legend: { data: [t('admin.sales.credits'), t('admin.sales.deductions')] },
  xAxis: { type: 'category', data: summary.value?.daily.map(item => item.day) ?? [] }, yAxis: { type: 'value' },
  dataZoom: [{ type: 'inside' }], series: [
    { name: t('admin.sales.credits'), type: 'line', connectNulls: false, showSymbol: false, data: summary.value?.daily.map(item => item.credits) ?? [] },
    { name: t('admin.sales.deductions'), type: 'line', connectNulls: false, showSymbol: false, data: summary.value?.daily.map(item => item.deductions) ?? [] },
  ],
}))
const reset = () => {
  epoch++; readController?.abort(); writeController?.abort()
  summary.value = null; entries.value = null; selected.value = []; draftChannels.value = []
  loading.value = false; writing.value = false; error.value = ''; notice.value = ''; needsRefresh.value = false
  period.value = '30'; from.value = ''; to.value = ''
  query.value = { page: 1, pageSize: 20, channel: '', q: '', excluded: '0' }; loadedQuery = { ...query.value }
}
const read = async (range: SalesRangeQuery, filters: SalesEntryQuery, updateSettings = true) => {
  if (!scope.value || isSwitching.value) return false
  const generation = epoch; const captured = { ...scope.value }
  readController?.abort(); const controller = new AbortController(); readController = controller
  loading.value = true; error.value = ''
  try {
    const [overview, list] = await Promise.all([getSalesSummary(captured, range, controller.signal), getSalesEntries(captured, range, filters, controller.signal)])
    if (!current(generation) || controller.signal.aborted || readController !== controller) return false
    if (overview.range.from !== list.range.from || overview.range.to !== list.range.to) throw new Error(t('admin.sales.dateChanged'))
    summary.value = overview; entries.value = list; loadedQuery = { ...filters, page: list.page }; query.value.page = list.page
    selected.value = []; needsRefresh.value = false
    if (updateSettings) draftChannels.value = [...overview.settings.excludedChannels]
    return true
  } catch (cause) {
    if (current(generation) && readController === controller && !controller.signal.aborted && !aborted(cause)) error.value = reason(cause)
    return false
  } finally {
    if (current(generation) && readController === controller) loading.value = false
  }
}
const chosenRange = (): SalesRangeQuery | null => {
  if (period.value !== 'custom') return { days: Number(period.value) }
  if (!from.value || !to.value) { error.value = t('admin.sales.rangeRequired'); return null }
  return { from: from.value, to: to.value }
}
const load = () => {
  if (writing.value || loading.value) return
  const range = chosenRange()
  if (!range) return
  notice.value = ''
  void read(range, { ...query.value, page: 1 })
}
const reloadLoaded = async (updateSettings: boolean) => {
  if (!summary.value) return false
  return read({ from: summary.value.range.from, to: summary.value.range.to }, { ...loadedQuery }, updateSettings)
}
const page = (offset: number) => {
  if (loading.value || writing.value || !summary.value) return
  void read({ from: summary.value.range.from, to: summary.value.range.to }, { ...loadedQuery, page: loadedQuery.page + offset }, false)
}
const toggle = (row: SalesEntry, checked: boolean) => {
  if ((row.rawAmount <= 0 && !row.hasExclusion) || writing.value || loading.value || needsRefresh.value) return
  const key = keyOf(row)
  selected.value = selected.value.filter(item => keyOf(item) !== key)
  if (checked) selected.value.push(keyOnly(row))
}
const togglePage = (checked: boolean) => {
  if (writing.value || loading.value || needsRefresh.value) return
  selected.value = checked ? selectableRows.value.map(keyOnly) : []
}
const changeExclusions = async (restore: boolean) => {
  if (!(restore ? canRestore.value : canExclude.value) || !scope.value || isSwitching.value) return
  const generation = epoch; const captured = { ...scope.value }; const chosen = selected.value.map(item => ({ ...item }))
  const controller = new AbortController(); writeController = controller
  writing.value = true; error.value = ''; notice.value = ''
  try {
    const result = await (restore ? restoreSalesEntries : excludeSalesEntries)(captured, chosen, controller.signal)
    if (!current(generation) || controller.signal.aborted || writeController !== controller) return
    selected.value = []; needsRefresh.value = true
    notice.value = t('admin.sales.changed', { requested: result.requested, changed: result.changed, unchanged: result.unchanged, from: result.range.from, to: result.range.to })
    // The POST range describes only the selected dates. Re-read the original page range.
    await reloadLoaded(false)
  } catch (cause) {
    if (current(generation) && writeController === controller && !controller.signal.aborted && !aborted(cause)) error.value = reason(cause)
  } finally {
    if (current(generation) && writeController === controller) writing.value = false
  }
}
const saveSettings = async () => {
  if (!scope.value || isSwitching.value || writing.value || loading.value || needsRefresh.value) return
  const generation = epoch; const captured = { ...scope.value }; const values = [...draftChannels.value]
  const controller = new AbortController(); writeController = controller
  writing.value = true; error.value = ''; notice.value = ''
  try {
    const result = await saveSalesSettings(captured, { excludedChannels: values }, controller.signal)
    if (!current(generation) || controller.signal.aborted || writeController !== controller) return
    draftChannels.value = [...result.settings.excludedChannels]; selected.value = []; needsRefresh.value = true
    notice.value = t('admin.sales.settingsSaved')
    await reloadLoaded(true)
  } catch (cause) {
    if (current(generation) && writeController === controller && !controller.signal.aborted && !aborted(cause)) error.value = reason(cause)
  } finally {
    if (current(generation) && writeController === controller) writing.value = false
  }
}
watch(() => [scope.value?.workspaceAdminAccountId, scope.value?.workspaceCreatedAt, isSwitching.value], () => {
  reset()
  if (scope.value && !isSwitching.value) void read({ days: 30 }, { ...query.value })
}, { immediate: true })
onMounted(() => { if (!currentAccount.value) void loadCurrentAccount() })
onBeforeUnmount(() => { disposed = true; reset() })
</script>

<template>
  <div class="mx-auto max-w-7xl space-y-6 pb-12">
    <header class="flex flex-wrap items-center justify-between gap-4">
      <div>
        <h1 class="text-2xl font-semibold text-foreground">{{ t('admin.sales.title') }}</h1>
        <p class="mt-2 max-w-3xl text-sm text-muted-foreground">{{ summary ? t('admin.sales.intro', { channels: includedChannels }) : t('admin.sales.introPending') }}</p>
      </div>
      <Button variant="outline" :disabled="!scope || loading || writing || isSwitching" @click="load">
        <Loader2 v-if="loading" class="mr-2 h-4 w-4 animate-spin" />
        <RefreshCw v-else class="mr-2 h-4 w-4" />{{ t('admin.sales.refresh') }}</Button>
    </header>
    <p v-if="!scope" class="rounded-lg border border-border p-4 text-sm">{{ t('admin.sales.noWorkspace') }}</p>
    <p v-if="error" role="alert" class="rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive">{{ error }}</p>
    <p v-if="notice" role="status" class="rounded-lg border border-border p-4 text-sm">{{ notice }}</p>
    <p v-if="needsRefresh" role="status" class="rounded-lg border border-amber-500/30 p-4 text-sm text-amber-700 dark:text-amber-300">{{ t('admin.sales.savedNeedsRefresh') }}</p>
    <section class="space-y-3 rounded-xl border border-border/50 bg-card p-5">
      <h2 class="font-semibold">{{ t('admin.sales.rangeTitle') }}</h2>
      <p class="text-sm text-muted-foreground">{{ t('admin.sales.rangeHelp') }}</p>
      <form class="flex flex-wrap items-end gap-3" @submit.prevent="load">
        <label class="text-sm">{{ t('admin.sales.period') }}<select v-model="period" :disabled="loading || writing" class="mt-2 block h-10 rounded-md border border-border bg-background px-3">
            <option value="7">{{ t('admin.sales.seven') }}</option>
            <option value="30">{{ t('admin.sales.thirty') }}</option>
            <option value="90">{{ t('admin.sales.ninety') }}</option>
            <option value="custom">{{ t('admin.sales.custom') }}</option>
          </select>
        </label>
        <template v-if="period === 'custom'">
          <label class="text-sm">{{ t('admin.sales.from') }}<Input v-model="from" type="date" :max="yesterday" :disabled="loading || writing" class="mt-2" />
          </label>
          <label class="text-sm">{{ t('admin.sales.to') }}<Input v-model="to" type="date" :max="yesterday" :disabled="loading || writing" class="mt-2" />
          </label>
        </template>
        <Button type="submit" :disabled="!scope || loading || writing">{{ t('admin.sales.apply') }}</Button>
      </form>
    </section>
    <p v-if="loading && !summary" role="status" class="text-sm text-muted-foreground">{{ t('admin.sales.loading') }}</p>
    <template v-if="summary">
      <section class="space-y-2 rounded-xl border border-border/50 bg-card p-5 text-sm">
        <h2 class="font-semibold">{{ summary.range.from }} — {{ summary.range.to }} · {{ summary.range.days }}{{ t('admin.sales.dayUnit') }}</h2>
        <p>{{ t('admin.sales.lastSynced') }}{{ summary.coverage.lastSyncedAt ? formatLastUsedAt(summary.coverage.lastSyncedAt) : t('admin.sales.unknown') }} · Asia/Singapore</p>
        <p v-if="summary.coverage.complete">{{ t('admin.sales.complete') }}</p>
        <p v-else class="text-amber-700 dark:text-amber-300">{{ t('admin.sales.partial', { from: summary.coverage.from || t('admin.sales.unknown'), to: summary.coverage.to || t('admin.sales.unknown') }) }}</p>
        <p v-if="!summary.coverage.hasKnownData" class="text-amber-700 dark:text-amber-300">{{ t('admin.sales.noData') }}</p>
        <p class="text-muted-foreground">{{ t('admin.sales.partialHelp') }} <RouterLink to="/admin/user-activity" class="underline">{{ t('admin.sales.openActivity') }}</RouterLink>
        </p>
        <ul v-if="summary.coverage.warnings.length" class="space-y-2 text-muted-foreground">
          <li v-for="warning in summary.coverage.warnings" :key="warning">{{ warning }}</li>
        </ul>
      </section>
      <section class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <div v-for="metric in [{ key: 'credits', value: summary.totals.credits }, { key: 'deductions', value: summary.totals.deductions }, { key: 'payers', value: summary.totals.payers }, { key: 'average', value: summary.totals.averagePerPayer }]" :key="metric.key" class="rounded-xl border border-border/50 bg-card p-5">
          <p class="text-sm text-muted-foreground">{{ t(`admin.sales.${metric.key}`) }}</p>
          <p class="mt-2 text-2xl font-semibold tabular-nums">{{ number(metric.value) }}</p>
        </div>
      </section>
      <section class="space-y-3 rounded-xl border border-border/50 bg-card p-5 text-sm">
        <h2 class="font-semibold">{{ t('admin.sales.customers') }}</h2>
        <p class="text-muted-foreground">{{ t('admin.sales.customerHelp') }}</p>
        <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <p>{{ t('admin.sales.repeat') }} {{ number(summary.totals.repeatPayers) }}</p>
          <p>{{ t('admin.sales.firstRecorded') }} {{ number(summary.totals.newPayers) }}</p>
          <p>{{ t('admin.sales.registered') }} {{ number(summary.totals.registered) }}</p>
          <p>{{ t('admin.sales.registeredPaid') }} {{ number(summary.totals.registeredPaid) }} · {{ percentage(summary.totals.conversion) }}</p>
        </div>
        <p>{{ t('admin.sales.previous', { from: summary.previousRange.from, to: summary.previousRange.to }) }} {{ number(summary.previous?.credits) }} · {{ t('admin.sales.change') }} {{ delta }}</p>
      </section>
      <section class="rounded-xl border border-border/50 bg-card p-5">
        <h2 class="font-semibold">{{ t('admin.sales.trend') }}</h2>
        <p class="mt-2 text-sm text-muted-foreground">{{ t('admin.sales.trendHelp') }}</p>
        <DashboardEChart :option="chart" :accessible-label="t('admin.sales.trend')" />
      </section>
      <section class="overflow-hidden rounded-xl border border-border/50 bg-card">
        <div class="p-5">
          <h2 class="font-semibold">{{ t('admin.sales.channels') }}</h2>
          <p class="mt-2 text-sm text-muted-foreground">{{ t('admin.sales.channelHelp') }}</p>
        </div>
        <div class="overflow-x-auto">
          <table class="w-full text-left text-sm">
            <thead class="border-y border-border/50 bg-muted/30 text-muted-foreground">
              <tr>
                <th class="px-4 py-3">{{ t('admin.sales.channelLabel') }}</th>
                <th class="px-4 py-3">{{ t('admin.sales.raw') }}</th>
                <th class="px-4 py-3">{{ t('admin.sales.excludedAmount') }}</th>
                <th class="px-4 py-3">{{ t('admin.sales.effective') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border/50">
              <tr v-for="item in summary.channels" :key="item.channel">
                <td class="px-4 py-3">{{ t(`admin.sales.channel.${item.channel}`) }}<span v-if="item.channelExcluded" class="ml-2 text-muted-foreground">{{ t('admin.sales.wholeChannel') }}</span>
                </td>
                <td class="px-4 py-3 tabular-nums">{{ number(item.rawAmount) }}</td>
                <td class="px-4 py-3 tabular-nums">{{ number(item.excludedAmount) }}</td>
                <td class="px-4 py-3 tabular-nums">{{ number(item.effectiveAmount) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
      <section class="overflow-hidden rounded-xl border border-border/50 bg-card">
        <div class="space-y-2 p-5">
          <h2 class="font-semibold">{{ t('admin.sales.topPayers') }}</h2>
          <p class="text-sm text-muted-foreground">{{ t('admin.sales.concentration') }} {{ t('admin.sales.top1') }} {{ percentage(summary.concentration.top1Share) }} · {{ t('admin.sales.top10') }} {{ percentage(summary.concentration.top10Share) }}</p>
          <p class="text-xs text-muted-foreground">{{ t('admin.sales.rankingHelp') }}</p>
        </div>
        <div class="overflow-x-auto">
          <table class="w-full text-left text-sm">
            <thead class="border-y border-border/50 bg-muted/30 text-muted-foreground">
              <tr>
                <th class="px-4 py-3">{{ t('admin.sales.customer') }}</th>
                <th class="px-4 py-3">{{ t('admin.sales.credits') }}</th>
                <th class="px-4 py-3">{{ t('admin.sales.paidDays') }}</th>
                <th class="px-4 py-3">{{ t('admin.sales.lastDay') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border/50">
              <tr v-for="item in summary.topPayers" :key="item.siteUserId">
                <td class="break-all px-4 py-3">{{ item.email || item.username || item.siteUserId }}<p class="text-xs text-muted-foreground">{{ item.siteUserId }}</p>
                </td>
                <td class="px-4 py-3 tabular-nums">{{ number(item.credits) }}</td>
                <td class="px-4 py-3">{{ item.paidDays }}</td>
                <td class="px-4 py-3">{{ item.lastDay }}</td>
              </tr>
              <tr v-if="!summary.topPayers.length">
                <td colspan="4" class="px-4 py-6 text-center text-muted-foreground">{{ t('admin.sales.noPayers') }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
      <section class="space-y-4 rounded-xl border border-border/50 bg-card p-5">
        <h2 class="font-semibold">{{ t('admin.sales.settings') }}</h2>
        <p class="text-sm text-muted-foreground">{{ t('admin.sales.exclusionHelp') }}</p>
        <p class="text-sm text-muted-foreground">{{ t('admin.sales.settingsHelp') }}</p>
        <fieldset :disabled="loading || writing || needsRefresh" class="space-y-4">
          <div class="flex flex-wrap gap-4">
            <label v-for="channel in channels" :key="channel" class="flex items-center gap-2 text-sm">
              <input v-model="draftChannels" :value="channel" type="checkbox" class="h-4 w-4 rounded border-border" />{{ t(`admin.sales.channel.${channel}`) }}</label>
          </div>
          <p class="text-sm">{{ t('admin.sales.draftExclusions') }} {{ channelDraftHelp }}</p>
          <p v-if="settingsDirty" class="text-sm text-amber-700 dark:text-amber-300">{{ t('admin.sales.unsaved') }}</p>
          <Button :disabled="loading || writing || needsRefresh" @click="saveSettings">{{ t('admin.sales.saveSettings') }}</Button>
        </fieldset>
        <p class="text-sm">{{ t('admin.sales.exclusions', { entries: summary.exclusions.entries }) }} {{ number(summary.exclusions.amount) }} · {{ t('admin.sales.excludedDeductions') }} {{ number(summary.exclusions.deductions) }}</p>
      </section>
      <section v-if="entries" class="overflow-hidden rounded-xl border border-border/50 bg-card">
        <div class="space-y-3 p-5">
          <h2 class="font-semibold">{{ t('admin.sales.entries') }} · {{ entries.total }}</h2>
          <p class="text-sm text-muted-foreground">{{ t('admin.sales.entryHelp') }}</p>
          <p class="text-sm text-muted-foreground">{{ t('admin.sales.exclusionHelp') }}</p>
          <form class="flex flex-wrap items-end gap-3" @submit.prevent="load">
            <label class="text-sm">{{ t('admin.sales.channelLabel') }}<select v-model="query.channel" :disabled="loading || writing" class="mt-2 block h-10 rounded-md border border-border bg-background px-3">
                <option value="">{{ t('admin.sales.allChannels') }}</option>
                <option v-for="channel in channels" :key="channel" :value="channel">{{ t(`admin.sales.channel.${channel}`) }}</option>
              </select>
            </label>
            <label class="text-sm">{{ t('admin.sales.search') }}<Input v-model="query.q" maxlength="100" :disabled="loading || writing" class="mt-2" :placeholder="t('admin.sales.searchPlaceholder')" />
            </label>
            <label class="flex h-10 items-center gap-2 text-sm">
              <input v-model="query.excluded" type="checkbox" true-value="1" false-value="0" :disabled="loading || writing" />{{ t('admin.sales.excludedOnly') }}</label>
            <label class="text-sm">{{ t('admin.sales.pageSize') }}<select v-model.number="query.pageSize" :disabled="loading || writing" class="mt-2 block h-10 rounded-md border border-border bg-background px-3">
                <option :value="20">20</option>
                <option :value="50">50</option>
                <option :value="100">100</option>
              </select>
            </label>
            <Button type="submit" :disabled="loading || writing || [...query.q].length > 100">{{ t('admin.sales.filter') }}</Button>
          </form>
          <div class="flex flex-wrap items-center gap-3">
            <span class="text-sm">{{ t('admin.sales.selected', { count: selected.length }) }}</span>
            <Button variant="outline" :disabled="!canExclude" @click="changeExclusions(false)">{{ t('admin.sales.exclude') }}</Button>
            <Button variant="outline" :disabled="!canRestore" @click="changeExclusions(true)">{{ t('admin.sales.restore') }}</Button>
          </div>
        </div>
        <div class="overflow-x-auto">
          <table class="w-full min-w-[950px] text-left text-sm">
            <thead class="border-y border-border/50 bg-muted/30 text-muted-foreground">
              <tr>
                <th class="px-4 py-3">
                  <input type="checkbox" :checked="allSelected" :disabled="loading || writing || needsRefresh || !selectableRows.length" :aria-label="t('admin.sales.selectPage')" @change="togglePage(($event.target as HTMLInputElement).checked)" />
                </th>
                <th class="px-4 py-3">{{ t('admin.sales.customer') }}</th>
                <th class="px-4 py-3">{{ t('admin.sales.day') }}</th>
                <th class="px-4 py-3">{{ t('admin.sales.channelLabel') }}</th>
                <th class="px-4 py-3">{{ t('admin.sales.raw') }}</th>
                <th class="px-4 py-3">{{ t('admin.sales.excludedAmount') }}</th>
                <th class="px-4 py-3">{{ t('admin.sales.effective') }}</th>
                <th class="px-4 py-3">{{ t('admin.sales.events') }}</th>
                <th class="px-4 py-3">{{ t('admin.sales.status') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border/50">
              <tr v-for="item in entries.items" :key="keyOf(item)">
                <td class="px-4 py-3">
                  <input type="checkbox" :checked="selectedSet.has(keyOf(item))" :disabled="(item.rawAmount <= 0 && !item.hasExclusion) || loading || writing || needsRefresh" :aria-label="t('admin.sales.selectRow', { id: item.siteUserId, day: item.day })" @change="toggle(item, ($event.target as HTMLInputElement).checked)" />
                </td>
                <td class="break-all px-4 py-3">{{ item.email || item.username || item.siteUserId }}<p class="text-xs text-muted-foreground">{{ item.siteUserId }}</p>
                </td>
                <td class="whitespace-nowrap px-4 py-3">{{ item.day }}</td>
                <td class="px-4 py-3">{{ t(`admin.sales.channel.${item.channel}`) }}</td>
                <td class="px-4 py-3 tabular-nums">{{ number(item.rawAmount) }}</td>
                <td class="px-4 py-3 tabular-nums">{{ number(item.excludedAmount) }}</td>
                <td class="px-4 py-3 tabular-nums">{{ number(item.effectiveAmount) }}</td>
                <td class="px-4 py-3">{{ item.events }}<p class="text-xs text-muted-foreground">{{ t('admin.sales.eventsExcluded') }} {{ item.excludedEvents }}</p>
                </td>
                <td class="px-4 py-3">{{ t(`admin.sales.entryStatus.${item.exclusionStatus}`) }}<p v-if="item.hasExclusion && item.rawAmount <= 0 && !item.channelExcluded" class="text-xs text-muted-foreground">{{ t('admin.sales.snapshotInactive') }}</p>
                  <p v-if="item.channelExcluded && item.hasExclusion" class="text-xs text-muted-foreground">{{ t('admin.sales.individualSaved') }}</p>
                </td>
              </tr>
              <tr v-if="!entries.items.length">
                <td colspan="9" class="px-4 py-8 text-center text-muted-foreground">{{ t('admin.sales.noEntries') }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="flex flex-wrap items-center justify-between gap-3 border-t border-border/50 p-4 text-sm">
          <span>{{ t('admin.sales.page', { page: entries.page, pages: Math.max(1, Math.ceil(entries.total / entries.pageSize)) }) }}</span>
          <div class="flex gap-2">
            <Button variant="outline" size="sm" :disabled="loading || writing || entries.page <= 1" @click="page(-1)">{{ t('admin.sales.previousPage') }}</Button>
            <Button variant="outline" size="sm" :disabled="loading || writing || entries.page * entries.pageSize >= entries.total" @click="page(1)">{{ t('admin.sales.nextPage') }}</Button>
          </div>
        </div>
      </section>
    </template>
  </div>
</template>
