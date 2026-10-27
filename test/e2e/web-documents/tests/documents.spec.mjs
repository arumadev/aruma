import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import { test, expect, value, call, signIn, signOut, listen, unlisten, eventsOf, expectLast, unique, arumaURL } from './fixtures.mjs';

// Text in every width of UTF-16: ASCII, two-byte, a CJK run, right-to-left, an emoji (a surrogate
// pair) and a mathematical letter outside the BMP.
const text = 'héllo ✓ 日本語 عربي 😀 𝒳 end';

// Writes go through the lite SDK (REST), so the listen cases do not wait for the Write channel.
const liteSet = (page, path, data) => value(page, ({ lite, liteDb }, [path, data]) => lite.setDoc(lite.doc(liteDb, path), data), [path, data]);
const liteGet = (page, path) => value(page, async ({ lite, liteDb }, path) => {
  const s = await lite.getDoc(lite.doc(liteDb, path));
  return s.exists() ? s.data() : null;
}, path);

const transports = [['stream', 'streaming WebChannel'], ['long-polling', 'experimentalForceLongPolling']];

for (const [transport, label] of transports) {
  test.describe(`full SDK, ${label}`, () => {
    test('onSnapshot on a document and a query sees a change, and stops on unsubscribe', async ({ app, network }) => {
      const page = await app(transport);
      await signIn(page);
      const base = `e2e/${unique('listen')}`;
      await liteSet(page, `${base}/docs/a`, { n: 1 });

      await listen(page, 'doc', `${base}/docs/a`);
      await expectLast(page, 'doc', (e) => e.data?.n === 1, 'the document, from the server');

      await listen(page, 'query', `${base}/docs`, { where: ['n', '>=', 2] });
      await expectLast(page, 'query', (e) => e.docs?.length === 0, 'the empty query result');

      await liteSet(page, `${base}/docs/a`, { n: 2 });
      await expectLast(page, 'doc', (e) => e.data?.n === 2, 'the document after a change');
      await expectLast(page, 'query', (e) => e.docs?.length === 1 && e.changes[0][0] === 'added', 'the document entering the query');

      await liteSet(page, `${base}/docs/b`, { n: 5 });
      await expectLast(page, 'query', (e) => e.docs?.length === 2 && e.docs[1][0] === 'b', 'a second document');

      await unlisten(page, 'doc');
      await unlisten(page, 'query');
      const seen = (await eventsOf(page, 'doc')).length + (await eventsOf(page, 'query')).length;
      await liteSet(page, `${base}/docs/a`, { n: 3 });
      await liteSet(page, `${base}/docs/c`, { n: 7 });
      await page.waitForTimeout(1500);
      expect((await eventsOf(page, 'doc')).length + (await eventsOf(page, 'query')).length, 'events after unsubscribe').toBe(seen);

      // The channel really was Aruma's, over the transport asked for.
      expect(network.aruma.some((r) => r.path === '/google.firestore.v1.Firestore/Listen/channel')).toBe(true);
    });

    test('a listener the rules deny gets permission-denied, and the next one still works', async ({ app }) => {
      const page = await app(transport);
      await signIn(page);
      const base = `e2e/${unique('denied')}`;
      await liteSet(page, `${base}/d/ok`, { n: 1 });

      await listen(page, 'locked', 'locked/secret');
      await expectLast(page, 'locked', (e) => e.error === 'permission-denied', 'permission-denied on /locked');
      await listen(page, 'ok', `${base}/d/ok`);
      await expectLast(page, 'ok', (e) => e.data?.n === 1, 'an allowed listener after a denied one');
    });

    test('signed out, only the rules for everyone apply; signing in opens the rest', async ({ app }) => {
      const page = await app(transport);
      await signIn(page);
      const id = unique('open');
      await liteSet(page, `open/${id}`, { n: 1 });
      await liteSet(page, `e2e/${id}`, { n: 2 });
      await signOut(page);

      await listen(page, 'open', `open/${id}`);
      await expectLast(page, 'open', (e) => e.data?.n === 1, 'signed-out read of /open');
      await listen(page, 'private', `e2e/${id}`);
      await expectLast(page, 'private', (e) => e.error === 'permission-denied', 'signed-out read of /e2e');

      await signIn(page);
      await listen(page, 'signed-in', `e2e/${id}`);
      await expectLast(page, 'signed-in', (e) => e.data?.n === 2, 'signed-in read of /e2e');
    });

    test('non-ASCII text arrives whole (UTF-16 framing), also in a long payload', async ({ app }) => {
      const page = await app(transport);
      await signIn(page);
      const base = `e2e/${unique('utf16')}`;
      const long = text.repeat(2000);
      await liteSet(page, `${base}/docs/日本語-ключ-😀`, { text, 'عربي': 'قيمة', long });

      await listen(page, 'one', `${base}/docs/日本語-ключ-😀`);
      await expectLast(page, 'one', (e) => e.data?.text === text && e.data['عربي'] === 'قيمة' && e.data.long === long, 'the document with its text');
      await listen(page, 'all', `${base}/docs`);
      await expectLast(page, 'all', (e) => e.docs?.length === 1 && e.docs[0][0] === '日本語-ключ-😀' && e.docs[0][1].long === long, 'the same in a query');
    });

    test.describe('writes over the Write channel', () => {
      test('setDoc, updateDoc, deleteDoc and a non-ASCII value, seen by a listener', async ({ app }) => {
        const page = await app(transport);
        await signIn(page);
        const path = `e2e/${unique('write')}/docs/a`;
        await listen(page, 'doc', path);
        await expectLast(page, 'doc', (e) => e.data === null, 'no document yet');

        await value(page, ({ fs, db }, [path, text]) => fs.setDoc(fs.doc(db, path), { n: 1, text }), [path, text]);
        await expectLast(page, 'doc', (e) => e.data?.n === 1 && e.data.text === text, 'setDoc');
        await value(page, ({ fs, db }, path) => fs.updateDoc(fs.doc(db, path), { n: fs.increment(4) }), path);
        await expectLast(page, 'doc', (e) => e.data?.n === 5, 'updateDoc');
        expect(await liteGet(page, path)).toMatchObject({ n: 5, text });
        await value(page, ({ fs, db }, path) => fs.deleteDoc(fs.doc(db, path)), path);
        await expectLast(page, 'doc', (e) => e.data === null, 'deleteDoc');
      });

      test('a batch is atomic', async ({ app }) => {
        const page = await app(transport);
        await signIn(page);
        const base = `e2e/${unique('batch')}/docs`;
        await value(page, ({ fs, db }, base) => {
          const batch = fs.writeBatch(db);
          batch.set(fs.doc(db, `${base}/a`), { n: 1 });
          batch.set(fs.doc(db, `${base}/b`), { n: 2 });
          return batch.commit();
        }, base);
        expect(await liteGet(page, `${base}/a`)).toEqual({ n: 1 });
        expect(await liteGet(page, `${base}/b`)).toEqual({ n: 2 });
      });

      test('a transaction reads and writes', async ({ app }) => {
        const page = await app(transport);
        await signIn(page);
        const path = `e2e/${unique('txn')}/docs/a`;
        await liteSet(page, path, { n: 1 });
        const result = await value(page, ({ fs, db }, path) => fs.runTransaction(db, async (t) => {
          const ref = fs.doc(db, path);
          const n = (await t.get(ref)).data().n;
          t.update(ref, { n: n + 10 });
          return n;
        }), path);
        expect(result).toBe(1);
        expect(await liteGet(page, path)).toEqual({ n: 11 });
      });

      test('a write the rules deny fails with permission-denied', async ({ app }) => {
        const page = await app(transport);
        await signIn(page);
        const r = await call(page, ({ fs, db }) => fs.setDoc(fs.doc(db, 'locked/x'), { n: 1 }));
        expect(r.code).toBe('permission-denied');
      });
    });
  });
}

test.describe('lite SDK over REST', () => {
  test('get, query and setDoc', async ({ app, network }) => {
    const page = await app();
    await signIn(page);
    const base = `e2e/${unique('lite')}/docs`;
    await liteSet(page, `${base}/a`, { n: 1, text });
    await liteSet(page, `${base}/b`, { n: 2 });
    expect(await liteGet(page, `${base}/a`)).toEqual({ n: 1, text });
    expect(await liteGet(page, `${base}/missing`)).toBeNull();
    const ids = await value(page, async ({ lite, liteDb }, base) => {
      const s = await lite.getDocs(lite.query(lite.collection(liteDb, base), lite.where('n', '>=', 2)));
      return s.docs.map((d) => d.id);
    }, base);
    expect(ids).toEqual(['b']);
    expect(network.aruma.some((r) => r.path.startsWith('/v1/projects/'))).toBe(true);
    expect(network.aruma.some((r) => r.path.includes('/channel'))).toBe(false);
  });

  test('signed out, /open reads and the rest is denied', async ({ app }) => {
    const page = await app();
    await signIn(page);
    const id = unique('lite-open');
    await liteSet(page, `open/${id}`, { n: 1 });
    await signOut(page);
    expect(await liteGet(page, `open/${id}`)).toEqual({ n: 1 });
    expect((await call(page, ({ lite, liteDb }, id) => lite.getDoc(lite.doc(liteDb, `e2e/${id}`)), id)).code).toBe('permission-denied');
    expect((await call(page, ({ lite, liteDb }, id) => lite.setDoc(lite.doc(liteDb, `open/${id}`), { n: 2 }), id)).code).toBe('permission-denied');
  });

  test('a write the rules deny fails with permission-denied, and nothing is stored', async ({ app }) => {
    const page = await app();
    await signIn(page);
    const r = await call(page, ({ lite, liteDb }) => lite.setDoc(lite.doc(liteDb, 'locked/x'), { n: 1 }));
    expect(r.code).toBe('permission-denied');
    expect((await call(page, ({ lite, liteDb }) => lite.getDoc(lite.doc(liteDb, 'locked/x')))).code).toBe('permission-denied');
  });
});

test.describe('the web guard', () => {
  test('the built bundle names no Google Firestore host', () => {
    const dist = fileURLToPath(new URL('../dist/', import.meta.url));
    const files = [];
    const walk = (dir) => {
      for (const entry of readdirSync(dir, { withFileTypes: true })) {
        if (entry.isDirectory()) walk(join(dir, entry.name));
        else if (/\.(js|mjs|html|css|json|map)$/.test(entry.name)) files.push(join(dir, entry.name));
      }
    };
    walk(dist);
    expect(files.length).toBeGreaterThan(0);
    const aruma = new URL(arumaURL).host;
    let carriesAruma = false;
    for (const file of files) {
      const code = readFileSync(file, 'utf8');
      expect(code.includes('firestore.googleapis.com'), `${file} names Google's Firestore host`).toBe(false);
      carriesAruma ||= code.includes(aruma);
    }
    expect(carriesAruma, 'the bundle holds Aruma as its Firestore host').toBe(true);
  });

  test('at run time every request goes to Aruma or the app, and none to a Google host', async ({ app, network }) => {
    const page = await app();
    await signIn(page);
    const base = `e2e/${unique('guard')}/docs`;
    await liteSet(page, `${base}/a`, { n: 1 });
    await listen(page, 'doc', `${base}/a`);
    await expectLast(page, 'doc', (e) => e.data?.n === 1, 'the document');
    // The network fixture fails the test on any blocked request; this makes sure it saw traffic.
    expect(network.aruma.length).toBeGreaterThan(0);
    expect(network.blocked).toEqual([]);
    const origins = await page.evaluate(() => performance.getEntriesByType('resource').map((r) => new URL(r.name).origin));
    expect(new Set(origins)).toEqual(new Set([`http://localhost:${new URL(page.url()).port}`, arumaURL]));
  });
});
