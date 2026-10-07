import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const rootDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')

test('connection-health core gate keeps every question-answer regression member', async () => {
  const script = await readFile(path.join(rootDir, 'scripts/test-core-regression.sh'), 'utf8')
  for (const member of [
    'connection-health-question-answer.test.ts',
    'connection-health-question-answer.behavior.test.ts',
    'connection-health-question-answer-compact-layout.behavior.test.ts',
    'connection-health-question-answer-repeat-queue.behavior.test.ts',
    'connection-health-question-answer-preferences.test.ts',
    'connection-health-question-answer-batch.behavior.test.ts',
    'connection-health-question-keywords.behavior.test.ts',
    'connection-health-today-accuracy.behavior.test.ts',
    'connection-health-intelligence-weight.behavior.test.ts',
    'connection-health-account-tier.behavior.test.ts',
    'connection-health-account-management.behavior.test.ts',
    'connection-health-priority-failure-reason.behavior.test.ts',
    'connection-health-quick-probe.behavior.test.ts',
    'group-health-setup-exclusion-outcome.behavior.test.ts',
    'connection-health-history-display.test.ts',
    'connection-health-manual-probe-cancel.test.ts',
    'question-answer-review-fixture.test.mjs',
    'question-answer-batch-review-fixture.test.mjs',
    'question-answer-keyword-highlight-fixture.test.mjs',
    "Test.*QuestionAnswer",
    "Test.*QuestionAnswerKeyword",
    "TestHandler.*RetiredIntelligenceWeightRequest",
  ]) {
    assert.match(script, new RegExp(member.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
  }
  assert.match(script, /go test -race/)
  assert.doesNotMatch(script, /connection-health-priority-candidates\.behavior\.test\.ts/)
})

test('full gate runs fixture safety and guards core membership', async () => {
  const script = await readFile(path.join(rootDir, 'scripts/test-full-regression.sh'), 'utf8')
  for (const member of [
    'question-answer-review-fixture.test.mjs',
    'question-answer-batch-review-fixture.test.mjs',
    'question-answer-keyword-highlight-fixture.test.mjs',
    'test-core-regression-membership.test.mjs',
  ]) {
    assert.match(script, new RegExp(member.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
  }
})

test('connection-health core gate includes protocol concurrency contracts', async () => {
  const script = await readFile(path.join(rootDir, 'scripts/test-core-regression.sh'), 'utf8')
  assert.match(script, /go test -race[^\n]+Priority\|Scheduler\|Refresh\|Sync\|Regression\|Protocol\|ActionCheckpoint\|ActionDiagnostics\|ActionDispatch\|RuntimeLease/)
  assert.match(script, /go test -race[^\n]+TierSort\|ManualSettings\|TaskB/)
  assert.match(script, /go test -race \.\/internal\/modules\/upstream -run 'TestTaskBConcurrency'/)
})

test('core gate retains globalization recipient, language and amount regressions', async () => {
  const script = await readFile(path.join(rootDir, 'scripts/test-core-regression.sh'), 'utf8')
  for (const member of [
    './cmd/notification-cleanup', './internal/modules/settings', './internal/shared/businesstime',
    './internal/modules/my_sites', './internal/modules/group_rate_campaigns',
    'locale-configuration.test.ts', 'globalization-amount.behavior.test.ts', 'globalization-time-input.behavior.test.ts',
    'notification-recipients.behavior.test.ts', 'user-last-used.test.ts',
    'run_globalization_gate',
  ]) {
    assert.ok(script.includes(member), `missing globalization core regression: ${member}`)
  }
})
