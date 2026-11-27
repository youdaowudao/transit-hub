import { onBeforeUnmount, ref, watch } from 'vue'
import { checkScopedRealConnections, ScopedCheckApiError } from '../api/mySites'
import type { ScopedCheckResult } from '../types/mySites'
import { t, te } from '@/locales'
import { safeScopedErrorText } from '../utils/scopedErrorText'

type Account = { id: string; createdAt?: string; platform?: string } | null | undefined
const safe = (value: unknown) => { const text = typeof value === 'string' ? value : ''; return safeScopedErrorText(te(text) ? t(text) : text) }
export const accountInventorySummary = (result: ScopedCheckResult) => {
  const statuses = Object.values(result.confirmedStatuses)
  return `本次确认 ${statuses.filter(v => v === 'active').length} 条对接的主站账号仍在、${statuses.filter(v => v === 'missing').length} 条已删除；${result.unconfirmedConnectionIds.length + result.changedConnectionIds.length} 条未确认，${result.skippedConnectionIds.length} 条未检查。未删除任何记录、未修改主站或上游 Key。`
}

/** The same explicit inventory operation is used on all three buttons; no polling. */
export function useMainAccountInventoryCheck(account: () => Account, reload: (current: () => boolean) => Promise<boolean>) {
  const busy = ref(false)
  const site = ref('')
  const summary = ref('')
  const error = ref('')
  let controller: AbortController | null = null
  let generation = 0
  function reset() {
    generation++
    controller?.abort()
    controller = null
    busy.value = false
    site.value = ''
    summary.value = ''
    error.value = ''
  }
  watch(() => [account()?.id, account()?.createdAt, account()?.platform], reset, { flush: 'sync' })
  onBeforeUnmount(reset)
  async function run(siteId = '', owner: () => boolean = () => true) {
    const snapshot = account()
    if (busy.value || !snapshot?.id || !snapshot.createdAt || snapshot.platform !== 'sub2api' || !owner()) return
    const seq = ++generation
    const request = new AbortController()
    controller = request
    const current = () => seq === generation && controller === request && !request.signal.aborted && account()?.id === snapshot.id && account()?.createdAt === snapshot.createdAt && account()?.platform === 'sub2api' && owner()
    busy.value = true
    site.value = siteId
    summary.value = ''
    error.value = ''
    let result: ScopedCheckResult | undefined
    let failure = ''
    try {
      try {
        result = await checkScopedRealConnections({ workspaceAdminAccountId: snapshot.id, workspaceCreatedAt: snapshot.createdAt, mode: 'inventory', ...(siteId ? { siteId } : { workspaceWide: true }) }, request.signal)
      } catch (err) {
        if (!current()) return
        if (err instanceof ScopedCheckApiError) {
          result = err.result
          failure = [err.message, err.detail, err.next].map(safe).filter(Boolean).join('；')
        } else failure = `主站账号核对结果未能取得：${safe(err instanceof Error ? err.message : '') || '原因无法确认'}；请重新读取本地对接状态并查看Hub后端记录。`
      }
      if (!current()) return
      if (result) {
        summary.value = accountInventorySummary(result)
        const details = result.issues.map(issue => [issue.accountName, issue.detail, issue.next].map(safe).filter(Boolean).join('：'))
        failure = [failure, ...new Set(details)].filter(Boolean).join('；')
      }
      error.value = failure
      let read = false
      try { read = await reload(current) } catch { /* reload failure is reported below */ }
      if (!current()) return
      if (!read) {
        const reason = result?.confirmedConnectionIds.length && !result.storageUncertain
          ? '账号状态已保存，但页面未能读取最新状态；请重新打开或点击刷新，未执行重新对接。'
          : '页面未能读取最新对接状态，请重新打开并检查Hub连接；本次没有执行重新对接。'
        error.value = [error.value, reason].filter(Boolean).join('；')
      }
    } finally {
      if (seq === generation && controller === request) {
        controller = null
        busy.value = false
      }
    }
  }
  return { busy, site, summary, error, run, reset }
}
