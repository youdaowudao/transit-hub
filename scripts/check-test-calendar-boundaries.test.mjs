import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const guardPath = fileURLToPath(new URL('./check-test-calendar-boundaries.mjs', import.meta.url))

const runGuard = (files) => {
  const root = mkdtempSync(join(tmpdir(), 'transithub-test-calendar-'))
  try {
    for (const [relativePath, source] of Object.entries(files)) {
      const filePath = join(root, relativePath)
      mkdirSync(dirname(filePath), { recursive: true })
      writeFileSync(filePath, source)
    }
    return spawnSync(process.execPath, [guardPath, '--root', root], { encoding: 'utf8' })
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
}

test('rejects Go tests that mix fixed dates with a live calendar clock', () => {
  const result = runGuard({
    'backend/example_test.go': `package example

func TestInjectedClockIsSafe(t *testing.T) {
  now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
  _ = now
  _ = "2026-08-22"
}

func TestElapsedClockIsSafe(t *testing.T) {
  started := time.Now()
  _ = started
  _ = "2026-08-22"
}

func TestElapsedClockDoesNotBindAnotherFormatter(t *testing.T) {
  started := time.Now()
  formatted := injectedClock.Format("2006-01-02")
  _ = []any{started, formatted, "2028-01-02"}
}

func TestCommentedLiveClockIsSafe(t *testing.T) {
  // Historical note: businesstime.Today() used to make this fixture expire.
  _ = "2028-01-02"
}

func TestCommentedFixedDateIsSafe(t *testing.T) {
  today := businesstime.Today()
  // Historical fixture: 2028-01-02.
  _ = today
}

func TestFixedConstructorTextIsSafe(t *testing.T) {
  today := businesstime.Today()
  _ = []any{today, "example: time.Date(2031, 1, 2, 0, 0, 0, 0, time.UTC)"}
}

func TestLiveBusinessDayIsRejected(t *testing.T) {
  today := businesstime.Today()
  _ = today
  _ = "2026-08-22"
}

func TestFormattedWallClockIsRejected(t *testing.T) {
  today := time.Now().In(time.Local).Format("2006-01-02")
  _ = today
  _ = "2026-08-22"
}

func TestCapturedWallClockIsRejected(t *testing.T) {
  now := time.Now()
  today := now.In(time.Local).Format("2006-01-02")
  _ = []any{today, "2033-01-02"}
}

func TestFarFutureFixedDateIsRejected(t *testing.T) {
  today := businesstime.Today()
  _ = today
  _ = "2031-01-02"
}
`,
  })

  assert.equal(result.status, 1)
  assert.match(result.stderr, /example_test\.go:TestLiveBusinessDayIsRejected/)
  assert.match(result.stderr, /example_test\.go:TestFormattedWallClockIsRejected/)
  assert.match(result.stderr, /example_test\.go:TestCapturedWallClockIsRejected/)
  assert.match(result.stderr, /example_test\.go:TestFarFutureFixedDateIsRejected/)
  assert.doesNotMatch(result.stderr, /TestInjectedClockIsSafe/)
  assert.doesNotMatch(result.stderr, /TestElapsedClockIsSafe/)
  assert.doesNotMatch(result.stderr, /TestElapsedClockDoesNotBindAnotherFormatter/)
  assert.doesNotMatch(result.stderr, /TestCommentedLiveClockIsSafe/)
  assert.doesNotMatch(result.stderr, /TestCommentedFixedDateIsSafe/)
  assert.doesNotMatch(result.stderr, /TestFixedConstructorTextIsSafe/)
})

test('rejects Vitest blocks that mix fixed dates with the live JavaScript clock', () => {
  const result = runGuard({
    'frontend/tests/example.test.ts': `import { it, it as check, test, vi } from 'vitest'

it('injected clock is safe', () => {
  const now = new Date('2026-08-22T10:00:00Z')
  return now
})

it('live date is rejected', () => {
  const now = new Date()
  return ['2026-08-22', now]
})

test('live timestamp is rejected', () => {
  return ['2026-08-22', Date.now()]
})

it.each([1])('table live date is rejected', () => {
  return ['2026-08-22', new Date()]
})

test.concurrent('concurrent live date is rejected', () => {
  return ['2026-08-22', Date.now()]
})

check.skipIf(false).sequential.for([1])(
  \`dynamic modifier chain \${1}\`,
  () => ['2031-01-02', Date.now()],
)

test.runIf(true).fails('conditional modifier chain is rejected', () => {
  return ['2032-01-02', new Date()]
})

it('commented live date is safe', () => {
  // Historical note: Date.now() used to make this fixture expire.
  return '2028-01-02'
})

it('commented fixed date is safe', () => {
  const now = new Date()
  // Historical fixture: 2028-01-02.
  return now
})

it('live-clock text in a string is safe', () => {
  return ['2028-01-02', 'Date.now()', 'new Date()']
})

test('options callback is rejected', { timeout: 100 }, () => {
  return ['2033-01-02', Date.now()]
})

it.each([1])('table options callback is rejected', { timeout: 100 }, () => {
  return ['2034-01-02', new Date()]
})

it('template expression is rejected', () => {
  return ['2035-01-02', \`\${Date.now()}\`]
})

it('numeric date constructor is rejected', () => {
  return [new Date(2036, 0, 2), new Date()]
})

it('UTC date constructor is rejected', () => {
  return [Date.UTC(2037, 0, 2), Date.now()]
})

test('trailing timeout callback is rejected', () => {
  return ['2038-01-02', Date.now()]
}, 100)

it.each([1])('table trailing timeout callback is rejected', () => {
  return ['2039-01-02', new Date()]
}, 100)

it('fixed fake system time is safe', () => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2040-01-02T10:00:00Z'))
  const now = Date.now()
  vi.useRealTimers()
  return ['2040-01-02', now]
})

it('fixed fake timer option is safe', () => {
  vi.useFakeTimers({ now: new Date('2042-01-02T10:00:00Z') })
  const now = new Date()
  vi.useRealTimers()
  return ['2042-01-02', now]
})

it('fixed fake system time variable is safe', () => {
  const fixedNow = new Date('2043-01-02T10:00:00Z')
  vi.useFakeTimers()
  vi.setSystemTime(fixedNow)
  const now = Date.now()
  vi.useRealTimers()
  return ['2043-01-02', now]
})

it('conditional fake system time is rejected', () => {
  vi.useFakeTimers()
  if (false) vi.setSystemTime(new Date('2044-01-02T10:00:00Z'))
  return ['2044-01-02', Date.now()]
})

it('restored real time is rejected', () => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2041-01-02T10:00:00Z'))
  vi.useRealTimers()
  return ['2041-01-02', Date.now()]
})
`,
    'frontend/tests/hooked.test.ts': `import { afterEach, beforeEach, describe, it, vi } from 'vitest'

const FIXED_HOOK_NOW = new Date('2045-01-02T10:00:00Z')
const FIXED_TEST_NOW = new Date('2047-01-02T10:00:00Z')

describe('fixed clock scope', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(FIXED_HOOK_NOW)
  })

  afterEach(() => vi.useRealTimers())

  it('fixed beforeEach system time is safe', () => {
    return ['2045-01-02', Date.now()]
  })
})

it('outside fixed beforeEach scope is rejected', () => {
  return ['2046-01-02', Date.now()]
})

it('fixed outer system time variable is safe', () => {
  vi.useFakeTimers()
  vi.setSystemTime(FIXED_TEST_NOW)
  const now = Date.now()
  vi.useRealTimers()
  return ['2047-01-02', now]
})
`,
  })

  assert.equal(result.status, 1)
  assert.match(result.stderr, /example\.test\.ts:live date is rejected/)
  assert.match(result.stderr, /example\.test\.ts:live timestamp is rejected/)
  assert.match(result.stderr, /example\.test\.ts:table live date is rejected/)
  assert.match(result.stderr, /example\.test\.ts:concurrent live date is rejected/)
  assert.match(result.stderr, /example\.test\.ts:dynamic test at line/)
  assert.match(result.stderr, /example\.test\.ts:conditional modifier chain is rejected/)
  assert.match(result.stderr, /example\.test\.ts:options callback is rejected/)
  assert.match(result.stderr, /example\.test\.ts:table options callback is rejected/)
  assert.match(result.stderr, /example\.test\.ts:template expression is rejected/)
  assert.match(result.stderr, /example\.test\.ts:numeric date constructor is rejected/)
  assert.match(result.stderr, /example\.test\.ts:UTC date constructor is rejected/)
  assert.match(result.stderr, /example\.test\.ts:trailing timeout callback is rejected/)
  assert.match(result.stderr, /example\.test\.ts:table trailing timeout callback is rejected/)
  assert.match(result.stderr, /example\.test\.ts:restored real time is rejected/)
  assert.match(result.stderr, /example\.test\.ts:conditional fake system time is rejected/)
  assert.match(result.stderr, /hooked\.test\.ts:outside fixed beforeEach scope is rejected/)
  assert.doesNotMatch(result.stderr, /injected clock is safe/)
  assert.doesNotMatch(result.stderr, /commented live date is safe/)
  assert.doesNotMatch(result.stderr, /commented fixed date is safe/)
  assert.doesNotMatch(result.stderr, /live-clock text in a string is safe/)
  assert.doesNotMatch(result.stderr, /fixed fake system time is safe/)
  assert.doesNotMatch(result.stderr, /fixed fake timer option is safe/)
  assert.doesNotMatch(result.stderr, /fixed fake system time variable is safe/)
  assert.doesNotMatch(result.stderr, /hooked\.test\.ts:fixed beforeEach system time is safe/)
  assert.doesNotMatch(result.stderr, /hooked\.test\.ts:fixed outer system time variable is safe/)
})

test('does not combine fixed dates and live clocks from separate tests', () => {
  const result = runGuard({
    'frontend/tests/separate.test.ts': `import { test } from 'vitest'

test('fixed fixture', () => '2026-08-22')
test('live elapsed value', () => Date.now())
`,
    'backend/separate_test.go': `package example

func TestFixedFixture(t *testing.T) { _ = "2026-08-22" }
func TestLiveBusinessDay(t *testing.T) { _ = businesstime.Today() }
func TestDynamicFormatOnly(t *testing.T) { _ = time.Now().Format("2006-01-02") }
`,
  })

  assert.equal(result.status, 0, result.stderr)
})
