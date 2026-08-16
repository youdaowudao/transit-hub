<script setup lang="ts">
import { computed, onMounted, ref, watch, type Component } from 'vue'
import { useMediaQuery } from '@vueuse/core'
import { useRouter } from 'vue-router'
import type { EChartsCoreOption } from 'echarts/core'
import {
  Activity,
  AlertTriangle,
  ArrowRight,
  CircleCheckBig,
  ChevronDown,
  Clock3,
  Gauge,
  Landmark,
  Layers3,
  Loader2,
  Lock,
  PiggyBank,
  RefreshCw,
  ShieldCheck,
  ShoppingCart,
  TrendingUp,
  Wallet,
} from 'lucide-vue-next'
import AdminLoginModal from '../components/dashboard/AdminLoginModal.vue'
import BalanceFilterModal from '../components/dashboard/BalanceFilterModal.vue'
import DashboardEChart from '../components/dashboard/DashboardEChart.vue'
import GroupUsageTodayModal from '../components/dashboard/GroupUsageTodayModal.vue'
import StatCard from '../components/dashboard/StatCard.vue'
import UpstreamBalanceBreakdownModal from '../components/dashboard/UpstreamBalanceBreakdownModal.vue'
import UpstreamKeyUsageTodayModal from '../components/dashboard/UpstreamKeyUsageTodayModal.vue'
import DailyStatsPanel from '../components/dashboard/DailyStatsPanel.vue'
import {
  getDashboardMetrics,
  getDashboardTrends,
  getGroupProfitToday,
  getGroupUsageToday,
  getUpstreamBalanceBreakdown,
  type GroupUsageTodayResponse,
  type GroupProfitTodayResponse,
  type UpstreamBalanceBreakdownResponse,
} from '../api/dashboardAdmin'
import { getConnectionHealthStoredSummary } from '../api/connectionHealth'
import { useDashboardAdmin } from '../composables/useDashboardAdmin'
import { useDashboardChartTheme } from '../composables/useDashboardChartTheme'
import {
  getDashboardDataSnapshot,
  saveDashboardDataSnapshot,
  updateDashboardOperationalSnapshot,
  type DashboardDataSnapshot,
} from '../composables/useDashboardDataCache'
import { useDashboardMetrics } from '../composables/useDashboardMetrics'
import { useAdminAccounts } from '../composables/useAdminAccounts'
import type { ConnectionHealthStoredSummary } from '../types/connectionHealth'
import type { DashboardColorToken, DashboardMetricData, DashboardMetricKey, DashboardPeriod } from '../types/dashboard'
import type { DashboardAdminPlatform, Sub2apiAuthMethod } from '../types/dashboardAdmin'
import {
  buildProfitMarginSeries,
  calculateProfitMargin,
  computeDelta,
  computeDashboardMetricDelta,
  formatCny,
  formatDateTime,
} from '../utils/dashboard'

import { t, te, locale } from '@/locales'
const router = useRouter()
const { metrics, liveData, applyRawData } = useDashboardMetrics()
const { theme: chartTheme } = useDashboardChartTheme()
const isNarrowScreen = useMediaQuery('(max-width: 639px)')
const { currentAccount } = useAdminAccounts()
const workspaceID = computed(() => currentAccount.value?.id ?? '')

const {
  status: adminStatus,
  isModalOpen: adminModalOpen,
  isSubmitting: adminSubmitting,
  isRefreshingCredentials: adminRefreshingCredentials,
  errorKey: adminErrorKey,
  checkStatus: checkAdminStatus,
  submitLogin: submitAdminLogin,
  updateAdminCredentials,
  openModal: openAdminModal,
  closeModal: closeAdminModal,
} = useDashboardAdmin()

const adminIdentity = computed(() => adminStatus.value.identity || adminStatus.value.baseUrl || '')
const balanceFilterPlatform = computed<DashboardAdminPlatform>(() =>
  adminStatus.value.platform === 'newapi' ? 'newapi' : 'sub2api',
)
const adminLoginInitialValue = computed(() => ({
  platform: (adminStatus.value.platform as DashboardAdminPlatform) || 'sub2api',
  siteUrl: adminStatus.value.baseUrl || '',
  authMethod: (adminStatus.value.authMethod as Sub2apiAuthMethod) || 'password',
  email: adminStatus.value.identity || '',
}))

const balanceFilterOpen = ref(false)
const groupUsageTodayOpen = ref(false)
const upstreamKeyUsageTodayOpen = ref(false)
const upstreamBalanceBreakdownOpen = ref(false)
const dailyStatsPanelOpen = ref(false)
const additionalCostsExpanded = ref(false)

const openBalanceFilter = () => { balanceFilterOpen.value = true }
const closeBalanceFilter = () => { balanceFilterOpen.value = false }
const onBalanceFilterSaved = () => { void loadAllData({ skipStatusCheck: true }) }
const openGroupUsageToday = () => { groupUsageTodayOpen.value = true }
const closeGroupUsageToday = () => { groupUsageTodayOpen.value = false }
const openUpstreamKeyUsageToday = () => { upstreamKeyUsageTodayOpen.value = true }
const closeUpstreamKeyUsageToday = () => { upstreamKeyUsageTodayOpen.value = false }
const openUpstreamBalanceBreakdown = () => { upstreamBalanceBreakdownOpen.value = true }
const closeUpstreamBalanceBreakdown = () => { upstreamBalanceBreakdownOpen.value = false }
const openDailyStatsPanel = () => { dailyStatsPanelOpen.value = true }
const closeDailyStatsPanel = () => { dailyStatsPanelOpen.value = false }
const openGroupList = () => { void router.push({ name: 'AdminGroupAssociations' }) }

const handleMetricCardClick = (key: string) => {
  switch (key) {
    case 'todayProfit':
      openGroupUsageToday()
      break
    case 'todayPurchase':
      openUpstreamKeyUsageToday()
      break
    case 'upstreamBalance':
      openUpstreamBalanceBreakdown()
      break
    case 'siteBalance':
      openBalanceFilter()
      break
  }
}

const groupCount = ref<number | null>(null)
const groupUsage = ref<GroupUsageTodayResponse | null>(null)
const groupProfit = ref<GroupProfitTodayResponse | null>(null)
const balanceBreakdown = ref<UpstreamBalanceBreakdownResponse | null>(null)
const healthSummary = ref<ConnectionHealthStoredSummary | null>(null)
const operationalLoading = ref(false)
const operationalLoadError = ref(false)
const groupRevenueLoading = ref(false)
const groupProfitLoading = ref(false)
const groupUsageLoadError = ref(false)
const groupProfitLoadError = ref(false)
const balanceLoadError = ref(false)
const healthLoadError = ref(false)
const initialLoading = ref(true)
const isRefreshingData = ref(false)
const refreshDataFailed = ref(false)
const lastUpdatedAt = ref<number | null>(null)

const hydrateSnapshot = (snapshot: DashboardDataSnapshot | null): boolean => {
  if (!snapshot) return false
  adminStatus.value = snapshot.adminStatus
  groupCount.value = snapshot.live.groupCount ?? null
  groupUsage.value = snapshot.groupUsage ?? null
  groupProfit.value = snapshot.groupProfit ?? null
  balanceBreakdown.value = snapshot.balanceBreakdown ?? null
  healthSummary.value = snapshot.healthSummary ?? null
  applyRawData(snapshot.live, snapshot.trends)
  lastUpdatedAt.value = snapshot.updatedAt
  initialLoading.value = false
  return true
}

const initialSnapshot = getDashboardDataSnapshot(workspaceID.value)
hydrateSnapshot(initialSnapshot)

// 次要面板失败不阻断五项核心指标。三个请求都只读，其中健康摘要和余额明细只读本地缓存。
const loadOperationalData = async () => {
  operationalLoading.value = true
  operationalLoadError.value = false
  groupRevenueLoading.value = true
  groupProfitLoading.value = true
  groupUsageLoadError.value = false
  groupProfitLoadError.value = false
  balanceLoadError.value = false
  healthLoadError.value = false

  // Each panel publishes as soon as its own read completes. A slow balance or
  // health request must not keep authoritative group revenue in a spinner.
  const groupRequest = getGroupUsageToday()
    .then((value) => {
      groupUsage.value = value
      return value
    })
    .catch((error) => {
      groupUsageLoadError.value = true
      throw error
    })
    .finally(() => {
      groupRevenueLoading.value = false
    })
  const groupProfitRequest = getGroupProfitToday()
    .then((value) => {
      groupProfit.value = value
      return value
    })
    .catch((error) => {
      groupProfitLoadError.value = true
      throw error
    })
    .finally(() => {
      groupProfitLoading.value = false
    })
  const balanceRequest = getUpstreamBalanceBreakdown()
    .then((value) => {
      balanceBreakdown.value = value
      return value
    })
    .catch((error) => {
      balanceLoadError.value = true
      throw error
    })
  const healthRequest = getConnectionHealthStoredSummary()
    .then((value) => {
      healthSummary.value = value
      return value
    })
    .catch((error) => {
      healthLoadError.value = true
      throw error
    })
  const [groupResult, groupProfitResult, balanceResult, healthResult] = await Promise.allSettled([
    groupRequest,
    groupProfitRequest,
    balanceRequest,
    healthRequest,
  ])
  groupUsageLoadError.value = groupResult.status === 'rejected'
  groupProfitLoadError.value = groupProfitResult.status === 'rejected'
  balanceLoadError.value = balanceResult.status === 'rejected'
  healthLoadError.value = healthResult.status === 'rejected'
  operationalLoadError.value = groupUsageLoadError.value || groupProfitLoadError.value || balanceLoadError.value || healthLoadError.value
  operationalLoading.value = false

  const key = workspaceID.value
  if (key) {
    updateDashboardOperationalSnapshot(key, {
      ...(groupResult.status === 'fulfilled' ? { groupUsage: groupResult.value } : {}),
      ...(groupProfitResult.status === 'fulfilled' ? { groupProfit: groupProfitResult.value } : {}),
      ...(balanceResult.status === 'fulfilled' ? { balanceBreakdown: balanceResult.value } : {}),
      ...(healthResult.status === 'fulfilled' ? { healthSummary: healthResult.value } : {}),
    })
  }
}

const loadAllData = async (options: { skipStatusCheck?: boolean } = {}) => {
  if (isRefreshingData.value) return
  isRefreshingData.value = true
  refreshDataFailed.value = false

  if (!options.skipStatusCheck) {
    await checkAdminStatus({ preserveAuthenticatedOnError: metrics.value.length > 0 })
  }
  if (!adminStatus.value.authenticated) {
    initialLoading.value = false
    isRefreshingData.value = false
    return
  }

  const trendsRequest = getDashboardTrends(30).catch(() => null)
  try {
    const live = await getDashboardMetrics()
    const previous = getDashboardDataSnapshot(workspaceID.value)
    const cachedTrends = previous?.trends ?? { points: [] }
    groupCount.value = live.groupCount ?? null
    applyRawData(live, cachedTrends)
    const updatedAt = Date.now()
    lastUpdatedAt.value = updatedAt
    const key = workspaceID.value
    if (key) {
      saveDashboardDataSnapshot(key, {
        adminStatus: { ...adminStatus.value },
        live,
        trends: cachedTrends,
        updatedAt,
        groupUsage: previous?.groupUsage,
        groupProfit: previous?.groupProfit,
        balanceBreakdown: previous?.balanceBreakdown,
        healthSummary: previous?.healthSummary,
      })
    }
    void loadOperationalData()
    void trendsRequest.then((trends) => {
      if (!trends) {
        refreshDataFailed.value = true
        return
      }
      applyRawData(live, trends)
      if (key) {
        const current = getDashboardDataSnapshot(key)
        if (current) saveDashboardDataSnapshot(key, { ...current, live, trends })
      }
    })
  } catch {
    refreshDataFailed.value = true
  } finally {
    initialLoading.value = false
    isRefreshingData.value = false
  }
}

const adminExpiry = computed(
  () => formatDateTime(adminStatus.value.expiresAt, locale) ?? t('admin.dashboard.adminAuth.timeUnknown'),
)

const lastUpdatedLabel = computed(() => {
  if (lastUpdatedAt.value == null) return t('admin.dashboard.dataStatus.waiting')
  return new Intl.DateTimeFormat(locale, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).format(new Date(lastUpdatedAt.value))
})

onMounted(() => { void loadAllData() })

watch(() => adminStatus.value.authenticated, (authenticated) => {
  if (authenticated && !isRefreshingData.value) {
    if (metrics.value.length === 0) initialLoading.value = true
    void loadAllData({ skipStatusCheck: true })
  }
})

watch(workspaceID, (next, previous) => {
  if (!next || next === previous || isRefreshingData.value) return
  groupUsage.value = null
  groupProfit.value = null
  balanceBreakdown.value = null
  healthSummary.value = null
  metrics.value = []
  const restored = hydrateSnapshot(getDashboardDataSnapshot(next))
  if (!restored) initialLoading.value = true
  void loadAllData()
})

const METRIC_META: Record<DashboardMetricKey, { icon: Component; labelKey: string; color: DashboardColorToken }> = {
  todayProfit: { icon: TrendingUp, labelKey: 'admin.dashboard.metrics.todayProfit', color: 'primary' },
  siteBalance: { icon: Wallet, labelKey: 'admin.dashboard.metrics.siteBalance', color: 'accent' },
  todayPurchase: { icon: ShoppingCart, labelKey: 'admin.dashboard.metrics.todayPurchase', color: 'warning' },
  netProfit: { icon: PiggyBank, labelKey: 'admin.dashboard.metrics.netProfit', color: 'signal' },
  upstreamBalance: { icon: Landmark, labelKey: 'admin.dashboard.metrics.upstreamBalance', color: 'primary' },
}

const metricMap = computed(() => new Map(metrics.value.map(metric => [metric.key, metric])))
const metric = (key: DashboardMetricKey): DashboardMetricData | undefined => metricMap.value.get(key)
const deltaCaption = computed(() => t('admin.dashboard.delta.vsPrev'))
const metricErrorText = (reason: string | undefined) => {
  if (!reason) return ''
  const detail = te(reason) ? t(reason) : reason
  return t('admin.dashboard.common.metricLoadError', { reason: detail })
}

const percentFormatter = computed(() => new Intl.NumberFormat(locale, {
  style: 'percent',
  maximumFractionDigits: 1,
}))
const numberFormatter = computed(() => new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }))

const profitMarginState = computed(() => {
  const cq = liveData.value?.costQuality
  return calculateProfitMargin({
    revenue: metric('todayProfit')?.current,
    netProfit: metric('netProfit')?.current,
    costComplete: cq?.complete,
    costMode: cq?.mode,
    confirmedCost: cq?.confirmedCost,
    collectedSites: cq?.collectedSites,
  })
})

const marginSeries = computed(() => {
  const revenue = metric('todayProfit')?.series.month ?? []
  const profit = metric('netProfit')?.series.month ?? []
  return buildProfitMarginSeries(revenue, profit)
})

interface DashboardCoreCard {
  key: string
  label: string
  icon: Component
  color: DashboardColorToken
  value: string
  deltaDirection: ReturnType<typeof computeDelta>['direction']
  deltaText: string
  statusText: string
  clickable: boolean
  negativeWhenUp: boolean
}

const cards = computed<DashboardCoreCard[]>(() => {
  const cq = liveData.value?.costQuality
  const costMode = cq?.mode ?? (cq?.complete ? 'exact' : 'partial')
  const costIncomplete = cq && (costMode === 'partial' || costMode === 'unavailable')
  const costFallback = costMode === 'fallback' || costMode === 'retained'
  const fallbackTime = cq?.fallbackAt
    ? formatDateTime(Date.parse(cq.fallbackAt), locale) ?? t('admin.dashboard.common.unavailable')
    : t('admin.dashboard.common.unavailable')
  const result: DashboardCoreCard[] = (['todayProfit', 'todayPurchase', 'netProfit'] as DashboardMetricKey[]).flatMap((key) => {
    const current = metric(key)
    if (!current) return []
    const delta = computeDashboardMetricDelta(key, current.series.month)

    const deltaUnavailable = delta.unavailable
    const fallbackStatus = costFallback && (key === 'todayPurchase' || key === 'netProfit')
      ? t('admin.dashboard.costQuality.fallback', {
          fallback: cq?.fallbackSites ?? 0,
          expected: cq?.expectedSites ?? 0,
          time: fallbackTime,
        })
      : ''
    const partialStatus = costIncomplete && (key === 'todayPurchase' || key === 'netProfit')
      ? (current.current == null
          ? t('admin.dashboard.costQuality.costUnavailable')
          : key === 'todayPurchase'
            ? t('admin.dashboard.costQuality.partial', {
                cost: formatCny(current.current),
                collected: cq?.collectedSites ?? 0,
                expected: cq?.expectedSites ?? 0,
              })
            : t('admin.dashboard.costQuality.netProfitCeiling', { value: formatCny(current.current) }))
      : ''
    return [{
      key,
      label: t(METRIC_META[key].labelKey),
      icon: METRIC_META[key].icon,
      color: METRIC_META[key].color,
      value: formatCny(current.current),
      deltaDirection: delta.direction,
      deltaText: deltaUnavailable ? '' : formatCny(Math.abs(delta.amount)),
      statusText: metricErrorText(current.error)
        || fallbackStatus
        || partialStatus
        || (deltaUnavailable ? t('admin.dashboard.costQuality.deltaUnsettled') : ''),
      clickable: key === 'todayProfit' || key === 'todayPurchase',
      negativeWhenUp: key === 'todayPurchase',
    }]
  })
  const marginState = profitMarginState.value
  const marginDelta = marginState.mode === 'exact'
    ? computeDelta(marginSeries.value)
    : { amount: 0, direction: 'flat' as const, unavailable: true }
  const marginError = metric('netProfit')?.error || metric('todayProfit')?.error
  const marginStatusText = metricErrorText(marginError)
    || (marginState.mode === 'fallback'
      ? t('admin.dashboard.costQuality.fallback', {
          fallback: cq?.fallbackSites ?? 0,
          expected: cq?.expectedSites ?? 0,
          time: fallbackTime,
        })
      : marginState.mode === 'ceiling' && marginState.value != null
      ? t('admin.dashboard.costQuality.marginCeiling', { value: numberFormatter.value.format(marginState.value) })
      : marginState.mode === 'unavailable'
        ? (costIncomplete
            ? t('admin.dashboard.costQuality.costUnavailable')
            : t('admin.dashboard.common.unavailable'))
        : marginDelta.unavailable
          ? t('admin.dashboard.costQuality.deltaUnsettled')
          : '')
  result.push({
    key: 'profitMargin',
    label: t('admin.dashboard.metrics.profitMargin'),
    icon: Gauge,
    color: 'accent',
    value: marginState.value == null
      ? t('admin.dashboard.common.unavailable')
      : percentFormatter.value.format(marginState.value / 100),
    deltaDirection: marginDelta.direction,
    deltaText: marginDelta.unavailable
      ? ''
      : t('admin.dashboard.delta.percentagePoints', { value: numberFormatter.value.format(Math.abs(marginDelta.amount)) }),
    statusText: marginStatusText,
    clickable: false,
    negativeWhenUp: false,
  })
  return result
})

const additionalCostLines = computed(() => {
  const summary = liveData.value?.additionalCosts
  if (!summary) return []
  return [
    { label: `充值手续费${summary.feeRate != null ? ` (${(summary.feeRate * 100).toFixed(2)}%)` : ''}`, value: summary.rechargeFee },
    { label: '活动赠送摊销', value: summary.promotion },
    { label: '服务器及固定费用', value: summary.fixed },
    { label: '手工调整', value: summary.adjustment },
  ]
})

const displayedTodayRevenue = computed(() => liveData.value?.todayProfit ?? null)
const displayedTodayCost = computed(() => {
  if (liveData.value?.todayPurchase != null) return liveData.value.todayPurchase
  const quality = liveData.value?.costQuality
  return quality && quality.collectedSites > 0 ? quality.confirmedCost : null
})
const displayedOperatingCost = computed(() => {
  const additionalCost = liveData.value?.additionalCosts?.total
  return displayedTodayCost.value != null && additionalCost != null
    ? displayedTodayCost.value + additionalCost
    : null
})
const displayedAdjustedNetProfit = computed(() => (
  displayedTodayRevenue.value != null && displayedOperatingCost.value != null
    ? displayedTodayRevenue.value - displayedOperatingCost.value
    : null
))

type GroupMetricMode = 'profit' | 'revenue'
type GroupContributionItem = GroupUsageTodayResponse['groups'][number] & {
  contributionKind?: 'unallocated_profit'
}

const groupMetricMode = ref<GroupMetricMode>('profit')
const groupMetricModes: GroupMetricMode[] = ['profit', 'revenue']
const groupRevenueTotal = computed(() => groupUsage.value?.totalRevenue ?? groupUsage.value?.total ?? 0)
const groupProfitAvailable = computed(() => groupProfit.value != null)
const groupMetricLabel = computed(() => t(
  groupMetricMode.value === 'profit'
    ? 'admin.dashboard.groups.profitAmount'
    : 'admin.dashboard.groups.revenueAmount',
))
const groupSubtitle = computed(() => t(
  groupMetricMode.value === 'profit'
    ? 'admin.dashboard.groups.subtitleProfit'
    : 'admin.dashboard.groups.subtitleRevenue',
))
const groupTopThreeLabel = computed(() => t(
  groupMetricMode.value === 'profit'
    ? 'admin.dashboard.groups.topThreeProfitShare'
    : 'admin.dashboard.groups.topThreeRevenueShare',
))
const groupMetricValue = (item: GroupContributionItem): number | null => {
  if (groupMetricMode.value === 'revenue') return item.todayRevenue ?? item.todayAmount
  return item.todayProfit ?? null
}
const groupDisplayName = (item: GroupContributionItem) => item.groupName

const period = ref<DashboardPeriod>('week')
const periods: DashboardPeriod[] = ['week', 'month']
const selectedSeries = (key: DashboardMetricKey) => metric(key)?.series[period.value] ?? []
const sumSeries = (key: DashboardMetricKey): number | null => {
  const values = selectedSeries(key)
    .map(point => point.value)
    .filter((value): value is number => value != null && Number.isFinite(value))
  return values.length > 0 ? values.reduce((total, value) => total + value, 0) : null
}

const periodTotals = computed(() => ({
  revenue: sumSeries('todayProfit'),
  cost: sumSeries('todayPurchase'),
  profit: sumSeries('netProfit'),
}))
const periodCostLabel = computed(() => selectedSeries('todayPurchase').some(point => point.quality === 'confirmed')
  ? t('admin.dashboard.performance.periodConfirmedCost')
  : t('admin.dashboard.performance.periodCost'))
const periodProfitLabel = computed(() => selectedSeries('netProfit').some(point => point.quality === 'ceiling')
  ? t('admin.dashboard.performance.periodProfitCeiling')
  : t('admin.dashboard.performance.periodProfit'))

const compactCurrency = (value: number) => {
  const absolute = Math.abs(value)
  if (absolute >= 1_000_000) return `¥${numberFormatter.value.format(value / 1_000_000)}M`
  if (absolute >= 1_000) return `¥${numberFormatter.value.format(value / 1_000)}K`
  return `¥${numberFormatter.value.format(value)}`
}

const performanceChartOption = computed<EChartsCoreOption>(() => {
  const revenue = selectedSeries('todayProfit')
  const cost = selectedSeries('todayPurchase')
  const profit = selectedSeries('netProfit')
  const theme = chartTheme.value
  const costName = cost.some(point => point.quality === 'confirmed')
    ? t('admin.dashboard.costQuality.trendConfirmedCost')
    : t('admin.dashboard.metrics.todayPurchase')
  const profitName = profit.some(point => point.quality === 'ceiling')
    ? t('admin.dashboard.costQuality.trendProfitCeiling')
    : t('admin.dashboard.metrics.netProfit')
  const chartData = (points: typeof revenue) => points.map((point) => {
    if (point.value == null) return null
    return {
      value: point.value,
      quality: point.quality,
      expected: point.expected,
      collected: point.collected,
      itemStyle: point.quality === 'exact'
        ? undefined
        : { opacity: 0.58, borderType: 'dashed', borderWidth: 1 },
    }
  })
  const commonSeries = (name: string, points: typeof revenue, color: string) => ({
    name,
    data: chartData(points),
    itemStyle: { color },
    tooltip: { valueFormatter: (value: number | null) => formatCny(value) },
  })
  return {
    animationDuration: 350,
    textStyle: { color: theme.muted, fontFamily: 'Segoe UI Variable, Segoe UI, sans-serif' },
    tooltip: {
      trigger: 'axis',
      confine: true,
      backgroundColor: theme.card,
      borderColor: theme.border,
      textStyle: { color: theme.foreground },
      axisPointer: { type: 'shadow', shadowStyle: { color: theme.border } },
      formatter: (params: unknown) => {
        const entries = (Array.isArray(params) ? params : [params]) as Array<{
          axisValue?: unknown
          seriesName?: unknown
          value?: unknown
          data?: { quality?: string; expected?: number | null; collected?: number | null }
        }>
        const axisValue = escapeTooltipHtml(String(entries[0]?.axisValue ?? ''))
        const rows = entries.map((entry) => {
          const quality = entry.data?.quality === 'fallback'
            ? t('admin.dashboard.costQuality.trendFallback')
            : entry.data?.quality === 'confirmed'
              ? t('admin.dashboard.costQuality.trendConfirmed')
              : entry.data?.quality === 'ceiling'
              ? t('admin.dashboard.costQuality.trendCeiling')
              : entry.data?.quality === 'unavailable'
                ? t('admin.dashboard.costQuality.trendUnavailable')
                : ''
          const label = quality
            ? `${quality} ${String(entry.seriesName ?? '')}`
            : String(entry.seriesName ?? '')
          const coverage = entry.data?.expected != null
            ? ` (${t('admin.dashboard.costQuality.trendCoverage', {
                collected: entry.data.collected ?? 0,
                expected: entry.data.expected,
              })})`
            : ''
          const amount = typeof entry.value === 'number' && Number.isFinite(entry.value)
            ? formatCny(entry.value)
            : '¥—'
          return `${entry.seriesName ? '<span style="display:inline-block;margin-right:4px;border-radius:50%;width:8px;height:8px;background:currentColor"></span>' : ''}${escapeTooltipHtml(label)}${escapeTooltipHtml(coverage)}: ${amount}`
        }).join('<br/>')
        return `${axisValue}<br/>${rows}`
      },
    },
    legend: {
      top: 0,
      right: 0,
      itemWidth: 10,
      itemHeight: 10,
      textStyle: { color: theme.muted },
    },
    grid: { top: 42, right: 8, bottom: 8, left: 8, containLabel: true },
    xAxis: {
      type: 'category',
      data: revenue.map(point => point.label),
      axisLine: { lineStyle: { color: theme.border } },
      axisTick: { show: false },
      axisLabel: { color: theme.muted, hideOverlap: true },
    },
    yAxis: {
      type: 'value',
      axisLabel: { color: theme.muted, formatter: (value: number) => compactCurrency(value) },
      splitLine: { lineStyle: { color: theme.border, type: 'dashed' } },
    },
    dataZoom: period.value === 'month' ? [{ type: 'inside', start: 0, end: 100 }] : [],
    series: [
      { ...commonSeries(t('admin.dashboard.metrics.todayProfit'), revenue, theme.primary), type: 'bar', barMaxWidth: 18, itemStyle: { color: theme.primary, borderRadius: [3, 3, 0, 0] } },
      { ...commonSeries(costName, cost, theme.warning), type: 'bar', barMaxWidth: 18, itemStyle: { color: theme.warning, borderRadius: [3, 3, 0, 0] } },
      { ...commonSeries(profitName, profit, theme.signal), type: 'line', smooth: 0.22, symbol: 'circle', symbolSize: 5, lineStyle: { width: 2.5, color: theme.signal }, itemStyle: { color: theme.signal } },
    ],
  }
})

const profitContributionGroups = computed<GroupContributionItem[]>(() => {
  if (!groupProfitAvailable.value) return []
  const directGroups = (groupProfit.value?.groups ?? [])
    .filter((item) => item.todayProfit != null)
  if (displayedAdjustedNetProfit.value == null) return directGroups
  const directProfit = directGroups.reduce((total, item) => total + (item.todayProfit ?? 0), 0)
  const unallocatedProfit = displayedAdjustedNetProfit.value - directProfit
  return [
    ...directGroups,
    {
      groupId: '__unallocated_profit__',
      groupName: t('admin.dashboard.groups.unallocatedProfit'),
      todayAmount: 0,
      todayRevenue: 0,
      todayProfit: unallocatedProfit,
      contributionKind: 'unallocated_profit',
    },
  ]
})
const displayGroups = computed<GroupContributionItem[]>(() => (
  groupMetricMode.value === 'profit'
    ? profitContributionGroups.value
    : (groupUsage.value?.groups ?? [])
))
const activeGroupLoading = computed(() => groupMetricMode.value === 'profit'
  ? groupProfitLoading.value
  : groupRevenueLoading.value)
const activeGroupLoadError = computed(() => groupMetricMode.value === 'profit'
  ? groupProfitLoadError.value
  : groupUsageLoadError.value)
const activeGroupData = computed(() => groupMetricMode.value === 'profit'
  ? groupProfit.value
  : groupUsage.value)
const groupFallbackText = computed(() => {
  if (activeGroupLoadError.value && activeGroupData.value) {
    return t('admin.dashboard.groups.refreshFailedUsingExisting')
  }
  if (groupMetricMode.value === 'revenue' && groupUsage.value?.fallback) {
    return t('admin.dashboard.groups.revenueFallback', {
      time: groupUsage.value.fallbackAt
        ? formatDateTime(Date.parse(groupUsage.value.fallbackAt), locale) ?? t('admin.dashboard.common.unavailable')
        : t('admin.dashboard.common.unavailable'),
    })
  }
  if (groupMetricMode.value === 'profit' && (groupProfit.value?.fallbackGroups ?? 0) > 0) {
    const fallback = t('admin.dashboard.groups.profitFallback', { count: groupProfit.value?.fallbackGroups ?? 0 })
    return (groupProfit.value?.unavailableGroups ?? 0) > 0
      ? `${fallback} ${t('admin.dashboard.groups.profitUnavailableGroups', { count: groupProfit.value?.unavailableGroups ?? 0 })}`
      : fallback
  }
  if (groupMetricMode.value === 'profit' && (groupProfit.value?.unavailableGroups ?? 0) > 0) {
    return t('admin.dashboard.groups.profitUnavailableGroups', { count: groupProfit.value?.unavailableGroups ?? 0 })
  }
  return ''
})
const sortedGroups = computed(() => {
  return [...displayGroups.value]
    .filter((item) => groupMetricValue(item) != null)
    .sort((a, b) => (groupMetricValue(b) ?? 0) - (groupMetricValue(a) ?? 0))
})
const topGroups = computed(() => {
  if (groupMetricMode.value !== 'profit') return sortedGroups.value.slice(0, 6)
  const unallocated = sortedGroups.value.find((item) => item.contributionKind === 'unallocated_profit')
  const direct = sortedGroups.value
    .filter((item) => item.contributionKind !== 'unallocated_profit')
    .slice(0, unallocated ? 5 : 6)
  return unallocated ? [...direct, unallocated] : direct
})
const groupConcentration = computed(() => {
  const total = groupMetricMode.value === 'profit' ? null : groupRevenueTotal.value
  if (total == null || total <= 0) return null
  return sortedGroups.value.slice(0, 3).reduce((sum, item) => sum + (groupMetricValue(item) ?? 0), 0) / total
})

const escapeTooltipHtml = (value: string) => {
  const entities: Record<string, string> = {
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;',
  }
  return value.replace(/[&<>"']/g, character => entities[character] ?? character)
}

const groupTooltipContent = (params: unknown, mutedColor: string, foregroundColor: string) => {
  const point = (Array.isArray(params) ? params[0] : params) as { name?: unknown; value?: unknown } | undefined
  if (!point) return ''
  const name = escapeTooltipHtml(String(point.name ?? ''))
  const amount = Number(point.value)
  const value = escapeTooltipHtml(formatCny(Number.isFinite(amount) ? amount : 0))
  const label = escapeTooltipHtml(groupMetricLabel.value)
  const item = topGroups.value.find((group) => groupDisplayName(group) === String(point.name ?? ''))
  if (groupMetricMode.value === 'profit' && item?.contributionKind === 'unallocated_profit') {
    return `<div style="margin-bottom:6px;color:${foregroundColor};font-weight:600">${name}</div>`
      + `<div style="display:flex;min-width:160px;justify-content:space-between;gap:20px;color:${mutedColor}">`
      + `<span>${label}</span><strong style="color:${foregroundColor}">${value}</strong></div>`
  }
  if (groupMetricMode.value === 'profit' && item?.directRevenue != null && item.directCost != null) {
    const directRevenue = escapeTooltipHtml(formatCny(item.directRevenue))
    const directCost = escapeTooltipHtml(formatCny(item.directCost))
    return `<div style="margin-bottom:6px;color:${foregroundColor};font-weight:600">${name}</div>`
      + `<div style="display:flex;min-width:160px;justify-content:space-between;gap:20px;color:${mutedColor}">`
      + `<span>已归属营收</span><strong style="color:${foregroundColor}">${directRevenue}</strong></div>`
      + `<div style="display:flex;min-width:160px;justify-content:space-between;gap:20px;color:${mutedColor}">`
      + `<span>已归属成本</span><strong style="color:${foregroundColor}">${directCost}</strong></div>`
      + `<div style="display:flex;min-width:160px;justify-content:space-between;gap:20px;color:${mutedColor}">`
      + `<span>${label}</span><strong style="color:${foregroundColor}">${value}</strong></div>`
  }
  return `<div style="margin-bottom:6px;color:${foregroundColor};font-weight:600">${name}</div>`
    + `<div style="display:flex;min-width:160px;justify-content:space-between;gap:20px;color:${mutedColor}">`
    + `<span>${label}</span><strong style="color:${foregroundColor}">${value}</strong></div>`
}

const groupChartOption = computed<EChartsCoreOption>(() => {
  const theme = chartTheme.value
  return {
    animationDuration: 350,
    textStyle: { color: theme.muted, fontFamily: 'Segoe UI Variable, Segoe UI, sans-serif' },
    tooltip: {
      trigger: 'item',
      confine: true,
      backgroundColor: theme.card,
      borderColor: theme.border,
      textStyle: { color: theme.foreground },
      formatter: (params: unknown) => groupTooltipContent(params, theme.muted, theme.foreground),
    },
    grid: { top: 4, right: 14, bottom: 4, left: 8, containLabel: true },
    xAxis: {
      type: 'value',
      splitNumber: isNarrowScreen.value ? 3 : 5,
      axisLabel: { color: theme.muted, hideOverlap: true, formatter: (value: number) => compactCurrency(value) },
      splitLine: { lineStyle: { color: theme.border, type: 'dashed' } },
    },
    yAxis: {
      type: 'category',
      inverse: true,
      data: topGroups.value.map(item => groupDisplayName(item)),
      axisLine: { show: false },
      axisTick: { show: false },
      axisLabel: {
        color: theme.muted,
        width: 112,
        overflow: 'truncate',
      },
    },
    series: [{
      name: groupMetricLabel.value,
      type: 'bar',
      data: topGroups.value.map(item => groupMetricValue(item) ?? 0),
      barMaxWidth: 20,
      itemStyle: { color: theme.primary, borderRadius: [0, 4, 4, 0] },
      emphasis: { itemStyle: { color: theme.primary, opacity: 0.88 } },
    }],
  }
})

const siteBalance = computed(() => metric('siteBalance')?.current ?? 0)
const upstreamBalance = computed(() => metric('upstreamBalance')?.current ?? 0)
const coverageRatio = computed(() => siteBalance.value > 0 ? (upstreamBalance.value / siteBalance.value) * 100 : null)
const averageDailyCost = computed(() => {
  const values = selectedSeries('todayPurchase')
    .map(point => point.value)
    .filter((v): v is number => v !== null)
  return values.length > 0 ? values.reduce((sum, value) => sum + value, 0) / values.length : 0
})
const runwayDays = computed(() => averageDailyCost.value > 0 ? upstreamBalance.value / averageDailyCost.value : null)
const coverageWidth = computed(() => `${Math.min(coverageRatio.value ?? 0, 100)}%`)
const coverageTone = computed(() => {
  if (coverageRatio.value == null) return 'bg-muted-foreground'
  if (coverageRatio.value >= 100) return 'bg-signal'
  if (coverageRatio.value >= 40) return 'bg-warning'
  return 'bg-destructive'
})

const upstreamIssueCount = computed(() => (balanceBreakdown.value?.sites ?? []).filter(
  site => site.balance == null || site.status === 'error',
).length)
const healthRiskCount = computed(() => (healthSummary.value?.attentionTargets ?? 0) + (healthSummary.value?.suspendedTargets ?? 0))
const attentionDataUnavailable = computed(() => healthLoadError.value || balanceLoadError.value)

const attentionItems = computed(() => {
  const items: Array<{
    key: string
    icon: Component
    title: string
    description: string
    count: number
    tone: string
    routeName: string
  }> = []
  if (healthRiskCount.value > 0) {
    items.push({
      key: 'health',
      icon: Activity,
      title: t('admin.dashboard.attention.healthTitle'),
      description: t('admin.dashboard.attention.healthDescription', {
        attention: healthSummary.value?.attentionTargets ?? 0,
        suspended: healthSummary.value?.suspendedTargets ?? 0,
      }),
      count: healthRiskCount.value,
      tone: 'text-warning bg-warning/10',
      routeName: 'AdminConnectionHealth',
    })
  }
  if ((healthSummary.value?.recentFailureEvents ?? 0) > 0) {
    items.push({
      key: 'events',
      icon: AlertTriangle,
      title: t('admin.dashboard.attention.failuresTitle'),
      description: t('admin.dashboard.attention.failuresDescription'),
      count: healthSummary.value?.recentFailureEvents ?? 0,
      tone: 'text-destructive bg-destructive/10',
      routeName: 'AdminConnectionHealth',
    })
  }
  if (upstreamIssueCount.value > 0) {
    items.push({
      key: 'upstream',
      icon: Landmark,
      title: t('admin.dashboard.attention.upstreamTitle'),
      description: t('admin.dashboard.attention.upstreamDescription'),
      count: upstreamIssueCount.value,
      tone: 'text-warning bg-warning/10',
      routeName: 'AdminUpstream',
    })
  }
  return items
})

const lastProbeLabel = computed(() => {
  const value = healthSummary.value?.lastProbeAt
  if (!value) return t('admin.dashboard.attention.neverProbed')
  return formatDateTime(Date.parse(value), locale) ?? t('admin.dashboard.attention.neverProbed')
})
</script>

<template>
  <div class="space-y-6">
    <div
      v-if="adminStatus.authenticated"
      class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border/60 bg-card px-4 py-2.5 shadow-sm"
    >
      <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-sm">
        <span class="inline-flex h-2 w-2 shrink-0 rounded-full bg-signal" />
        <span class="truncate text-muted-foreground">{{ t('admin.dashboard.adminAuth.loggedInAs', { identity: adminIdentity }) }}</span>
        <span class="text-xs text-muted-foreground">
          {{ t('admin.dashboard.adminAuth.expiresAt') }} {{ adminExpiry }}
        </span>
        <span
          v-if="metrics.length > 0"
          class="inline-flex items-center gap-1 text-xs"
          :class="refreshDataFailed ? 'text-destructive' : 'text-muted-foreground'"
        >
          <RefreshCw v-if="isRefreshingData" class="h-3 w-3 animate-spin" />
          {{ isRefreshingData
            ? t('admin.dashboard.dataStatus.refreshing')
            : refreshDataFailed
              ? t('admin.dashboard.dataStatus.failed')
              : t('admin.dashboard.dataStatus.updatedAt', { time: lastUpdatedLabel }) }}
        </span>
      </div>
      <div class="flex items-center gap-1">
        <button
          type="button"
          class="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:cursor-not-allowed disabled:opacity-60"
          :disabled="isRefreshingData"
          :title="t('admin.dashboard.dataStatus.refresh')"
          :aria-label="t('admin.dashboard.dataStatus.refresh')"
          @click="loadAllData()"
        >
          <RefreshCw class="h-3.5 w-3.5" :class="{ 'animate-spin': isRefreshingData }" />
        </button>
        <button
          type="button"
          class="inline-flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:cursor-not-allowed disabled:opacity-60"
          :disabled="adminRefreshingCredentials"
          @click="updateAdminCredentials"
        >
          <ShieldCheck class="h-3.5 w-3.5" />
          {{ adminRefreshingCredentials ? t('admin.dashboard.adminAuth.updatingCredentials') : t('admin.dashboard.adminAuth.updateCredentials') }}
        </button>
        <button
          type="button"
          class="inline-flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          @click="openDailyStatsPanel"
        >
          <Activity class="h-3.5 w-3.5" />
          {{ t('admin.dashboard.dailyStats.title') }}
        </button>
      </div>
    </div>

    <div
      v-else-if="!initialLoading && !adminModalOpen"
      class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-warning/30 bg-warning/5 px-4 py-2.5"
    >
      <div class="flex items-center gap-2 text-sm text-muted-foreground">
        <span class="inline-flex h-2 w-2 rounded-full bg-warning" />
        {{ t('admin.dashboard.adminAuth.notLoggedIn') }}
      </div>
      <button
        type="button"
        class="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90"
        @click="openAdminModal"
      >
        <ShieldCheck class="h-4 w-4" />
        {{ t('admin.dashboard.adminAuth.login') }}
      </button>
    </div>

    <section
      v-if="initialLoading"
      class="space-y-4"
      role="status"
      :aria-label="t('admin.dashboard.loading')"
    >
      <div class="grid grid-cols-2 gap-3 xl:grid-cols-4">
        <div
          v-for="item in 4"
          :key="item"
          class="min-h-[132px] animate-pulse rounded-lg border border-border/60 bg-card p-4 sm:min-h-[142px] sm:p-5"
        >
          <div class="flex items-start justify-between gap-4">
            <div class="flex-1 space-y-3">
              <div class="h-3 w-20 rounded bg-muted" />
              <div class="h-7 w-28 max-w-full rounded bg-muted" />
            </div>
            <div class="h-9 w-9 rounded-lg bg-muted" />
          </div>
          <div class="mt-5 h-3 w-32 max-w-full rounded bg-muted" />
        </div>
      </div>
      <div class="grid gap-4 xl:grid-cols-12">
        <div class="h-[430px] animate-pulse rounded-lg border border-border/60 bg-card p-5 xl:col-span-8">
          <div class="h-4 w-28 rounded bg-muted" />
          <div class="mt-4 h-[350px] rounded bg-muted/60" />
        </div>
        <div class="h-[430px] animate-pulse rounded-lg border border-border/60 bg-card p-5 xl:col-span-4">
          <div class="h-4 w-24 rounded bg-muted" />
          <div class="mt-6 space-y-5">
            <div class="h-12 rounded bg-muted/60" />
            <div class="h-12 rounded bg-muted/60" />
            <div class="h-24 rounded bg-muted/60" />
          </div>
        </div>
      </div>
    </section>

    <template v-else-if="adminStatus.authenticated">
      <div
        v-if="metrics.length === 0"
        class="flex flex-col items-center justify-center gap-4 rounded-lg border border-dashed border-destructive/30 bg-destructive/5 px-6 py-16 text-center"
      >
        <p class="text-sm text-muted-foreground">{{ t('admin.dashboard.loadError') }}</p>
        <button
          type="button"
          class="inline-flex items-center gap-1.5 rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90"
          @click="loadAllData()"
        >
          <RefreshCw class="h-4 w-4" />
          {{ t('admin.dashboard.retry') }}
        </button>
      </div>

      <template v-else-if="metrics.length > 0">
        <section class="grid grid-cols-2 gap-3 xl:grid-cols-4">
          <StatCard
            v-for="card in cards"
            :key="card.key"
            :label="card.label"
            :value="card.value"
            :icon="card.icon"
            :color="card.color"
            :delta-direction="card.deltaDirection"
            :delta-text="card.deltaText"
            :delta-caption="deltaCaption"
            :status-text="card.statusText"
            :clickable="card.clickable"
            :negative-when-up="card.negativeWhenUp"
            @click="handleMetricCardClick(card.key)"
          />
        </section>

        <section v-if="liveData?.additionalCosts">
          <article class="overflow-hidden rounded-lg border border-border/60 bg-card shadow-sm">
            <button
              type="button"
              class="grid w-full grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] items-center gap-4 px-4 py-3 text-left transition-colors hover:bg-surface/40 sm:px-5"
              :aria-expanded="additionalCostsExpanded"
              aria-controls="dashboard-operating-cost-details"
              @click="additionalCostsExpanded = !additionalCostsExpanded"
            >
              <span class="flex min-w-0 flex-col gap-0.5 sm:flex-row sm:items-baseline sm:gap-3">
                <span class="text-sm font-medium text-muted-foreground">今日经营成本</span>
                <span class="text-lg font-bold tabular-nums text-foreground">{{ formatCny(displayedOperatingCost) }}</span>
              </span>
              <span class="flex min-w-0 flex-col gap-0.5 sm:flex-row sm:items-baseline sm:gap-3">
                <span class="text-sm font-medium text-muted-foreground">调整后净利润</span>
                <span class="text-lg font-bold tabular-nums text-foreground">{{ formatCny(displayedAdjustedNetProfit) }}</span>
              </span>
              <ChevronDown
                class="h-4 w-4 text-muted-foreground transition-transform"
                :class="additionalCostsExpanded ? 'rotate-180' : ''"
                aria-hidden="true"
              />
            </button>
            <div
              v-if="additionalCostsExpanded"
              id="dashboard-operating-cost-details"
              class="border-t border-border/60 px-4 pb-3 sm:px-5"
            >
              <dl class="divide-y divide-border/60">
                <div class="flex items-center justify-between gap-4 py-2 text-sm">
                  <dt class="text-muted-foreground">上游直接成本</dt>
                  <dd class="font-medium tabular-nums text-foreground">{{ formatCny(displayedTodayCost) }}</dd>
                </div>
                <div v-for="item in additionalCostLines" :key="item.label" class="flex items-center justify-between gap-4 py-2 text-sm">
                  <dt class="text-muted-foreground">{{ item.label }}</dt>
                  <dd class="font-medium tabular-nums text-foreground">{{ formatCny(item.value) }}</dd>
                </div>
                <div class="flex items-center justify-between gap-4 py-2 text-sm">
                  <dt class="text-muted-foreground">附加成本合计</dt>
                  <dd class="font-semibold tabular-nums text-foreground">{{ formatCny(liveData.additionalCosts.total) }}</dd>
                </div>
              </dl>
            </div>
          </article>
        </section>

        <section class="grid gap-4 xl:grid-cols-12">
          <article class="min-w-0 rounded-lg border border-border/60 bg-card p-4 shadow-sm sm:p-5 xl:col-span-8">
            <div class="flex flex-wrap items-start justify-between gap-4">
              <div>
                <h2 class="text-base font-semibold text-foreground">{{ t('admin.dashboard.performance.title') }}</h2>
                <p class="mt-1 text-sm text-muted-foreground">{{ t('admin.dashboard.performance.subtitle') }}</p>
              </div>
              <div
                class="inline-flex items-center rounded-lg border border-border/60 bg-surface/50 p-1"
                role="group"
                :aria-label="t('admin.dashboard.period.label')"
              >
                <button
                  v-for="item in periods"
                  :key="item"
                  type="button"
                  :aria-pressed="period === item"
                  class="rounded-md px-3 py-1.5 text-xs font-medium transition-colors sm:text-sm"
                  :class="period === item ? 'bg-card text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'"
                  @click="period = item"
                >
                  {{ t(`admin.dashboard.period.${item}`) }}
                </button>
              </div>
            </div>

            <dl class="mt-5 grid grid-cols-3 divide-x divide-border/60 border-y border-border/60 py-3">
              <div class="min-w-0 px-2 first:pl-0 sm:px-4">
                <dt class="truncate text-xs text-muted-foreground">{{ t('admin.dashboard.performance.periodRevenue') }}</dt>
                <dd class="mt-1 truncate text-sm font-semibold tabular-nums text-foreground sm:text-base">{{ formatCny(periodTotals.revenue) }}</dd>
              </div>
              <div class="min-w-0 px-2 sm:px-4">
                <dt class="truncate text-xs text-muted-foreground">{{ periodCostLabel }}</dt>
                <dd class="mt-1 truncate text-sm font-semibold tabular-nums text-foreground sm:text-base">{{ formatCny(periodTotals.cost) }}</dd>
              </div>
              <div class="min-w-0 px-2 pr-0 sm:px-4 sm:pr-0">
                <dt class="truncate text-xs text-muted-foreground">{{ periodProfitLabel }}</dt>
                <dd class="mt-1 truncate text-sm font-semibold tabular-nums text-signal sm:text-base">{{ formatCny(periodTotals.profit) }}</dd>
              </div>
            </dl>

            <div class="mt-4 h-[270px] sm:h-[310px]">
              <DashboardEChart
                :option="performanceChartOption"
                :accessible-label="t('admin.dashboard.performance.chartAria')"
              />
            </div>
          </article>

          <aside class="min-w-0 rounded-lg border border-border/60 bg-card p-4 shadow-sm sm:p-5 xl:col-span-4">
            <div class="flex items-center justify-between gap-3">
              <div>
                <h2 class="text-base font-semibold text-foreground">{{ t('admin.dashboard.capital.title') }}</h2>
                <p class="mt-1 text-sm text-muted-foreground">{{ t('admin.dashboard.capital.subtitle') }}</p>
              </div>
              <Landmark class="h-5 w-5 shrink-0 text-primary" />
            </div>

            <div class="mt-6 divide-y divide-border/60 border-y border-border/60">
              <button type="button" class="flex w-full items-center justify-between gap-4 py-4 text-left" @click="openBalanceFilter">
                <dt class="text-sm text-muted-foreground">{{ t('admin.dashboard.capital.siteBalance') }}</dt>
                <dd class="font-semibold tabular-nums text-foreground">{{ formatCny(siteBalance) }}</dd>
              </button>
              <button type="button" class="flex w-full items-center justify-between gap-4 py-4 text-left" @click="openUpstreamBalanceBreakdown">
                <dt class="text-sm text-muted-foreground">{{ t('admin.dashboard.capital.upstreamBalance') }}</dt>
                <dd class="font-semibold tabular-nums text-foreground">{{ formatCny(upstreamBalance) }}</dd>
              </button>
            </div>

            <div class="mt-6">
              <div class="flex items-end justify-between gap-3">
                <span class="text-sm text-muted-foreground">{{ t('admin.dashboard.capital.coverage') }}</span>
                <span class="text-xl font-semibold tabular-nums text-foreground">
                  {{ coverageRatio == null ? t('admin.dashboard.common.unavailable') : percentFormatter.format(coverageRatio / 100) }}
                </span>
              </div>
              <div class="mt-2 h-2 overflow-hidden rounded-full bg-muted">
                <div class="h-full rounded-full transition-[width]" :class="coverageTone" :style="{ width: coverageWidth }" />
              </div>
              <p class="mt-2 text-xs leading-5 text-muted-foreground">{{ t('admin.dashboard.capital.coverageHint') }}</p>
            </div>

            <div class="mt-6 flex items-center justify-between border-t border-border/60 pt-5">
              <div>
                <p class="text-sm text-muted-foreground">{{ t('admin.dashboard.capital.runway') }}</p>
                <p class="mt-1 text-xs text-muted-foreground">{{ t('admin.dashboard.capital.runwayHint') }}</p>
              </div>
              <div class="flex items-center gap-2 text-foreground">
                <Clock3 class="h-4 w-4 text-muted-foreground" />
                <span class="text-xl font-semibold tabular-nums">
                  {{ runwayDays == null ? t('admin.dashboard.common.unavailable') : t('admin.dashboard.capital.runwayValue', { value: numberFormatter.format(runwayDays) }) }}
                </span>
              </div>
            </div>
          </aside>
        </section>

        <section class="grid gap-4 xl:grid-cols-12">
          <article class="min-w-0 rounded-lg border border-border/60 bg-card p-4 shadow-sm sm:p-5 xl:col-span-7">
            <div class="flex flex-wrap items-start justify-between gap-4">
              <div>
                <h2 class="text-base font-semibold text-foreground">{{ t('admin.dashboard.groups.title') }}</h2>
                <p class="mt-1 text-sm text-muted-foreground">{{ groupSubtitle }}</p>
              </div>
              <div class="flex flex-wrap items-center justify-end gap-2">
                <div
                  class="inline-flex items-center rounded-lg border border-border/60 bg-surface/50 p-1"
                  role="group"
                  :aria-label="t('admin.dashboard.groups.modeLabel')"
                >
                  <button
                    v-for="mode in groupMetricModes"
                    :key="mode"
                    type="button"
                    :aria-pressed="groupMetricMode === mode"
                    class="rounded-md px-2.5 py-1.5 text-xs font-medium transition-colors sm:text-sm"
                    :class="groupMetricMode === mode ? 'bg-card text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'"
                    @click="groupMetricMode = mode"
                  >
                    {{ t(`admin.dashboard.groups.mode${mode === 'profit' ? 'Profit' : 'Revenue'}`) }}
                  </button>
                </div>
                <button
                  type="button"
                  class="inline-flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm font-medium text-primary transition-colors hover:bg-primary/10"
                  @click="openGroupList"
                >
                  <Layers3 class="h-4 w-4" />
                  {{ t('admin.dashboard.groups.total', { count: groupCount ?? 0 }) }}
                </button>
              </div>
            </div>

            <p v-if="groupFallbackText" class="mt-3 text-xs text-warning">{{ groupFallbackText }}</p>
            <div v-if="activeGroupLoading && !activeGroupData" class="flex h-[280px] items-center justify-center text-muted-foreground">
              <Loader2 class="h-5 w-5 animate-spin" />
            </div>
            <div v-else-if="activeGroupLoadError && !activeGroupData" class="flex h-[280px] flex-col items-center justify-center gap-3 text-sm text-muted-foreground">
              <AlertTriangle class="h-5 w-5 text-warning" />
              <span>{{ t('admin.dashboard.groups.loadError') }}</span>
              <button type="button" class="text-sm font-medium text-primary hover:underline" @click="loadOperationalData">
                {{ t('admin.dashboard.retry') }}
              </button>
            </div>

            <div v-else>
              <div v-if="groupMetricMode === 'profit' && !groupProfitAvailable" class="flex h-[280px] items-center justify-center text-sm text-muted-foreground">
                {{ t('admin.dashboard.groups.profitUnavailable') }}
              </div>
              <div v-else-if="topGroups.length > 0" class="mt-4 h-[280px]">
                <DashboardEChart
                  :option="groupChartOption"
                  :accessible-label="t(groupMetricMode === 'profit' ? 'admin.dashboard.groups.chartAriaProfit' : 'admin.dashboard.groups.chartAriaRevenue')"
                />
              </div>
              <div v-else class="flex h-[280px] items-center justify-center text-sm text-muted-foreground">
                {{ t('admin.dashboard.groups.empty') }}
              </div>
            </div>

            <div class="flex flex-wrap items-center justify-between gap-2 border-t border-border/60 pt-4 text-sm">
              <span class="text-muted-foreground">{{ groupTopThreeLabel }}</span>
              <span class="font-semibold tabular-nums text-foreground">
                {{ groupConcentration == null ? t('admin.dashboard.common.unavailable') : percentFormatter.format(groupConcentration) }}
              </span>
            </div>
          </article>

          <aside class="min-w-0 self-start rounded-lg border border-border/60 bg-card p-4 shadow-sm sm:p-5 xl:col-span-5">
            <div class="flex items-start justify-between gap-3">
              <div>
                <h2 class="text-base font-semibold text-foreground">{{ t('admin.dashboard.attention.title') }}</h2>
                <p class="mt-1 text-sm text-muted-foreground">{{ t('admin.dashboard.attention.subtitle') }}</p>
              </div>
              <button
                type="button"
                class="rounded-md p-2 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:opacity-50"
                :disabled="operationalLoading"
                :title="t('admin.dashboard.attention.refresh')"
                :aria-label="t('admin.dashboard.attention.refresh')"
                @click="loadOperationalData"
              >
                <RefreshCw class="h-4 w-4" :class="{ 'animate-spin': operationalLoading }" />
              </button>
            </div>

            <div v-if="operationalLoading && !healthSummary && !balanceBreakdown" class="flex h-[260px] items-center justify-center text-muted-foreground">
              <Loader2 class="h-5 w-5 animate-spin" />
            </div>
            <div v-else-if="attentionItems.length > 0" class="mt-5 divide-y divide-border/60 border-y border-border/60">
              <button
                v-for="item in attentionItems"
                :key="item.key"
                type="button"
                class="flex w-full items-center gap-3 py-4 text-left transition-colors hover:bg-muted/40"
                @click="router.push({ name: item.routeName })"
              >
                <span :class="['flex h-9 w-9 shrink-0 items-center justify-center rounded-lg', item.tone]">
                  <component :is="item.icon" class="h-4 w-4" />
                </span>
                <span class="min-w-0 flex-1">
                  <span class="flex items-center gap-2">
                    <span class="truncate text-sm font-medium text-foreground">{{ item.title }}</span>
                    <span class="rounded-full bg-muted px-2 py-0.5 text-xs font-semibold tabular-nums text-foreground">{{ item.count }}</span>
                  </span>
                  <span class="mt-1 block text-xs leading-5 text-muted-foreground">{{ item.description }}</span>
                </span>
                <ArrowRight class="h-4 w-4 shrink-0 text-muted-foreground" />
              </button>
            </div>
            <div v-else-if="attentionDataUnavailable" class="flex min-h-[260px] flex-col items-center justify-center text-center">
              <span class="flex h-12 w-12 items-center justify-center rounded-full bg-warning/10 text-warning">
                <AlertTriangle class="h-6 w-6" />
              </span>
              <h3 class="mt-4 text-sm font-semibold text-foreground">{{ t('admin.dashboard.attention.unavailableTitle') }}</h3>
              <p class="mt-1 max-w-xs text-sm leading-6 text-muted-foreground">{{ t('admin.dashboard.attention.unavailableDescription') }}</p>
              <button type="button" class="mt-3 text-sm font-medium text-primary hover:underline" @click="loadOperationalData">
                {{ t('admin.dashboard.retry') }}
              </button>
            </div>
            <div v-else class="flex min-h-[260px] flex-col items-center justify-center text-center">
              <span class="flex h-12 w-12 items-center justify-center rounded-full bg-signal/10 text-signal">
                <CircleCheckBig class="h-6 w-6" />
              </span>
              <h3 class="mt-4 text-sm font-semibold text-foreground">{{ t('admin.dashboard.attention.allClearTitle') }}</h3>
              <p class="mt-1 max-w-xs text-sm leading-6 text-muted-foreground">{{ t('admin.dashboard.attention.allClearDescription') }}</p>
            </div>

            <div class="mt-4 flex flex-wrap items-center justify-between gap-2 border-t border-border/60 pt-4 text-xs text-muted-foreground">
              <span>{{ t('admin.dashboard.attention.lastProbe', { time: lastProbeLabel }) }}</span>
              <span v-if="operationalLoadError" class="text-destructive">{{ t('admin.dashboard.attention.partialLoadError') }}</span>
            </div>
          </aside>
        </section>
      </template>
    </template>

    <div
      v-else-if="!adminModalOpen"
      class="flex flex-col items-center justify-center gap-4 rounded-lg border border-dashed border-border/60 bg-card/40 px-6 py-16 text-center"
    >
      <div class="flex h-14 w-14 items-center justify-center rounded-full bg-muted text-muted-foreground">
        <Lock class="h-6 w-6" />
      </div>
      <div class="space-y-1.5">
        <h2 class="text-lg font-semibold text-foreground">{{ t('admin.dashboard.adminAuth.dataLocked.title') }}</h2>
        <p class="max-w-md text-sm text-muted-foreground">{{ t('admin.dashboard.adminAuth.dataLocked.description') }}</p>
      </div>
      <button
        type="button"
        class="inline-flex items-center gap-1.5 rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90"
        @click="openAdminModal"
      >
        <ShieldCheck class="h-4 w-4" />
        {{ t('admin.dashboard.adminAuth.login') }}
      </button>
    </div>

    <AdminLoginModal
      :open="adminModalOpen"
      :submitting="adminSubmitting"
      :error-key="adminErrorKey"
      :initial-value="adminLoginInitialValue"
      @submit="submitAdminLogin"
      @close="closeAdminModal"
    />
    <BalanceFilterModal
      :open="balanceFilterOpen"
      :platform="balanceFilterPlatform"
      @close="closeBalanceFilter"
      @saved="onBalanceFilterSaved"
    />
    <GroupUsageTodayModal :open="groupUsageTodayOpen" @close="closeGroupUsageToday" />
    <UpstreamKeyUsageTodayModal :open="upstreamKeyUsageTodayOpen" @close="closeUpstreamKeyUsageToday" />
    <UpstreamBalanceBreakdownModal :open="upstreamBalanceBreakdownOpen" @close="closeUpstreamBalanceBreakdown" />
    <!-- 每日明细面板 -->
    <div
      v-if="dailyStatsPanelOpen"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/50"
      @click.self="closeDailyStatsPanel"
    >
      <div class="bg-background rounded-xl shadow-2xl w-full max-w-4xl max-h-[85vh] overflow-hidden flex flex-col mx-4">
        <div class="flex items-center justify-between px-5 py-4 border-b border-border">
          <h2 class="text-base font-semibold">{{ t('admin.dashboard.dailyStats.title') }}</h2>
          <button class="text-muted-foreground hover:text-foreground" aria-label="关闭" @click="closeDailyStatsPanel">✕</button>
        </div>
        <div class="p-5 overflow-y-auto flex-1">
          <DailyStatsPanel />
        </div>
      </div>
    </div>
  </div>
</template>
