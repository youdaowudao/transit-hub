// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { cancelQuestionAnswerBatch, getLatestQuestionAnswerBatch, setQuestionAnswerJudgment, startQuestionAnswerBatch } from '@/modules/admin/api/connectionHealth'

afterEach(() => { vi.unstubAllGlobals() })
const target = 'sub2api:ws1:legacy-error'
const key = 'admin.connectionHealth.errors.questionAnswerStorage'

it.each([
  ['create', () => startQuestionAnswerBatch(target, ['m'], ['q'], 'medium', 1)],
  ['cancel', () => cancelQuestionAnswerBatch(target, 'batch')],
  ['judge', () => setQuestionAnswerJudgment(target, 'record', 'correct', '2026-10-22T00:00:00Z')],
] as const)('explains an unconfirmed legacy %s without changing the business key or resending', async (step, execute) => {
  const fetcher = vi.fn(async () => new Response(JSON.stringify({ message: key }), { status: 500 }))
  vi.stubGlobal('fetch', fetcher)
  const error = await execute().catch(error => error)
  expect(error.errorKey).toBe(key)
  expect(error.operationResult).toBe('unknown')
  expect(error.message).toContain('无法确认')
  if (step === 'create') expect(error.message).toMatch(/最近批次|历史/)
  else expect(error.message).toContain('重新读取')
  expect(fetcher).toHaveBeenCalledTimes(1)
})

it('keeps a legacy read failure read-only and directs the user to reread', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ message: key }), { status: 500 })))
  const error = await getLatestQuestionAnswerBatch(target).catch(error => error)
  expect(error.errorKey).toBe(key)
  expect(error.message).toContain('重新读取')
  expect(error.message).not.toContain('是否已提交')
})

const bodies = [null, [], false, 500, 'bad response', { message: 500 }, { message: 'admin.connectionHealth.errors.questionAnswerStorage', detail: {} }, { message: 'admin.connectionHealth.errors.questionAnswerStorage', operationResult: 'garbage' }]

it.each(bodies)('treats an invalid legacy error payload as unconfirmed with specific guidance: %j', async body => {
  for (const [operation, execute] of [
    ['create', () => startQuestionAnswerBatch(target, ['m'], ['q'], 'medium', 1)],
    ['cancel', () => cancelQuestionAnswerBatch(target, 'batch')],
    ['judge', () => setQuestionAnswerJudgment(target, 'record', 'correct', '2026-10-22T00:00:00Z')],
    ['read', () => getLatestQuestionAnswerBatch(target)],
  ] as const) {
    const fetcher = vi.fn(async () => new Response(JSON.stringify(body), { status: 500 }))
    vi.stubGlobal('fetch', fetcher)
    const error = await execute().catch(error => error)
    expect(error.operationResult, operation).toBe('unknown')
    expect(error.message, operation).not.toContain('Cannot read')
    expect(error.message, operation).not.toContain('[object Object]')
    if (operation === 'create') {
      expect(error.message).toContain('无法确认')
      expect(error.message).toMatch(/最近批次|历史/)
      expect(error.message).toContain('不会自动重发')
    } else {
      expect(error.message).toContain('重新读取')
      if (operation === 'read') expect(error.message).not.toContain('是否已提交')
      else expect(error.message).toContain('无法确认')
    }
    expect(fetcher).toHaveBeenCalledTimes(1)
  }
})
