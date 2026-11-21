import { shallowRef } from 'vue'
import type { AdminGroupAccount, AdminGroupHealth, TestProtocol } from '../types/connectionHealth'

type ChanshuiProtocol = 'chat' | 'openai' | 'anthropic'
interface CatalogAccount {
  targetId: string
  accountId: string
  accountName: string
  priority?: number
  models?: string
  probeModels?: string[]
  protocol?: ChanshuiProtocol
}
interface CatalogGroup {
  id: string
  name: string
  accountsError?: boolean
  accounts: CatalogAccount[]
}
interface ChanshuiCatalog {
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

const catalogProbeModels = (account: AdminGroupAccount): string[] | undefined => {
  if (account.models?.split(',').some(model => model.trim())) return undefined

  const probeModels = [...(account.modelHealth ?? []), ...(account.unprobedModels ?? [])]
  const names = probeModels
    .filter(model => !model.modelControlClosed)
    .map(model => model.modelName.trim())
    .filter(Boolean)
  return names.length ? Array.from(new Set(names)) : undefined
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
          probeModels: catalogProbeModels(account),
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
