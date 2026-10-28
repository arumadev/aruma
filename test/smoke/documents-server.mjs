// Starts Aruma with the Document Database for the Node smoke runners (admin-node/documents-run.mjs,
// web/documents-run.mjs): test/smoke/genconfig -serve -documents on a throwaway database that
// genconfig drops when it stops, and runs a test file in a Node process that refuses every
// connection beyond loopback (test/e2e/netguard.cjs).
//
//   ARUMA_SMOKE_ADDR       Aruma's listen address (default 127.0.0.1:0: any free port)
//   ARUMA_SMOKE_DB_PREFIX  prefix of the throwaway database (default aruma_smoke_)
//   ARUMA_TEST_DATABASE_URL  the Postgres admin URL (default: the local server)
import { execFileSync, spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const root = join(fileURLToPath(new URL('.', import.meta.url)), '..', '..');

// buildGenconfig builds genconfig once for a run and returns its path and a cleanup.
export function buildGenconfig() {
  const dir = mkdtempSync(join(tmpdir(), 'aruma-smoke-documents-bin-'));
  const bin = join(dir, 'genconfig');
  execFileSync('go', ['build', '-o', bin, './test/smoke/genconfig'], { cwd: root, stdio: 'inherit' });
  return { bin, remove: () => rmSync(dir, { recursive: true, force: true }) };
}

// startServer runs genconfig -serve with the Document Database flags given (-documents,
// -documents-dev-owner, -documents-open-rules) and resolves once the server is up, with the
// directory of its config files and its host:port.
export async function startServer(bin, flags) {
  const dir = mkdtempSync(join(tmpdir(), 'aruma-smoke-documents-'));
  const args = ['-out', dir, '-serve', ...flags,
    '-addr', process.env.ARUMA_SMOKE_ADDR || '127.0.0.1:0',
    '-db-prefix', process.env.ARUMA_SMOKE_DB_PREFIX || 'aruma_smoke_'];
  const child = spawn(bin, args, { cwd: root, stdio: ['ignore', 'pipe', 'inherit'] });
  const exited = new Promise((resolve) => child.on('exit', resolve));
  const exports = await new Promise((resolve, reject) => {
    let out = '';
    child.stdout.on('data', (chunk) => {
      out += chunk;
      if (out.includes('ARUMA_CLI_TOKEN=') && out.endsWith('\n')) {
        resolve(Object.fromEntries([...out.matchAll(/^export (\w+)=(.*)$/gm)].map((m) => [m[1], m[2]])));
      }
    });
    exited.then((code) => reject(new Error(`genconfig exited with ${code}`)));
  });
  const stop = async () => {
    if (child.exitCode === null) child.kill('SIGTERM'); // genconfig drops its database on the way out
    await exited;
    rmSync(dir, { recursive: true, force: true });
  };
  return { dir, addr: exports.ARUMA_URL.replace(/^http:\/\//, ''), stop };
}

// runGuarded runs node --test on file under the network guard, with env added, and resolves
// to its exit code.
export async function runGuarded(file, cwd, env) {
  const preload = ['--require', join(root, 'test', 'e2e', 'netguard.cjs')];
  const childEnv = { ...process.env, ...env };
  for (const name of ['FIRESTORE_EMULATOR_HOST', 'FIREBASE_FIRESTORE_EMULATOR_ADDRESS', 'GOOGLE_APPLICATION_CREDENTIALS',
    'GOOGLE_CLOUD_PROJECT', 'FIRESTORE_PREFER_REST', 'ARUMA_URL', 'NODE_EXTRA_CA_CERTS', 'GRPC_DEFAULT_SSL_ROOTS_FILE_PATH']) {
    delete childEnv[name];
  }
  const test = spawn(process.execPath, [...preload, '--test', file], { cwd, stdio: 'inherit', env: childEnv });
  const forward = (sig) => test.kill(sig);
  process.on('SIGINT', forward).on('SIGTERM', forward);
  const code = await new Promise((resolve) => test.on('exit', (c, sig) => resolve(c ?? (sig ? 1 : 0))));
  process.off('SIGINT', forward).off('SIGTERM', forward);
  return code;
}

// expectedFailures reads an expected-failure list of test/compat/documents/expected-failures
// (ADR-028 §7.3) into a Map of test name -> the text its failure must contain ('' for any).
// One entry per line: the test's full name, then optionally " :: " and that text; # comments.
export function expectedFailures(text) {
  const out = new Map();
  for (const [i, raw] of text.split('\n').entries()) {
    const line = raw.replace(/#.*/, '').trim();
    if (!line) continue;
    const [name, reason = ''] = line.split(' :: ').map((s) => s.trim());
    if (out.has(name)) throw new Error(`line ${i + 1}: ${name} is listed twice`);
    out.set(name, reason);
  }
  return out;
}

// checked wraps node:test's test so that a test named in the list must fail (with its reason in
// the failure), and fails when it passes. It returns the wrapper and a function that reports the
// listed names (of the given prefix) no test carried.
export function checked(test, list, listName) {
  const seen = new Set();
  const wrapped = (name, fn) => {
    seen.add(name);
    if (!list.has(name)) return test(name, fn);
    const reason = list.get(name);
    return test(name, async (t) => {
      let failure;
      try {
        await fn(t);
      } catch (e) {
        failure = e;
      }
      if (!failure) throw new Error(`${name} is listed in ${listName} but passed: remove it from the list`);
      const text = `${failure?.code ?? ''} ${failure?.message ?? failure}`;
      if (reason && !text.includes(reason)) {
        throw new Error(`listed in ${listName} to fail with "${reason}", but failed otherwise: ${text}`);
      }
      t.diagnostic(`expected failure (${listName}): ${text.slice(0, 300)}`);
    });
  };
  const stale = (prefix) => [...list.keys()].filter((n) => n.startsWith(prefix) && !seen.has(n));
  return { test: wrapped, stale };
}
