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
    'connection-health-question-answer-repeat-queue.behavior.test.ts',
    'connection-health-question-keywords.behavior.test.ts',
    'question-answer-review-fixture.test.mjs',
    'question-answer-batch-review-fixture.test.mjs',
    'question-answer-keyword-highlight-fixture.test.mjs',
    "Test.*QuestionAnswer",
    "Test.*QuestionAnswerKeyword",
  ]) {
    assert.match(script, new RegExp(member.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
  }
  assert.match(script, /go test -race/)
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
