// A WebSocket that stays silent through the proxy, both ways, for longer than the 60 s the
// operator guide asks every hop to allow (docs/guides/rtdb-operator.md §5). The protocol is
// spoken by hand, so nothing is sent while the connection is idle: the SDKs would send a
// keep-alive after 45 s and hide a proxy that closes idle connections after 60.
//
//   node idle-ws.mjs <databaseURL> <idle seconds>
//
// With RTDB_ADMIN_AUTH (the Authorization header of an admin) in the environment. It opens
// /.ws as the SDKs do, checks the hello, listens on /caddy/idle, stays silent, then writes
// that node over REST through the same proxy and must see the push on the idle socket, and
// a request of its own must still be answered. The database's rules must let anyone read and
// write /caddy. It prints one JSON report and exits 1 when a check fails.
const [databaseURL, idleArg] = process.argv.slice(2);
const idleMs = Number(idleArg) * 1000;
const auth = process.env.RTDB_ADMIN_AUTH;
if (!databaseURL || !(idleMs > 0) || !auth) {
  console.error('usage: RTDB_ADMIN_AUTH=... node idle-ws.mjs <databaseURL> <idle seconds>');
  process.exit(2);
}
const u = new URL(databaseURL);
const ns = u.hostname.split('.')[0];
const wsURL = `wss://${u.host}/.ws?v=5&ns=${ns}`;
const t0 = Date.now();
const report = { wsURL, idleSeconds: idleMs / 1000, checks: {} };
const check = (name, ok, detail) => {
  report.checks[name] = ok ? 'ok' : `FAILED${detail === undefined ? '' : ': ' + JSON.stringify(detail)}`;
};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const frames = [];
let waiters = [];
let closed = null;
const ws = new WebSocket(wsURL);
ws.onmessage = (e) => {
  const f = JSON.parse(e.data);
  frames.push({ t: Date.now() - t0, f });
  waiters = waiters.filter((w) => !w(f));
};
ws.onclose = (e) => {
  closed = { t: Date.now() - t0, code: e.code, reason: e.reason };
};
const next = (match, ms = 10000) =>
  new Promise((resolve) => {
    const old = frames.find((x) => match(x.f));
    if (old) return resolve(old.f);
    const timer = setTimeout(() => resolve(null), ms);
    waiters.push((f) => {
      if (!match(f)) return false;
      clearTimeout(timer);
      resolve(f);
      return true;
    });
  });
const send = (r, a, b) => ws.send(JSON.stringify({ t: 'd', d: { r, a, b } }));
const reply = (r) => next((f) => f.t === 'd' && f.d.r === r);
const push = (value) => next((f) => f.t === 'd' && f.d.a === 'd' && f.d.b.p.replace(/^\//, '') === 'caddy/idle' && JSON.stringify(f.d.b.d) === JSON.stringify(value), 15000);

async function rest(value) {
  const res = await fetch(`${u.origin}/caddy/idle.json`, {
    method: 'PUT',
    headers: { Authorization: auth, 'Content-Type': 'application/json' },
    body: JSON.stringify(value),
    signal: AbortSignal.timeout(10000),
  });
  return `${res.status} ${(await res.text()).trim()}`;
}

try {
  const hello = await next((f) => f.t === 'c' && f.d.t === 'h');
  report.hello = hello?.d.d;
  // The SDK adopts h for every later connection: it must be the host and port the client used.
  check('hello names the host the client used, port included', hello?.d.d.h === u.host, hello?.d.d);
  check('the first label is the namespace', ns.endsWith('-default-rtdb'), ns);

  report.before = await rest({ n: 1 });
  send(1, 'q', { p: '/caddy/idle', h: '' });
  const listen = await reply(1);
  const first = await push({ n: 1 });
  check('listen through the proxy', listen?.d.b.s === 'ok' && first !== null, { listen, first });

  const idleStart = Date.now() - t0;
  await sleep(idleMs);
  report.idle = { from: idleStart, to: Date.now() - t0, framesDuring: frames.filter((x) => x.t > idleStart).length };
  check(`the socket survives ${idleMs / 1000} s of silence both ways`, closed === null && report.idle.framesDuring === 0, { closed, idle: report.idle });

  report.after = await rest({ n: 2 });
  check('a write over REST reaches the idle socket', (await push({ n: 2 })) !== null, report.after);
  send(2, 'p', { p: '/caddy/idle', d: { n: 3 } });
  const put = await reply(2);
  check('a request on the idle socket is answered', put?.d.b.s === 'ok', put);
} catch (e) {
  report.error = String((e && e.stack) || e);
}
report.closed = closed;
ws.close();
console.log(JSON.stringify(report, null, 2));
process.exit(report.error || Object.values(report.checks).some((c) => c !== 'ok') ? 1 : 0);
