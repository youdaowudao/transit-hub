import type { MassEmailUser } from '../types/massEmail'

export const USER_LAST_USED_TIMEZONE = 'Asia/Shanghai'

const DATE_KEY_PATTERN = /^\d{4}-\d{2}-\d{2}$/

const dateParts = (value: Date, timeZone: string): Record<string, string> => {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: 'h23',
  }).formatToParts(value)

  return Object.fromEntries(parts.map((part) => [part.type, part.value]))
}

const parsedDate = (value: string | Date): Date | null => {
  const date = value instanceof Date ? value : new Date(value)
  return Number.isNaN(date.getTime()) ? null : date
}

export const dateKeyInTimeZone = (
  value: string | Date,
  timeZone = USER_LAST_USED_TIMEZONE,
): string | null => {
  const date = parsedDate(value)
  if (!date) return null
  const parts = dateParts(date, timeZone)
  return `${parts.year}-${parts.month}-${parts.day}`
}

export const formatLastUsedAt = (
  value: string | Date,
  timeZone = USER_LAST_USED_TIMEZONE,
): string => {
  const date = parsedDate(value)
  if (!date) return ''
  const parts = dateParts(date, timeZone)
  return `${parts.year}-${parts.month}-${parts.day} ${parts.hour}:${parts.minute}:${parts.second}`
}

const previousDateKey = (dateKey: string): string => {
  const [year, month, day] = dateKey.split('-').map(Number)
  const value = new Date(Date.UTC(year, month - 1, day))
  value.setUTCDate(value.getUTCDate() - 1)
  return [value.getUTCFullYear(), value.getUTCMonth() + 1, value.getUTCDate()]
    .map((part, index) => index === 0 ? String(part) : String(part).padStart(2, '0'))
    .join('-')
}

export const normalizeSelectedDates = (values: string[]): string[] => (
  [...new Set(values.filter((value) => DATE_KEY_PATTERN.test(value)))].sort((left, right) => right.localeCompare(left))
)

export const defaultSelectedDates = (now = new Date()): string[] => {
  const today = dateKeyInTimeZone(now)
  if (!today) return []
  return [today, previousDateKey(today)]
}

export interface UserLastUsedRow {
  id: string
  username: string
  lastUsedAt: string
  displayLastUsedAt: string
}

export interface UserLastUsedGroup {
  date: string
  items: UserLastUsedRow[]
}

export const groupUsersByLastUsedDate = (
  users: MassEmailUser[],
  selectedDates: string[],
): UserLastUsedGroup[] => {
  const dates = normalizeSelectedDates(selectedDates)
  const selected = new Set(dates)
  const grouped = new Map(dates.map((date) => [date, [] as UserLastUsedRow[]]))
  const seenUserIds = new Set<string>()

  for (const user of users) {
    if (seenUserIds.has(user.id)) continue
    const username = user.username?.trim()
    const lastUsedAt = user.lastUsedAt?.trim()
    if (!username || !lastUsedAt) continue

    const date = dateKeyInTimeZone(lastUsedAt)
    const displayLastUsedAt = formatLastUsedAt(lastUsedAt)
    if (!date || !displayLastUsedAt || !selected.has(date)) continue

    seenUserIds.add(user.id)
    grouped.get(date)?.push({ id: user.id, username, lastUsedAt, displayLastUsedAt })
  }

  return dates.map((date) => ({
    date,
    items: (grouped.get(date) ?? []).sort((left, right) => (
      new Date(right.lastUsedAt).getTime() - new Date(left.lastUsedAt).getTime()
    )),
  }))
}

export const pageContainsDateBefore = (
  users: MassEmailUser[],
  earliestSelectedDate: string,
): boolean => users.some((user) => {
  const date = user.lastUsedAt ? dateKeyInTimeZone(user.lastUsedAt) : null
  return date !== null && date < earliestSelectedDate
})

export const pageIsLastUsedTimeTail = (users: MassEmailUser[]): boolean => (
  users.length > 0 && users.every((user) => !user.lastUsedAt || dateKeyInTimeZone(user.lastUsedAt) === null)
)
