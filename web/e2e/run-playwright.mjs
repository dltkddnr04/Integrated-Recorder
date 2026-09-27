import { spawn } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'

const dataDir = mkdtempSync(join(tmpdir(), 'integrated-recorder-ui-e2e-'))
const child = spawn('npx', ['playwright', 'test', ...process.argv.slice(2)], {
  env: { ...process.env, IR_E2E_DATA_DIR: dataDir },
  stdio: 'inherit',
})

let stopping = false
const stop = signal => {
  if (stopping) return
  stopping = true
  child.kill(signal)
}
process.on('SIGINT', () => stop('SIGINT'))
process.on('SIGTERM', () => stop('SIGTERM'))

const exitCode = await new Promise((resolve, reject) => {
  child.once('error', reject)
  child.once('exit', (code, signal) => resolve(code ?? (signal ? 1 : 0)))
})
try { rmSync(dataDir, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 }) } catch { /* E2E data cleanup is best effort */ }
process.exitCode = exitCode
