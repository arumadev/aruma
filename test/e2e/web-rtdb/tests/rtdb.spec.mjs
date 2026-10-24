import {
  test, expect, call, value, events, signIn, connected, admin, adminIncrement, disableUser, server,
  arumaURL, config, ns, firebaseDatabaseHost,
} from './fixtures.mjs';

// Realtime Database end to end with the official firebase package in production mode: two
// browsers signed in with Aruma Auth and an admin writer over REST, against a real server under
// the rules of ../rtdb-rules.json, with zero requests to Google (the network fixture). The
// numbers are the scenarios of test/e2e/README.md.

const email = (major, name) => `${name}-${major}-${Date.now().toString(36)}@example.com`;

test('1. sign-in with Aruma Auth; the database SDK in production mode on the generated databaseURL', async ({ client, firebaseMajor, network }) => {
  const a = await client();
  const uid = await signIn(a, email(firebaseMajor, 'signin'));
  expect(uid).toHaveLength(28);
  const sdk = await value(a, (e) => ({
    root: e.dbSdk.ref(e.db).toString(),
    host: e.db._repoInternal.repoInfo_.host,
    namespace: e.db._repoInternal.repoInfo_.namespace,
    secure: e.db._repoInternal.repoInfo_.secure,
    emulator: e.db._repoInternal.repoInfo_.isUsingEmulator,
  }));
  const url = new URL(config.databaseURL);
  expect(sdk).toEqual({ root: `${url.origin}/`, host: url.host, namespace: ns, secure: false, emulator: false });
  // A write under the user's own path proves the connection carries Aruma's ID token.
  await value(a, (e, uid) => e.dbSdk.set(e.dbSdk.ref(e.db, `users/${uid}/profile`), { name: 'سارة' }), uid);
  expect(network.aruma.some((p) => p.startsWith('/identitytoolkit.googleapis.com/v1/accounts:signUp?'))).toBe(true);
  expect(network.sockets.length).toBeGreaterThan(0);
});

test('2. rules: the own data is readable and writable, another user\'s is denied', async ({ client, firebaseMajor }) => {
  const a = await client();
  const b = await client();
  const ua = await signIn(a, email(firebaseMajor, 'rules'));
  const ub = await signIn(b);

  await value(a, (e, uid) => e.dbSdk.set(e.dbSdk.ref(e.db, `users/${uid}`), { note: 'mine' }), ua);
  expect(await value(a, (e, uid) => e.dbSdk.get(e.dbSdk.ref(e.db, `users/${uid}`)).then((s) => s.val()), ua)).toEqual({ note: 'mine' });
  await value(b, (e, uid) => e.dbSdk.set(e.dbSdk.ref(e.db, `users/${uid}`), { note: 'theirs' }), ub);

  // A at B's data: write, read and listen are all permission_denied, and B's data is unchanged.
  // get() rejects with a plain Error carrying the server's text, without a code (Repo.ts,
  // repoGetValue); set() and listeners get the code.
  expect(await call(a, (e, uid) => e.dbSdk.set(e.dbSdk.ref(e.db, `users/${uid}/note`), 'overwritten'), ub)).toEqual({ code: 'PERMISSION_DENIED' });
  expect(await call(a, (e, uid) => e.dbSdk.get(e.dbSdk.ref(e.db, `users/${uid}`)).then((s) => s.val()), ub)).toEqual({ code: 'Error: Permission denied' });
  await value(a, (e, uid) => e.listen('other', `users/${uid}`), ub);
  await expect.poll(() => events(a, 'other')).toEqual([{ cancel: 'PERMISSION_DENIED' }]);
  expect((await admin('GET', `users/${ub}`)).body).toEqual({ note: 'theirs' });

  // A fresh ID token goes to the open connection; the listen on the own data stays and is told
  // the admin's write.
  await value(a, (e, uid) => e.listen('own', `users/${uid}`), ua);
  await value(a, (e) => e.auth.currentUser.getIdToken(true));
  expect((await admin('PATCH', `users/${ua}`, { fromAdmin: 1 })).status).toBe(200);
  await expect.poll(async () => (await events(a, 'own')).at(-1)?.val).toEqual({ note: 'mine', fromAdmin: 1 });

  // Signed out, not even the own data: the open listen is cancelled, a new read denied.
  await value(a, (e) => e.authSdk.signOut(e.auth));
  await expect.poll(async () => (await events(a, 'own')).at(-1)).toEqual({ cancel: 'PERMISSION_DENIED' });
  expect(await call(a, (e, uid) => e.dbSdk.get(e.dbSdk.ref(e.db, `users/${uid}`)).then((s) => s.val()), ua)).toEqual({ code: 'Error: Permission denied' });
});

test('3. two clients: writes reach the other\'s listeners in order, with the server\'s timestamp', async ({ client, firebaseMajor }) => {
  const a = await client();
  const b = await client();
  const ua = await signIn(a, email(firebaseMajor, 'chat'));
  const ub = await signIn(b);
  const room = `rooms/r${firebaseMajor}/messages`;
  for (const page of [a, b]) {
    await value(page, (e, path) => {
      e.listen('added', path, { on: 'child_added', query: [['orderByChild', 'ts']] });
      e.listen('all', path, { query: [['orderByChild', 'ts']] });
    }, room);
  }

  // The SDK estimates the server's clock from the handshake: the same machine's, give or take.
  const offset = await value(a, (e) => new Promise((resolve) => e.dbSdk.onValue(e.dbSdk.ref(e.db, '.info/serverTimeOffset'), (s) => resolve(s.val()), { onlyOnce: true })));
  expect(Math.abs(offset)).toBeLessThan(1000);

  const t0 = Date.now();
  const push = (page, from, text) => value(page, (e, a) => e.dbSdk.push(e.dbSdk.ref(e.db, a.room), { from: a.from, text: a.text, ts: e.dbSdk.serverTimestamp() }).then((r) => r.key), { room, from, text });
  const keys = [];
  for (const text of ['a1', 'a2', 'a3']) keys.push(await push(a, ua, text));
  for (const text of ['b1', 'b2']) keys.push(await push(b, ub, text));
  const posted = await admin('POST', room, { from: 'admin', text: 'admin', ts: { '.sv': 'timestamp' } });
  expect(posted.status).toBe(200);
  keys.push(posted.body.name);
  const t1 = Date.now();

  // A message dated in the past passes .validate and sorts first; one from the future is refused.
  expect(await call(b, (e, room) => e.dbSdk.push(e.dbSdk.ref(e.db, room), { from: 'b', text: 'late', ts: 1 }).then(() => 'ok'), room)).toEqual({ value: 'ok' });
  expect(await call(b, (e, room) => e.dbSdk.push(e.dbSdk.ref(e.db, room), { from: 'b', text: 'future', ts: Date.now() + 3_600_000 }), room)).toEqual({ code: 'PERMISSION_DENIED' });

  // The server's timestamps follow the order of the writes, within the test's clock.
  const stored = (await admin('GET', room)).body;
  const ts = keys.map((k) => stored[k].ts);
  expect(ts).toEqual([...ts].sort((x, y) => x - y));
  for (const t of ts) {
    expect(t).toBeGreaterThanOrEqual(t0 - 5);
    expect(t).toBeLessThanOrEqual(t1 + 5);
  }

  // B saw its refused message come and go (child_added, then child_removed when the server said
  // no); A never did.
  const written = (m) => keys.includes(m.key);
  for (const page of [a, b]) {
    await expect.poll(async () => (await events(page, 'added')).filter(written).length).toBe(6);
    const added = (await events(page, 'added')).filter(written);
    // The texts arrive as written, A's before B's before the admin's.
    expect(added.map((m) => m.val.text)).toEqual(['a1', 'a2', 'a3', 'b1', 'b2', 'admin']);
    expect(added.map((m) => m.key)).toEqual(keys);
    // The child_added of a client's own write carries the SDK's estimate of the server's clock
    // (Date.now() + .info/serverTimeOffset, off by the handshake's latency: B's was 8 ms behind
    // on CI, so its b1 sorted before A's a3), and the server's value replaces it as it comes
    // back. So the estimates are near the server's, and the query settles on the server's.
    for (const m of added) expect(Math.abs(m.val.ts - stored[m.key].ts), m.val.text).toBeLessThan(1000);
    await expect.poll(async () => {
      const last = (await events(page, 'all')).at(-1);
      return keys.map((k) => last.val[k]?.ts);
    }).toEqual(ts);
    // The query's value event lists the children in the order of ts, the stray ts: 1 first.
    const all = (await events(page, 'all')).at(-1);
    expect(all.keys.slice(1)).toEqual(keys);
    expect(all.val[all.keys[0]].text).toBe('late');
  }
});

test('4. queries: orderByChild with .indexOn, limitToLast, a new child entering the window', async ({ client, firebaseMajor }) => {
  const a = await client();
  const b = await client();
  await signIn(a, email(firebaseMajor, 'board'));
  await signIn(b);
  const board = `boards/b${firebaseMajor}`;
  const scores = [50, 10, 80, 30, 70, 20, 60, 40];
  expect((await admin('PUT', board, Object.fromEntries(scores.map((s, i) => [`p${i}`, { score: s }])))).status).toBe(200);

  await value(a, (e, board) => {
    const query = [['orderByChild', 'score'], ['limitToLast', 3]];
    e.listen('top', board, { query });
    e.listen('added', board, { on: 'child_added', query });
    e.listen('removed', board, { on: 'child_removed', query });
  }, board);
  await expect.poll(async () => (await events(a, 'top')).at(-1)?.keys).toEqual(['p6', 'p4', 'p2']);
  expect((await events(a, 'added')).map((x) => [x.key, x.prev])).toEqual([['p6', null], ['p4', 'p6'], ['p2', 'p4']]);

  // B adds a child above the window's lowest: it enters after p4, p6 leaves.
  await value(b, (e, board) => e.dbSdk.set(e.dbSdk.ref(e.db, `${board}/p8`), { score: 75 }), board);
  await expect.poll(async () => (await events(a, 'top')).at(-1)?.keys).toEqual(['p4', 'p8', 'p2']);
  expect((await events(a, 'added')).at(-1)).toEqual({ key: 'p8', val: { score: 75 }, prev: 'p4' });
  expect((await events(a, 'removed')).map((x) => x.key)).toEqual(['p6']);

  // Below the window: nothing changes for A.
  const before = (await events(a, 'top')).length;
  expect((await admin('PUT', `${board}/p9`, { score: 5 })).status).toBe(200);
  // An admin update moves p1 from the bottom to the top: it enters, p4 leaves.
  expect((await admin('PATCH', board, { 'p1/score': 99 })).status).toBe(200);
  await expect.poll(async () => (await events(a, 'top')).at(-1)?.keys).toEqual(['p8', 'p2', 'p1']);
  expect((await events(a, 'top')).length).toBe(before + 1);
  expect((await events(a, 'removed')).map((x) => x.key)).toEqual(['p6', 'p4']);

  // A one-off query the server answers from its index: the two lowest.
  const low = await value(a, (e, board) => e.dbSdk.get(e.dbSdk.query(e.dbSdk.ref(e.db, board), e.dbSdk.orderByChild('score'), e.dbSdk.limitToFirst(2))).then((s) => {
    const k = [];
    s.forEach((c) => {
      k.push(c.key);
    });
    return k;
  }), board);
  expect(low).toEqual(['p9', 'p5']);

  // A child without .indexOn: the server sends every child with a no_index warning, as Firebase
  // does, and the SDK orders and limits them itself.
  expect((await admin('PATCH', board, { 'p0/tier': 'b', 'p3/tier': 'a', 'p5/tier': 'c' })).status).toBe(200);
  await value(a, (e, board) => e.listen('tier', board, { query: [['orderByChild', 'tier'], ['limitToLast', 2]] }), board);
  await expect.poll(async () => (await events(a, 'tier')).at(-1)?.keys).toEqual(['p0', 'p5']);
  expect(a.warnings.some((m) => m.includes('Using an unspecified index'))).toBe(true);
});

test('5. transactions: concurrent increments from two clients and the admin add up exactly', async ({ client, firebaseMajor }) => {
  const a = await client();
  const b = await client();
  await signIn(a, email(firebaseMajor, 'counter'));
  await signIn(b);
  const counter = `counters/c${firebaseMajor}`;
  expect((await admin('PUT', counter, 0)).status).toBe(200);
  const n = 20;
  const increments = (page) => value(page, (e, a) => Promise.all(Array.from({ length: a.n }, () => e.dbSdk.runTransaction(e.dbSdk.ref(e.db, a.counter), (v) => (v ?? 0) + 1).then((r) => r.committed))), { counter, n });

  const [ca, cb, attempts] = await Promise.all([
    increments(a),
    increments(b),
    Promise.all(Array.from({ length: n }, () => adminIncrement(counter))),
  ]);
  expect(ca).toEqual(Array(n).fill(true));
  expect(cb).toEqual(Array(n).fill(true));
  expect((await admin('GET', counter)).body).toBe(3 * n);
  expect(await value(a, (e, c) => e.dbSdk.get(e.dbSdk.ref(e.db, c)).then((s) => s.val()), counter)).toBe(3 * n);
  // The writers really raced: some conditional writes had to be retried.
  console.log(`admin increments retried ${attempts.reduce((s, x) => s + x - 1, 0)} times`);
});

test('6. onDisconnect presence: the other client sees offline on goOffline and when the tab dies', async ({ client, firebaseMajor }) => {
  const a = await client();
  const b = await client();
  const ua = await signIn(a, email(firebaseMajor, 'presence'));
  await signIn(b);
  const goOnline = () => value(a, async (e, uid) => {
    const ref = e.dbSdk.ref(e.db, `status/${uid}`);
    await e.dbSdk.onDisconnect(ref).set({ state: 'offline', at: e.dbSdk.serverTimestamp() });
    await e.dbSdk.set(ref, { state: 'online', at: e.dbSdk.serverTimestamp() });
  }, ua);
  const state = async () => (await events(b, 'status')).at(-1)?.val?.state;

  await value(b, (e, uid) => e.listen('status', `status/${uid}`), ua);
  await goOnline();
  await expect.poll(state).toBe('online');
  // More of the API on the user's own data: an update (om) that runs, and a remove that is
  // cancelled (oc) and so does not.
  await value(a, async (e, uid) => {
    const user = e.dbSdk.ref(e.db, `users/${uid}`);
    await e.dbSdk.set(user, { typing: true });
    await e.dbSdk.onDisconnect(user).update({ lastSeen: e.dbSdk.serverTimestamp() });
    const typing = e.dbSdk.onDisconnect(e.dbSdk.child(user, 'typing'));
    await typing.remove();
    await typing.cancel();
  }, ua);

  // A disconnects cleanly.
  await value(a, (e) => e.dbSdk.goOffline(e.db));
  await expect.poll(state, { timeout: 10_000 }).toBe('offline');
  expect(typeof (await events(b, 'status')).at(-1).val.at).toBe('number');
  const user = (await admin('GET', `users/${ua}`)).body;
  expect(user.typing).toBe(true);
  expect(typeof user.lastSeen).toBe('number');

  // Back online, then the tab dies: its socket closes with it.
  await value(a, (e) => e.dbSdk.goOnline(e.db));
  await connected(a);
  await goOnline();
  await expect.poll(state).toBe('online');
  await a.context().close();
  await expect.poll(state, { timeout: 10_000 }).toBe('offline');
});

test('7. offline: with the server down a write stays local, and lands when it is back', async ({ client, firebaseMajor }) => {
  const a = await client();
  const ua = await signIn(a, email(firebaseMajor, 'offline'));
  await connected(a);
  await value(a, (e, uid) => e.listen('notes', `users/${uid}/notes`, { on: 'child_added' }), ua);

  await server('down');
  try {
    await connected(a, false);
    // The write is applied locally at once; its promise waits for the server.
    await value(a, (e, uid) => {
      window.offlineWrite = e.dbSdk.set(e.dbSdk.ref(e.db, `users/${uid}/notes/n1`), { text: 'written offline', at: e.dbSdk.serverTimestamp() }).then(() => 'landed', (err) => err.code);
    }, ua);
    await expect.poll(async () => (await events(a, 'notes')).map((x) => x.key)).toEqual(['n1']);
    await a.waitForTimeout(3000);
    expect(await value(a, () => Promise.race([window.offlineWrite, new Promise((r) => setTimeout(() => r('pending'), 100))]))).toBe('pending');
  } finally {
    await server('up');
  }

  // The SDK reconnects by itself (its backoff is at most 30 s) and sends the queued write.
  expect(await value(a, () => window.offlineWrite)).toBe('landed');
  const stored = (await admin('GET', `users/${ua}/notes/n1`)).body;
  expect(stored.text).toBe('written offline');
  expect(typeof stored.at).toBe('number');
});

test('7b. long polling: with WebSockets refused, auth, rules, listens and an offline write go over /.lp', async ({ client, firebaseMajor, network }) => {
  // A network that refuses WebSockets, as some proxies do: the SDK falls back to long polling.
  const a = await client({ setup: (context) => context.routeWebSocket(/\/\.ws/, (ws) => ws.close()) });
  const ua = await signIn(a, email(firebaseMajor, 'longpoll'));
  const before = network.aruma.length;
  await connected(a);
  await value(a, (e, uid) => e.listen('mine', `users/${uid}/feed`, { on: 'child_added' }), ua);

  await value(a, (e, uid) => e.dbSdk.set(e.dbSdk.ref(e.db, `users/${uid}/feed/own`), 1), ua);
  expect(await call(a, (e) => e.dbSdk.set(e.dbSdk.ref(e.db, 'users/someone-else/feed/x'), 1))).toEqual({ code: 'PERMISSION_DENIED' });
  expect((await admin('PUT', `users/${ua}/feed/fromAdmin`, 2)).status).toBe(200);
  await expect.poll(async () => (await events(a, 'mine')).map((x) => x.key)).toEqual(['own', 'fromAdmin']);

  await server('down');
  try {
    await connected(a, false);
    await value(a, (e, uid) => {
      window.offlineWrite = e.dbSdk.set(e.dbSdk.ref(e.db, `users/${uid}/feed/offline`), 3).then(() => 'landed', (err) => err.code);
    }, ua);
    await expect.poll(async () => (await events(a, 'mine')).map((x) => x.key)).toContain('offline');
  } finally {
    await server('up');
  }
  expect(await value(a, () => window.offlineWrite)).toBe('landed');
  expect((await admin('GET', `users/${ua}/feed/offline`)).body).toBe(3);

  // Every database request went over long polling: sessions started, data sent, nothing upgraded.
  const lp = network.aruma.slice(before).filter((p) => p.startsWith('/.lp'));
  expect(lp.filter((p) => /[?&]start=t/.test(p)).length).toBeGreaterThanOrEqual(2); // before and after the restart
  expect(lp.filter((p) => /[?&]seg0=/.test(p)).length).toBeGreaterThan(0);
  expect(lp.every((p) => new URLSearchParams(p.split('?')[1]).get('ns') === ns)).toBe(true);
});

test('8. revocation: an admin disables the user and the client\'s listens are cancelled within seconds', async ({ client, firebaseMajor }) => {
  const a = await client();
  const ua = await signIn(a, email(firebaseMajor, 'revoked'));
  await value(a, (e, uid) => e.dbSdk.set(e.dbSdk.ref(e.db, `users/${uid}`), { secret: 1 }), ua);
  await value(a, (e, a) => {
    e.listen('own', `users/${a.uid}`);
    e.listen('room', `rooms/r${a.major}/messages`, { on: 'child_added' });
  }, { uid: ua, major: firebaseMajor });
  await expect.poll(async () => (await events(a, 'own')).length).toBe(1);

  const t0 = Date.now();
  await disableUser(ua);
  await expect.poll(() => events(a, 'own'), { timeout: 5_000, intervals: [100] }).toContainEqual({ cancel: 'PERMISSION_DENIED' });
  await expect.poll(() => events(a, 'room'), { timeout: 5_000, intervals: [100] }).toContainEqual({ cancel: 'PERMISSION_DENIED' });
  console.log(`listens cancelled ${Date.now() - t0} ms after the user was disabled`);
  // The database SDK asked Auth for a fresh token, which a disabled user does not get: Auth signs
  // the user out.
  await expect.poll(() => value(a, (e) => e.auth.currentUser?.uid ?? null)).toBeNull();
});

test('9. network guard: Google and Firebase database hosts are refused', async ({ client, network }) => {
  const a = await client();
  const before = { blocked: network.blocked.length, urls: network.urls.length };
  const tries = await value(a, async () => {
    const fetched = await fetch('https://smoke-app-default-rtdb.firebaseio.com/.json').then(() => 'reached', () => 'refused');
    const socket = await new Promise((resolve) => {
      const ws = new WebSocket('wss://smoke-app-default-rtdb.europe-west1.firebasedatabase.app/.ws?v=5');
      ws.onopen = () => resolve('reached');
      ws.onerror = () => resolve('refused');
    });
    return { fetched, socket };
  });
  expect(tries).toEqual({ fetched: 'refused', socket: 'refused' });
  const caught = network.blocked.slice(before.blocked);
  expect(caught.map((u) => new URL(u).hostname)).toEqual(['smoke-app-default-rtdb.firebaseio.com', 'smoke-app-default-rtdb.europe-west1.firebasedatabase.app']);
  expect(caught.every((u) => firebaseDatabaseHost.test(new URL(u).hostname))).toBe(true);
  // The guard's own self-test: forget what it was asked to refuse.
  network.blocked.splice(before.blocked);
  network.urls.splice(before.urls);
  network.sockets = network.sockets.filter((u) => new URL(u).hostname === '127.0.0.1');
  expect(arumaURL).toMatch(/^http:\/\/127\.0\.0\.1:/);
});
