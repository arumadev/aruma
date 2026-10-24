// Runs storage.test.mjs against a live Aruma with Cloud Storage (npm run test:storage), twice:
//
//   loopback  the server on http://127.0.0.1:<port>, the development form;
//   tls       the server behind a TLS front at https://storage.aruma.test, the production form:
//             public_url, the key file's token_uri, the session URLs and the download URLs are
//             all on that name, a subdomain, as behind Caddy (genconfig -public-host).
//
// For each it builds test/smoke/genconfig, starts it with -serve -storage on a throwaway
// database that genconfig drops when it stops, and runs the test in a Node process that refuses
// every connection beyond loopback (test/experiments/netguard.cjs, its log in the run's
// directory). In the tls form, *.aruma.test also resolves to the front inside that process only
// (test/compat/firebase-js/rtdb/hosts.cjs) and the run's CA is trusted (NODE_EXTRA_CA_CERTS).
//
// Environment:
//   ARUMA_SMOKE_STORAGE_MODES  the forms to run, comma-separated (default loopback,tls)
//   ARUMA_SMOKE_ADDR           Aruma's listen address (default 127.0.0.1:0: any free port)
//   ARUMA_SMOKE_TLS_ADDR       the TLS front's address (default 127.0.0.1:0)
//   ARUMA_SMOKE_DB_PREFIX      prefix of the throwaway database (default aruma_smoke_)
//   ARUMA_TEST_DATABASE_URL    the Postgres admin URL (default: the local server)
import { execFileSync, spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = fileURLToPath(new URL('.', import.meta.url));
const root = join(here, '..', '..', '..');
const host = 'storage.aruma.test';

async function startServer(bin, mode) {
  const dir = mkdtempSync(join(tmpdir(), `aruma-smoke-storage-${mode}-`));
  const args = ['-out', dir, '-serve', '-storage',
    '-addr', process.env.ARUMA_SMOKE_ADDR || '127.0.0.1:0',
    '-db-prefix', process.env.ARUMA_SMOKE_DB_PREFIX || 'aruma_smoke_'];
  if (mode === 'tls') args.push('-public-host', host, '-public-tls', process.env.ARUMA_SMOKE_TLS_ADDR || '127.0.0.1:0');
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
  return { dir, tls: exports.ARUMA_PUBLIC_TLS_ADDR, stop };
}

async function run(bin, mode) {
  const server = await startServer(bin, mode);
  try {
    const preload = ['--require', join(root, 'test', 'experiments', 'netguard.cjs')];
    const env = { ...process.env, ARUMA_SMOKE_DIR: server.dir, ARUMA_SMOKE_MODE: mode, ARUMA_NETGUARD_LOG: join(server.dir, 'netguard.log') };
    for (const name of ['STORAGE_EMULATOR_HOST', 'FIREBASE_STORAGE_EMULATOR_HOST', 'GOOGLE_APPLICATION_CREDENTIALS', 'ARUMA_URL']) {
      delete env[name];
    }
    delete env.NODE_EXTRA_CA_CERTS;
    if (mode === 'tls') {
      preload.push('--require', join(root, 'test', 'compat', 'firebase-js', 'rtdb', 'hosts.cjs'));
      env.ARUMA_TEST_TLS_PORT = server.tls.slice(server.tls.lastIndexOf(':') + 1);
      env.NODE_EXTRA_CA_CERTS = join(server.dir, 'public-ca.pem');
    }
    const test = spawn(process.execPath, [...preload, '--test', join(here, 'storage.test.mjs')], { cwd: here, stdio: 'inherit', env });
    const forward = (sig) => test.kill(sig);
    process.on('SIGINT', forward).on('SIGTERM', forward);
    const code = await new Promise((resolve) => test.on('exit', (c, sig) => resolve(c ?? (sig ? 1 : 0))));
    process.off('SIGINT', forward).off('SIGTERM', forward);
    return code;
  } finally {
    await server.stop();
  }
}

const bin = join(mkdtempSync(join(tmpdir(), 'aruma-smoke-storage-bin-')), 'genconfig');
execFileSync('go', ['build', '-o', bin, './test/smoke/genconfig'], { cwd: root, stdio: 'inherit' });
let code = 0;
try {
  for (const mode of (process.env.ARUMA_SMOKE_STORAGE_MODES || 'loopback,tls').split(',')) {
    console.log(`\n--- @aruma/admin-storage against Aruma, ${mode} form`);
    const c = await run(bin, mode.trim());
    code ||= c;
  }
} finally {
  rmSync(join(bin, '..'), { recursive: true, force: true });
}
process.exit(code);
