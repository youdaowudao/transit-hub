import type { AccountFocusLabel } from '../types/connectionHealth'

export type AccountLabels = Readonly<Record<string, AccountFocusLabel>>
export interface HistoryAccountLabel {
  targetId: string
  batchId: string
  name: string
  note: string
}

// Only pass actual name fields here. Identifiers remain operation parameters.
export const accountNameText = (...candidates: unknown[]): string => {
  for (const value of candidates) {
    if (typeof value !== 'string') continue
    const name = value.replace(/[\u0000-\u001f\u007f-\u009f]/g, '').trim()
    if (name) return name
  }
  return ''
}

export const displayAccountName = (...candidates: unknown[]): string =>
  accountNameText(...candidates) || '账号名称暂未读到'

export const accountReminderLabel = (label?: Partial<AccountFocusLabel>): string => {
  const name = accountNameText(label?.name)
  const sites = [...new Set((label?.siteNames ?? []).map(value => accountNameText(value)).filter(Boolean))]
  return (name ? `账号「${name}」` : '账号（名称暂未读到）')
    + (sites.length ? `（网站「${sites.join('、')}」）` : '')
}

export const accountLabelForTarget = (labels: AccountLabels | undefined, targetId: string, ...names: unknown[]): AccountFocusLabel => ({
  name: accountNameText(...names, labels?.[targetId]?.name),
  siteNames: labels?.[targetId]?.siteNames ?? [],
})

export const currentAccountNames = (labels?: AccountLabels): Record<string, string> =>
  Object.fromEntries(Object.entries(labels ?? {}).flatMap(([id, label]) => {
    const name = accountNameText(label.name)
    return name ? [[id, name]] : []
  }))

export const historicalAccountLabel = (targetId: string, batchId: string, snapshotName: unknown, currentName: unknown): HistoryAccountLabel => {
  const snapshot = accountNameText(snapshotName), current = accountNameText(currentName)
  return {
    targetId, batchId, name: snapshot || current,
    note: snapshot ? '' : current ? '当时未记录账号名称，以下名称来自当前资料'
      : '当时未记录账号名称。请结合本次分组、计划、模型和执行时间核对；需要进一步定位时，请维护人员按批次和记录编号查后台日志。',
  }
}

export const missingAccountNameHelp = '账号名称暂未读到。请先刷新本页；仍未读到时，到分组倍率核对这条对接，或请维护人员按提醒时间检查后台记录。'
