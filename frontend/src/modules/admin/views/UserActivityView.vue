<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Copy, Loader2, RefreshCw, X } from 'lucide-vue-next'
import type { EChartsCoreOption } from 'echarts/core'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { t } from '@/locales'
import DashboardEChart from '../components/dashboard/DashboardEChart.vue'
import { useAdminAccounts } from '../composables/useAdminAccounts'
import { getActivityDetail, getActivitySummary, getActivitySync, getActivityUsers, startActivitySync } from '../api/userActivity'
import type { ActivityDay, ActivityDetail, ActivityPeriod, ActivityScope, ActivitySummary, ActivitySync, ActivityUserList, ActivityUserQuery } from '../types/userActivity'
const { currentAccount, isSwitching, loadCurrentAccount } = useAdminAccounts()
const scope = computed<ActivityScope | null>(() => currentAccount.value ? { workspaceAdminAccountId: currentAccount.value.id, workspaceCreatedAt: currentAccount.value.createdAt } : null)
const summary = ref<ActivitySummary | null>(null)
const list = ref<ActivityUserList | null>(null)
const sync = ref<ActivitySync | null>(null)
const detail = ref<ActivityDetail | null>(null)
const loading = ref(false)
const starting = ref(false)
const detailLoading = ref(false)
const error = ref('')
const detailError = ref('')
const copied = ref('')
const days = ref(60)
const query = ref<ActivityUserQuery>({ page: 1, pageSize: 20, segment: 'all', inactiveDays: 30, search: '', role: '', status: '', sort: 'lastUsedAt', order: 'desc' })
let epoch = 0
let detailEpoch = 0
let loadController: AbortController | null = null
let pollController: AbortController | null = null
let detailController: AbortController | null = null
let startController: AbortController | null = null
let pollTimer: ReturnType<typeof setTimeout> | null = null
let disposed = false
const running = computed(() => sync.value?.status === 'running')
const number = (value: number | null | undefined) => value == null ? t('admin.userActivity.unknown') : value.toLocaleString('zh-CN', { maximumFractionDigits: 6 })
const date = (value?: string | null) => value ? new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Singapore', dateStyle: 'medium', timeStyle: 'medium' }).format(new Date(value)) : t('admin.userActivity.unknown')
const reason = (cause: unknown) => cause instanceof Error ? cause.message : t('admin.userActivity.readError')
const aborted = (cause: unknown) => cause instanceof Error && cause.name === 'AbortError'
const current = (generation: number) => !disposed && generation === epoch
const stopPoll = () => { if (pollTimer) clearTimeout(pollTimer); pollTimer = null; pollController?.abort(); pollController = null }
const reset = () => {
  epoch++; detailEpoch++; stopPoll(); loadController?.abort(); detailController?.abort(); startController?.abort()
  summary.value = null; list.value = null; sync.value = null; detail.value = null
  error.value = ''; detailError.value = ''; copied.value = ''; loading.value = false; starting.value = false; detailLoading.value = false
}
const schedulePoll = () => {
  stopPoll()
  if (!running.value || !scope.value || disposed) return
  const generation = epoch
  const captured = { ...scope.value }
  pollTimer = setTimeout(async () => {
    pollTimer = null
    const controller = new AbortController(); pollController = controller
    try {
      const state = await getActivitySync(captured, controller.signal)
      if (!current(generation) || controller.signal.aborted || pollController !== controller) return
      sync.value = state
      if (state.status === 'running') schedulePoll()
      else await load()
    } catch (cause) {
      if (current(generation) && pollController === controller && !controller.signal.aborted && !aborted(cause)) { error.value = reason(cause); schedulePoll() }
    }
  }, 5000)
}
const load = async () => {
  if (!scope.value || isSwitching.value || starting.value) return
  const generation = epoch; const captured = { ...scope.value }
  loadController?.abort(); const controller = new AbortController(); loadController = controller
  loading.value = true; error.value = ''
  try {
    const [overview, users] = await Promise.all([getActivitySummary(captured, days.value, controller.signal), getActivityUsers(captured, { ...query.value }, controller.signal)])
    if (!current(generation) || controller.signal.aborted || loadController !== controller) return
    summary.value = overview; list.value = users; sync.value = overview.sync; schedulePoll()
  } catch (cause) { if (current(generation) && loadController === controller && !controller.signal.aborted && !aborted(cause)) error.value = reason(cause) }
  finally { if (current(generation) && loadController === controller) loading.value = false }
}
const start = async () => {
  if (!scope.value || running.value || starting.value || loading.value || isSwitching.value) return
  const generation = epoch; const captured = { ...scope.value }; const controller = new AbortController(); startController = controller
  starting.value = true; error.value = ''
  try { const state = await startActivitySync(captured, controller.signal); if (current(generation)) { sync.value = state; schedulePoll() } }
  catch (cause) { if (current(generation) && !aborted(cause)) error.value = reason(cause) }
  finally { if (current(generation)) starting.value = false }
}
const apply = () => { query.value.page = 1; void load() }
const page = (offset: number) => { query.value.page += offset; void load() }
const openDetail = async (id: string) => {
  if (!scope.value) return
  const generation = epoch; const request = ++detailEpoch; const captured = { ...scope.value }
  detailController?.abort(); const controller = new AbortController(); detailController = controller
  detail.value = null; detailLoading.value = true; detailError.value = ''
  try { const result = await getActivityDetail(captured, id, controller.signal); if (current(generation) && request === detailEpoch) detail.value = result }
  catch (cause) { if (current(generation) && request === detailEpoch && !aborted(cause)) detailError.value = reason(cause) }
  finally { if (current(generation) && request === detailEpoch) detailLoading.value = false }
}
const closeDetail = () => { detailEpoch++; detailController?.abort(); detail.value = null; detailError.value = ''; detailLoading.value = false }
const copy = async (email: string) => { const generation = epoch; try { await navigator.clipboard.writeText(email); if (current(generation)) copied.value = email } catch { if (current(generation)) error.value = t('admin.userActivity.copyError') } }
const partial = (point: ActivityDay) => !point.temporary && (!summary.value?.coverage.usageFrom || point.day < summary.value.coverage.usageFrom || point.day > summary.value.coverage.usageTo)
const delta = (period: ActivityPeriod) => period.current && period.previous && period.previous.cost > 0 ? `${((period.current.cost / period.previous.cost - 1) * 100).toFixed(1)}%` : t('admin.userActivity.unknown')
const chart = computed<EChartsCoreOption>(() => ({
  tooltip: { trigger: 'axis' }, grid: { left: 55, right: 20, top: 20, bottom: 55 },
  xAxis: { type: 'category', data: summary.value?.daily.map(point => point.day) ?? [] }, yAxis: { type: 'value' },
  dataZoom: [{ type: 'inside' }], series: [{ name: t('admin.userActivity.cost'), type: 'line', connectNulls: false, data: summary.value?.daily.map(point => point.cost) ?? [], showSymbol: false }],
}))
watch(() => [scope.value?.workspaceAdminAccountId, scope.value?.workspaceCreatedAt, isSwitching.value], () => { reset(); query.value.page = 1; if (scope.value && !isSwitching.value) void load() }, { immediate: true })
onMounted(() => { if (!currentAccount.value) void loadCurrentAccount() })
onBeforeUnmount(() => { disposed = true; reset() })
</script>

<template>
  <div class="mx-auto max-w-7xl space-y-6 pb-12">
    <header class="flex flex-wrap items-center justify-between gap-4">
      <div>
        <h1 class="text-2xl font-semibold text-foreground">{{ t('admin.userActivity.title') }}</h1>
        <p class="mt-2 max-w-3xl text-sm text-muted-foreground">{{ t('admin.userActivity.intro') }}</p>
      </div>
      <div class="flex gap-2">
        <Button variant="outline" :disabled="!scope || loading || starting || isSwitching" @click="load">
          <RefreshCw class="mr-2 h-4 w-4" />{{ t('admin.userActivity.refresh') }}</Button>
        <Button :disabled="!scope || isSwitching || loading || starting || running" @click="start">
          <Loader2 v-if="starting || running" class="mr-2 h-4 w-4 animate-spin" />{{ t('admin.userActivity.start') }}</Button>
      </div>
    </header>
    <p v-if="!scope" class="rounded-lg border border-border p-4 text-sm">{{ t('admin.userActivity.noWorkspace') }}</p>
    <p v-if="error" role="alert" class="rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive">{{ error }}</p>
    <p v-if="running" role="status" class="rounded-lg border border-border bg-card p-4 text-sm">{{ t('admin.userActivity.running') }}</p>
    <p v-else-if="sync?.status === 'failed'" role="alert" class="rounded-lg border border-destructive/30 p-4 text-sm text-destructive">{{ sync.error }}</p>
    <p v-else-if="sync?.status === 'success'" class="text-sm text-muted-foreground">{{ t('admin.userActivity.lastSuccess') }}{{ date(sync.lastSyncedAt) }}</p>
    <p v-if="loading && !summary" role="status" class="text-sm text-muted-foreground">{{ t('admin.userActivity.loading') }}</p>
    <template v-if="summary">
      <section class="rounded-xl border border-border/50 bg-card p-5 text-sm">
        <h2 class="font-semibold">{{ t('admin.userActivity.coverage') }}</h2>
        <p class="mt-2">{{ t('admin.userActivity.usageRange') }}{{ summary.coverage.usageFrom || t('admin.userActivity.unknown') }} — {{ summary.coverage.usageTo || t('admin.userActivity.unknown') }}</p>
        <p>{{ t('admin.userActivity.rechargeRange') }}{{ summary.coverage.rechargeFrom || t('admin.userActivity.unknown') }} — {{ summary.coverage.rechargeTo || t('admin.userActivity.unknown') }}</p>
        <p class="mt-2 text-muted-foreground">{{ summary.coverage.usageFrom ? t('admin.userActivity.incomplete', { from: summary.coverage.usageFrom, to: summary.coverage.usageTo }) : t('admin.userActivity.noCoverage') }}</p>
        <p class="mt-1 text-muted-foreground">{{ t('admin.userActivity.partialHelp') }}</p>
        <ul v-if="summary.warnings.length" class="mt-3 space-y-2 text-amber-700 dark:text-amber-300">
          <li v-for="warning in summary.warnings" :key="warning">{{ warning }}</li>
        </ul>
      </section>
      <section class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <div class="rounded-xl border border-border/50 bg-card p-5">
          <h2 class="text-sm text-muted-foreground">{{ t('admin.userActivity.users') }}</h2>
          <p class="mt-2 text-2xl font-semibold">{{ summary.money.observedAt ? summary.sync.userCount : t('admin.userActivity.unknown') }}</p>
          <p class="mt-2 text-xs text-muted-foreground">{{ t('admin.userActivity.snapshotHelp') }}</p>
        </div>
        <div class="rounded-xl border border-border/50 bg-card p-5">
          <h2 class="text-sm text-muted-foreground">{{ t('admin.userActivity.totalBalance') }}</h2>
          <p class="mt-2 text-2xl font-semibold">{{ number(summary.money.totalBalance) }}</p>
          <p class="mt-2 text-xs text-muted-foreground">{{ date(summary.money.observedAt) }}</p>
        </div>
        <div class="rounded-xl border border-border/50 bg-card p-5">
          <h2 class="text-sm text-muted-foreground">{{ t('admin.userActivity.todayCost') }}</h2>
          <p class="mt-2 text-2xl font-semibold">{{ number(summary.today.cost) }}</p>
          <p class="mt-2 text-xs text-muted-foreground">{{ t('admin.userActivity.temporary') }} · {{ date(summary.today.observedAt) }}</p>
        </div>
        <div class="rounded-xl border border-border/50 bg-card p-5">
          <h2 class="text-sm text-muted-foreground">{{ t('admin.userActivity.todayRecharge') }}</h2>
          <p class="mt-2 text-2xl font-semibold">{{ number(summary.money.todayRecharge) }}</p>
          <p class="mt-2 text-xs text-muted-foreground">{{ t('admin.userActivity.deduction') }}{{ number(summary.money.todayDeduction) }}</p>
        </div>
      </section>
      <section class="grid gap-4 md:grid-cols-2">
        <article v-for="(period, name) in summary.periods" :key="name" class="rounded-xl border border-border/50 bg-card p-5">
          <h2 class="font-semibold">{{ t('admin.userActivity.period', { days: period.days }) }}</h2>
          <p class="mt-2 text-xs text-muted-foreground">{{ t('admin.userActivity.periodHelp') }}</p>
          <dl class="mt-4 grid grid-cols-2 gap-3 text-sm">
            <div>
              <dt>{{ t('admin.userActivity.cost') }}</dt>
              <dd class="mt-1 font-semibold">{{ number(period.current?.cost) }}</dd>
            </div>
            <div>
              <dt>{{ t('admin.userActivity.change') }}</dt>
              <dd class="mt-1 font-semibold">{{ delta(period) }}</dd>
            </div>
            <div>
              <dt>{{ t('admin.userActivity.requests') }}</dt>
              <dd>{{ number(period.current?.requests) }}</dd>
            </div>
            <div>
              <dt>{{ t('admin.userActivity.activeUsers') }}</dt>
              <dd>{{ number(period.current?.activeUsers) }}</dd>
            </div>
            <div>
              <dt>{{ t('admin.userActivity.retained') }}</dt>
              <dd>{{ number(period.retention?.retained) }}</dd>
            </div>
            <div>
              <dt>{{ t('admin.userActivity.churned') }}</dt>
              <dd>{{ number(period.retention?.churned) }}</dd>
            </div>
          </dl>
        </article>
      </section>
      <section class="rounded-xl border border-border/50 bg-card p-5">
        <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
          <h2 class="font-semibold">{{ t('admin.userActivity.daily') }}</h2>
          <label class="flex items-center gap-2 text-sm">{{ t('admin.userActivity.days') }}<Input v-model.number="days" type="number" min="1" max="400" class="w-24" />
            <Button variant="outline" :disabled="loading || days < 1 || days > 400" @click="load">{{ t('admin.userActivity.readLocal') }}</Button>
          </label>
        </div>
        <div class="h-64">
          <DashboardEChart :option="chart" :accessible-label="t('admin.userActivity.daily')" />
        </div>
        <details class="mt-4">
          <summary class="cursor-pointer text-sm">{{ t('admin.userActivity.dailyDetails') }}</summary>
          <div class="mt-3 max-h-80 overflow-auto">
            <table class="w-full text-sm">
              <thead class="text-left text-muted-foreground">
                <tr>
                  <th class="py-2">{{ t('admin.userActivity.day') }}</th>
                  <th>{{ t('admin.userActivity.requests') }}</th>
                  <th>{{ t('admin.userActivity.cost') }}</th>
                  <th>{{ t('admin.userActivity.recharge') }}</th>
                  <th>{{ t('admin.userActivity.dataStatus') }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-border/50">
                <tr v-for="point in summary.daily" :key="point.day">
                  <td class="py-2">{{ point.day }}</td>
                  <td>{{ number(point.requests) }}</td>
                  <td>{{ number(point.cost) }}</td>
                  <td>{{ number(point.recharge) }}</td>
                  <td>{{ point.temporary ? t('admin.userActivity.temporary') : partial(point) ? t('admin.userActivity.partial') : t('admin.userActivity.verified') }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </details>
      </section>
      <section class="rounded-xl border border-border/50 bg-card p-5">
        <h2 class="font-semibold">{{ t('admin.userActivity.channels') }}</h2>
        <p class="mt-2 text-sm text-muted-foreground">{{ t('admin.userActivity.moneyHelp') }}</p>
        <div v-if="summary.money.channels.length" class="mt-3 flex flex-wrap gap-5 text-sm">
          <span v-for="channel in summary.money.channels" :key="channel.channel">{{ t(`admin.userActivity.channel.${channel.channel}`) }}：{{ number(channel.amount) }} · {{ channel.events }}</span>
        </div>
        <p v-else class="mt-3 text-sm text-muted-foreground">{{ t('admin.userActivity.noFundingRows') }}</p>
      </section>
    </template>
    <section class="overflow-hidden rounded-xl border border-border/50 bg-card">
      <form class="space-y-4 p-5" @submit.prevent="apply">
        <h2 class="font-semibold">{{ t('admin.userActivity.userList') }}</h2>
        <p class="text-sm text-muted-foreground">{{ t('admin.userActivity.listHelp') }}</p>
        <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <label class="text-sm">{{ t('admin.userActivity.search') }}<Input v-model="query.search" maxlength="100" class="mt-1" />
          </label>
          <label class="text-sm">{{ t('admin.userActivity.segment') }}<select v-model="query.segment" class="mt-1 h-10 w-full rounded-md border border-border bg-background px-3">
              <option v-for="segment in ['all','active_1d','active_7d','active_30d','inactive','churned_7d','declining','growing']" :key="segment" :value="segment">{{ t(`admin.userActivity.segmentName.${segment}`) }}</option>
            </select>
          </label>
          <label v-if="query.segment === 'inactive'" class="text-sm">{{ t('admin.userActivity.inactiveDays') }}<Input v-model.number="query.inactiveDays" type="number" min="1" max="3650" class="mt-1" />
          </label>
          <label class="text-sm">{{ t('admin.userActivity.role') }}<select v-model="query.role" class="mt-1 h-10 w-full rounded-md border border-border bg-background px-3">
              <option value="">{{ t('admin.userActivity.all') }}</option>
              <option value="user">{{ t('admin.userActivity.regularUser') }}</option>
              <option value="admin">{{ t('admin.userActivity.administrator') }}</option>
            </select>
          </label>
          <label class="text-sm">{{ t('admin.userActivity.status') }}<select v-model="query.status" class="mt-1 h-10 w-full rounded-md border border-border bg-background px-3">
              <option value="">{{ t('admin.userActivity.all') }}</option>
              <option value="active">{{ t('admin.userActivity.active') }}</option>
              <option value="disabled">{{ t('admin.userActivity.disabled') }}</option>
            </select>
          </label>
          <label class="text-sm">{{ t('admin.userActivity.sort') }}<select v-model="query.sort" class="mt-1 h-10 w-full rounded-md border border-border bg-background px-3">
              <option v-for="field in ['lastUsedAt','cost7d','cost30d','balance','createdAt']" :key="field" :value="field">{{ t(`admin.userActivity.sortName.${field}`) }}</option>
            </select>
          </label>
          <label class="text-sm">{{ t('admin.userActivity.order') }}<select v-model="query.order" class="mt-1 h-10 w-full rounded-md border border-border bg-background px-3">
              <option value="desc">{{ t('admin.userActivity.desc') }}</option>
              <option value="asc">{{ t('admin.userActivity.asc') }}</option>
            </select>
          </label>
          <label class="text-sm">{{ t('admin.userActivity.pageSize') }}<select v-model.number="query.pageSize" class="mt-1 h-10 w-full rounded-md border border-border bg-background px-3">
              <option v-for="size in [20,50,100]" :key="size" :value="size">{{ size }}</option>
            </select>
          </label>
          <div class="flex items-end">
            <Button type="submit" :disabled="!scope || loading || starting || isSwitching">{{ t('admin.userActivity.filter') }}</Button>
          </div>
        </div>
      </form>
      <div class="overflow-x-auto">
        <table class="w-full text-left text-sm">
          <thead class="border-y border-border/50 bg-muted/30 text-muted-foreground">
            <tr>
              <th class="px-4 py-3">{{ t('admin.userActivity.user') }}</th>
              <th class="px-4 py-3">{{ t('admin.userActivity.status') }}</th>
              <th class="px-4 py-3">{{ t('admin.userActivity.balance') }}</th>
              <th class="px-4 py-3">{{ t('admin.userActivity.lastUsed') }}</th>
              <th class="px-4 py-3">{{ t('admin.userActivity.cost7d') }}</th>
              <th class="px-4 py-3">{{ t('admin.userActivity.cost30d') }}</th>
              <th class="px-4 py-3">{{ t('admin.userActivity.actions') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border/50">
            <tr v-for="user in list?.items ?? []" :key="user.siteUserId">
              <td class="px-4 py-3">
                <div class="break-all font-medium">{{ user.email || user.username || user.siteUserId }}</div>
                <div class="text-xs text-muted-foreground">{{ user.siteUserId }} · {{ user.role }}</div>
              </td>
              <td class="px-4 py-3">{{ user.status === 'disabled' ? t('admin.userActivity.disabled') : user.status === 'active' ? t('admin.userActivity.active') : user.status }}</td>
              <td class="px-4 py-3">{{ number(user.balance) }}</td>
              <td class="px-4 py-3">{{ user.lastUsedAt ? date(user.lastUsedAt) : t('admin.userActivity.lastUsedUnknown') }}</td>
              <td class="px-4 py-3">{{ number(user.cost7d) }}</td>
              <td class="px-4 py-3">{{ number(user.cost30d) }}</td>
              <td class="px-4 py-3">
                <div class="flex gap-2">
                  <Button variant="outline" size="sm" @click="openDetail(user.siteUserId)">{{ t('admin.userActivity.detail') }}</Button>
                  <Button variant="ghost" size="sm" :disabled="!user.email" :aria-label="t('admin.userActivity.copy')" @click="copy(user.email)">
                    <Copy class="h-4 w-4" />
                  </Button>
                </div>
                <span v-if="copied === user.email && user.email" class="text-xs text-emerald-700">{{ t('admin.userActivity.copied') }}</span>
              </td>
            </tr>
            <tr v-if="list && !list.items.length">
              <td colspan="7" class="px-4 py-8 text-center text-muted-foreground">{{ t('admin.userActivity.emptyUsers') }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="flex items-center justify-between gap-3 p-4 text-sm">
        <span>{{ t('admin.userActivity.total', { total: list?.total ?? t('admin.userActivity.unknown'), page: query.page }) }}</span>
        <div class="flex gap-2">
          <Button variant="outline" :disabled="loading || query.page <= 1" @click="page(-1)">{{ t('admin.userActivity.previous') }}</Button>
          <Button variant="outline" :disabled="loading || !list || query.page * query.pageSize >= list.total" @click="page(1)">{{ t('admin.userActivity.next') }}</Button>
        </div>
      </div>
    </section>
    <section v-if="detail || detailLoading || detailError" class="rounded-xl border border-border/50 bg-card p-5">
      <div class="flex items-center justify-between">
        <h2 class="font-semibold">{{ t('admin.userActivity.detail') }}</h2>
        <Button variant="ghost" size="sm" :aria-label="t('admin.userActivity.close')" @click="closeDetail">
          <X class="h-4 w-4" />
        </Button>
      </div>
      <p class="mt-2 text-sm text-muted-foreground">{{ t('admin.userActivity.detailHelp') }}</p>
      <p v-if="detailLoading" role="status" class="mt-4">{{ t('admin.userActivity.loading') }}</p>
      <p v-if="detailError" role="alert" class="mt-4 text-destructive">{{ detailError }}</p>
      <template v-if="detail">
        <div class="mt-4 grid gap-3 text-sm sm:grid-cols-3">
          <p>{{ detail.profile.email || detail.profile.username || detail.profile.siteUserId }}</p>
          <p>{{ t('admin.userActivity.created') }}{{ date(detail.profile.createdAt) }}</p>
          <p>{{ t('admin.userActivity.balance') }}：{{ number(detail.profile.balance) }}</p>
          <p>{{ t('admin.userActivity.todayCost') }}：{{ number(detail.profile.costToday) }}</p>
          <p>{{ t('admin.userActivity.temporary') }}：{{ date(detail.profile.todayUsageObservedAt) }}</p>
          <p>{{ t('admin.userActivity.lastUsed') }}：{{ detail.profile.lastUsedAt ? date(detail.profile.lastUsedAt) : t('admin.userActivity.lastUsedUnknown') }}</p>
        </div>
        <div class="mt-4 grid gap-4 sm:grid-cols-2">
          <div v-for="(period,name) in detail.periods" :key="name" class="text-sm">
            <p class="font-medium">{{ t('admin.userActivity.period', { days: period.days }) }}</p>
            <p>{{ t('admin.userActivity.cost') }}：{{ number(period.current?.cost) }} · {{ t('admin.userActivity.change') }}：{{ delta(period) }}</p>
          </div>
        </div>
        <div class="mt-5 max-h-80 overflow-auto">
          <table class="w-full text-sm">
            <thead class="text-left text-muted-foreground">
              <tr>
                <th>{{ t('admin.userActivity.day') }}</th>
                <th>{{ t('admin.userActivity.requests') }}</th>
                <th>{{ t('admin.userActivity.cost') }}</th>
                <th>{{ t('admin.userActivity.recharge') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border/50">
              <tr v-for="point in detail.daily" :key="point.day">
                <td class="py-2">{{ point.day }}</td>
                <td>{{ number(point.requests) }}</td>
                <td>{{ number(point.cost) }}</td>
                <td>{{ number(point.recharge) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </section>
  </div>
</template>
