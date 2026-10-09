import { ref } from 'vue'
import { t, te } from '@/locales'
import { formatConnectionHealthTime } from '../composables/useConnectionHealth'
import type { ModelControlDecision, ModelControlItem, ModelControlRound } from '../types/connectionHealth'

// targetId includes workspace identity; shared across the dialog and drawer projections.
export const modelControlBusyTargets = ref(new Set<string>())
const busyOwners = new Map<string, symbol>()
export const acquireModelControlBusy = (targetId: string): symbol | null => {
  if (modelControlBusyTargets.value.has(targetId)) return null
  const owner = Symbol(targetId); busyOwners.set(targetId, owner); modelControlBusyTargets.value = new Set([...modelControlBusyTargets.value, targetId]); return owner
}
export const releaseModelControlBusy = (targetId: string, owner: symbol) => {
  if (busyOwners.get(targetId) !== owner) return
  busyOwners.delete(targetId); const next = new Set(modelControlBusyTargets.value); next.delete(targetId); modelControlBusyTargets.value = next
}
export const modelControlReason = (key?: string): string => key ? (te(key) ? t(key) : te(`admin.connectionHealth.modelControl.reasons.${key}`) ? t(`admin.connectionHealth.modelControl.reasons.${key}`) : t('admin.connectionHealth.errors.request')) : ''
export const modelControlDecision = (decision: ModelControlDecision): string => t(`admin.connectionHealth.modelControl.decisions.${decision}`)
export const modelControlText = (key: string, params?: Record<string, string | number>): string => t(`admin.connectionHealth.modelControl.text.${key}`, params)
export const modelControlState = (item: ModelControlItem): string => item.control.conflictReason ? modelControlText('manualRequired') : item.control.pending ? modelControlPendingState(item.control.pending.state) : t(`admin.connectionHealth.modelControl.states.${item.control.observation.state}`)
export const modelControlPendingState = (state: string): string => t(`admin.connectionHealth.modelControl.pending.${['in_progress', 'not_completed', 'unknown'].includes(state) ? state : 'in_progress'}`)
export const modelControlTime = (value?: string | null): string => formatConnectionHealthTime(value ?? null)
export const modelControlRoundText = (round: ModelControlRound): string => `${round.source === 'manual' ? modelControlText('manualSource') : round.source === 'run_now' ? modelControlText('runNowSource') : modelControlText('scheduledSource')}${round.scheduleName ? ` · ${round.scheduleName}` : ''} · ${modelControlTime(round.createdAt)}`
export const modelControlEntryState = (state: string): string => te(`admin.connectionHealth.modelControl.entries.${state}`) ? t(`admin.connectionHealth.modelControl.entries.${state}`) : state

export const modelControlRuleRevision = ref({ workspace: '', modelName: '', revision: 0 })
export const modelControlRuleChanged = (workspace: string, modelName: string) => { modelControlRuleRevision.value = { workspace, modelName, revision: modelControlRuleRevision.value.revision + 1 } }

export const modelControlEventLabel = (event: string): string => te(`admin.connectionHealth.modelControl.events.${event}`) ? t(`admin.connectionHealth.modelControl.events.${event}`) : modelControlText('eventFallback')
