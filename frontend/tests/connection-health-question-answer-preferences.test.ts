// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type {
  AdminGroupAccount,
  AdminGroupHealth,
  ManualProbeModelOption,
  QuestionAnswerReasoningEffort,
  TestQuestion,
} from '@/modules/admin/types/connectionHealth'
import {
  connectionHealthPreferencesStorageKey,
  createDefaultConnectionHealthPreferences,
  readConnectionHealthPreferences,
  writeConnectionHealthPreferences,
} from '@/modules/admin/utils/connectionHealthPreferences'
import * as questionAnswerModule from '@/modules/admin/utils/questionAnswers'

interface SelectionPreferences {
  modelIds: string[]
  questionIds: string[]
  reasoningEffort: QuestionAnswerReasoningEffort
  repeatCount: number
}

interface BatchTarget {
  targetId: string
  accountName: string
  platform: string
  type: string
  status: string
  groupIds: string[]
  groupNames: string[]
}

interface QuestionAnswerModuleContract {
  resolveQuestionAnswerSelection?: (
    preferences: SelectionPreferences,
    models: ManualProbeModelOption[],
    questions: TestQuestion[],
  ) => SelectionPreferences
  collectQuestionAnswerBatchTargets?: (groups: AdminGroupHealth[]) => BatchTarget[]
  reconcileQuestionAnswerBatchTargetIds?: (storedIds: string[], targets: BatchTarget[]) => string[]
  selectQuestionAnswerBatchTargets?: (targets: BatchTarget[], selectedIds: string[]) => BatchTarget[]
  compatibleQuestionAnswerModelIds?: (
    selectedModelIds: string[],
    models: ManualProbeModelOption[],
  ) => { compatible: string[]; incompatible: string[] }
}

const questionAnswers = questionAnswerModule as QuestionAnswerModuleContract

const model = (id: string): ManualProbeModelOption => ({ id, name: id })

const question = (
  id: string,
  overrides: Partial<TestQuestion> = {},
): TestQuestion => ({
  id,
  name: `Question ${id}`,
  body: `Body ${id}`,
  keywords: [],
  enabled: true,
  isDefault: false,
  createdAt: '2026-08-31T00:00:00Z',
  updatedAt: '2026-08-31T00:00:00Z',
  ...overrides,
})

const account = (
  targetId: string,
  overrides: Partial<AdminGroupAccount> = {},
): AdminGroupAccount => ({
  id: `account-${targetId}`,
  name: `Account ${targetId}`,
  platform: 'sub2api',
  type: 'proxy',
  status: 'active',
  targetId,
  probeAvailable: true,
  modelHealth: [],
  ...overrides,
})

const group = (
  id: string,
  accounts: AdminGroupAccount[],
  overrides: Partial<AdminGroupHealth> = {},
): AdminGroupHealth => ({
  id,
  name: `Group ${id}`,
  platform: 'sub2api',
  status: 'active',
  type: 'shared',
  isExclusive: false,
  subscriptionType: '',
  multiplier: null,
  multiplierDisplay: '-',
  accountCount: accounts.length,
  healthSummary: {
    totalModels: 0,
    healthyModels: 0,
    degradedModels: 0,
    suspendedModels: 0,
    disabledModels: 0,
    unconfiguredModels: 0,
    lastProbeAt: null,
  },
  accounts,
  ...overrides,
})

beforeEach(() => {
  window.localStorage.clear()
})

afterEach(() => {
  vi.restoreAllMocks()
  window.localStorage.clear()
})

describe('connection health question-answer preferences', () => {
  it('adds safe question-answer defaults without losing old v1 display preferences', () => {
    window.localStorage.setItem(connectionHealthPreferencesStorageKey('admin-a'), JSON.stringify({
      version: 1,
      groupOrder: ['g2', 'g1'],
      hiddenGroupIds: ['hidden'],
      hideUnmonitoredAccounts: true,
      questionAnswerUnreadTargetIds: ['target-unread'],
    }))

    const preferences = readConnectionHealthPreferences('admin-a') as ReturnType<typeof readConnectionHealthPreferences> & {
      questionAnswer?: unknown
    }

    expect(preferences).toMatchObject({
      version: 1,
      groupOrder: ['g2', 'g1'],
      hiddenGroupIds: ['hidden'],
      hideUnmonitoredAccounts: true,
      questionAnswerUnreadTargetIds: ['target-unread'],
      questionAnswer: {
        modelIds: [],
        questionIds: [],
        reasoningEffort: 'medium',
        repeatCount: 1,
        batchTargetIds: [],
      },
    })
  })

  it('normalizes malformed question-answer fields while retaining valid selections', () => {
    window.localStorage.setItem(connectionHealthPreferencesStorageKey('admin-a'), JSON.stringify({
      version: 1,
      groupOrder: [],
      hiddenGroupIds: [],
      hideUnmonitoredAccounts: false,
      questionAnswerUnreadTargetIds: [],
      questionAnswer: {
        modelIds: [' model-b ', 'model-b', '', 7, 'model-a'],
        questionIds: ['q2', null, ' q1 ', 'q2'],
        reasoningEffort: 'invalid',
        repeatCount: 1.5,
        batchTargetIds: ['target-b', 'target-b', ' ', 'target-a'],
      },
    }))

    const preferences = readConnectionHealthPreferences('admin-a') as ReturnType<typeof readConnectionHealthPreferences> & {
      questionAnswer?: unknown
    }

    expect(preferences.questionAnswer).toEqual({
      modelIds: ['model-b', 'model-a'],
      questionIds: ['q2', 'q1'],
      reasoningEffort: 'medium',
      repeatCount: 1,
      batchTargetIds: ['target-b', 'target-a'],
    })
  })

  it('keeps administrator scopes isolated and treats optional storage failures as non-blocking', () => {
    const first = {
      ...createDefaultConnectionHealthPreferences(),
      questionAnswer: {
        modelIds: ['model-a'],
        questionIds: ['q1'],
        reasoningEffort: 'high' as const,
        repeatCount: 4,
        batchTargetIds: ['target-a'],
      },
    }
    const second = {
      ...createDefaultConnectionHealthPreferences(),
      questionAnswer: {
        modelIds: ['model-b'],
        questionIds: ['q2'],
        reasoningEffort: 'low' as const,
        repeatCount: 2,
        batchTargetIds: ['target-b'],
      },
    }

    writeConnectionHealthPreferences('admin-a', first)
    writeConnectionHealthPreferences('admin-b', second)

    expect(readConnectionHealthPreferences('admin-a')).toMatchObject(first)
    expect(readConnectionHealthPreferences('admin-b')).toMatchObject(second)

    vi.spyOn(Storage.prototype, 'setItem').mockImplementationOnce(() => {
      throw new DOMException('quota exceeded', 'QuotaExceededError')
    })
    expect(() => writeConnectionHealthPreferences('admin-a', first)).not.toThrow()
  })
})

describe('question-answer selection helpers', () => {
  it('restores only legal models and enabled questions without mutating the saved preference', () => {
    expect(typeof questionAnswers.resolveQuestionAnswerSelection).toBe('function')
    const preferences: SelectionPreferences = {
      modelIds: ['model-b', 'missing', 'model-a'],
      questionIds: ['disabled', 'q2', 'missing'],
      reasoningEffort: 'xhigh',
      repeatCount: 10,
    }
    const snapshot = structuredClone(preferences)

    const resolved = questionAnswers.resolveQuestionAnswerSelection!(
      preferences,
      [model('model-a'), model('model-b')],
      [question('q1', { isDefault: true }), question('q2'), question('disabled', { enabled: false })],
    )

    expect(resolved).toEqual({
      modelIds: ['model-b', 'model-a'],
      questionIds: ['q2'],
      reasoningEffort: 'xhigh',
      repeatCount: 10,
    })
    expect(preferences).toEqual(snapshot)
  })

  it('uses the legal model and question defaults only when every saved choice is unavailable', () => {
    expect(typeof questionAnswers.resolveQuestionAnswerSelection).toBe('function')
    const saved: SelectionPreferences = {
      modelIds: ['missing'],
      questionIds: ['missing'],
      reasoningEffort: 'medium',
      repeatCount: 1,
    }

    expect(questionAnswers.resolveQuestionAnswerSelection!(
      saved,
      [model('model-a'), model('gpt-5.6-sol'), model('model-b')],
      [question('q1'), question('q2', { isDefault: true }), question('disabled', { enabled: false, isDefault: true })],
    )).toEqual({
      modelIds: ['gpt-5.6-sol'],
      questionIds: ['q2'],
      reasoningEffort: 'medium',
      repeatCount: 1,
    })

    expect(questionAnswers.resolveQuestionAnswerSelection!(
      saved,
      [model('model-a'), model('model-b')],
      [question('q1')],
    )).toMatchObject({ modelIds: ['model-a'], questionIds: [] })
  })

  it('deduplicates complete group targets by targetId without filtering account state', () => {
    expect(typeof questionAnswers.collectQuestionAnswerBatchTargets).toBe('function')
    const targets = questionAnswers.collectQuestionAnswerBatchTargets!([
      group('g1', [
        account('t1', { name: 'First t1', probeAvailable: false, schedulable: false, status: 'disabled' }),
        account('t2'),
      ]),
      group('g2', [account('t1', { name: 'Duplicate t1' }), account('t3')]),
    ])

    expect(targets.map(target => target.targetId)).toEqual(['t1', 't2', 't3'])
    expect(targets[0]).toMatchObject({
      targetId: 't1',
      accountName: 'First t1',
      status: 'disabled',
      groupIds: ['g1', 'g2'],
      groupNames: ['Group g1', 'Group g2'],
    })
  })

  it('reconciles stale ids but derives source and submit order from stable target display order', () => {
    expect(typeof questionAnswers.reconcileQuestionAnswerBatchTargetIds).toBe('function')
    expect(typeof questionAnswers.selectQuestionAnswerBatchTargets).toBe('function')
    const targets: BatchTarget[] = [
      { targetId: 't1', accountName: 'One', platform: 'sub2api', type: 'proxy', status: 'active', groupIds: ['g1'], groupNames: ['One'] },
      { targetId: 't2', accountName: 'Two', platform: 'sub2api', type: 'proxy', status: 'active', groupIds: ['g1'], groupNames: ['One'] },
      { targetId: 't3', accountName: 'Three', platform: 'sub2api', type: 'proxy', status: 'active', groupIds: ['g1'], groupNames: ['One'] },
    ]

    expect(questionAnswers.reconcileQuestionAnswerBatchTargetIds!(['t2', 't2', 'gone', 't1'], targets)).toEqual(['t2', 't1'])
    expect(questionAnswers.selectQuestionAnswerBatchTargets!(targets, ['t2', 't1']).map(target => target.targetId)).toEqual(['t1', 't2'])
  })

  it('keeps selected model order while reporting incompatible models separately', () => {
    expect(typeof questionAnswers.compatibleQuestionAnswerModelIds).toBe('function')

    expect(questionAnswers.compatibleQuestionAnswerModelIds!(
      ['model-b', 'model-a', 'missing'],
      [model('model-a'), model('model-b')],
    )).toEqual({
      compatible: ['model-b', 'model-a'],
      incompatible: ['missing'],
    })
  })
})
