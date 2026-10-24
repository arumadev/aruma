// On the server (Node, the SSR bundle): the official SDK in production mode reads public/ssr.png's
// link through Storage's Node build, which sends with fetch. The link it returns is rendered into
// the page's HTML.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { initializeApp, getApps } from 'firebase/app';
import { getDownloadURL, getStorage, ref } from 'firebase/storage';

export const ssr = true;

export async function load() {
  const e2e = JSON.parse(readFileSync(join(process.env.ARUMA_E2E_DIR, 'e2e.json'), 'utf8'));
  const app = getApps().find((a) => a.name === 'ssr') ?? initializeApp(e2e.config, 'ssr');
  const storage = getStorage(app);
  const url = await getDownloadURL(ref(storage, e2e.ssr));
  return { e2e, ssr: { url, host: storage.host, protocol: storage._protocol } };
}
