// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ConnectionHealthApiError, addModelControlManaged, executeModelControl, getModelControlTarget, listModelControlItems, parseModelControlItem, previewModelControl, removeModelControlManaged, verifyModelControl } from '@/modules/admin/api/connectionHealth'
import { managed, targetId } from './fixtures/v2911ModelControl'
const preview = (item = managed()) => ({ item, entries: [], groups: [], blockReasonKey: '', requestHealth: { checked: false, allowed: null, reasonKey: '' }, accountStatus: 'active', accountSchedulable: true, planFingerprint: 'fp', noRemoteWrite: false })
let response: unknown, status: number
const calls: RequestInit[] = []
beforeEach(() => { status = 200; calls.length = 0; vi.stubGlobal('fetch', vi.fn(async (_url, init: RequestInit) => { calls.push(init); return new Response(JSON.stringify(response), { status }) })) })
afterEach(() => vi.unstubAllGlobals())
const reads = [
  { name: 'target', load: () => getModelControlTarget(targetId), wrap: (item: unknown) => ({ targetId, items: [item], candidates: [] }) },
  { name: 'list', load: () => listModelControlItems('all', '', 1), wrap: (item: unknown) => ({ items: [item], page: 1, totalPages: 1, counts: {} }) },
  { name: 'verify', load: () => verifyModelControl([targetId]), wrap: (item: unknown) => ({ items: [item], errors: [] }) },
  { name: 'manage', load: () => addModelControlManaged(targetId, 'M'), wrap: (item: unknown) => item },
  { name: 'preview', load: () => previewModelControl(targetId, 'M', 'close', managed().basis), wrap: (item: unknown) => preview(item as ReturnType<typeof managed>) },
  { name: 'execute', load: () => executeModelControl(targetId, 'M', 'close', managed().basis, 'fp', false), wrap: (item: unknown) => ({ item, outcome: 'closed', reasonKey: '', entries: [], groups: [], hintKeys: [] }) },
]
describe('V3.0.0 all model item response boundaries', () => {
 it.each(reads)('$name accepts the current question contract and rejects missing statistics', async ({ load, wrap }) => {
  response = wrap(managed()); await expect(load()).resolves.toBeDefined()
  const invalid = managed(); delete (invalid.round as any).questions
  response = wrap(invalid); await expect(load()).rejects.toThrow('缺少逐题统计')
 })
 it.each([
  ['重复题目编号', (item: ReturnType<typeof managed>) => item.round!.questions.push({ ...item.round!.questions[0]! })],
  ['非法回答数量', (item: ReturnType<typeof managed>) => { item.round!.questions[0]!.correct = 0.5 }],
  ['非法回答数量', (item: ReturnType<typeof managed>) => { item.round!.failed = -1 }],
  ['非法正确率', (item: ReturnType<typeof managed>) => { item.round!.accuracyPercent = Number.NaN }],
  ['非法正确率', (item: ReturnType<typeof managed>) => { item.round!.questions[0]!.accuracyPercent = 101 }],
  ['重复题目编号', (item: ReturnType<typeof managed>) => { item.round!.duplicateQuestionIds = ['unknown'] }],
  ['缺少逐题统计', (item: ReturnType<typeof managed>) => { item.round!.questions = [] }],
 ] as const)('rejects %s without falling back to aggregate counts', (reason, mutate) => {
  const item = managed(); mutate(item); expect(() => parseModelControlItem(item)).toThrow(reason)
 })
 it('rejects verify success and conflict objects outside the submitted account set', async () => {
  const item = managed(); item.targetId = 'sub2api:ws1:other'
  response = { items: [item], errors: [] }; await expect(verifyModelControl([targetId])).rejects.toThrow('对象不匹配')
  status = 409; response = { message: 'conflict', current: item }; await expect(verifyModelControl([targetId])).rejects.toThrow('对象不匹配')
 })
 it('validates previous round as well as latest round', () => {
  const item = managed(); item.previousRound = { ...item.round!, questions: [] }; expect(() => parseModelControlItem(item)).toThrow('缺少逐题统计')
 })
 it.each(['close', 'add', 'restore'] as const)('passes human confirmation for %s', async op => {
  response = { item: managed(), outcome: 'closed', reasonKey: '', entries: [], groups: [], hintKeys: [] }; await executeModelControl(targetId, 'M', op, managed().basis, 'fp', true)
  expect(JSON.parse(String(calls[0]!.body))).toHaveProperty('confirmWithoutEvidence', true)
 })
 it.each([
  ['rule model', (item: ReturnType<typeof managed>) => { item.rule.modelName = 'other' }],
  ['rule version', (item: ReturnType<typeof managed>) => { item.basis.ruleVersion = 9 }],
 ] as const)('rejects inconsistent %s using existing fields', (_name, mutate) => { const item = managed(); mutate(item); expect(() => parseModelControlItem(item)).toThrow('对象不匹配') })
 it.each([{ outcome: 'unrecognized' }, { entries: null }, { groups: 'invalid' }, { hintKeys: [1] }])('unreadable result envelope remains uncertain %j', async patch => {
  response = { item: managed(), outcome: 'closed', reasonKey: '', entries: [], groups: [], hintKeys: [], ...patch }; await expect(executeModelControl(targetId, 'M', 'close', managed().basis, 'fp', false)).rejects.toThrow('无效执行结果')
 })
 it('retains account operation payload and quality scope', async () => {
  response = { item: managed(), outcome: 'closed', reasonKey: '', entries: [], groups: [], hintKeys: [] }; await executeModelControl(targetId, 'M', 'close_account', managed().basis, 'fp', true)
  expect(JSON.parse(String(calls[0]!.body))).not.toHaveProperty('confirmWithoutEvidence')
 })
})
describe('V3.0.0 conflicts carry validated current state', () => {
 it.each([...reads, { name: 'remove', load: () => removeModelControlManaged(targetId, 'M', 1, false), wrap: (item: unknown) => item }])('$name validates 409 current', async ({ load }) => {
  status = 409; response = { message: 'admin.connectionHealth.errors.modelControlBasisChanged', current: managed() }
  await expect(load()).rejects.toBeInstanceOf(ConnectionHealthApiError)
  const invalid = managed(); invalid.round!.questions[0]!.cancelled = -1
  response = { message: 'conflict', current: invalid }; await expect(load()).rejects.toThrow('非法回答数量')
 })
 it('matches a single model current and validates full preview structure', async () => {
  status = 409; response = { message: 'conflict', current: managed('another-model') }
  await expect(previewModelControl(targetId, 'M', 'close', managed().basis)).rejects.toThrow('对象不匹配')
  response = { message: 'conflict', current: { ...preview(), entries: 'invalid' } }
  await expect(previewModelControl(targetId, 'M', 'close', managed().basis)).rejects.toThrow('无效预览结构')
  response = { message: 'conflict', current: preview() }
  await expect(previewModelControl(targetId, 'M', 'close', managed().basis)).rejects.toMatchObject({ current: preview() })
 })
 it('invalid execution results and conflicts report uncertainty after the request was sent', async () => {
  const invalid = managed(); invalid.round!.questions = []
  response = { item: invalid }; await expect(executeModelControl(targetId, 'M', 'close', managed().basis, 'fp', false)).rejects.toThrow('操作请求已发送')
  status = 409; response = { message: 'conflict', current: invalid }
  const failure = await executeModelControl(targetId, 'M', 'close', managed().basis, 'fp', false).catch(error => error)
  expect(failure).not.toBeInstanceOf(ConnectionHealthApiError); expect(failure.message).toContain('不要直接重试'); expect(failure.message).not.toContain('没有修改主站')
 })
 it('does not expose invalid JSON text in an execution failure', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{invalid private text')))
  const error = await executeModelControl(targetId, 'M', 'close', managed().basis, 'fp', false).catch(error => error)
  expect(error.message).toContain('返回内容不是可读取的数据'); expect(error.message).not.toContain('private text')
 })
})
