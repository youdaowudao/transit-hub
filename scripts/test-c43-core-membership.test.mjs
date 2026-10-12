import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

test('C4.3 rules are included in the existing frontend and backend race core gates', async () => {
  const script = await readFile(new URL('./test-core-regression.sh', import.meta.url), 'utf8')
  assert.match(script, /npm run test -- probe-models-c43\.behavior\.test\.ts/)
  assert.match(script, /go test -race[^\n]+ProbeModelC43/)
})
