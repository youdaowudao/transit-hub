import assert from 'node:assert/strict'
import { chmod, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import { spawnSync } from 'node:child_process'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const rootDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const scriptPath = path.join(rootDir, 'scripts/question-answer-review-fixture.sh')
const sqlPath = path.join(
  rootDir,
  'backend/internal/modules/connection_health/testdata/task1_question_answer_browser_fixture.sql',
)

const fixtureEnv = {
  TASK1_BROWSER_DATABASE_URL: 'postgres://fixture:do-not-print@localhost:5432/transithub',
  TASK1_BROWSER_USER_ID: 'task1-browser-user',
  TASK1_BROWSER_TARGET_ID: 'sub2api:task1-browser-target',
}

async function runFixture(action, overrides = {}) {
  const fakeBin = await mkdtemp(path.join(os.tmpdir(), 'task1-fixture-test-'))
  const psqlPath = path.join(fakeBin, 'psql')
  await writeFile(psqlPath, `#!/usr/bin/env bash
set -eu
if [[ "\${FAKE_PSQL_ASSERT_NO_REDIRECT_ENV:-}" == "1" ]]; then
  for variable_name in PGHOST PGHOSTADDR PGPORT PGDATABASE PGUSER PGPASSWORD PGPASSFILE PGSERVICE PGSERVICEFILE; do
    if [[ -n "\${!variable_name+x}" ]]; then
      printf 'unsafe-libpq-environment:%s' "$variable_name" >&2
      exit 73
    fi
  done
fi
for argument in "$@"; do
  case "$argument" in
    *inet_server_addr*)
      printf '%s' "\${FAKE_PSQL_SERVER_ADDR:-127.0.0.1}"
      exit "\${FAKE_PSQL_SERVER_ADDR_EXIT:-0}"
      ;;
  esac
done
printf '%s' "\${FAKE_PSQL_OUTPUT:-}"
exit "\${FAKE_PSQL_EXIT:-0}"
`)
  await chmod(psqlPath, 0o755)

  try {
    return spawnSync('bash', [scriptPath, action], {
      cwd: rootDir,
      encoding: 'utf8',
      env: {
        ...process.env,
        PATH: `${fakeBin}:${process.env.PATH ?? ''}`,
        ...fixtureEnv,
        ...overrides,
      },
    })
  } finally {
    await rm(fakeBin, { force: true, recursive: true })
  }
}

test('fixture wrapper rejects missing required environment before psql', async () => {
  const result = await runFixture('count', { TASK1_BROWSER_USER_ID: '' })
  assert.notEqual(result.status, 0)
  assert.match(`${result.stdout}\n${result.stderr}`, /TASK1_BROWSER_USER_ID/)
})

test('fixture wrapper rejects non-loopback databases and unknown actions', async () => {
  const remote = await runFixture('count', {
    TASK1_BROWSER_DATABASE_URL: 'postgres://fixture:secret@db.example.com:5432/transithub',
  })
  assert.notEqual(remote.status, 0)
  assert.match(`${remote.stdout}\n${remote.stderr}`, /回环|loopback/i)

  const unknown = await runFixture('destroy')
  assert.notEqual(unknown.status, 0)
  assert.match(`${unknown.stdout}\n${unknown.stderr}`, /prepare|advance|count|cleanup/)
})

test('fixture wrapper accepts every supported loopback host without printing credentials', async () => {
  for (const [host, serverAddr] of [
    ['localhost', '127.0.0.1/32'],
    ['127.0.0.1', '127.0.0.1/32'],
    ['[::1]', '::1/128'],
  ]) {
    const databaseURL = `postgres://fixture:do-not-print@${host}:5432/transithub`
    const result = await runFixture('count', {
      TASK1_BROWSER_DATABASE_URL: databaseURL,
      FAKE_PSQL_SERVER_ADDR: serverAddr,
      FAKE_PSQL_OUTPUT: 'fixture_total=0 non_fixture_active=0\n',
    })
    assert.equal(result.status, 0, `${host}: ${result.stderr}`)
    assert.match(result.stdout, /fixture_total=0/)
    assert.doesNotMatch(`${result.stdout}\n${result.stderr}`, /do-not-print|postgres:\/\//)
  }
})

test('fixture wrapper rejects libpq parameters that can redirect a loopback URI', async () => {
  for (const query of [
    'hostaddr=203.0.113.10',
    'host=db.example.com',
    'service=remote-database',
    'servicefile=%2Ftmp%2Fremote.conf',
  ]) {
    const result = await runFixture('count', {
      TASK1_BROWSER_DATABASE_URL: `postgres://fixture:secret@localhost:5432/transithub?${query}`,
      FAKE_PSQL_OUTPUT: 'fixture_total=0 non_fixture_active=0\n',
    })
    assert.notEqual(result.status, 0, query)
    assert.match(`${result.stdout}\n${result.stderr}`, /回环|连接目标|host|service/i)
  }
})

test('fixture wrapper rejects libpq multi-host urls before any action can fail over', async () => {
  for (const authority of [
    'localhost:5432,db.example.com:5432',
    'localhost:5432,127.0.0.1:5432',
  ]) {
    const result = await runFixture('prepare', {
      TASK1_BROWSER_DATABASE_URL: `postgres://fixture:secret@${authority}/transithub`,
      FAKE_PSQL_SERVER_ADDR: '127.0.0.1/32',
      FAKE_PSQL_OUTPUT: 'action-must-not-run\n',
    })
    assert.notEqual(result.status, 0, authority)
    assert.match(`${result.stdout}\n${result.stderr}`, /回环|多主机|failover|连接目标/i)
    assert.doesNotMatch(result.stdout, /action-must-not-run/)
  }
})

test('fixture wrapper clears inherited libpq connection variables before the first connection', async () => {
  const result = await runFixture('count', {
    FAKE_PSQL_ASSERT_NO_REDIRECT_ENV: '1',
    PGHOST: 'db.example.com',
    PGHOSTADDR: '203.0.113.10',
    PGPORT: '6543',
    PGDATABASE: 'other-database',
    PGUSER: 'other-user',
    PGPASSWORD: 'do-not-print-environment-secret',
    PGPASSFILE: '/tmp/remote.pgpass',
    PGSERVICE: 'remote-database',
    PGSERVICEFILE: '/tmp/remote-service.conf',
    FAKE_PSQL_OUTPUT: 'fixture_total=0 non_fixture_active=0\n',
  })

  assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`)
  assert.match(result.stdout, /fixture_total=0/)
  assert.doesNotMatch(`${result.stdout}\n${result.stderr}`, /do-not-print-environment-secret/)
})

test('fixture wrapper confirms the actual PostgreSQL server is loopback before running an action', async () => {
  const result = await runFixture('prepare', {
    FAKE_PSQL_SERVER_ADDR: '203.0.113.10',
    FAKE_PSQL_OUTPUT: 'should-not-run\n',
  })
  assert.notEqual(result.status, 0)
  assert.match(`${result.stdout}\n${result.stderr}`, /回环|实际|server/i)
  assert.doesNotMatch(result.stdout, /should-not-run/)
})

test('fixture wrapper propagates prepare, advance, and cleanup safety failures', async () => {
  for (const [action, diagnostic] of [
    ['prepare', 'fixture id already exists'],
    ['prepare', 'non-fixture active batch exists'],
    ['advance', 'expected exactly two old states'],
    ['cleanup', 'expected exactly eight deleted rows'],
  ]) {
    const result = await runFixture(action, {
      FAKE_PSQL_EXIT: '44',
      FAKE_PSQL_OUTPUT: `${diagnostic}\n`,
    })
    assert.notEqual(result.status, 0, `${action} must not swallow psql failure`)
    assert.match(`${result.stdout}\n${result.stderr}`, new RegExp(diagnostic))
  }
})

test('fixture SQL uses exact IDs, the production advisory lock, and no broad delete', async () => {
  const sql = await readFile(sqlPath, 'utf8')
  assert.match(sql, /question-answer\|/)
  assert.match(sql, /pg_advisory_xact_lock/)
  assert.match(sql, /task1-review-20260830-batch/)
  assert.match(sql, /task1-review-20260830-pending/)
  assert.match(sql, /task1-review-20260830-running/)
  assert.match(sql, /GET DIAGNOSTICS|ROW_COUNT/)
  assert.doesNotMatch(sql, /DELETE[\s\S]{0,500}\bLIKE\b/i)
})
