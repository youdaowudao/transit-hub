import { shallowRef } from 'vue'
import type { AdminGroupHealth, TestProtocol } from '../types/connectionHealth'
import { mergeConnectionHealthGroupOrder, readConnectionHealthPreferences } from '../utils/connectionHealthPreferences'

type ChanshuiProtocol = 'chat' | 'openai' | 'anthropic'
interface CatalogAccount {
  targetId: string
  accountId: string
  accountName: string
  priority?: number
  models?: string
  protocol?: ChanshuiProtocol
}
interface CatalogGroup {
  id: string
  name: string
  accountsError?: boolean
  accounts: CatalogAccount[]
}
export interface ChanshuiCatalog {
  workspaceId: string
  groups: CatalogGroup[]
}

export const chanshuiCatalog = shallowRef<ChanshuiCatalog | null>(null)
let session = 0
export const chanshuiCatalogSession = () => session
const protocols: Record<TestProtocol, ChanshuiProtocol> = {
  chat_completions: 'chat', responses: 'openai', messages: 'anthropic',
}
const compareAccounts = (a: CatalogAccount, b: CatalogAccount) => {
  const priorityA = a.priority ?? Infinity, priorityB = b.priority ?? Infinity
  if (priorityA !== priorityB) return priorityA - priorityB
  return a.targetId < b.targetId ? -1 : Number(a.targetId > b.targetId)
}

// hiddenTargetIds 是分组健康页已读到的屏蔽清单；被屏蔽账号与分组健康账号表一样不进入检测名单。
export function publishChanshuiCatalog(workspaceId: string, groups: AdminGroupHealth[], sourceSession: number, hiddenTargetIds: ReadonlySet<string> = new Set()) {
  // Data from a read that began in the previous session cannot be published.
  if (sourceSession !== session) return
  const next: ChanshuiCatalog = { workspaceId, groups: groups.map(group => {
    const accounts = new Map<string, CatalogAccount>()
    if (!group.accountsError) {
      for (const account of group.accounts) {
        if (!account.targetId || accounts.has(account.targetId) || hiddenTargetIds.has(account.targetId)) continue
        const protocol = account.testConfiguration?.protocol
        accounts.set(account.targetId, {
          targetId: account.targetId,
          accountId: account.id,
          accountName: account.name || account.id,
          priority: Number.isFinite(account.priority) ? account.priority : undefined,
          models: account.models,
          protocol: protocol ? protocols[protocol] : undefined,
        })
      }
    }
    return { id: group.id, name: group.name, accountsError: Boolean(group.accountsError), accounts: [...accounts.values()].sort(compareAccounts) }
  }) }
  if (JSON.stringify(next) !== JSON.stringify(chanshuiCatalog.value)) chanshuiCatalog.value = next
}

export function clearChanshuiCatalogForWorkspace(workspaceId: string) {
  if (chanshuiCatalog.value?.workspaceId !== workspaceId) chanshuiCatalog.value = null
}

export function clearChanshuiCatalogForSession() {
  session++
  chanshuiCatalog.value = null
}

export const chanshuiCatalogStorageKey = (workspaceId: string) => `transithub.chanshui.catalog.v1:${workspaceId}`
export const chanshuiCatalogStorageWarning = '本次名单可以使用，但浏览器没能记住它。整页刷新后可能显示上次保存的旧名单，也可能需要重新读取；请检查本站存储空间和浏览器设置。'
const invalidCatalog = () => new Error('返回的分组账号名单格式或归属不正确，请检查后端与前端版本后重试')
const record = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value)
const identifier = (value: unknown): value is string => typeof value === 'string' && !!value.trim()

// Rebuild the public whitelist: neither local records nor HTTP results may carry
// credentials or become execution authority. Health's existing memory publisher
// deliberately does not call this persistence path.
export function validateChanshuiCatalog(value: unknown, workspaceId: string): ChanshuiCatalog {
  if (!identifier(workspaceId) || !record(value) || value.workspaceId !== workspaceId || !Array.isArray(value.groups)) throw invalidCatalog()
  const groupIds = new Set<string>()
  const groups: CatalogGroup[] = value.groups.map(raw => {
    if (!record(raw) || !identifier(raw.id) || groupIds.has(raw.id) || typeof raw.name !== 'string' || !Array.isArray(raw.accounts)
      || (raw.accountsError !== undefined && typeof raw.accountsError !== 'boolean') || (raw.accountsError && raw.accounts.length)) throw invalidCatalog()
    groupIds.add(raw.id)
    const targets = new Set<string>()
    const accounts: CatalogAccount[] = raw.accounts.map(item => {
      if (!record(item) || !identifier(item.accountId) || typeof item.accountName !== 'string'
        || item.targetId !== `sub2api:${workspaceId}:${item.accountId}` || targets.has(item.targetId as string)
        || (item.priority !== undefined && (typeof item.priority !== 'number' || !Number.isFinite(item.priority)))
        || (item.models !== undefined && typeof item.models !== 'string')
        || (item.protocol !== undefined && !['chat', 'openai', 'anthropic'].includes(item.protocol as string))) throw invalidCatalog()
      targets.add(item.targetId as string)
      return {
        targetId: item.targetId as string, accountId: item.accountId, accountName: item.accountName,
        ...(item.priority === undefined ? {} : { priority: item.priority as number }),
        ...(item.models === undefined ? {} : { models: item.models as string }),
        ...(item.protocol === undefined ? {} : { protocol: item.protocol as ChanshuiProtocol }),
      }
    })
    return { id: raw.id, name: raw.name, ...(raw.accountsError === undefined ? {} : { accountsError: raw.accountsError as boolean }), accounts }
  })
  return { workspaceId, groups }
}

export function restoreChanshuiCatalog(workspaceId: string): ChanshuiCatalog | null {
  if (chanshuiCatalog.value?.workspaceId === workspaceId) return chanshuiCatalog.value
  let raw: string | null
  try { raw = window.localStorage.getItem(chanshuiCatalogStorageKey(workspaceId)) } catch { return null }
  if (raw === null) return null
  try {
    const restored = validateChanshuiCatalog(JSON.parse(raw), workspaceId)
    chanshuiCatalog.value = restored
    return restored
  } catch {
    try { window.localStorage.removeItem(chanshuiCatalogStorageKey(workspaceId)) } catch { /* Reading a fresh list remains available. */ }
    return null
  }
}

export function acceptChanshuiCatalog(workspaceId: string, value: unknown, sourceSession: number, stillCurrent: () => boolean): { catalog: ChanshuiCatalog; warning: string } | null {
  if (sourceSession !== session || !stillCurrent()) return null
  const next = validateChanshuiCatalog(value, workspaceId)
  const preferences = readConnectionHealthPreferences(workspaceId)
  const order = chanshuiCatalog.value?.workspaceId === workspaceId ? chanshuiCatalog.value.groups.map(group => group.id) : preferences.groupOrder
  const byId = new Map(next.groups.map(group => [group.id, group]))
  next.groups = mergeConnectionHealthGroupOrder(order, next.groups.map(group => group.id))
    .filter(id => !preferences.hiddenGroupIds.includes(id))
    .map(id => { const group = byId.get(id)!; return { ...group, accounts: [...group.accounts].sort(compareAccounts) } })
  if (sourceSession !== session || !stillCurrent()) return null
  if (JSON.stringify(next) !== JSON.stringify(chanshuiCatalog.value)) chanshuiCatalog.value = next
  if (!stillCurrent()) return null
  try {
    const raw = JSON.stringify(next), key = chanshuiCatalogStorageKey(workspaceId)
    if (window.localStorage.getItem(key) !== raw) window.localStorage.setItem(key, raw)
    return { catalog: next, warning: '' }
  } catch {
    return { catalog: next, warning: chanshuiCatalogStorageWarning }
  }
}
