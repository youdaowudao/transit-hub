import assert from 'node:assert/strict'
import { chmod, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import { spawnSync } from 'node:child_process'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const rootDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const scriptPath = path.join(rootDir, 'scripts/question-answer-keyword-highlight-fixture.sh')
const sqlPath = path.join(
  rootDir,
  'backend/internal/modules/connection_health/testdata/task3_question_answer_keyword_highlight_browser_fixture.sql',
)

const fixtureEnv = {
  TASK3_BROWSER_DATABASE_URL: 'postgres://fixture:do-not-print@localhost:5432/transithub',
  TASK3_BROWSER_USER_ID: 'task3-browser-user',
  TASK3_BROWSER_TARGET_ID: 'sub2api:task3-browser-target',
}

async function runFixture(action, overrides = {}) {
  const fakeBin = await mkdtemp(path.join(os.tmpdir(), 'task3-keyword-fixture-test-'))
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

test('task3 fixture wrapper rejects missing environment, remote databases and unknown actions', async () => {
  const missing = await runFixture('count', { TASK3_BROWSER_USER_ID: '' })
  assert.notEqual(missing.status, 0)
  assert.match(`${missing.stdout}\n${missing.stderr}`, /TASK3_BROWSER_USER_ID/)

  const remote = await runFixture('count', {
    TASK3_BROWSER_DATABASE_URL: 'postgres://fixture:secret@db.example.com:5432/transithub',
  })
  assert.notEqual(remote.status, 0)
  assert.match(`${remote.stdout}\n${remote.stderr}`, /回环|loopback/i)

  const unknown = await runFixture('destroy')
  assert.notEqual(unknown.status, 0)
  assert.match(`${unknown.stdout}\n${unknown.stderr}`, /prepare|count|cleanup/)
})

test('task3 fixture wrapper accepts supported loopback hosts without printing credentials', async () => {
  for (const [host, serverAddr] of [
    ['localhost', '127.0.0.1/32'],
    ['127.0.0.1', '127.0.0.1/32'],
    ['[::1]', '::1/128'],
  ]) {
    const result = await runFixture('count', {
      TASK3_BROWSER_DATABASE_URL: `postgres://fixture:do-not-print@${host}:5432/transithub`,
      FAKE_PSQL_SERVER_ADDR: serverAddr,
      FAKE_PSQL_OUTPUT: 'fixture_total=0 latest=0 old_snapshot=0 highlight_limit=0 highlight_overlap=0 non_fixture_active=0\n',
    })
    assert.equal(result.status, 0, `${host}: ${result.stderr}`)
    assert.match(result.stdout, /fixture_total=0/)
    assert.doesNotMatch(`${result.stdout}\n${result.stderr}`, /do-not-print|postgres:\/\//)
  }
})

test('task3 fixture wrapper rejects URL and environment routes that can redirect libpq', async () => {
  for (const query of [
    'hostaddr=203.0.113.10',
    'host=db.example.com',
    'service=remote-database',
    'servicefile=%2Ftmp%2Fremote.conf',
  ]) {
    const result = await runFixture('count', {
      TASK3_BROWSER_DATABASE_URL: `postgres://fixture:secret@localhost:5432/transithub?${query}`,
    })
    assert.notEqual(result.status, 0, query)
    assert.match(`${result.stdout}\n${result.stderr}`, /回环|连接目标|host|service/i)
  }

  for (const authority of [
    'localhost:5432,db.example.com:5432',
    'localhost:5432,127.0.0.1:5432',
  ]) {
    const result = await runFixture('prepare', {
      TASK3_BROWSER_DATABASE_URL: `postgres://fixture:secret@${authority}/transithub`,
      FAKE_PSQL_OUTPUT: 'action-must-not-run\n',
    })
    assert.notEqual(result.status, 0, authority)
    assert.match(`${result.stdout}\n${result.stderr}`, /回环|多主机|failover|连接目标/i)
    assert.doesNotMatch(result.stdout, /action-must-not-run/)
  }

  const inherited = await runFixture('count', {
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
    FAKE_PSQL_OUTPUT: 'fixture_total=0\n',
  })
  assert.equal(inherited.status, 0, `${inherited.stdout}\n${inherited.stderr}`)
  assert.doesNotMatch(`${inherited.stdout}\n${inherited.stderr}`, /do-not-print-environment-secret/)
})

test('task3 fixture wrapper verifies the actual server and propagates exact-row failures', async () => {
  const remoteServer = await runFixture('prepare', {
    FAKE_PSQL_SERVER_ADDR: '203.0.113.10',
    FAKE_PSQL_OUTPUT: 'should-not-run\n',
  })
  assert.notEqual(remoteServer.status, 0)
  assert.match(`${remoteServer.stdout}\n${remoteServer.stderr}`, /回环|实际|server/i)
  assert.doesNotMatch(remoteServer.stdout, /should-not-run/)

  for (const [action, diagnostic] of [
    ['prepare', 'fixture id already exists'],
    ['prepare', 'non-fixture active batch exists'],
    ['cleanup', 'expected exactly ten deleted rows'],
  ]) {
    const result = await runFixture(action, {
      FAKE_PSQL_EXIT: '44',
      FAKE_PSQL_OUTPUT: `${diagnostic}\n`,
    })
    assert.notEqual(result.status, 0, `${action} must not swallow psql failure`)
    assert.match(`${result.stdout}\n${result.stderr}`, new RegExp(diagnostic))
  }
})

test('task3 fixture fixes snapshot states and ordinary answers with more than three matches', async () => {
  const script = await readFile(scriptPath, 'utf8')
  const sql = await readFile(sqlPath, 'utf8')

  assert.match(script, /prepare\|count\|cleanup/)
  assert.match(sql, /task3-latest-20260831/)
  assert.match(sql, /task3-old-snapshot-20260831/)
  assert.match(sql, /task3-highlight-limit-20260831/)
  assert.match(sql, /task3-highlight-overlap-20260831/)
  assert.match(sql, /question_keyword_snapshot/)
  assert.match(sql, /ARRAY\[\]::text\[\]/)
  assert.match(sql, /question_keyword_snapshot\s+IS\s+NULL/i)
  assert.match(sql, /<script>alert\(1\)<\/script>/)
  assert.match(sql, /generate_series\(1, 20\)/)
  assert.match(sql, /Error one\. Error two\. Error three\. Error four\./)
  assert.match(sql, /错误码 one；错误码 two；错误码 three；错误码 four。/)
  assert.doesNotMatch(sql, /repeat\('ab', 524288\)|octet_length\(answer_body\)\s*=\s*1048576/i)
  assert.match(sql, /pg_advisory_xact_lock/)
  assert.match(sql, /expected exactly ten inserted rows/)
  assert.match(sql, /expected exactly ten deleted rows/)
  assert.doesNotMatch(sql, /DELETE[\s\S]{0,500}\bLIKE\b/i)
})
