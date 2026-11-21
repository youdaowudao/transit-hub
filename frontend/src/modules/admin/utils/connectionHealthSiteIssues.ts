import type { AccountFocusLabel, AdminGroupHealth } from '../types/connectionHealth'
import type { RealConnection } from '../types/mySites'
import type { UpstreamSiteResponse } from '../types/upstream'
import { accountNameText, displayAccountName } from './connectionHealthAccountLabel'
import { parseUpstreamLink } from './upstream'

export type BoundSiteRateState =
  | { kind: 'idle' }
  | { kind: 'ready'; sites: UpstreamSiteResponse[]; connections: RealConnection[]; boundIds: Set<string>; accountLabels: Record<string, AccountFocusLabel> }
  | { kind: 'failed'; failedSites: boolean; failedConnections: boolean; reasons: string[]; accountLabels: Record<string, AccountFocusLabel> }

export const isSub2APIBinding = (connection: RealConnection): boolean =>
  !connection.adminPlatform || connection.adminPlatform === 'sub2api'

const loginErrors = new Set(['loginRejected', 'loginIncomplete', 'refreshTokenRejected', 'accessTokenRejected', 'auth'])
const networkErrors = new Set(['networkTimeout', 'networkUnreachable', 'tlsFailed', 'network'])
const siteFailureReasons = new Set(['site_unavailable', 'snapshot_stale', 'snapshot_updating', 'groups_unavailable'])

export const isSiteLevelPriorityFailure = (reason: string): boolean => siteFailureReasons.has(reason)

export const siteWebsiteLink = (site?: UpstreamSiteResponse): string =>
  parseUpstreamLink(site?.websiteUrl).href || parseUpstreamLink(site?.baseUrl).href

export const collectConnectionHealthSiteIssues = (state: BoundSiteRateState, groups: AdminGroupHealth[]) => {
  if (state.kind !== 'ready') return []

  const accountsBySite = new Map<string, Map<string, RealConnection>>()
  for (const connection of state.connections) {
    if (!connection || !isSub2APIBinding(connection) || (connection.status && connection.status !== 'active')) continue
    const { upstreamSiteId, adminAccountId } = connection
    if (!upstreamSiteId) continue
    const accounts = accountsBySite.get(upstreamSiteId) ?? new Map<string, RealConnection>()
    if (!accounts.has(adminAccountId) || !accounts.get(adminAccountId)?.adminAccountName) accounts.set(adminAccountId, connection)
    accountsBySite.set(upstreamSiteId, accounts)
  }

  const problemSites = state.sites.filter(site => site?.enabled !== false && site?.status === 'error' && accountsBySite.has(site.id))
  if (!problemSites.length) return []

  const accountsById = new Map<string, { name: string; groups: Map<string, string> }>()
  for (const group of groups) {
    for (const account of group.accounts ?? []) {
      const entry = accountsById.get(account.id) ?? { name: '', groups: new Map<string, string>() }
      entry.name = accountNameText(entry.name, account.name)
      entry.groups.set(group.id, group.name)
      accountsById.set(account.id, entry)
    }
  }

  return problemSites.map(site => {
    const bindings = [...(accountsBySite.get(site.id)?.values() ?? [])]
    const error = site.errorKey?.split('.').at(-1) ?? ''
    return {
      id: site.id,
      name: site.name,
      issueKey: site.errorKey ?? '',
      category: loginErrors.has(error) ? 'login' : networkErrors.has(error) ? 'network' : 'other',
      href: siteWebsiteLink(site),
      count: bindings.length,
      accounts: bindings.slice(0, 5).map(binding => {
        const account = accountsById.get(binding.adminAccountId)
        return { name: displayAccountName(account?.name, binding.adminAccountName), groups: [...(account?.groups.values() ?? [])] }
      }),
    }
  })
}
