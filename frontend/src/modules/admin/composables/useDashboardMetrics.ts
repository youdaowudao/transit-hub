// 仪表盘指标数据来源。
//
// 从后端 /api/dashboard/metrics 获取实时指标，
// 从 /api/dashboard/trends 获取历史快照，两者组合后驱动统计卡片与趋势图。

import { ref } from 'vue'
import type {
  DashboardColorToken,
  DashboardMetricData,
  DashboardMetricKey,
  TrendPoint,
} from '../types/dashboard'
import {
  getDashboardMetrics,
  getDashboardTrends,
  type DashboardMetricsResponse,
  type DashboardTrendPoint,
  type DashboardTrendsResponse,
} from '../api/dashboardAdmin'
import { selectDashboardTrendValue } from '../utils/dashboard'

const METRIC_CONFIGS: { key: DashboardMetricKey; color: DashboardColorToken }[] = [
  { key: 'todayProfit', color: 'primary' },
  { key: 'siteBalance', color: 'accent' },
  { key: 'todayPurchase', color: 'warning' },
  { key: 'netProfit', color: 'signal' },
  { key: 'upstreamBalance', color: 'primary' },
]

function dateLabel(dateStr: string | undefined): string {
  if (!dateStr) return ''
  const [, month, day] = dateStr.split('-').map(Number)
  if (!month || !day) return dateStr
  return `${month}/${day}`
}

function buildMetricData(
  key: DashboardMetricKey,
  color: DashboardColorToken,
  live: DashboardMetricsResponse,
  trendPoints: DashboardTrendPoint[],
): DashboardMetricData {
  // 对于 nullable 指标，取值时需区分 null（不可用）和 0（真实零值）。
  const rawValue = key === 'todayPurchase'
    ? live.operatingCost
    : key === 'netProfit'
      ? live.adjustedNetProfit
      : live[key]
  const current = typeof rawValue === 'number' ? rawValue : (rawValue ?? null)

  // 使用本地类型，允许 nullable 字段携带 null（live 数据来源可能为 null）
  type LivePoint = {
    date: string
    todayProfit: number | null
    siteBalance: number
    todayPurchase: number | null
    netProfit: number | null
    confirmedCost?: number | null
    netProfitCeiling?: number | null
    settlementStatus?: string
    costExpectedCount?: number | null
    costCollectedCount?: number | null
    costFreshCount?: number | null
    costRetainedCount?: number | null
    costMissingCount?: number | null
    costQualityMode?: string
    costMode?: string
    upstreamBalance: number
    operatingCost?: number | null
    adjustedNetProfit?: number | null
  }
  const pointsByDate = new Map<string, LivePoint>()
  for (const point of trendPoints) {
    if (point.date) pointsByDate.set(point.date, {
      date: point.date,
      todayProfit: point.todayProfit,
      siteBalance: point.siteBalance,
      todayPurchase: point.operatingCost ?? null,
      netProfit: point.adjustedNetProfit ?? null,
      confirmedCost: point.confirmedCost,
      netProfitCeiling: point.netProfitCeiling,
      settlementStatus: point.settlementStatus,
      costExpectedCount: point.costExpectedCount,
      costCollectedCount: point.costCollectedCount,
      costFreshCount: point.costFreshCount,
      costRetainedCount: point.costRetainedCount,
      costMissingCount: point.costMissingCount,
      costQualityMode: point.costQualityMode,
      costMode: point.costQualityMode,
      upstreamBalance: point.upstreamBalance,
      operatingCost: point.operatingCost,
      adjustedNetProfit: point.adjustedNetProfit,
    })
  }
  if (live.date) {
    // 保留 null，不再用 coerceNumeric 强转，与历史趋势点的处理保持一致。
    pointsByDate.set(live.date, {
      date: live.date,
      todayProfit: live.todayProfit,
      siteBalance: live.siteBalance,
      todayPurchase: live.operatingCost ?? null,
      netProfit: live.adjustedNetProfit ?? null,
      confirmedCost: live.operatingCost ?? null,
      netProfitCeiling: live.adjustedNetProfit ?? null,
      settlementStatus: live.settlementStatus,
      costExpectedCount: live.costQuality?.expectedSites,
      costCollectedCount: live.costQuality?.collectedSites,
      costFreshCount: live.costQuality?.freshSites,
      costRetainedCount: live.costQuality?.retainedSites,
      costMissingCount: live.costQuality?.missingSites,
      costQualityMode: live.costQuality?.mode,
      costMode: live.costQuality?.mode,
      upstreamBalance: live.upstreamBalance,
      operatingCost: live.operatingCost,
      adjustedNetProfit: live.adjustedNetProfit,
    })
  }

  const monthPoints: TrendPoint[] = Array.from(pointsByDate.values())
    .sort((a, b) => a.date.localeCompare(b.date))
    .map((p) => {
      const provisionalQuality = p.costMode === 'fallback' || p.costMode === 'retained'
        ? 'fallback'
        : key === 'todayPurchase' ? 'confirmed' : 'ceiling'
      const provisionalValue = key === 'todayPurchase'
        ? p.confirmedCost
        : key === 'netProfit'
          ? (p.costMode === 'fallback' || p.costMode === 'retained' ? p.netProfit : p.netProfitCeiling)
          : null
      const selected = selectDashboardTrendValue({
        formalValue: p[key] as number | null | undefined,
        provisionalValue,
        status: key === 'todayPurchase' || key === 'netProfit'
          ? p.settlementStatus ?? 'unavailable'
          : 'final',
        provisionalQuality,
      })
      return {
        label: dateLabel(p.date),
        value: selected.value,
        quality: selected.quality,
        status: p.settlementStatus,
        expected: p.costExpectedCount,
        collected: p.costCollectedCount,
        date: p.date,
      }
    })

  const week = monthPoints.slice(-7)
  const month = monthPoints.slice(-30)

  return {
    key,
    color,
    current: typeof current === 'number' ? current : null,  // 保留 null，不强转成 0
    error: live.metricErrors?.[key],
    series: { week, month },
  }
}

export function useDashboardMetrics() {
  const metrics = ref<DashboardMetricData[]>([])
  const liveData = ref<DashboardMetricsResponse | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)

  const fetchMetrics = async () => {
    loading.value = true
    error.value = null
    try {
      const [live, trends] = await Promise.all([
        getDashboardMetrics(),
        getDashboardTrends(30),
      ])
      liveData.value = live
      metrics.value = METRIC_CONFIGS.map(({ key, color }) =>
        buildMetricData(key, color, live, trends.points),
      )
    } catch (err) {
      error.value = err instanceof Error ? err.message : 'admin.dashboard.loadError'
    } finally {
      loading.value = false
    }
  }

  const applyRawData = (live: DashboardMetricsResponse, trends: DashboardTrendsResponse) => {
    liveData.value = live
    metrics.value = METRIC_CONFIGS.map(({ key, color }) =>
      buildMetricData(key, color, live, trends.points),
    )
  }

  return { metrics, liveData, loading, error, fetchMetrics, applyRawData }
}
