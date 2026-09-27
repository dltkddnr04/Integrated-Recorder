import { spawn, spawnSync } from 'node:child_process'
import { cpSync, existsSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, resolve, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import process from 'node:process'

const webDir = dirname(fileURLToPath(import.meta.url))
const repositoryRoot = resolve(webDir, '../..')
const dataDir = process.env.IR_E2E_DATA_DIR
if (!dataDir) throw new Error('IR_E2E_DATA_DIR must be set by Playwright config')

const buildDir = mkdtempSync(join(tmpdir(), 'integrated-recorder-e2e-build-'))
const serverBinary = join(buildDir, 'integrated-recorder-e2e-server')
const moduleCache = join(buildDir, 'go-mod-cache')
const moduleCacheSource = spawnSync('go', ['env', 'GOMODCACHE'], { encoding: 'utf8' }).stdout.trim()
if (moduleCacheSource && existsSync(moduleCacheSource)) cpSync(moduleCacheSource, moduleCache, { recursive: true })
const goEnvironment = { ...process.env, GOMODCACHE: moduleCache, GOCACHE: join(buildDir, 'go-build-cache') }
const build = spawnSync('go', ['build', '-o', serverBinary, './web/e2e/backend'], {
  cwd: repositoryRoot,
  env: goEnvironment,
  stdio: 'inherit',
})
if (build.status !== 0) {
  try { rmSync(buildDir, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 }) } catch { /* preserve the original build failure */ }
  process.exit(build.status ?? 1)
}

const server = spawn(serverBinary, [], {
  cwd: repositoryRoot,
  env: { ...goEnvironment, DATA_DIR: dataDir, ADDR: '127.0.0.1:4173' },
  stdio: 'inherit',
})

let stopping = false
const forwardSignal = signal => {
  if (stopping) return
  stopping = true
  server.kill(signal)
}
process.on('SIGINT', () => forwardSignal('SIGINT'))
process.on('SIGTERM', () => forwardSignal('SIGTERM'))

const exitCode = await new Promise((resolveExit, reject) => {
  server.once('error', reject)
  server.once('exit', (code, signal) => resolveExit(code ?? (signal ? 1 : 0)))
})
try { rmSync(buildDir, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 }) } catch { /* temporary cache cleanup is best effort */ }
process.exitCode = exitCode
