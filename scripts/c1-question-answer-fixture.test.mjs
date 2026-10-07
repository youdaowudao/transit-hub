import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const load = async relative => JSON.parse(await readFile(path.join(root, relative), 'utf8'))
const canonicalCases = cases => cases.map(value => ({ ...value })).sort((a, b) => a.id.localeCompare(b.id, 'en'))

test('C1 Go and TypeScript fixtures preserve every literal keyword case', async () => {
  const backend = await load('backend/internal/modules/connection_health/testdata/c1_keyword_cases.json')
  const frontend = await load('frontend/tests/fixtures/c1_keyword_cases.json')
  assert.deepEqual(canonicalCases(backend), canonicalCases(frontend))
  assert.equal(new Set(backend.map(value => value.id)).size, backend.length, 'case IDs must be unique')
  for (const id of ['first', 'later', 'fourth', 'ascii-case', 'chinese-exact', 'chinese-different', 'literal-symbol', 'not-regex', 'internal-space', 'space-exact', 'overlap', 'substring', 'non-ascii-case', 'non-ascii-exact', 'no-punctuation-normalization', 'empty-answer', 'empty-keywords', 'nil-keywords', 'blank-keywords']) {
    assert.ok(backend.some(value => value.id === id), `missing C1 case: ${id}`)
  }
})

test('C1 fixture comparison runs in both local core and full CI gates', async () => {
  for (const script of ['scripts/test-core-regression.sh', 'scripts/test-full-regression.sh']) {
    assert.ok((await readFile(path.join(root, script), 'utf8')).includes('c1-question-answer-fixture.test.mjs'), `${script} must compare C1 fixtures`)
  }
  const core = await readFile(path.join(root, 'scripts/test-core-regression.sh'), 'utf8')
  for (const member of ['c1-question-answer-red.behavior.test.ts', 'c1-question-answer-keywords.test.ts']) {
    assert.ok(core.includes(member), `missing C1 core regression: ${member}`)
  }
})
