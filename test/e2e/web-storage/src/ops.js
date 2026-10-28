// The eleven operations of the web Storage e2e (the S0-B list),
// on the official SDK in production mode, as an app bundles it. Every bundle (Vite, webpack, esbuild,
// SvelteKit, Next.js, Expo, Flutter's JS) calls run() with the SDK modules it imported, and the
// results land in window.__done for Playwright.
//
// ok means the operation did what Firebase does; warn means it did so with a link that is not on
// Aruma (the ⚠ of S0-B), which counts as a failure here.

const big = () => new Uint8Array(3 * 1024 * 1024 + 17).map((_, i) => i % 251);
const small = () => new TextEncoder().encode('hello aruma\n');
const png = () => Uint8Array.from(atob('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=='), (c) => c.charCodeAt(0));

/** The unified link ADR-022 §16 requires, up to the token. */
export const unifiedPrefix = (aruma, bucket, path) => `${aruma}/v0/b/${encodeURIComponent(bucket)}/o/${encodeURIComponent(path)}?alt=media&token=`;

/**
 * @param {{ storage: object, st: object, aruma: string, bucket: string, dir: string, img?: (url: string) => Promise<number> }} o
 *   storage: the getStorage() instance; st: the firebase/storage module; dir: the folder to work in.
 */
export async function run({ storage, st, aruma, bucket, dir, img = loadImage }) {
  const results = [];
  const step = async (op, fn) => {
    try {
      const r = (await fn()) ?? {};
      results.push({ op, ok: r.warn ? false : true, warn: Boolean(r.warn), detail: String(r.detail ?? '') });
    } catch (e) {
      results.push({ op, ok: false, detail: `${e.code ?? ''} ${e.message ?? e}`.trim().slice(0, 300) });
    }
  };
  // A blocked request is retried until these run out (defaults: 2 and 10 minutes).
  storage.maxOperationRetryTime = 8000;
  storage.maxUploadRetryTime = 8000;
  const host = new URL(aruma).host;
  const ref = (p) => st.ref(storage, `${dir}/${p}`);
  const onAruma = (u, path) => {
    const want = unifiedPrefix(aruma, bucket, `${dir}/${path}`);
    return u.startsWith(want) && /^[0-9a-f-]{36}$/.test(u.slice(want.length)) ? null : `not the unified link: ${u.replace(/token=[^&]*/, 'token=…')}`;
  };
  let url;
  const data = big();

  await step('init', () => {
    const detail = `host=${storage.host} protocol=${storage._protocol}`;
    return { detail, warn: storage.host !== host || storage._protocol !== new URL(aruma).protocol.slice(0, -1) };
  });
  await step('upload.small', async () => {
    const r = await st.uploadBytes(ref('small.txt'), small(), { contentType: 'text/plain' });
    return { detail: `${r.metadata.size} bytes` };
  });
  await step('upload.resumable3MB', async () => {
    const r = await st.uploadBytesResumable(ref('big.bin'), data, { contentType: 'application/octet-stream' });
    return { detail: `${r.metadata.size} bytes` };
  });
  await step('getMetadata', async () => {
    const m = await st.getMetadata(ref('small.txt'));
    if (m.size !== 12 || m.contentType !== 'text/plain') throw new Error(`size ${m.size} ${m.contentType}`);
    return { detail: `size=${m.size}` };
  });
  await step('getDownloadURL', async () => {
    url = await st.getDownloadURL(ref('small.txt'));
    const bad = onAruma(url, 'small.txt');
    return { detail: bad ?? 'unified', warn: Boolean(bad) };
  });
  await step('download.getBytes', async () => {
    const b = new Uint8Array(await st.getBytes(ref('big.bin')));
    if (b.length !== data.length || b.some((v, i) => v !== data[i])) throw new Error(`got ${b.length} bytes, not the ones uploaded`);
    return { detail: `${b.length} bytes, equal` };
  });
  await step('download.getBlob', async () => {
    const b = await st.getBlob(ref('small.txt'));
    const text = await b.text();
    if (text !== 'hello aruma\n') throw new Error(`got ${JSON.stringify(text)}`);
    return { detail: `${b.size} bytes, equal` };
  });
  await step('list', async () => {
    const l = await st.list(st.ref(storage, dir));
    const names = l.items.map((i) => i.name).sort().join(',');
    if (names !== 'big.bin,small.txt') throw new Error(`items ${names}`);
    return { detail: names };
  });
  await step('downloadURL.fetch', async () => {
    const res = await fetch(url);
    const text = await res.text();
    if (res.status !== 200 || text !== 'hello aruma\n') throw new Error(`${res.status} ${JSON.stringify(text.slice(0, 80))}`);
    return { detail: `200 from ${new URL(url).host}` };
  });
  await step('downloadURL.img', async () => {
    await st.uploadBytes(ref('pixel.png'), png(), { contentType: 'image/png' });
    const u = await st.getDownloadURL(ref('pixel.png'));
    const bad = onAruma(u, 'pixel.png');
    const width = await img(u);
    return { detail: `${bad ?? 'unified'}, width ${width}`, warn: Boolean(bad) || width !== 1 };
  });
  await step('delete', async () => {
    for (const p of ['small.txt', 'big.bin', 'pixel.png']) await st.deleteObject(ref(p));
    try {
      await st.getMetadata(ref('small.txt'));
    } catch (e) {
      if (e.code === 'storage/object-not-found') return { detail: 'gone' };
      throw e;
    }
    throw new Error('still there');
  });
  return results;
}

/** Loads a link in an <img> and resolves to its natural width. */
export function loadImage(u) {
  return new Promise((resolve, reject) => {
    const el = new Image();
    el.onload = () => resolve(el.naturalWidth);
    el.onerror = () => reject(new Error(`<img> failed to load from ${new URL(u).host}`));
    el.src = u;
    el.id = 'img';
    document.body.append(el);
  });
}
