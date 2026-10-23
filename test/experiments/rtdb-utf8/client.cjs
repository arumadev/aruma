// The client of rtdb-utf8.sh: the official JS SDK writes and reads Arabic, CJK and emoji text
// whose messages span several 16384-unit frames, with characters on every boundary, both
// ways, and the whole database at the root. A frame that is not valid UTF-8 on its own makes
// the WebSocket fail (1007 in a browser); the SDK then reconnects, so the run also fails if
// either connection ever drops.
//
//   node client.cjs <base url> <namespace>
//
// It prints one JSON line per check and exits 0 when all pass.
const { initializeApp } = require('@firebase/app');
const { getDatabase, ref, set, get, remove, onValue, off } = require('@firebase/database');

const [base, ns] = process.argv.slice(2);
const databaseURL = `${base}?ns=${ns}`;
const open = (name) => getDatabase(initializeApp({ databaseURL, projectId: ns.replace(/-default-rtdb$/, '') }, name));
const writer = open('writer');
const reader = open('reader');
const say = (o) => console.log(JSON.stringify(o));
let failed = 0;
const check = (name, ok, extra = {}) => {
  if (!ok) failed++;
  say({ check: name, ok, ...extra });
};

// Every connection change after the first connect is a drop.
const drops = { writer: 0, reader: 0 };
for (const [name, db] of [['writer', writer], ['reader', reader]]) {
  let seen = false;
  onValue(ref(db, '.info/connected'), (s) => {
    if (s.val()) seen = true;
    else if (seen) drops[name]++;
  });
}

const fill = (unit, pad, total) => 'x'.repeat(pad) + unit.repeat(Math.ceil(total / unit.length));
const where = (a, b) => {
  let i = 0;
  while (i < a.length && a[i] === b[i]) i++;
  return i;
};

(async () => {
  await set(ref(writer, 'warmup'), 1);
  await get(ref(reader, 'warmup'));

  // Client to server and back: the SDK cuts its frames at 16384 UTF-16 code units, so a
  // frame of three-byte characters holds up to 49152 bytes. Characters outside the BMP are
  // left out here: the SDK can cut one in two (below).
  for (const [name, unit] of [['arabic', 'مرحبا بكم'], ['cjk', '中文字'], ['mixed', 'aب中ö']]) {
    for (let pad = 0; pad < 4; pad++) {
      const key = `${name}${pad}`;
      const value = fill(unit, pad, 40000);
      await set(ref(writer, `utf8/${key}`), value);
      const got = (await get(ref(reader, `utf8/${key}`))).val();
      check(`write and read ${key}`, got === value, got === value ? {} : { at: where(got, value) });
    }
  }

  // Server to client with emoji: written over REST, so no SDK splits them on the way in,
  // then read by the SDK as a get and as a listener's first data.
  for (let pad = 0; pad < 4; pad++) {
    const key = `emoji${pad}`;
    const value = fill('😀🎉', pad, 40000);
    const r = await fetch(`${base}/utf8/${key}.json?ns=${ns}`, { method: 'PUT', body: JSON.stringify(value) });
    check(`REST PUT ${key}`, r.ok, { status: r.status });
    const got = (await get(ref(reader, `utf8/${key}`))).val();
    check(`read ${key}`, got === value, got === value ? {} : { at: where(got, value) });
  }
  const listened = await new Promise((resolve) => {
    const q = ref(reader, 'utf8');
    onValue(q, (s) => {
      off(q);
      resolve(s.val());
    });
  });
  check('listen utf8', listened && Object.keys(listened).length === 16 && listened.emoji3 === fill('😀🎉', 3, 40000));

  // Emoji from the SDK: its split is by code units, so a surrogate pair can fall on a frame
  // boundary and each half is sent as U+FFFD. That is the SDK's doing, the same against
  // Firebase; recorded, not failed. The server stores what it received either way.
  for (let pad = 0; pad < 2; pad++) {
    const key = `sdkemoji${pad}`;
    const value = fill('😀', pad, 40000);
    await set(ref(writer, `utf8/${key}`), value);
    const got = (await get(ref(reader, `utf8/${key}`))).val();
    const replaced = got === value ? 0 : (got.match(/�/g) || []).length;
    say({ observe: `SDK emoji write ${key}`, exact: got === value, replacementChars: replaced });
  }

  // The whole database at the root, set and removed by the SDK.
  const root = { a: 'جذر', big: fill('中', 1, 20000) };
  await set(ref(writer), root);
  const gotRoot = (await get(ref(reader))).val();
  check('set root', gotRoot && gotRoot.a === root.a && gotRoot.big === root.big && Object.keys(gotRoot).length === 2);
  await remove(ref(writer));
  check('remove root', (await get(ref(reader))).val() === null);

  check('no connection dropped', drops.writer === 0 && drops.reader === 0, drops);
  say({ result: failed === 0 ? 'ok' : `${failed} failed` });
  process.exit(failed === 0 ? 0 : 1);
})().catch((e) => {
  say({ result: 'error', error: String(e) });
  process.exit(1);
});
