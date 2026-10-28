// Runs rtdb.test.mjs against a live Aruma in production form (npm run test:rtdb).
//
// It builds test/smoke/genconfig and starts it with -serve -rtdb-domain rtdb.aruma.test and a
// TLS front, on a throwaway database that genconfig drops when it stops. Then it runs the test
// in a Node process that trusts the front's CA (NODE_EXTRA_CA_CERTS), refuses every connection
// beyond loopback (test/e2e/netguard.cjs, with its log in the run's directory) and
// resolves *.aruma.test to the front (test/compat/firebase-js/rtdb/hosts.cjs). With
// ARUMA_RTDB_DATABASE_URL (below) it starts nothing, and the names resolve by the real DNS.
//
// Environment:
//   ARUMA_SMOKE_ADDR       Aruma's listen address (default 127.0.0.1:0: any free port)
//   ARUMA_SMOKE_TLS_ADDR   the TLS front's address (default 127.0.0.1:0)
//   ARUMA_SMOKE_DB_PREFIX  prefix of the throwaway database (default aruma_smoke_)
//   ARUMA_SMOKE_DIR with ARUMA_RTDB_TLS_ADDR
//                          use a genconfig -serve -rtdb-domain rtdb.aruma.test -rtdb-tls ...
//                          already running, whose files are in ARUMA_SMOKE_DIR
//   ARUMA_SMOKE_DIR with ARUMA_RTDB_DATABASE_URL
//                          use a server behind a front of its own, such as Caddy:
//                          ARUMA_SMOKE_DIR holds the
//                          project's service-account.json and an expected.json with projectId
//                          and that databaseURL, ARUMA_RTDB_CA the CA to trust (optional), and
//                          ARUMA_NETGUARD_LOOPBACK_NAMES the names the real DNS resolves to
//                          this machine (the database host and the key's token_uri host)
//   ARUMA_TEST_DATABASE_URL  the Postgres admin URL (default: the local server)
import { execFileSync, spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = fileURLToPath(new URL('.', import.meta.url));
const root = join(here, '..', '..', '..');

async function startServer() {
  if (process.env.ARUMA_SMOKE_DIR && process.env.ARUMA_RTDB_DATABASE_URL) {
    return { dir: process.env.ARUMA_SMOKE_DIR, external: true, stop: async () => {} };
  }
  if (process.env.ARUMA_SMOKE_DIR && process.env.ARUMA_RTDB_TLS_ADDR) {
    return { dir: process.env.ARUMA_SMOKE_DIR, tls: process.env.ARUMA_RTDB_TLS_ADDR, stop: async () => {} };
  }
  const dir = mkdtempSync(join(tmpdir(), 'aruma-smoke-rtdb-'));
  const bin = join(dir, 'genconfig');
  execFileSync('go', ['build', '-o', bin, './test/smoke/genconfig'], { cwd: root, stdio: 'inherit' });
  const child = spawn(bin, [
    '-out', dir, '-serve',
    '-addr', process.env.ARUMA_SMOKE_ADDR || '127.0.0.1:0',
    '-db-prefix', process.env.ARUMA_SMOKE_DB_PREFIX || 'aruma_smoke_',
    '-rtdb-domain', 'rtdb.aruma.test',
    '-rtdb-tls', process.env.ARUMA_SMOKE_TLS_ADDR || '127.0.0.1:0',
  ], { cwd: root, stdio: ['ignore', 'pipe', 'inherit'] });
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
  return { dir, tls: exports.ARUMA_RTDB_TLS_ADDR, stop };
}

const server = await startServer();
let code = 1;
try {
  const preload = ['--require', join(root, 'test', 'e2e', 'netguard.cjs')];
  const env = { ...process.env, ARUMA_SMOKE_DIR: server.dir, ARUMA_NETGUARD_LOG: join(server.dir, 'netguard.log') };
  if (server.external) {
    // The real DNS and the front's own certificate: nothing is redirected.
    delete env.NODE_EXTRA_CA_CERTS;
    if (process.env.ARUMA_RTDB_CA) env.NODE_EXTRA_CA_CERTS = process.env.ARUMA_RTDB_CA;
  } else {
    preload.push('--require', join(root, 'test', 'compat', 'firebase-js', 'rtdb', 'hosts.cjs'));
    env.ARUMA_TEST_TLS_PORT = server.tls.slice(server.tls.lastIndexOf(':') + 1);
    env.NODE_EXTRA_CA_CERTS = join(server.dir, 'rtdb-ca.pem');
  }
  const test = spawn(process.execPath, [...preload, join(here, 'rtdb.test.mjs')], { cwd: here, stdio: 'inherit', env });
  for (const sig of ['SIGINT', 'SIGTERM']) process.on(sig, () => test.kill(sig));
  code = await new Promise((resolve) => test.on('exit', (c, sig) => resolve(c ?? (sig ? 1 : 0))));
} finally {
  await server.stop();
}
process.exit(code);
