#!/usr/bin/env node
import fs from 'node:fs'
import path from 'node:path'
import http from 'node:http'
import crypto from 'node:crypto'
import { execFileSync, spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const workspace = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const commonGit = execFileSync('git', ['rev-parse', '--git-common-dir'], { cwd: workspace, encoding: 'utf8' }).trim()
const project = path.resolve(workspace, commonGit, '..')
const runtime = path.join(workspace, '.cache/c5-preview')
const stateFile = path.join(runtime, 'processes.json')
const executable = path.join(runtime, 'transithub-c5-api')
const viteEntry = path.join(workspace, 'frontend/node_modules/vite/bin/vite.js')
const viteConfig = path.join(workspace, 'frontend/vite.c5-preview.config.ts')
const frontURL = 'http://100.107.57.101:5445'
const go = '/usr/local/go/bin/go'
const configurationFile = path.join(project, 'backend/.env')
const configurationDigest = () => crypto.createHash('sha256').update(fs.readFileSync(configurationFile)).digest('hex')
const buildEnvironment = {
  ...process.env,
  PATH: `/usr/local/go/bin:${path.dirname(process.execPath)}:${process.env.PATH ?? ''}`,
  GOCACHE: path.join(project, '.cache/go-build'),
  GOMODCACHE: path.join(project, 'backend/.cache/go-mod'),
  GOPATH: path.join(project, 'backend/.cache/gopath'),
  GOTMPDIR: path.join(project, '.cache/go-tmp'),
  TMPDIR: path.join(project, '.cache/c5/tmp'),
  GOTOOLCHAIN: 'local',
  GOMAXPROCS: '2',
}

function listenerPids(port) {
  const output = execFileSync('ss', ['-H', '-ltnp', `( sport = :${port} )`], { encoding: 'utf8' })
  return [...new Set([...output.matchAll(/pid=(\d+)/g)].map(match => Number(match[1])))]
}

function identity(pid) {
  const stat = fs.readFileSync(`/proc/${pid}/stat`, 'utf8')
  const fields = stat.slice(stat.lastIndexOf(')') + 2).split(' ')
  return {
    pid,
    startTime: fields[19],
    processGroup: Number(fields[2]),
    cwd: fs.readlinkSync(`/proc/${pid}/cwd`),
    exe: fs.readlinkSync(`/proc/${pid}/exe`),
  }
}

function matches(record) {
  try {
    if (!['backend', 'frontend'].includes(record.label)) return false
    const expectedCwd = record.label === 'backend' ? path.join(project, 'backend') : path.join(workspace, 'frontend')
    const expectedExe = record.label === 'backend' ? executable : fs.realpathSync(process.execPath)
    if (record.cwd !== expectedCwd || record.exe !== expectedExe) return false
    const current = identity(record.pid)
    return current.startTime === record.startTime && current.cwd === record.cwd && current.exe === record.exe && current.processGroup === record.pid
  } catch { return false }
}

function readState() {
  return fs.existsSync(stateFile) ? JSON.parse(fs.readFileSync(stateFile, 'utf8')) : null
}

function saveState(state) {
  fs.writeFileSync(stateFile, `${JSON.stringify(state, null, 2)}\n`, { mode: 0o600 })
}

function statusCode(url) {
  return new Promise(resolve => {
    const request = http.get(url, { timeout: 2000 }, response => {
      response.resume()
      resolve(response.statusCode)
    })
    request.on('timeout', () => request.destroy())
    request.on('error', () => resolve(0))
  })
}

const wait = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds))

async function waitHealthy(url, record) {
  for (let attempt = 0; attempt < 40; attempt++) {
    if (!matches(record)) throw new Error('临时进程已退出；请检查对应日志，勿改动 C1。')
    if (await statusCode(url) === 200) return
    await wait(250)
  }
  throw new Error('临时服务未在限定时间内就绪。')
}

async function launch(label, command, args, cwd, env) {
  const logFile = path.join(runtime, `${label}.log`)
  const fd = fs.openSync(logFile, 'a', 0o600)
  let child
  try { child = spawn(command, args, { cwd, env, detached: true, stdio: ['ignore', fd, fd] }) }
  finally { fs.closeSync(fd) }
  await new Promise((resolve, reject) => { child.once('spawn', resolve); child.once('error', reject) })
  child.unref()
  for (let attempt = 0; attempt < 10; attempt++) {
    try {
      const record = identity(child.pid)
      if (record.exe === fs.realpathSync(command)) return { ...record, label }
    } catch { /* Wait for exec, not for an unrelated process. */ }
    await wait(20)
  }
  throw new Error(`无法确认 ${label} 的独立进程身份。`)
}

async function stop(state) {
  if (!state) { console.log('C5 临时服务未启动。'); return }
  if (state.workspace !== workspace) throw new Error('进程记录不属于此工作区，拒绝停止。')
  const owned = state.processes ?? []
  for (const record of owned) {
    if (!fs.existsSync(`/proc/${record.pid}`)) continue
    if (!matches(record)) throw new Error('进程身份已变化，拒绝按旧 PID 停止。')
    process.kill(-record.pid, 'SIGTERM')
  }
  for (let attempt = 0; attempt < 48; attempt++) {
    if (owned.every(record => !matches(record))) break
    await wait(250)
  }
  if (owned.some(matches) || listenerPids(5445).length || listenerPids(5556).length) {
    throw new Error('临时进程或端口尚未退出；保留记录，不强杀、不误停 C1。')
  }
  fs.unlinkSync(stateFile)
  console.log('C5 临时进程已停止，5445/5556 已释放；数据库、Redis 和 C1 未停止。')
}

function removeRuntimeDirectory(directory) {
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    const target = path.join(directory, entry.name)
    if (entry.isDirectory()) removeRuntimeDirectory(target)
    else fs.unlinkSync(target)
  }
  fs.rmdirSync(directory)
}

async function clean() {
  await stop(readState())
  if (listenerPids(5445).length || listenerPids(5556).length) throw new Error('临时端口仍被占用，拒绝清理运行文件。')
  if (fs.existsSync(runtime)) {
    if (fs.realpathSync(runtime) !== runtime) throw new Error('临时目录归属不明，拒绝清理。')
    removeRuntimeDirectory(runtime)
  }
  console.log('仅 C5 临时二进制、缓存和日志已清理；二进制及缓存可重新生成，日志删除不可恢复，验证摘要保留在项目文档。')
}

async function start() {
  if (readState()) throw new Error('已有 C5 进程记录；先运行 status，确认后再 stop，不重复启动。')
  if (listenerPids(5445).length || listenerPids(5556).length) throw new Error('临时端口被占用，拒绝换端口或停止占用者。')
  const c1 = { frontend: listenerPids(5444), backend: listenerPids(5555) }
  if (c1.frontend.length !== 1 || c1.backend.length !== 1) throw new Error('C1 固定服务状态不明确，停止启动。')
  if (identity(c1.backend[0]).cwd !== path.join(project, 'backend')) throw new Error('现有后端并非主工作区，停止启动。')
  for (const name of ['DATABASE_URL', 'REDIS_URL', 'ADMIN_EMAIL', 'ADMIN_PASSWORD', 'SMTP_ENCRYPTION_KEY', 'PUBLIC_DIR', 'TICKET_UPLOAD_DIR']) {
    if (process.env[name]) throw new Error('当前终端存在连接配置覆写；拒绝覆盖原 backend/.env。')
  }
  fs.mkdirSync(runtime, { recursive: true, mode: 0o700 })
  const configurationSHA256 = configurationDigest()
  execFileSync(go, ['build', '-p', '1', '-o', executable, './cmd/api'], { cwd: path.join(workspace, 'backend'), env: buildEnvironment, stdio: 'inherit' })
  if (configurationDigest() !== configurationSHA256) throw new Error('构建期间原启动配置变化，停止启动。')
  const state = { workspace, c1, configurationSHA256, processes: [] }
  saveState(state)
  try {
    const backend = await launch('backend', executable, [], path.join(project, 'backend'), {
      ...buildEnvironment, PORT: '5556', TRANSITHUB_API_ONLY: '1',
    })
    state.processes.push(backend)
    saveState(state)
    await waitHealthy('http://127.0.0.1:5556/api/health', backend)
    const frontend = await launch('frontend', process.execPath, [viteEntry, '--config', viteConfig, '--configLoader', 'bundle'], path.join(workspace, 'frontend'), {
      ...buildEnvironment, npm_config_cache: path.join(project, '.playwright-cli/npm-cache'),
    })
    state.processes.push(frontend)
    saveState(state)
    await waitHealthy(frontURL, frontend)
    if (await statusCode(`${frontURL}/api/health`) !== 200) throw new Error('C5 前端到临时后端的代理检查失败。')
    if (JSON.stringify(c1.frontend) !== JSON.stringify(listenerPids(5444)) || JSON.stringify(c1.backend) !== JSON.stringify(listenerPids(5555))) {
      throw new Error('启动期间 C1 进程发生变化，停止 C5 临时服务并报告。')
    }
    console.log(`C5 临时预览：${frontURL}；后端 5556；原数据库/Redis；C1 5444/5555 保持运行。`)
  } catch (error) {
    await stop(state)
    throw error
  }
}

try {
  const action = process.argv[2]
  if (action === 'start') await start()
  else if (action === 'stop') await stop(readState())
  else if (action === 'clean') await clean()
  else if (action === 'status') {
    const state = readState()
    console.log(JSON.stringify({ running: Boolean(state?.processes?.length && state.processes.every(matches)), url: frontURL,
      c5Ports: { frontend: listenerPids(5445), backend: listenerPids(5556) }, c1Ports: { frontend: listenerPids(5444), backend: listenerPids(5555) } }))
  } else throw new Error('用法：node scripts/c5-preview.mjs start|status|stop|clean')
} catch (error) {
  console.error(error instanceof Error ? error.message : '临时服务操作失败。')
  process.exitCode = 1
}
