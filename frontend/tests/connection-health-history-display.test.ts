import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

import {
  buildConnectionHealthRecordSummary,
  formatConnectionHealthElapsed,
  isConnectionHealthCurrentFailure,
  latestConnectionHealthProbeFailure,
} from '../src/modules/admin/composables/useConnectionHealth'

const detailSource = readFileSync(
  new URL('../src/modules/admin/components/dashboard/AdminGroupHealthDetail.vue', import.meta.url),
  'utf8',
)
const cardSource = readFileSync(
  new URL('../src/modules/admin/components/dashboard/ConnectionHealthLinkDetailCard.vue', import.meta.url),
  'utf8',
)
const eventsDialogSource = readFileSync(
  new URL('../src/modules/admin/components/dashboard/ConnectionHealthEventsDialog.vue', import.meta.url),
  'utf8',
)

describe('connection health historical failure display', () => {
  it('does not treat a preserved error after a newer success as current failure', () => {
    expect(isConnectionHealthCurrentFailure({
      lastFailureAt: '2026-08-10T10:00:00Z',
      lastProbeAt: '2026-08-10T10:05:00Z',
      lastSuccessAt: '2026-08-10T10:05:00Z',
    })).toBe(false)
  })

  it('accepts a current failure before any successful probe', () => {
    expect(isConnectionHealthCurrentFailure({
      lastFailureAt: '2026-08-10T10:00:00Z',
      lastProbeAt: '2026-08-10T10:00:00Z',
      lastSuccessAt: null,
    })).toBe(true)
  })

  it('does not infer current failure from missing or invalid probe times', () => {
    expect(isConnectionHealthCurrentFailure({
      lastFailureAt: '2026-08-10T10:00:00Z',
      lastProbeAt: null,
      lastSuccessAt: null,
    })).toBe(false)
    expect(isConnectionHealthCurrentFailure({
      lastFailureAt: 'not-a-date',
      lastProbeAt: '2026-08-10T10:00:00Z',
      lastSuccessAt: null,
    })).toBe(false)
  })

  it('formats elapsed time only when it has a valid failure time', () => {
    expect(formatConnectionHealthElapsed(59, '2026-08-10T10:00:00Z')).toBe('不到 1 分钟')
    expect(formatConnectionHealthElapsed(61 * 60, '2026-08-10T10:00:00Z')).toBe('1 小时')
    expect(formatConnectionHealthElapsed(25 * 60 * 60, '2026-08-10T10:00:00Z')).toBe('1 天')
    expect(formatConnectionHealthElapsed(30, null)).toBe('')
    expect(formatConnectionHealthElapsed(Number.NaN, '2026-08-10T10:00:00Z')).toBe('')
    expect(formatConnectionHealthElapsed(-1, '2026-08-10T10:00:00Z')).toBe('')
  })

  it('derives fallback failures from actual model probe failures, including remote actions', () => {
    const failure = latestConnectionHealthProbeFailure([
      { modelName: '*', result: 'server_error', createdAt: '2026-08-10T10:04:00Z', remoteAction: '' },
      { modelName: 'gpt-5.6-sol', result: 'unsupported', createdAt: '2026-08-10T10:03:00Z', remoteAction: '' },
      { modelName: 'gpt-5.6-sol', result: 'server_error', createdAt: '2026-08-10T10:02:00Z', remoteAction: 'newapi_channel_disabled' },
      { modelName: 'gpt-5.6-sol', result: 'network_fluctuation', createdAt: '2026-08-10T10:01:00Z', remoteAction: '' },
    ])

    expect(failure?.result).toBe('server_error')
    expect(failure?.remoteAction).toBe('newapi_channel_disabled')
  })

  it('keeps at most 60 loaded records and excludes action-only records from availability', () => {
    const action = { result: 'manual_disable', id: 'action' }
    expect(buildConnectionHealthRecordSummary([])).toEqual({ records: [], availabilityPct: null })
    expect(buildConnectionHealthRecordSummary([
      { result: 'ok', id: 'ok' },
      action,
      { result: 'server_error', id: 'failure' },
    ])).toMatchObject({ availabilityPct: 50 })

    const moreThanSixty = Array.from({ length: 61 }, (_, index) => ({ result: 'ok', id: String(index) }))
    const summary = buildConnectionHealthRecordSummary(moreThanSixty)
    expect(summary.records).toHaveLength(60)
    expect(summary.records[0]?.id).toBe('59')
  })

  it('keeps the compact dropdown free of history detail and gates its reason on current failure', () => {
    expect(detailSource).toContain('hasValidConnectionHealthTime(model.lastFailureAt)')
    expect(detailSource).toContain('isConnectionHealthCurrentFailure(model) && model.lastErrorKey')
    expect(detailSource).not.toContain('model.lastErrorDetail')
  })

  it('uses the shared event card gate in both focused and global dialog modes', () => {
    expect(cardSource).toContain('const showErrorDetail = computed(() => props.isActionCard || hasFailureTime.value)')
    expect(cardSource).toContain('props.failureFromLoadedRecords')
    expect(eventsDialogSource).toContain('latestConnectionHealthProbeFailure(eventsDesc)')
    expect(eventsDialogSource).toContain(':is-action-card="card.isActionCard"')
    expect(eventsDialogSource).toContain(':failure-from-loaded-records="card.failureFromLoadedRecords"')
    expect(eventsDialogSource).toContain('buildConnectionHealthRecordSummary(eventsDesc)')
  })

  it('keeps probe-only metrics out of account action cards', () => {
    expect(cardSource).toContain('<div v-if="!isActionCard" class="flex shrink-0 flex-col items-end gap-1">')
    expect(cardSource).toContain('<div v-if="!isActionCard" class="mt-2 grid gap-2"')
    expect(cardSource).toContain('<div v-if="!isActionCard" class="mt-1.5 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">')
    expect(cardSource).toContain('<div v-if="!isActionCard" class="mt-2.5">')
    expect(cardSource).toContain('<div v-if="!isActionCard" class="mt-3">')
  })
})
