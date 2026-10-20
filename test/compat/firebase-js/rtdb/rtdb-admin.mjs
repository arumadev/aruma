// The two calls run.sh makes on the target before the suites, through the same names, TLS and
// network guard as the tests:
//
//   node rtdb-admin.mjs rules <rules-url> <rules-file>   PUT the rules with the Authorization
//                                                        header in RTDB_ADMIN_AUTH, then GET
//                                                        them back and compare
//   node rtdb-admin.mjs handshake <databaseURL>          open /.ws as the SDK does and print
//                                                        the server's hello ("h" control frame)
//
// Exits 1 when the target does not answer as expected.
import { readFileSync } from 'node:fs';

const [command, ...args] = process.argv.slice(2);

async function rules(url, file) {
  const auth = process.env.RTDB_ADMIN_AUTH;
  if (!auth) throw new Error('RTDB_ADMIN_AUTH is not set');
  const body = readFileSync(file, 'utf8');
  const headers = { Authorization: auth, 'Content-Type': 'application/json; charset=utf-8' };
  const put = await fetch(url, { method: 'PUT', headers, body, signal: AbortSignal.timeout(10000) });
  const answer = (await put.text()).trim();
  if (!put.ok) throw new Error(`PUT ${url}: ${put.status} ${answer}`);
  const get = await fetch(url, { headers: { Authorization: auth }, signal: AbortSignal.timeout(10000) });
  const loaded = await get.text();
  if (!get.ok) throw new Error(`GET ${url}: ${get.status} ${loaded.trim()}`);
  if (JSON.stringify(JSON.parse(loaded)) !== JSON.stringify(JSON.parse(body))) {
    throw new Error(`GET ${url} returned other rules: ${loaded.trim()}`);
  }
  console.log(`rules loaded: ${put.status} ${answer}`);
}

// wsURL builds the WebSocket URL the JS SDK builds (RepoInfo.ts repoInfoConnectionURL): the
// databaseURL's host, /.ws, the protocol version and, as for every host outside firebaseio.com,
// the namespace: the ns parameter, or the host's first label.
function wsURL(databaseURL) {
  const u = new URL(databaseURL);
  const ns = u.searchParams.get('ns') || u.hostname.split('.')[0];
  return `${u.protocol === 'https:' ? 'wss' : 'ws'}://${u.host}/.ws?v=5&ns=${encodeURIComponent(ns)}`;
}

function handshake(databaseURL) {
  const url = wsURL(databaseURL);
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(url);
    const timer = setTimeout(() => {
      ws.close();
      reject(new Error(`${url}: no hello within 10 s`));
    }, 10000);
    ws.onerror = event => {
      clearTimeout(timer);
      reject(new Error(`${url}: ${event.message || event.error?.message || 'connection failed'}`));
    };
    ws.onmessage = event => {
      clearTimeout(timer);
      ws.close();
      const frame = JSON.parse(event.data);
      const hello = frame?.t === 'c' && frame.d?.t === 'h' ? frame.d.d : null;
      if (!hello) return reject(new Error(`${url}: first frame is not a hello: ${event.data}`));
      // The SDK sets serverTimeOffset from ts (Repo.ts); report how far it is from this clock.
      console.log(JSON.stringify({ url, hello, offsetMs: hello.ts - Date.now() }, null, 2));
      resolve();
    };
  });
}

try {
  if (command === 'rules' && args.length === 2) await rules(...args);
  else if (command === 'handshake' && args.length === 1) await handshake(...args);
  else {
    console.error('usage: rtdb-admin.mjs rules <rules-url> <rules-file> | handshake <databaseURL>');
    process.exit(2);
  }
} catch (err) {
  console.error(err.message);
  process.exit(1);
}
