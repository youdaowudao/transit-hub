import type { SignupRiskSchedule } from '../types/notifications'

export const signupRiskTimeZones = [
  { value: 'Asia/Singapore', label: '新加坡（UTC+8）', offset: 8 },
  { value: 'Asia/Tokyo', label: '东京（UTC+9）', offset: 9 },
  { value: 'UTC', label: 'UTC', offset: 0 },
] as const
export const defaultSignupRiskSchedule = (): SignupRiskSchedule => ({ workStart: '09:00', workEnd: '18:00', timeZone: 'Asia/Singapore' })
export const validSignupRiskSchedule = (value: SignupRiskSchedule): boolean => {
  const clock = /^([01]\d|2[0-3]):[0-5]\d$/
  return clock.test(value.workStart) && clock.test(value.workEnd) && value.workStart < value.workEnd && signupRiskTimeZones.some(zone => zone.value === value.timeZone)
}
export const sameSignupRiskSettings = (a: SignupRiskSchedule & { enabled: boolean }, b: SignupRiskSchedule & { enabled: boolean }): boolean => (
  a.enabled === b.enabled && a.workStart === b.workStart && a.workEnd === b.workEnd && a.timeZone === b.timeZone
)
