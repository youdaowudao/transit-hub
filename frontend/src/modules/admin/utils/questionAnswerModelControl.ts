import { ref } from 'vue'
import { t, te } from '@/locales'
import { addModelControlManaged } from '../api/connectionHealth'
import { formatConnectionHealthTime } from '../composables/useConnectionHealth'
import type { ModelControlCounts, ModelControlDecision, ModelControlEvent, ModelControlItem, ModelControlRound } from '../types/connectionHealth'

export const modelControlBusyTargets = ref(new Set<string>())
const busyOwners = new Map<string, symbol>()
export const acquireModelControlBusy = (targetId: string): symbol | null => {
  if (modelControlBusyTargets.value.has(targetId)) return null
  const owner = Symbol(targetId)
  busyOwners.set(targetId, owner)
  modelControlBusyTargets.value = new Set([...modelControlBusyTargets.value, targetId])
  return owner
}
export const releaseModelControlBusy = (targetId: string, owner: symbol) => {
  if (busyOwners.get(targetId) !== owner) return
  busyOwners.delete(targetId)
  const next = new Set(modelControlBusyTargets.value)
  next.delete(targetId)
  modelControlBusyTargets.value = next
}
export const modelControlReason = (key?: string): string => key ? (te(key) ? t(key) : te(`admin.connectionHealth.modelControl.reasons.${key}`) ? t(`admin.connectionHealth.modelControl.reasons.${key}`) : t('admin.connectionHealth.errors.request')) : ''
export const modelControlDecision = (decision: ModelControlDecision): string => t(`admin.connectionHealth.modelControl.decisions.${decision}`)
export const modelControlText = (key: string, params?: Record<string, string | number>): string => t(`admin.connectionHealth.modelControl.text.${key}`, params)
export const modelControlState = (item: ModelControlItem): string => item.control.conflictReason ? modelControlText('manualRequired') : item.control.pending ? modelControlPendingState(item.control.pending.state) : t(`admin.connectionHealth.modelControl.states.${item.control.observation.state}`)
export const modelControlPendingState = (state: string): string => t(`admin.connectionHealth.modelControl.pending.${['in_progress', 'not_completed', 'unknown'].includes(state) ? state : 'in_progress'}`)
export const modelControlTime = (value?: string | null): string => formatConnectionHealthTime(value ?? null)
export const modelControlRoundText = (round: ModelControlRound): string => `${round.source === 'manual' ? modelControlText('manualSource') : round.source === 'run_now' ? modelControlText('runNowSource') : modelControlText('scheduledSource')}${round.scheduleName ? ` · ${round.scheduleName}` : ''} · ${modelControlTime(round.createdAt)}`
export const modelControlEntryState = (state: string): string => te(`admin.connectionHealth.modelControl.entries.${state}`) ? t(`admin.connectionHealth.modelControl.entries.${state}`) : ''
export const modelControlRuleRevision = ref({ workspace: '', revision: 0 })
export const modelControlRuleChanged = (workspace: string) => { modelControlRuleRevision.value = { workspace, revision: modelControlRuleRevision.value.revision + 1 } }
export const modelControlEventLabel = (event: string): string => te(`admin.connectionHealth.modelControl.events.${event}`) ? t(`admin.connectionHealth.modelControl.events.${event}`) : modelControlText('eventFallback')
export const addManagedModel = (targetId: string, model: string, signal?: AbortSignal) => addModelControlManaged(targetId, model, signal)

export interface ModelControlVerifyIssue { reasonKey: string; at: number }
export interface ModelControlVerifyResult { items: ModelControlItem[]; errors: Array<{ targetId: string; reasonKey: string }> }
export const createModelControlVerifyState = ({ verify }: { verify: (ids: string[]) => Promise<ModelControlVerifyResult> }) => {
  const issues = ref(new Map<string, ModelControlVerifyIssue>())
  const unconfirmed = ref(new Map<string, number>())
  const issued = new Map<string, number>(), processed = new Map<string, number>()
  let generation = 0
  const handleVerifyResult = (result: ModelControlVerifyResult | unknown, targetIds: string[], seqs: Map<string, number>, issuedAt: number): ModelControlVerifyResult => {
    const accepted = new Set<string>(), nextIssues = new Map(issues.value), nextUnconfirmed = new Map(unconfirmed.value)
    const valid = Boolean(result && typeof result === 'object' && Array.isArray((result as ModelControlVerifyResult).items) && Array.isArray((result as ModelControlVerifyResult).errors))
    const response = valid ? result as ModelControlVerifyResult : null
    for (const targetId of targetIds) {
      const seq = seqs.get(targetId)
      if (seq === undefined || !issued.has(targetId) || seq < (processed.get(targetId) ?? 0)) continue
      processed.set(targetId, seq)
      accepted.add(targetId)
      const failure = response?.errors.find(error => error.targetId === targetId)
      if (!response || failure) { nextIssues.set(targetId, { reasonKey: failure?.reasonKey ?? 'request_failed', at: Date.now() }); continue }
      nextIssues.delete(targetId)
      for (const [key, at] of nextUnconfirmed) {
        const prefix = `${targetId}|`
        if (!key.startsWith(prefix) || issuedAt <= at) continue
        const model = key.slice(prefix.length)
        const item = response.items.find(item => item.targetId === targetId && item.modelName === model)
        if (item && !item.control.pending && !item.control.accountPending) nextUnconfirmed.delete(key)
      }
    }
    issues.value = nextIssues
    unconfirmed.value = nextUnconfirmed
    return { items: response?.items.filter(item => accepted.has(item.targetId)) ?? [], errors: response?.errors.filter(error => accepted.has(error.targetId)) ?? [] }
  }
  const verifyNow = async (targetIds: string[]) => {
    const ids = [...new Set(targetIds)], seqs = new Map<string, number>(), issuedAt = Date.now(), scope = generation
    for (const id of ids) { const seq = (issued.get(id) ?? 0) + 1; issued.set(id, seq); seqs.set(id, seq) }
    let result: unknown
    try { result = await verify(ids) } catch (error) { result = error }
    if (scope !== generation) return { items: [], errors: [] }
    return handleVerifyResult(result, ids, seqs, issuedAt)
  }
  return {
    issues, verifyNow, handleVerifyResult,
    markUnconfirmed(targetId: string, model: string) { unconfirmed.value = new Map(unconfirmed.value).set(`${targetId}|${model}`, Date.now()) },
    isUnconfirmed: (targetId: string, model: string) => unconfirmed.value.has(`${targetId}|${model}`),
    accountUnconfirmed: (targetId: string) => [...unconfirmed.value.keys()].some(key => key.startsWith(`${targetId}|`)),
    dispose() { generation++; issued.clear(); processed.clear(); issues.value = new Map(); unconfirmed.value = new Map() },
  }
}

const openIsolationReasons = new Set(['OpenAIPassthrough', 'EmptyMapping', 'WildcardMapping', 'WildcardFallback'])
export const modelControlSummaryStatus = (item: ModelControlItem): string => {
  if (Object.keys(item.control.closedEntries).length) return 'closed'
  const observation = item.control.observation
  if (['serving', 'last_model'].includes(observation.state)) return 'open'
  if (observation.state === 'not_isolatable' && openIsolationReasons.has(observation.reasonKey.replace(/^.*modelControl/, ''))) return 'open'
  return ['not_provided', 'account_missing'].includes(observation.state) ? observation.state : 'unknown'
}
export const modelControlItemCounts = (items: ModelControlItem[]): ModelControlCounts => items.reduce((counts, item) => {
  const state = modelControlSummaryStatus(item)
  counts.total++; if (state === 'open') counts.open++; if (state === 'closed') counts.closed++
  if (item.attention) counts.attention++; if (item.decision === 'no_evidence') counts.untested++
  return counts
}, { total: 0, open: 0, closed: 0, attention: 0, untested: 0 })
export const modelControlCardView = (item: ModelControlItem, opts: { issue?: ModelControlVerifyIssue; availableModels?: string[] | null; executeUnconfirmed?: boolean; accountUnconfirmed?: boolean }) => {
  const state = item.control.observation.state, decision = item.decision, closed = Object.keys(item.control.closedEntries).length > 0
  const summary = modelControlSummaryStatus(item), uncertain = Boolean(opts.executeUnconfirmed || opts.accountUnconfirmed)
  const isolationIsOpen = state === 'not_isolatable' && openIsolationReasons.has(item.control.observation.reasonKey.replace(/^.*modelControl/, ''))
  const observationUnknown = state === 'unverified' || (state === 'not_isolatable' && !isolationIsOpen)
  let label = modelControlDecision(decision), tone: 'red' | 'green' | 'neutral' = 'neutral'
  if (item.control.conflictReason) label = modelControlText('manualRequired')
  else if (state === 'account_missing') label = modelControlText('accountMissingLabel')
  else if (observationUnknown || summary === 'unknown') label = modelControlText('unknownSiteLabel')
  else if (decision === 'close_recommended' || decision === 'usable') {
    tone = decision === 'close_recommended' ? 'red' : 'green'
    if (closed) label = modelControlText(decision === 'usable' ? 'closedUsableLabel' : 'closedBadLabel')
    else if (state === 'not_provided') { label = modelControlText(decision === 'usable' ? 'missingUsableLabel' : 'missingNoActionLabel'); if (decision !== 'usable') tone = 'neutral' }
  } else if (state === 'not_provided') label = modelControlText('missingNoActionLabel')
  let primaryAction: 'close' | 'restore' | 'add' | null = null
  if (state !== 'account_missing' && decision !== 'testing') {
    if (decision === 'close_recommended' && ['serving', 'partially_closed'].includes(state)) primaryAction = 'close'
    else if (!item.control.conflictReason && closed) primaryAction = 'restore'
    else if (!item.control.conflictReason && state === 'not_provided' && decision === 'usable') primaryAction = 'add'
  }
  let sentence = modelControlText(state === 'serving' || state === 'last_model' ? 'siteOpen' : state === 'closed' ? 'siteClosed' : state === 'partially_closed' ? 'sitePartial' : state === 'not_provided' ? 'siteNotProvided' : state === 'account_missing' ? 'siteAccountMissing' : state === 'unverified' ? 'siteUnverified' : isolationIsOpen ? 'siteNotIsolatable' : 'siteUnknown', { reason: modelControlReason(item.control.observation.reasonKey) })
  if (opts.issue) {
    const reason = opts.issue.reasonKey
    sentence = reason.endsWith('Busy') ? modelControlText('verifyBusy') : reason.endsWith('Processing') ? modelControlText('verifyProcessing') : modelControlText('verifyFailed', { reason: modelControlReason(reason), time: item.control.observation.checkedAt ? modelControlTime(item.control.observation.checkedAt) : modelControlText('neverChecked'), state: modelControlState(item) })
  }
  if (uncertain) sentence = modelControlText('unknownResult')
  else if (!opts.issue && item.control.accountPending) sentence = modelControlText('accountPending')
  return { label, tone, stateSentence: sentence, primaryAction, showReread: Boolean(opts.issue || state === 'unverified' || item.control.accountPending || item.control.pending || uncertain) }
}

export const modelControlEventLines = (event: ModelControlEvent): { sentence: string; entries: string[]; more: number } => {
  const known = te(`admin.connectionHealth.modelControl.events.${event.eventType}`)
  let sentence = modelControlEventLabel(event.eventType)
  const detail = event.detail && typeof event.detail === 'object' ? event.detail as Record<string, unknown> : {}
  if (event.eventType === 'settings_saved') return { sentence: modelControlText('settingsSaved', { accuracy: Number(detail.minAccuracyPercent), min: Number(detail.minJudgedAnswers) }), entries: [], more: 0 }
  if (!known || ['rule_saved', 'rule_deleted'].includes(event.eventType)) return { sentence, entries: [], more: 0 }
  if ((event.eventType.endsWith('_unknown') || event.eventType.startsWith('account_schedulable_')) && typeof detail.reasonKey === 'string') sentence += ` · ${modelControlReason(detail.reasonKey)}`
  const lines: string[] = []
  const append = (source: unknown, prefix = '') => {
    const entries = Array.isArray(source) ? source : source && typeof source === 'object' ? Object.entries(source).map(([key, value]) => ({ key, value })) : []
    for (const entry of entries) {
      if (!entry || typeof entry !== 'object' || typeof entry.key !== 'string' || typeof entry.value !== 'string') continue
      const state = typeof entry.state === 'string' ? modelControlEntryState(entry.state) : ''
      lines.push(`${prefix}${entry.key} → ${entry.value}${state ? ` · ${state}` : ''}`)
    }
  }
  if (['managed_removed', 'managed_abandoned', 'account_missing_resolved'].includes(event.eventType)) {
    append(detail.closedEntries, `${modelControlText('stillClosedPrefix')} · `)
    append((detail.unconfirmedClose as { entries?: unknown } | null)?.entries, `${modelControlText('unconfirmed')} · `)
  } else append(Array.isArray(event.detail) ? event.detail : detail.entries)
  return { sentence, entries: lines.slice(0, 5), more: Math.max(0, lines.length - 5) }
}

export const modelControlSupplyWarning = (items: import('../types/connectionHealth').ModelControlSupplyItem[] = []) => {
  const missing = items.filter(item => item.open === 0 && item.unknown === 0)
  const low = items.filter(item => item.open === 1 || (item.open === 0 && item.unknown > 0))
  if (!missing.length && !low.length) return null
  return { tone: missing.length ? 'red' : 'amber', label: missing.length ? missing.length === 1 ? modelControlText('supplyMissingSingle', { model: missing[0]!.modelName }) : modelControlText('supplyMissingMany', { count: missing.length }) : modelControlText('supplyLow', { count: low.length }), tooltip: [...missing, ...low].map(item => `${item.modelName}：${modelControlText(item.open === 0 && item.unknown === 0 ? 'supplyNone' : item.open === 1 ? 'supplySingle' : 'supplyUncertain')}`).join('；') }
}
export const modelControlSupplyTooltip = (item: import('../types/connectionHealth').ModelControlSupplyItem) => `${modelControlText('countOpen')}：${item.openAccounts.join('、') || '—'}${item.open > item.openAccounts.length ? `（${modelControlText('supplyNamesMore', { count: item.open })}）` : ''}；${modelControlText('countClosed')}：${item.closedAccounts.join('、') || '—'}${item.closed ? '（TransitHub 关闭）' : ''}${item.closed > item.closedAccounts.length ? `（${modelControlText('supplyNamesMore', { count: item.closed })}）` : ''}`
export const modelControlSummaryTooltip = (summary: import('../types/connectionHealth').ModelControlAccountSummary) => {
  const labels = { open: '开放', closed: '关闭', not_provided: '白名单没有', unknown: '无法确认', account_missing: '账号已删除' }
  const lines = [modelControlText('summaryTooltipHint')]
  for (const [status, label] of Object.entries(labels)) {
    const models = summary.models.filter(model => model.status === status)
    if (models.length) lines.push(`${label}：${models.map(model => `${model.modelName}（${model.checkedAt ? `${modelControlTime(model.checkedAt)} 确认` : modelControlText('neverChecked')}）`).join('、')}`)
  }
  const attention = summary.models.filter(model => model.attention)
  if (attention.length) lines.push(`需处理：${attention.map(model => `${model.modelName}（${modelControlDecision(model.decision)}）`).join('、')}`)
  return lines.join('；')
}
