import { afterEach, describe, expect, it, vi } from 'vitest'
import { saveMySiteMapping } from '../src/modules/admin/api/mySites'
import type { MySiteMapping } from '../src/modules/admin/types/mySites'

const mappingWithLastRun = (): MySiteMapping => ({
  ownGroup: 'Gemini premium',
  upstreamTargets: [{ siteId: 'site-1', groupName: 'gemini' }],
  enableAutoPricing: true,
  autoPricingSource: 'primary_upstream',
  primaryUpstreamSiteId: 'site-1',
  primaryUpstreamGroupName: 'gemini',
  autoPricingStrategy: 'percentage',
  fixedIncrease: 0,
  percentageIncrease: 100,
  adjustThresholdPercent: 100,
  minMultiplier: null,
  maxMultiplier: null,
  enableAutoPricingNotify: false,
  autoPricingNotifyBotIds: [],
  autoPricingNotifyTemplate: '',
  lastAutoPricingRun: {
    status: 'skipped',
    reason: 'threshold_exceeded',
    trigger: 'manual',
    ranAt: '2026-08-25T03:25:25Z',
    oldReference: 0.1,
    newReference: 0.2,
    targetMultiplier: 0.4,
    oldOwnMultiplier: 0.2,
    newOwnMultiplier: 0.2,
  },
})

afterEach(() => vi.unstubAllGlobals())

describe('my sites mapping API', () => {
  it('serializes only writable fields in PATCH mapping saves', async () => {
    vi.stubGlobal('localStorage', { getItem: vi.fn().mockReturnValue(null) })
    const mapping = mappingWithLastRun()
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      authenticated: true,
      mappings: [mapping],
    }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await saveMySiteMapping(mapping, [mapping])

    const [url, options] = fetchMock.mock.calls[0]
    const requestBody = JSON.parse(String(options.body))
    const { lastAutoPricingRun: _lastRun, ...writableMapping } = mapping
    expect(url).toBe('/api/my-sites/mappings')
    expect(options.method).toBe('PATCH')
    expect(requestBody).toEqual({ mapping: writableMapping })
    expect(requestBody.mapping).not.toHaveProperty('lastAutoPricingRun')
  })

  it('serializes only writable fields in fallback PUT mapping saves', async () => {
    vi.stubGlobal('localStorage', { getItem: vi.fn().mockReturnValue(null) })
    const mapping = mappingWithLastRun()
    const otherMapping: MySiteMapping = {
      ...mappingWithLastRun(),
      ownGroup: 'Claude premium',
      upstreamTargets: [{ siteId: 'site-2', groupName: 'claude' }],
      primaryUpstreamSiteId: 'site-2',
      primaryUpstreamGroupName: 'claude',
    }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        message: 'admin.mySites.errors.request',
      }), { status: 400 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        authenticated: true,
        mappings: [mapping, otherMapping],
      }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await saveMySiteMapping(mapping, [mapping, otherMapping])

    const [url, options] = fetchMock.mock.calls[1]
    const requestBody = JSON.parse(String(options.body))
    const { lastAutoPricingRun: _mappingLastRun, ...writableMapping } = mapping
    const { lastAutoPricingRun: _otherLastRun, ...writableOtherMapping } = otherMapping
    expect(url).toBe('/api/my-sites/mappings')
    expect(options.method).toBe('PUT')
    expect(requestBody).toEqual({ mappings: [writableMapping, writableOtherMapping] })
    expect(requestBody.mappings).toHaveLength(2)
    expect(requestBody.mappings.every((item: MySiteMapping) => !Object.hasOwn(item, 'lastAutoPricingRun'))).toBe(true)
  })
})
