import type { UpstreamSite } from '../types/upstream'

export type UpstreamSortField = 'balance' | 'todayConsume' | 'historyRecharge'
export type UpstreamSortDirection = 'asc' | 'desc'

const metricCnyValue = (site: UpstreamSite, field: UpstreamSortField): number | null => {
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

    const firstValue = metricCnyValue(first, field)
    const secondValue = metricCnyValue(second, field)
    if (firstValue === null || secondValue === null) {
      if (firstValue === null && secondValue === null) return compareSitesByName(first, second)
      return firstValue === null ? 1 : -1
    }

    const diff = direction === 'asc' ? firstValue - secondValue : secondValue - firstValue
    if (diff !== 0) return diff
    return compareSitesByName(first, second)
  })
}
