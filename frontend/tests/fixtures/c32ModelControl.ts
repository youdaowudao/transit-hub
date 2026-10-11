import type { AdminGroupHealth, ModelHealth, ModelControlAutoSettings } from '@/modules/admin/types/connectionHealth'
import { c2Groups } from './c2QuestionAnswerSchedules'
import { now } from './v2911ModelControl'
export const c32Model = (modelName: string, patch: Partial<ModelHealth> = {}): ModelHealth => ({ modelName, providerFamily: 'openai', configured: true, state: 'healthy', currentWeight: 100, consecutiveFailures: 0, consecutiveSuccesses: 2, lastProbeAt: now, lastSuccessAt: now, lastFailureAt: null, lastLatencyMs: 10, lastSuccessLatencyMs: 10, lastErrorKey: '', lastErrorDetail: '', lastRemoteAction: '', updatedAt: now, ...patch })
export const c32Groups = (): AdminGroupHealth[] => {
 const groups = c2Groups(), g = groups[0]!, a = g.accounts[0]!
 Object.assign(a, { status: 'active', schedulable: true, probeAvailable: true, hasEnabledProbePolicy: true, hasEnabledPolicy: true, hasAssignedPolicy: true, probeModelsConfigured: true, modelHealth: [c32Model('closed', { state: 'suspended', currentWeight: 0, consecutiveFailures: 9, lastFailureAt: now, lastSuccessLatencyMs: 9000, modelControlClosed: true }), c32Model('open')], unprobedModels: [{ modelName: 'closed-unprobed', providerFamily: 'openai', modelControlClosed: true }] })
 Object.assign(g.healthSummary, { healthyModels: 1, suspendedModels: 0, unconfiguredModels: 0, pendingModels: 0 })
 return groups
}

export const c32AutoSettings = (): ModelControlAutoSettings => ({ autoCloseEnabled: false, autoCloseEnabledAt: null, autoRestoreEnabled: false, autoRestoreEnabledAt: null, healthCoordinationEnabled: false, verifyIntervalMinutes: 10, retryIntervalMinutes: 5, version: 0, lastScanStartedAt: null, lastScanFinishedAt: null, lastScanErrorKey: '' })
