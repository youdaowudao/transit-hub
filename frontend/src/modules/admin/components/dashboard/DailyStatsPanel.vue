<template>
  <div class="space-y-4">
    <!-- 快捷入口 + 区间选择 -->
    <div class="flex flex-wrap gap-2 items-center">
      <button
        v-for="sc in shortcuts"
        :key="sc.label"
        class="px-3 py-1 text-xs rounded-md border border-border hover:bg-accent/10 transition-colors"
        :class="activeShortcut === sc.label ? 'bg-primary/10 text-primary border-primary' : ''"
        @click="applyShortcut(sc)"
      >{{ sc.label }}</button>
      <div class="flex items-center gap-2 ml-auto">
        <input
          v-model="fromInput"
          type="date"
          class="text-xs px-2 py-1 border border-border rounded-md bg-background"
          :max="toInput"
          @change="loadStats"
        />
        <span class="text-muted-foreground text-xs">—</span>
        <input
          v-model="toInput"
          type="date"
          class="text-xs px-2 py-1 border border-border rounded-md bg-background"
          :min="fromInput"
          :max="todayStr"
          @change="loadStats"
        />
      </div>
    </div>

    <!-- 加载/错误状态 -->
    <div v-if="loading" class="text-sm text-muted-foreground py-4 text-center">
      {{ t('admin.dashboard.dailyStats.title') }}...
    </div>
    <div v-else-if="loadError" class="text-sm text-destructive py-4 text-center">
      {{ t('admin.dashboard.dailyStats.loadError') }}
    </div>

    <!-- 数据表格 -->
    <div v-else-if="items.length > 0" class="overflow-x-auto">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b border-border text-muted-foreground text-xs">
            <th class="text-left py-2 pr-4 font-medium">{{ t('admin.dashboard.dailyStats.colDate') }}</th>
            <th class="text-right py-2 px-2 font-medium">{{ t('admin.dashboard.dailyStats.colRevenue') }}</th>
            <th class="text-right py-2 px-2 font-medium">{{ t('admin.dashboard.dailyStats.colCost') }}</th>
            <th class="text-right py-2 px-2 font-medium">{{ t('admin.dashboard.dailyStats.colNetProfit') }}</th>
            <th class="text-right py-2 px-2 font-medium">调整后利润率</th>
            <th class="text-center py-2 px-2 font-medium">{{ t('admin.dashboard.dailyStats.colCoverage') }}</th>
            <th class="text-center py-2 pl-2 font-medium">{{ t('admin.dashboard.dailyStats.colStatus') }}</th>
          </tr>
        </thead>
        <tbody>
          <template v-for="item in items" :key="item.date">
            <tr
              class="border-b border-border/50 hover:bg-accent/5 cursor-pointer"
              :class="{ 'opacity-50': item.settlementStatus === 'missing' }"
              @click="toggleExpand(item.date)"
            >
              <td class="py-2 pr-4 font-mono text-xs">{{ item.date }}</td>
              <td class="text-right py-2 px-2">
                <span v-if="item.todayProfit != null">{{ formatCny(item.todayProfit) }}</span>
                <span v-else class="text-muted-foreground">—</span>
              </td>
              <td class="text-right py-2 px-2">
                <span v-if="item.operatingCost != null">{{ formatCny(item.operatingCost) }}</span>
                <span v-else class="text-muted-foreground" title="历史口径不完整">—</span>
              </td>
              <td class="text-right py-2 px-2">
                <span v-if="item.adjustedNetProfit != null" class="text-xs">{{ formatCny(item.adjustedNetProfit) }}</span>
                <span v-else class="text-muted-foreground">—</span>
              </td>
              <td class="text-right py-2 px-2"><span v-if="adjustedMargin(item) != null">{{ adjustedMargin(item)?.toFixed(1) }}%</span><span v-else class="text-muted-foreground">—</span></td>
              <td class="text-center py-2 px-2 text-xs">
                <span v-if="item.costCollectedCount != null && item.costExpectedCount != null">
                  {{ item.costCollectedCount }}/{{ item.costExpectedCount }}
                </span>
                <span v-else class="text-muted-foreground">—</span>
              </td>
              <td class="text-center py-2 pl-2">
                <span
                  class="inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium"
                  :class="statusClass(item.settlementStatus)"
                >{{ item.costQualityMode === 'retained' ? '已结算（沿用确认值）' : statusLabel(item.settlementStatus) }}</span>
              </td>
            </tr>
            <!-- 展开站点明细 -->
            <tr v-if="expandedDates.has(item.date) && item.siteCostsLoadError" class="bg-muted/30">
              <td colspan="7" class="py-2 pl-8 pr-4 text-xs text-destructive">
                {{ t('admin.dashboard.dailyStats.loadError') }}
              </td>
            </tr>
            <tr v-else-if="expandedDates.has(item.date)" class="bg-muted/30">
              <td colspan="7" class="py-2 pl-8 pr-4">
                <div v-if="item.additionalCosts" class="mb-3 grid gap-1 border-b border-border/60 pb-3 text-xs text-muted-foreground sm:grid-cols-2">
                  <span>充值手续费{{ item.additionalCosts.feeRate != null ? ` (${(item.additionalCosts.feeRate * 100).toFixed(2)}%)` : '' }}：{{ formatCny(item.additionalCosts.rechargeFee) }}</span>
                  <span>活动赠送摊销：{{ formatCny(item.additionalCosts.promotion) }}</span>
                  <span>服务器及固定费用：{{ formatCny(item.additionalCosts.fixed) }}</span>
                  <span>手工调整：{{ formatCny(item.additionalCosts.adjustment) }}</span>
                  <span>买号确认：{{ formatCny(item.additionalCosts.accountPurchase) }}</span>
                  <span>退款冲减：{{ formatCny(item.additionalCosts.accountRefund) }}</span>
                  <span>替代成本扣减：{{ item.replacementDeduction == null ? '暂无可靠数据' : formatCny(-item.replacementDeduction) }}</span>
                  <span>账号快照：{{ item.accountStatsQuality === 'complete' ? `${item.accountCompletedCount ?? 0}/${item.accountExpectedCount ?? 0} 完整` : '历史口径不完整' }}</span>
                  <span class="font-medium text-foreground">经营总成本：{{ formatCny(item.operatingCost) }}</span>
                  <span class="font-medium text-foreground">调整后净利润：{{ formatCny(item.adjustedNetProfit) }}</span>
                </div>
                <div v-if="item.siteCosts && item.siteCosts.length > 0" class="space-y-1">
                  <div
                    v-for="sc in item.siteCosts"
                    :key="sc.siteId"
                    class="flex items-center justify-between text-xs text-muted-foreground"
                  >
                    <span>{{ sc.siteName }} <span class="opacity-60">({{ sc.platform }})</span></span>
                    <span>
                      <span v-if="sc.adjustedCost != null">{{ formatCny(sc.adjustedCost) }}</span>
                      <span v-else class="text-destructive">{{ sc.errorReason || sc.status }}</span>
                      <span v-if="sc.lastAttemptError" class="ml-1 text-muted-foreground">({{ sc.lastAttemptError }})</span>
                    </span>
                  </div>
                </div>
                <div v-if="item.additionalCosts?.records?.length" class="mt-3 space-y-1 border-t border-border/60 pt-3 text-xs text-muted-foreground">
                  <div v-for="record in item.additionalCosts.records" :key="record.id" class="flex justify-between gap-4">
                    <span>{{ record.name }}{{ record.estimated ? '（预估）' : '' }}</span>
                    <span class="tabular-nums">{{ formatCny(record.amount) }}</span>
                  </div>
                </div>
                <p v-else-if="!item.additionalCosts" class="text-xs text-muted-foreground">{{ t('admin.dashboard.dailyStats.noData') }}</p>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>
    <div v-else class="text-sm text-muted-foreground py-4 text-center">
      {{ t('admin.dashboard.dailyStats.noData') }}
    </div>

    <!-- 分页 -->
    <div v-if="items.length > 0" class="flex justify-between items-center text-xs text-muted-foreground">
      <button
        class="px-3 py-1 border border-border rounded-md hover:bg-accent/10 disabled:opacity-40"
        :disabled="currentPage <= 1"
        @click="changePage(-1)"
      >‹</button>
      <span>{{ currentPage }}</span>
      <button
        class="px-3 py-1 border border-border rounded-md hover:bg-accent/10 disabled:opacity-40"
        :disabled="items.length < pageSize"
        @click="changePage(1)"
      >›</button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { getDailyStats, type DailyStatItem } from '../../api/dashboardAdmin'
import { formatCny } from '../../utils/dashboard'

import { t } from '@/locales'
// Shanghai 固定 UTC+8，无 DST。用加偏移量的方式获取上海业务日期。
function shanghaiDateStr(offsetDays = 0): string {
  const shanghaiMs = Date.now() + 8 * 60 * 60 * 1000
  const d = new Date(shanghaiMs + offsetDays * 24 * 60 * 60 * 1000)
  return d.toISOString().slice(0, 10)
}

const todayStr = shanghaiDateStr(0)

function daysAgo(n: number) {
  return shanghaiDateStr(-n)
}

interface Shortcut { label: string; from: string; to: string }
const shortcuts: Shortcut[] = [
  { label: t('admin.dashboard.dailyStats.shortcutYesterday'), from: daysAgo(1), to: daysAgo(1) },
  { label: t('admin.dashboard.dailyStats.shortcutDayBefore'), from: daysAgo(2), to: daysAgo(2) },
  { label: t('admin.dashboard.dailyStats.shortcut3DaysAgo'), from: daysAgo(3), to: daysAgo(3) },
  { label: t('admin.dashboard.dailyStats.last7Days'), from: daysAgo(7), to: daysAgo(1) },
  { label: t('admin.dashboard.dailyStats.last30Days'), from: daysAgo(30), to: daysAgo(1) },
]

const fromInput = ref(daysAgo(7))
const toInput = ref(daysAgo(1))
const activeShortcut = ref<string | null>(t('admin.dashboard.dailyStats.last7Days'))
const currentPage = ref(1)
const pageSize = 31
const items = ref<DailyStatItem[]>([])
const loading = ref(false)
const loadError = ref(false)
const expandedDates = ref(new Set<string>())

const adjustedMargin = (item: DailyStatItem): number | null => (
  item.todayProfit != null && item.todayProfit > 0 && item.adjustedNetProfit != null
    ? item.adjustedNetProfit / item.todayProfit * 100
    : null
)

function applyShortcut(sc: Shortcut) {
  fromInput.value = sc.from
  toInput.value = sc.to
  activeShortcut.value = sc.label
  currentPage.value = 1
  loadStats()
}

async function loadStats() {
  loading.value = true
  loadError.value = false
  try {
    const resp = await getDailyStats(fromInput.value, toInput.value, {
      page: currentPage.value,
      pageSize,
      expand: expandedDates.value.size > 0,
    })
    items.value = resp.items
  } catch {
    loadError.value = true
  } finally {
    loading.value = false
  }
}

function toggleExpand(date: string) {
  if (expandedDates.value.has(date)) {
    expandedDates.value.delete(date)
  } else {
    expandedDates.value.add(date)
    // 重新加载以获取站点明细。
    loadStats()
  }
}

function changePage(delta: number) {
  currentPage.value += delta
  loadStats()
}

function statusLabel(status: DailyStatItem['settlementStatus']) {
  const map: Record<string, string> = {
    final: t('admin.dashboard.dailyStats.statusFinal'),
    fallback: t('admin.dashboard.dailyStats.statusFallback'),
    partial_high: t('admin.dashboard.dailyStats.statusPartialHigh'),
    partial: t('admin.dashboard.dailyStats.statusPartial'),
    provisional: t('admin.dashboard.dailyStats.statusProvisional'),
    missing: t('admin.dashboard.dailyStats.statusMissing'),
  }
  return map[status] ?? status
}

function statusClass(status: DailyStatItem['settlementStatus']) {
  return {
    'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400': status === 'final',
    'bg-cyan-100 text-cyan-700 dark:bg-cyan-900/30 dark:text-cyan-400': status === 'partial_high',
    'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400': status === 'fallback',
    'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400': status === 'partial',
    'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400': status === 'provisional',
    'bg-muted text-muted-foreground': status === 'missing',
  }
}

onMounted(() => {
  loadStats()
})
</script>
