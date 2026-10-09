import type { HealthRulePreset, HealthRulePresetInput, HealthRuleVersion } from '../types/connectionHealth'

export const defaultHealthRulePreset = (): HealthRulePresetInput => ({
  name: '', failureThreshold: 3, successThreshold: 2, cooldownSeconds: 300,
  failedRetryIntervalSeconds: 600, longFailureAfterSeconds: 86400, longFailureIntervalSeconds: 3600,
  delayLineMs: { responses: 6000, chat_completions: 6000 }, observationSeconds: 300, recoveryStepPercent: 25,
})
export const copyHealthRulePresetInput = (preset: HealthRulePresetInput): HealthRulePresetInput => ({
  name: preset.name, failureThreshold: preset.failureThreshold, successThreshold: preset.successThreshold,
  cooldownSeconds: preset.cooldownSeconds, failedRetryIntervalSeconds: preset.failedRetryIntervalSeconds,
  longFailureAfterSeconds: preset.longFailureAfterSeconds, longFailureIntervalSeconds: preset.longFailureIntervalSeconds,
  delayLineMs: { ...preset.delayLineMs }, observationSeconds: preset.observationSeconds, recoveryStepPercent: preset.recoveryStepPercent,
})
export const isReadOnlyHealthRulePreset = (preset: HealthRulePreset) => preset.kind === 'legacy_snapshot' || preset.kind === 'legacy_default'

type NumericPresetKey = Exclude<keyof HealthRulePresetInput, 'name' | 'delayLineMs'>
export const healthRulePresetFields: Array<{ key: NumericPresetKey; label: string; help: string; scale: number; unit: string; min: number; max: number; only?: HealthRuleVersion }> = [
  { key: 'failureThreshold', label: '连续失败几次关停', help: '第一次失败记为疑似，之后降档，到此次数时关停。', scale: 1, unit: '次', min: 2, max: 10 },
  { key: 'cooldownSeconds', label: '关停后冷却多久再测', help: '刚被关停的账号，等冷却结束后再探活。', scale: 1, unit: '秒', min: 60, max: 3600 },
  { key: 'successThreshold', label: '关停后连续成功几次恢复', help: '被关停的账号需连续成功达到此次数，才能恢复。', scale: 1, unit: '次', min: 1, max: 10 },
  { key: 'failedRetryIntervalSeconds', label: '关停后仍失败的探活间隔', help: '被调度站关停、还没到长期失败的账号使用此间隔；旧规则仍使用原来的 2/5/10 分钟退避。', scale: 1, unit: '秒', min: 60, max: 3600, only: 'v2' },
  { key: 'longFailureAfterSeconds', label: '连续失败多久算长期', help: '从第一次失败起，中间没有任何一次成功；延迟也算成功。', scale: 3600, unit: '小时', min: 1, max: 168 },
  { key: 'longFailureIntervalSeconds', label: '长期失败的探活间隔', help: '连续失败达到长期时长的账号使用此间隔，包括调度站关停和主站关闭调度的账号。', scale: 60, unit: '分钟', min: 10, max: 1440 },
  { key: 'observationSeconds', label: '观察期', help: '旧规则下，被关停的账号恢复前的观察时间。', scale: 1, unit: '秒', min: 1, max: 3600, only: 'legacy' },
  { key: 'recoveryStepPercent', label: '恢复步进', help: '旧规则和 NewAPI 账号逐步恢复时，每次增加的比例。', scale: 1, unit: '%', min: 1, max: 100, only: 'legacy' },
]
export const validHealthRulePreset = (input: HealthRulePresetInput): boolean => !!input.name.trim() && Array.from(input.name.trim()).length <= 120
  && healthRulePresetFields.every(field => Number.isInteger(input[field.key]) && input[field.key] >= field.min * field.scale && input[field.key] <= field.max * field.scale)
  && Object.values(input.delayLineMs).every(value => Number.isInteger(value) && value >= 1000 && value <= 120000)
