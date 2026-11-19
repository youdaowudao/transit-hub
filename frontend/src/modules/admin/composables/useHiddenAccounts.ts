import { computed, ref } from 'vue'
import { t, te } from '@/locales'
import {
  hideAccount,
  listHiddenAccounts,
  unhideAccount,
  type HiddenAccount,
  type HiddenAccountWriteOutcome,
} from '../api/connectionHealth'
import { connectionHealthMessageKey } from './useConnectionHealth'

// 保存并重读当前工作区的屏蔽清单，忽略旧工作区迟到的响应。

export type HiddenListStatus = 'never' | 'loaded' | 'failed'

const knownReason = (key: string | undefined): string | null => {
  if (!key) return null
  const mapped = connectionHealthMessageKey(key, te)
  return te(mapped) ? t(mapped) : null
}

export const hiddenWriteReason = (outcome: HiddenAccountWriteOutcome): string => {
  if (outcome.kind === 'saved') return ''
  if (outcome.kind === 'rejected') return outcome.detail || knownReason(outcome.errorKey) || `服务器返回了无法识别的原因（HTTP ${outcome.status}）`
  if (outcome.reason === 'network') return '网络中断或超时'
  if (outcome.reason === 'unparseable') return '服务器返回了无法识别的结果'
  return knownReason(outcome.errorKey) || `服务器返回了无法识别的原因（HTTP ${outcome.status ?? '未知'}）`
}

// 按错误名称识别读取错误（不引用类本身），接口模块被测试整体替换时也不会在错误处理里再抛错。
type ReadErrorShape = { name: string; reason?: string; status?: number; errorKey?: string; detail?: string }
export const hiddenReadReason = (error: unknown): string => {
  const shaped = error as ReadErrorShape | null
  if (shaped && typeof shaped === 'object' && shaped.name === 'HiddenAccountsReadError') {
    if (shaped.reason === 'workspace') return shaped.detail || '当前工作区已变化，请重新选择工作区后重新读取'
    if (shaped.reason === 'network') return '网络中断或超时'
    if (shaped.reason === 'unparseable') return '服务器返回了无法识别的结果'
    return knownReason(shaped.errorKey) || `服务器返回了无法识别的原因（HTTP ${shaped.status ?? '未知'}）`
  }
  if (error instanceof Error) return knownReason(error.message) || '服务器返回了无法识别的结果'
  return '服务器返回了无法识别的结果'
}

export function useHiddenAccounts() {
  const items = ref<HiddenAccount[]>([])
  const status = ref<HiddenListStatus>('never')
  const readError = ref('')
  const writing = ref(false)
  const unconfirmed = ref('')
  let workspace = ''
  let readSequence = 0
  // 每次重置（进入页面、切换工作区）换一个会话代次；旧会话的读取、写入结果和提示一律丢弃。
  let session = 0
  // 写锁跟着实际请求走：切换工作区不提前释放，避免旧请求晚到覆盖用户之后的操作。
  let pendingWrite = false

  const hiddenTargetIds = computed(() => new Set(items.value.map(item => item.targetId)))

  const reset = (nextWorkspace: string) => {
    workspace = nextWorkspace
    session++
    readSequence++
    items.value = []
    status.value = 'never'
    readError.value = ''
    unconfirmed.value = ''
    writing.value = pendingWrite
  }

  /** 读取清单。manual=true 表示用户点了「重新读取」：只有这种读取成功才解除“结果未确认”提示。 */
  const load = async (manual = false): Promise<boolean> => {
    const owner = workspace
    const ownerSession = session
    if (!owner) return false
    const sequence = ++readSequence
    try {
      const result = await listHiddenAccounts(owner)
      if (sequence !== readSequence || ownerSession !== session) return false
      if (result.workspaceId !== owner) return false
      items.value = result.items
      status.value = 'loaded'
      readError.value = ''
      if (manual) unconfirmed.value = ''
      return true
    } catch (error) {
      if (error instanceof Error && error.name === 'AbortError') return false
      if (sequence !== readSequence || ownerSession !== session) return false
      readError.value = hiddenReadReason(error)
      if (status.value !== 'loaded') status.value = 'failed'
      else status.value = 'loaded'
      return false
    }
  }

  /** 屏蔽或取消屏蔽。页面同一时间只允许一个写入；写入开始和结束各作废一次在途读取。 */
  const write = async (targetId: string, hidden: boolean, accountName: string): Promise<{ outcome: HiddenAccountWriteOutcome; reread: boolean } | null> => {
    if (writing.value || !workspace) return null
    const ownerSession = session
    const ownerWorkspace = workspace
    writing.value = true
    pendingWrite = true
    readSequence++
    let outcome: HiddenAccountWriteOutcome
    try {
      outcome = hidden ? await hideAccount(targetId, accountName) : await unhideAccount(targetId)
    } finally {
      pendingWrite = false
      writing.value = false
    }
    if (ownerSession !== session) {
      // 旧会话的结果与提示丢弃；若已切回该请求所属的工作区，为当前会话重读一次清单。
      if (workspace === ownerWorkspace) void load()
      return null
    }
    readSequence++
    if (outcome.kind === 'saved') {
      if (hidden) {
        const next = items.value.filter(item => item.targetId !== targetId)
        items.value = [{ targetId, accountName, hiddenAt: outcome.hiddenAt ?? new Date().toISOString() }, ...next]
      } else {
        items.value = items.value.filter(item => item.targetId !== targetId)
      }
      const reread = await load()
      if (ownerSession !== session) return null
      return { outcome, reread }
    }
    if (outcome.kind === 'unknown') {
      unconfirmed.value = hiddenWriteReason(outcome)
      await load()
      if (ownerSession !== session) return null
      return { outcome, reread: false }
    }
    return { outcome, reread: false }
  }

  const nameFor = (targetId: string): string => items.value.find(item => item.targetId === targetId)?.accountName ?? ''

  return { items, status, readError, writing, unconfirmed, hiddenTargetIds, reset, load, write, nameFor }
}
