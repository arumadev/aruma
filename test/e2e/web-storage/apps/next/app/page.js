// A server component: on the server the official SDK (the server bundle's @firebase/storage) reads
// public/ssr.png's link, rendered into the HTML; the client component runs the eleven operations.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { getApps, initializeApp } from 'firebase/app';
import { getDownloadURL, getStorage, ref } from 'firebase/storage';
import Client from './client.js';

export const dynamic = 'force-dynamic';

export default async function Page() {
  const e2e = JSON.parse(readFileSync(join(process.env.ARUMA_E2E_DIR, 'e2e.json'), 'utf8'));
  const app = getApps().find((a) => a.name === 'ssr') ?? initializeApp(e2e.config, 'ssr');
  const storage = getStorage(app);
  const url = await getDownloadURL(ref(storage, e2e.ssr));
  return (
    <main>
      <p id="ssr-host">{`${storage._protocol}://${storage.host}`}</p>
      <img id="ssr" src={url} alt="rendered on the server" />
      <Client name={process.env.ARUMA_E2E_NAME ?? 'next'} />
    </main>
  );
}
