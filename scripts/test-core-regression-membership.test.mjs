import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const rootDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')

test('connection-health core gate keeps every question-answer regression member', async () => {
  const script = await readFile(path.join(rootDir, 'scripts/test-core-regression.sh'), 'utf8')
  for (const member of [
    'v300-default-selection.behavior.test.ts',
    'v300-managed-selection-critical.behavior.test.ts',
    'v300-c1-selection-critical.behavior.test.ts',
    'v300-model-control-api.behavior.test.ts',
    'v300-model-control-row.behavior.test.ts',
    './internal/database/migrations',
    'TestModelControlV300',
    'messages-c42.behavior.test.ts',
    'messages-c41.behavior.test.ts',
    'v2910-model-control.behavior.test.ts',
    'v2911-c1-managed.behavior.test.ts',
    'v2911-model-control-utils.test.ts',
    'v2911-model-control-extra.behavior.test.ts',
    'v2911-managed-selection.behavior.test.ts',
    'v2911-model-supply.behavior.test.ts',
    'ui-plain-language.test.ts',
    'c3-model-control.behavior.test.ts',
    'c3-question-answer-integration.behavior.test.ts',
    'c32-model-control-api.behavior.test.ts',
    'c32-model-control-auto.behavior.test.ts',
    'c32-model-control-health.behavior.test.ts',
    'c2-question-answer-red.behavior.test.ts',
    'c2-question-answer-http-origin.behavior.test.ts',
    'c2-question-answer-schedules.behavior.test.ts',
    'c2-recent-summaries.behavior.test.ts',
    'c1-question-answer-red.behavior.test.ts',
    'c1-question-answer-keywords.test.ts',
    'c1-question-answer.behavior.test.ts',
    'question-answer-errors-compatibility.behavior.test.ts',
    'question-answer-diagnostics.behavior.test.ts',
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
    'c1-question-answer-fixture.test.mjs',
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
    'c1-question-answer-fixture.test.mjs',
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

test('core gate includes C5 import settings and retained group rates behavior', async () => {
  const script = await readFile(path.join(rootDir, 'scripts/test-core-regression.sh'), 'utf8')
  for (const member of [
    './internal/modules/my_sites',
    'group-rates-import-settings.behavior.test.ts',
    'group-rates-missing-connection.behavior.test.ts',
  ]) {
    assert.ok(script.includes(member), `missing C5 core regression: ${member}`)
  }
})

test('core gate includes dated cost business and storage regressions', async () => {
  const script = await readFile(path.join(rootDir, 'scripts/test-core-regression.sh'), 'utf8')
  for (const member of ['singapore-cost-critical.behavior.test.ts', 'singapore-cost-dashboard-hydration.behavior.test.ts', 'singapore-cost-sites.behavior.test.ts', 'singapore-cost-daily-stats.behavior.test.ts']) assert.ok(script.includes(member), `missing dated cost core regression: ${member}`)
  assert.match(script, /go test -race \.\/internal\/modules\/upstream \.\/internal\/modules\/dashboard -run 'TestSingaporeCost'/)
  assert.ok(script.includes('./internal/modules/dashboard'))
})


test('core gate keeps the scoped group rates business regressions', async () => {
  const script = await readFile(path.join(rootDir, 'scripts/test-core-regression.sh'), 'utf8')
  assert.match(script, /group-rates-scoped-critical\.behavior\.test\.ts/)
  assert.match(script, /group-rates-scoped-additional\.behavior\.test\.ts/)
  assert.match(script, /group-rates-scoped-api\.additional\.test\.ts/)
  assert.match(script, /group-rates-import-settings\.behavior\.test\.ts/)
  assert.match(script, /group-rates-missing-connection\.behavior\.test\.ts/)
})

test('core gate retains group cost queue, cache refresh and shutdown regressions', async () => {
  const script = await readFile(path.join(rootDir, 'scripts/test-core-regression.sh'), 'utf8')
  for (const member of ['upstream-group-cost-refresh.behavior.test.ts', 'upstream-group-cost-additional.behavior.test.ts', 'upstream-group-cost-api.test.ts', 'upstream-group-rates-warning.behavior.test.ts']) {
    assert.ok(script.includes(member), `missing group cost regression: ${member}`)
  }
  assert.ok(script.includes("go test -race ./internal/modules/upstream ./internal/httpserver -run 'TestGroupCost(Queue|Retry|Snapshot|Shutdown)' -count=1"))
})


test('core gate discovers Responses answer behavior in its own step', async () => {
  const script = await readFile(path.join(rootDir, 'scripts/test-core-regression.sh'), 'utf8')
  assert.match(script, /run_step "frontend Responses question answer failure evidence"/)
  assert.match(script, /npm run test -- responses-question-answer\.behavior\.test\.ts"/)
  assert.ok(script.indexOf('frontend Responses question answer failure evidence') > script.indexOf('question answer browser fixture safety tests'))
})
