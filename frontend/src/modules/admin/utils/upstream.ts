import { t } from '@/locales'
import type { UpstreamSite } from '../types/upstream'

export type UpstreamSortField = 'balance' | 'todayConsume' | 'historyRecharge'
export type UpstreamSortDirection = 'asc' | 'desc'

const metricAmountValue = (site: UpstreamSite, field: UpstreamSortField): number | null => {
  const metric = site.metrics[field]
  if (metric.value === null || !Number.isFinite(metric.value) || site.rechargeRate <= 0 || !Number.isFinite(site.rechargeRate)) return null
  const value = metric.value * site.rechargeRate
  return Number.isFinite(value) ? value : null
}

const compareSitesByName = (first: UpstreamSite, second: UpstreamSite): number => {
  const nameDiff = first.name.localeCompare(second.name)
  if (nameDiff !== 0) return nameDiff
  return first.id.localeCompare(second.id)
}

export function sortUpstreamSites(
  sites: UpstreamSite[],
  field: UpstreamSortField,
  direction: UpstreamSortDirection,
): UpstreamSite[] {
  return [...sites].sort((first, second) => {
    if (first.enabled !== second.enabled) return first.enabled ? -1 : 1
    if (!first.enabled) return compareSitesByName(first, second)

    const firstValue = metricAmountValue(first, field)
    const secondValue = metricAmountValue(second, field)
    if (firstValue === null || secondValue === null) {
      if (firstValue === null && secondValue === null) return compareSitesByName(first, second)
      return firstValue === null ? 1 : -1
    }

    const diff = direction === 'asc' ? firstValue - secondValue : secondValue - firstValue
    if (diff !== 0) return diff
    return compareSitesByName(first, second)
  })
}

export const costFailureText = (reason?: string): string => {
  if (!reason) return ''
  if (/^(?:admin|auth|dashboard)\.[A-Za-z0-9_.]+$/.test(reason)) return t(reason)
  const known: Record<string, string> = {
    shared_source: '成本来源被多个分组共享，暂不归属',
    unresolved_source: '上游来源无法唯一确认',
    sample_decode_failed: '费用记录无法读取，请同步这个站点重试；不会修改主站。',
    retained_sample: '沿用最近一次已确认费用，可同步这个站点重新读取；不会修改主站。',
    auth_401: '上游未接受登录资料，请核查上游登录后同步这个站点重试；不会修改主站。',
    auth_403: '上游拒绝费用读取，具体原因尚未确认，请到上游核查当天消费记录后重试；不会修改主站。',
    network: '费用读取时无法连接上游，请检查连接后同步这个站点重试；不会修改主站。',
    invalid_response: '上游返回的费用字段或日期不完整，费用未更新；请到上游核查当天消费记录后同步这个站点重试；不会修改主站。',
    sample_unavailable: '今日费用暂无法读取，可同步这个站点重试；不会修改主站。',
    date_mismatch: '这份费用不属于新加坡今天，请同步这个站点重新读取；不会修改主站。',
    stale: '最近的费用记录已经过期，请同步这个站点重新读取；不会修改主站。',
    fetch_error: '今日费用暂无法读取，具体原因尚未确认，请到上游核查当天消费记录后同步这个站点重试；不会修改主站。',
  }
  return known[reason] ?? reason
}
